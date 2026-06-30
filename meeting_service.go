package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/tomvokac/parley/internal/analysis"
	"github.com/tomvokac/parley/internal/audio"
	"github.com/tomvokac/parley/internal/diagnostics"
	meetingexport "github.com/tomvokac/parley/internal/export"
	"github.com/tomvokac/parley/internal/llm"
	"github.com/tomvokac/parley/internal/store"
	"github.com/tomvokac/parley/internal/stt"
)

const (
	whisperHost = "127.0.0.1"
	whisperPort = 8765
	chunkWindow = 5 * time.Second
)

// StatusEvent is broadcast whenever the capture/transcription state changes.
type StatusEvent struct {
	State         string   `json:"state"` // idle | starting | listening | finalizing | error
	Message       string   `json:"message"`
	MicAvailable  bool     `json:"micAvailable"`
	ActiveSources []string `json:"activeSources"`
}

// analysisDiagLogger adapts analysis failures to Parley's local diagnostics
// files while reading the current logging level from settings each time.
type analysisDiagLogger struct {
	store *store.Store
}

func (l analysisDiagLogger) LogAnalysisFailure(f analysis.AnalysisFailure) {
	level := diagnostics.LevelTrace
	if s, err := l.store.GetSettings(); err == nil {
		level = s.LoggingLevel
	}
	event := diagnostics.AnalysisFailure{
		Timestamp:        f.Timestamp,
		SessionID:        f.SessionID,
		SessionTitle:     f.SessionTitle,
		ConnectionName:   f.ConnectionName,
		BaseURL:          f.BaseURL,
		Model:            f.Model,
		Kind:             f.Kind,
		Error:            f.Error,
		Attempt:          f.Attempt,
		MaxAttempts:      f.MaxAttempts,
		SkippedWindow:    f.SkippedWindow,
		TargetLen:        f.TargetLen,
		PendingLineCount: f.PendingLineCount,
		TimeoutMs:        f.Timeout.Milliseconds(),
		ElapsedMs:        f.Elapsed.Milliseconds(),
		TotalElapsedMs:   f.TotalElapsed.Milliseconds(),
		Request:          f.Request,
		Response:         f.Response,
		ErrorDetails:     f.ErrorDetails,
	}
	if level != diagnostics.LevelTrace {
		event.ErrorDetails = nil
	}
	err := diagnostics.LogAnalysisFailure(dataDir(), level, event)
	if err != nil {
		log.Printf("[diagnostics] write analysis failure: %v", err)
	}
}

// AnalysisStatusEvent reports non-fatal analysis problems while capture keeps
// running, such as an LLM request timeout or malformed model response.
type AnalysisStatusEvent struct {
	State   string `json:"state"` // ok | warning
	Message string `json:"message"`
}

// MeetingService is the Wails-bound façade that drives capture + transcription.
type MeetingService struct {
	store *store.Store

	mu       sync.Mutex
	running  bool
	capturer *audio.Capturer
	chunker  *stt.Chunker
	server   *stt.Server
	engine   *analysis.Engine

	sessionID     atomic.Int64 // active session row; 0 when not persisting
	lastSessionID atomic.Int64 // most recent persisted session, retained after Stop for export

	recMu      sync.Mutex
	recorders  map[audio.Source]*audio.MonoWAVWriter
	sessionDir string
}

// LoadedSession is a saved meeting's full state, returned to the frontend so it
// can repopulate the transcript and analysis panels (read-only view or resume).
type LoadedSession struct {
	Session   store.Session    `json:"session"`
	Segments  []stt.Segment    `json:"segments"`
	Analysis  analysis.State   `json:"analysis"`
	LiveNotes []store.LiveNote `json:"liveNotes"`
}

// NewMeetingService constructs the service.
func NewMeetingService(s *store.Store) *MeetingService {
	return &MeetingService{store: s, recorders: make(map[audio.Source]*audio.MonoWAVWriter)}
}

// ListDevices enumerates all audio devices (inputs and outputs) with stable IDs.
func (m *MeetingService) ListDevices() ([]audio.DeviceInfo, error) {
	return audio.ListDevices()
}

// IsRunning reports whether a session is active.
func (m *MeetingService) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Start launches a fresh meeting: whisper server, capture, transcription, and a
// new persisted session.
func (m *MeetingService) Start() error { return m.start(0) }

// Resume continues a previously saved meeting, appending new transcript/analysis
// to it. Pass the session id returned by ListSessions.
func (m *MeetingService) Resume(id int64) error { return m.start(id) }

// start launches capture+transcription. resumeID == 0 creates a new session;
// otherwise it appends to (and rehydrates analysis from) the given session.
func (m *MeetingService) start(resumeID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}
	emitStatus("starting", "Launching transcription engine…", nil)

	settings, _ := m.store.GetSettings()

	// Transcription endpoint: a configured remote whisper server, or the bundled
	// local engine launched as a subprocess.
	var sttURL string
	if remote := strings.TrimSpace(settings.SttBaseURL); remote != "" {
		log.Printf("[stt] using remote transcription server: %s", remote)
		sttURL = strings.TrimRight(remote, "/")
	} else {
		binPath, err := resolveResource(filepath.Join("resources", "whisper", "bin", "Release", "whisper-server.exe"))
		if err != nil {
			return m.fail("The local transcription engine isn't installed. Run \"task setup:whisper\" (or scripts/setup-whisper.ps1) to fetch it, or set a remote transcription URL in Settings.", err)
		}
		modelName := settings.WhisperModel
		if modelName == "" {
			modelName = "ggml-small.en-q5_1.bin"
		}
		modelPath, err := resolveResource(filepath.Join("resources", "whisper", "models", modelName))
		if err != nil {
			// The configured filename isn't present — commonly because the default
			// model changed but a stale name is still saved in Settings, or a
			// differently-named file was downloaded. Rather than hard-fail, fall
			// back to whatever model IS installed so the app keeps working.
			if alt, altName, ok := anyInstalledModel(); ok {
				log.Printf("[stt] configured model %q not found; falling back to installed model %q", modelName, altName)
				modelPath = alt
			} else {
				return m.fail(fmt.Sprintf("Transcription model %q is missing and no model is installed under resources/whisper/models. Run scripts/setup-whisper.ps1 to fetch one, pick another in Settings, or use a remote URL.", modelName), err)
			}
		}

		m.server = stt.NewServer(binPath, modelPath, whisperHost, whisperPort, filepath.Join(dataDir(), "whisper-server.log"))
		if err := m.server.Start(context.Background()); err != nil {
			return m.fail("The transcription engine didn't start. See the log file for details.", err)
		}
		sttURL = m.server.URL()
	}

	client := stt.NewClient(sttURL)
	m.chunker = stt.NewChunker(client, chunkWindow, m.onSegment)
	m.chunker.Start()

	m.openSession(resumeID)
	m.startEngine(resumeID)

	var err error
	m.capturer, err = audio.NewCapturer(func(src audio.Source, samples []int16) {
		m.chunker.Feed(src, samples)
		m.record(src, samples)
	})
	if err != nil {
		m.chunker.Stop()
		m.stopServer()
		m.closeSession()
		return m.fail("Couldn't access the audio system on this machine.", err)
	}
	if err := m.capturer.Start(m.captureSpecs()); err != nil {
		m.capturer.Stop()
		m.chunker.Stop()
		if m.engine != nil {
			m.engine.Stop()
			m.engine = nil
		}
		m.stopServer()
		m.closeSession()
		return m.fail("Couldn't start the selected audio device(s). Pick different sources in Audio settings.", err)
	}

	m.running = true
	emitListening(m.capturer.Active(), m.capturer.HasMic())
	return nil
}

// stopServer stops the local whisper subprocess if one was launched.
func (m *MeetingService) stopServer() {
	if m.server != nil {
		m.server.Stop()
		m.server = nil
	}
}

// anyInstalledModel returns the first *.bin model present under
// resources/whisper/models, used as a fallback when the model filename saved in
// Settings doesn't match what's actually installed.
func anyInstalledModel() (path, name string, ok bool) {
	dir, err := resolveResource(filepath.Join("resources", "whisper", "models"))
	if err != nil {
		return "", "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".bin") {
			return filepath.Join(dir, e.Name()), e.Name(), true
		}
	}
	return "", "", false
}

// captureSpecs resolves the configured sources, falling back to the default
// mic (You) + system loopback (Others) when nothing has been configured.
func (m *MeetingService) captureSpecs() []audio.SourceSpec {
	settings, err := m.store.GetSettings()
	if err == nil && len(settings.CaptureSources) > 0 {
		specs := make([]audio.SourceSpec, 0, len(settings.CaptureSources))
		for _, s := range settings.CaptureSources {
			specs = append(specs, audio.SourceSpec{
				ID:    s.ID,
				Kind:  s.Kind,
				Label: audio.Source(s.Label),
			})
		}
		return specs
	}
	return []audio.SourceSpec{
		{ID: "", Kind: audio.KindInput, Label: audio.You},
		{ID: "", Kind: audio.KindLoopback, Label: audio.Others},
	}
}

// Stop ends the session, flushing the final audio and closing recordings.
func (m *MeetingService) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return nil
	}
	// Order matters: stop feeds, flush+transcribe remaining audio, then kill server.
	emitStatus("finalizing", "Finalizing meeting…", nil)
	if id := m.sessionID.Load(); id != 0 {
		_ = m.store.SetSessionStatus(id, "finalizing")
	}
	if m.capturer != nil {
		m.capturer.Stop()
	}
	if m.chunker != nil {
		m.chunker.StopWithTimeout(5 * time.Minute)
	}
	if m.engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*m.engineTimeout())
		m.engine.Flush(ctx)
		cancel()
		m.engine.Stop()
		m.engine = nil
	}
	m.stopServer()

	m.recMu.Lock()
	for _, w := range m.recorders {
		_ = w.Close()
	}
	m.recorders = make(map[audio.Source]*audio.MonoWAVWriter)
	m.recMu.Unlock()

	m.closeSession()

	m.running = false
	emitStatus("idle", "Stopped", nil)
	return nil
}

// ServiceShutdown is called by Wails when the app is exiting. It gives Parley a
// best-effort chance to stop capture, drain final transcription/analysis, and
// terminate the local whisper subprocess.
func (m *MeetingService) ServiceShutdown() error {
	log.Println("[meeting] service shutdown")
	return m.Stop()
}

func (m *MeetingService) engineTimeout() time.Duration {
	settings, err := m.store.GetSettings()
	if err != nil || settings.AnalysisTimeoutSec <= 0 {
		return 30 * time.Second
	}
	return time.Duration(settings.AnalysisTimeoutSec) * time.Second
}

// openSession creates (or reuses, for resume) the persisted session and the
// on-disk audio directory for this part, recording the id for live persistence.
func (m *MeetingService) openSession(resumeID int64) {
	var profileID int64
	var profile store.Profile
	var hasProfile bool
	if s, err := m.store.GetSettings(); err == nil {
		profileID = s.ActiveProfileID
		if profileID != 0 {
			if p, err := m.store.GetProfile(profileID); err == nil {
				profile = p
				hasProfile = true
			}
		}
	}

	sessionID := resumeID
	if sessionID == 0 {
		title := sessionTitle(profile, hasProfile, time.Now())
		id, err := m.store.CreateSession(title, profileID, "")
		if err != nil {
			log.Printf("[session] could not create session: %v", err)
		}
		sessionID = id
	}
	m.sessionID.Store(sessionID)
	if sessionID != 0 {
		m.lastSessionID.Store(sessionID)
	}

	// Each Start/Resume writes audio to its own part directory so resumed
	// meetings don't clobber the earlier part's recordings.
	root := filepath.Join(recordingsDir(), fmt.Sprintf("session-%d", sessionID))
	if sessionID == 0 {
		root = filepath.Join(recordingsDir(), time.Now().Format("2006-01-02_15-04-05"))
	}
	m.sessionDir = filepath.Join(root, time.Now().Format("part-2006-01-02_15-04-05"))
	if err := os.MkdirAll(m.sessionDir, 0o755); err != nil {
		log.Printf("[rec] could not create session dir: %v", err)
	}
	if sessionID != 0 {
		_ = m.store.SetSessionAudioDir(sessionID, m.sessionDir)
		_ = m.store.SetSessionStatus(sessionID, "recording")
	}
}

func sessionTitle(profile store.Profile, hasProfile bool, now time.Time) string {
	if hasProfile {
		if name := strings.TrimSpace(profile.Name); name != "" {
			return name
		}
	}
	return "Meeting " + now.Format("Jan 2 2006, 3:04 PM")
}

// closeSession stamps the session's end time and stops live persistence.
func (m *MeetingService) closeSession() {
	if id := m.sessionID.Swap(0); id != 0 {
		_ = m.store.EndSession(id, m.sessionDir)
	}
}

// record appends samples to the per-source session WAV (created lazily).
func (m *MeetingService) record(src audio.Source, samples []int16) {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	w := m.recorders[src]
	if w == nil {
		name := sanitizeFilename(string(src)) + ".wav"
		var err error
		w, err = audio.NewMonoWAVWriter(filepath.Join(m.sessionDir, name), audio.SampleRate)
		if err != nil {
			return
		}
		m.recorders[src] = w
	}
	_ = w.Write(samples)
}

// sanitizeFilename lowercases a label and strips anything unsafe for a path.
func sanitizeFilename(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "source"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func (m *MeetingService) onSegment(seg stt.Segment) {
	application.Get().Event.Emit("transcript", seg)
	if id := m.sessionID.Load(); id != 0 {
		_ = m.store.AppendSegment(id, store.Segment{
			Source:  string(seg.Source),
			Text:    seg.Text,
			StartMs: seg.StartMs,
			EndMs:   seg.EndMs,
		})
	}
	if m.engine != nil {
		m.engine.Feed(string(seg.Source), seg.Text)
	}
}

// startEngine sets up the LLM analysis engine from current settings + the active
// context profile. If no endpoint is configured, analysis is skipped (transcript
// still works). When resumeID != 0 it rehydrates the saved analysis state so the
// continued meeting builds on prior topics/assertions.
func (m *MeetingService) startEngine(resumeID int64) {
	settings, err := m.store.GetSettings()
	if err != nil {
		application.Get().Event.Emit("analysis", analysis.State{})
		log.Printf("[analysis] could not load settings — analysis disabled: %v", err)
		return
	}
	conn, err := m.store.GetActiveLLMConnection()
	if err != nil || conn.BaseURL == "" {
		application.Get().Event.Emit("analysis", analysis.State{})
		log.Println("[analysis] no LLM connection configured — analysis disabled")
		return
	}

	var bg analysis.Context
	if settings.ActiveProfileID != 0 {
		if p, err := m.store.GetProfile(settings.ActiveProfileID); err == nil {
			bg = analysis.Context{Summary: p.Summary, People: p.People, Notes: p.Notes}
		}
	}

	apiKey, _ := m.store.GetConnectionAPIKey(conn.ID)
	log.Printf("[analysis] using LLM connection %q (%s, model %s)", conn.Name, conn.BaseURL, conn.Model)
	client := llm.NewClient(conn.BaseURL, apiKey, conn.Model)
	delay := time.Duration(settings.AnalysisIntervalSec) * time.Second
	timeout := time.Duration(settings.AnalysisTimeoutSec) * time.Second
	sessionID := m.sessionID.Load()

	m.engine = analysis.NewEngineWithTimeout(client, delay, timeout, bg, func(s analysis.State) {
		emitAnalysisStatus("ok", "")
		application.Get().Event.Emit("analysis", s)
		if sessionID != 0 {
			if data, err := json.Marshal(s); err == nil {
				_ = m.store.SaveAnalysis(sessionID, string(data))
			}
		}
	}, func(msg string) {
		emitAnalysisStatus("warning", msg)
	})
	title := ""
	if sessionID != 0 {
		if b, err := m.store.GetSessionBundle(sessionID); err == nil {
			title = b.Session.Title
		}
	}
	m.engine.SetFailureLogger(analysis.DiagnosticMeta{
		SessionID:      sessionID,
		SessionTitle:   title,
		ConnectionName: conn.Name,
		BaseURL:        conn.BaseURL,
		Model:          conn.Model,
	}, analysisDiagLogger{store: m.store})

	if resumeID != 0 {
		m.restoreEngine(resumeID)
	} else {
		application.Get().Event.Emit("analysis", analysis.State{}) // clear previous session
	}
	m.engine.Start()
}

// restoreEngine seeds the engine from a saved session and re-emits its analysis
// so the panels populate immediately on resume.
func (m *MeetingService) restoreEngine(id int64) {
	b, err := m.store.GetSessionBundle(id)
	if err != nil {
		log.Printf("[analysis] resume load failed: %v", err)
		return
	}
	var st analysis.State
	if b.AnalysisJSON != "" {
		_ = json.Unmarshal([]byte(b.AnalysisJSON), &st)
	}
	notes := make([]analysis.LiveNote, 0, len(b.LiveNotes))
	for _, n := range b.LiveNotes {
		notes = append(notes, analysis.LiveNote{Scope: n.Scope, TopicTitle: n.TopicTitle, Text: n.Text})
	}
	history := make([]struct{ Speaker, Text string }, 0, len(b.Segments))
	for _, s := range b.Segments {
		history = append(history, struct{ Speaker, Text string }{Speaker: s.Source, Text: s.Text})
	}
	m.engine.Restore(st, notes, history)
	application.Get().Event.Emit("analysis", st)
}

// AddLiveNote injects user context mid-meeting. scope is "meeting" (whole
// session) or "topic" (current topic only); it is fed to the analysis engine and
// persisted with the session.
func (m *MeetingService) AddLiveNote(scope, text string) (store.LiveNote, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return store.LiveNote{}, nil
	}

	m.mu.Lock()
	eng := m.engine
	m.mu.Unlock()

	note := store.LiveNote{Scope: scope, Text: text}
	if note.Scope != analysis.ScopeMeeting {
		note.Scope = analysis.ScopeTopic
	}
	if eng != nil {
		applied := eng.AddLiveNote(scope, text)
		note.Scope = applied.Scope
		note.TopicTitle = applied.TopicTitle
	}

	if id := m.sessionID.Load(); id != 0 {
		return m.store.AddLiveNote(id, note)
	}
	return note, nil
}

// ListSessions returns saved meetings, newest first.
func (m *MeetingService) ListSessions() ([]store.Session, error) {
	return m.store.ListSessions()
}

// LoadSession returns a saved meeting's full state for display.
func (m *MeetingService) LoadSession(id int64) (LoadedSession, error) {
	b, err := m.store.GetSessionBundle(id)
	if err != nil {
		return LoadedSession{}, err
	}
	out := LoadedSession{Session: b.Session, LiveNotes: b.LiveNotes}
	out.Segments = make([]stt.Segment, 0, len(b.Segments))
	for _, s := range b.Segments {
		out.Segments = append(out.Segments, stt.Segment{
			Source:  audio.Source(s.Source),
			Text:    s.Text,
			StartMs: s.StartMs,
			EndMs:   s.EndMs,
		})
	}
	if b.AnalysisJSON != "" {
		_ = json.Unmarshal([]byte(b.AnalysisJSON), &out.Analysis)
	}
	return out, nil
}

// ExportMarkdown saves the active or selected meeting's notes as a Markdown file.
// Pass sessionID=0 to export the currently running session.
func (m *MeetingService) ExportMarkdown(sessionID int64) (string, error) {
	sessionID = m.exportSessionID(sessionID)
	if sessionID == 0 {
		return "", fmt.Errorf("no meeting is available to export")
	}
	b, err := m.store.GetSessionBundle(sessionID)
	if err != nil {
		return "", err
	}
	filename := ""
	if strings.TrimSpace(b.Session.Title) != "" {
		filename = sanitizeFilename(b.Session.Title)
	}
	if filename == "" {
		filename = fmt.Sprintf("meeting-%d", sessionID)
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("export requires the Wails application runtime")
	}
	path, err := app.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:    "Export meeting notes",
		Filename: filename,
		Filters: []application.FileFilter{
			{DisplayName: "Markdown", Pattern: "*.md"},
		},
	}).PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(meetingexport.Markdown(b)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (m *MeetingService) exportSessionID(requested int64) int64 {
	if requested != 0 {
		return requested
	}
	if id := m.sessionID.Load(); id != 0 {
		return id
	}
	return m.lastSessionID.Load()
}

// RenameSession changes a saved meeting's title.
func (m *MeetingService) RenameSession(id int64, title string) error {
	return m.store.SetSessionTitle(id, strings.TrimSpace(title))
}

// DeleteSession permanently removes a saved meeting (not the one in progress).
func (m *MeetingService) DeleteSession(id int64) error {
	if m.sessionID.Load() == id {
		return fmt.Errorf("cannot delete the meeting that is currently recording")
	}
	return m.store.DeleteSession(id)
}

// fail logs the full underlying error (to the log file) and surfaces a short,
// friendly message to the UI via the error status.
func (m *MeetingService) fail(msg string, err error) error {
	log.Printf("[meeting] %s: %v", msg, err)
	emit("error", msg, nil, false)
	return fmt.Errorf("%s: %w", msg, err)
}

// emitStatus broadcasts a state with no active sources (mic unknown).
func emitStatus(state, message string, _ []audio.Source) {
	emit(state, message, nil, false)
}

// emitListening broadcasts the listening state with the sources that started and
// whether a microphone is among them.
func emitListening(active []audio.Source, hasMic bool) {
	emit("listening", "Listening", active, hasMic)
}

func emitAnalysisStatus(state, message string) {
	if app := application.Get(); app != nil {
		app.Event.Emit("analysisStatus", AnalysisStatusEvent{State: state, Message: message})
	}
}

func emit(state, message string, active []audio.Source, mic bool) {
	labels := make([]string, 0, len(active))
	for _, a := range active {
		labels = append(labels, string(a))
	}
	if app := application.Get(); app != nil {
		app.Event.Emit("status", StatusEvent{
			State:         state,
			Message:       message,
			MicAvailable:  mic,
			ActiveSources: labels,
		})
	}
}
