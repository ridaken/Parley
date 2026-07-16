package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	whisperHost       = "127.0.0.1"
	whisperPort       = 8765
	chunkWindow       = 5 * time.Second
	streamChunkWindow = 320 * time.Millisecond
)

type transcriptChunker interface {
	Feed(audio.Source, []int16)
	Start()
	Stop()
	StopWithTimeout(time.Duration)
}

type managedSTTServer interface {
	Start(context.Context) error
	Stop()
	URL() string
}

type localEngineResult struct {
	server    managedSTTServer
	streaming bool
	name      string
	model     string
}

// TranscriptionConfig is the user-facing selection saved by the model manager.
// ModelID is one of auto, nemotron, whisper:<filename>, or external.
type TranscriptionConfig struct {
	ModelID     string `json:"modelID"`
	ExternalURL string `json:"externalURL"`
}

// TranscriptionModelOption describes one selectable transcription provider.
type TranscriptionModelOption struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Kind              string `json:"kind"` // automatic | local | external
	Detail            string `json:"detail"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason"`
}

// RuntimeInfo is the build and transcription metadata currently in use. The
// frontend displays it persistently so packaged-version or model-selection
// problems can be identified without opening log files.
type RuntimeInfo struct {
	AppVersion           string `json:"appVersion"`
	TranscriptionModel   string `json:"transcriptionModel"`
	TranscriptionModelID string `json:"transcriptionModelID"`
	TranscriptionKind    string `json:"transcriptionKind"`   // local | external
	TranscriptionStatus  string `json:"transcriptionStatus"` // stopped | loading | ready | configured | error
	TranscriptionMessage string `json:"transcriptionMessage"`
}

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
	chunker  transcriptChunker
	engine   *analysis.Engine

	// The local transcription server belongs to the app, not an individual
	// meeting. ServiceStartup begins loading it in the background; Start waits on
	// the same result if preparation is still underway. Idle lifecycle controls or
	// ServiceShutdown can release the model weights.
	localMu     sync.Mutex
	localDone   chan struct{}
	localCancel context.CancelFunc
	localResult localEngineResult
	localErr    error
	localState  string
	localKey    string
	localGen    uint64

	hasNVIDIAGPU  func() bool
	newNemotron   func() (managedSTTServer, error)
	newCPUWhisper func(store.Settings, bool) (managedSTTServer, string, error)

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
	m := &MeetingService{
		store:        s,
		recorders:    make(map[audio.Source]*audio.MonoWAVWriter),
		hasNVIDIAGPU: stt.HasNVIDIAGPU,
	}
	m.newNemotron = func() (managedSTTServer, error) { return newNemotronServer() }
	m.newCPUWhisper = func(settings store.Settings, allowFallback bool) (managedSTTServer, string, error) {
		return newCPUWhisperServer(settings, allowFallback)
	}
	return m
}

// ServiceStartup begins loading the configured local transcription engine as
// soon as Parley opens. Preparation is deliberately asynchronous so model I/O
// never delays creation of the application window.
func (m *MeetingService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	settings, err := m.store.GetSettings()
	if err != nil {
		log.Printf("[stt] could not read settings for startup preload: %v", err)
		return nil
	}
	if settings.SttEngine == "external" {
		log.Printf("[stt] remote transcription configured; skipping local model preload")
		return nil
	}
	m.beginLocalEngine(ctx, settings)
	return nil
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

// GetRuntimeInfo reports the exact app version and the transcription model that
// has actually been selected. In particular, this reflects a CPU fallback when
// Nemotron was preferred but could not start, rather than merely echoing the
// configured model filename.
func (m *MeetingService) GetRuntimeInfo() RuntimeInfo {
	info := RuntimeInfo{AppVersion: appVersion, TranscriptionKind: "local", TranscriptionStatus: "stopped"}
	var settings store.Settings
	if m.store != nil {
		var err error
		settings, err = m.store.GetSettings()
		if err != nil {
			info.TranscriptionModel = "Unavailable"
			info.TranscriptionStatus = "error"
			info.TranscriptionMessage = "Could not read transcription settings."
			return info
		}
		info.TranscriptionModelID = transcriptionModelID(settings)
		if settings.SttEngine == "external" {
			remote := strings.TrimRight(strings.TrimSpace(settings.SttBaseURL), "/")
			info.TranscriptionKind = "external"
			info.TranscriptionModel = "External · " + remote
			info.TranscriptionStatus = "configured"
			info.TranscriptionMessage = "Parley does not manage the external server process."
			return info
		}
	}

	m.localMu.Lock()
	result := m.localResult
	loadErr := m.localErr
	state := m.localState
	m.localMu.Unlock()

	if info.TranscriptionModelID == "" {
		info.TranscriptionModelID = "auto"
	}
	info.TranscriptionModel = configuredTranscriptionLabel(settings)
	switch state {
	case "loading":
		info.TranscriptionStatus = "loading"
		info.TranscriptionMessage = "Loading the selected transcription model…"
	case "ready":
		info.TranscriptionModel = result.model
		info.TranscriptionStatus = "ready"
		info.TranscriptionMessage = "Ready for transcription."
	case "error":
		info.TranscriptionStatus = "error"
		info.TranscriptionMessage = "The selected model could not start."
		if loadErr != nil {
			info.TranscriptionMessage = loadErr.Error()
		}
	default:
		info.TranscriptionStatus = "stopped"
		info.TranscriptionMessage = "The selected local model is stopped."
	}
	return info
}

// Start launches a fresh meeting using the app's preloaded transcription engine,
// then starts capture and a new persisted session.
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

	// Transcription endpoint: the explicitly configured remote server or a
	// supervised local engine. Automatic local selection retains the original
	// Nemotron-first, Whisper-fallback behavior.
	var sttURL string
	streamingSTT := false
	if settings.SttEngine == "external" {
		remote := strings.TrimSpace(settings.SttBaseURL)
		if remote == "" {
			return m.fail("The external transcription URL is empty. Choose a local model or configure the server URL in Settings.", errors.New("external transcription URL is empty"))
		}
		log.Printf("[stt] using remote transcription server: %s", remote)
		sttURL = strings.TrimRight(remote, "/")
	} else {
		local, err := m.waitLocalEngine(context.Background(), settings)
		if err != nil {
			return m.fail("The local transcription engine couldn't start. Ensure the bundled CPU model is installed, or configure a remote transcription URL in Settings.", err)
		}
		sttURL = local.server.URL()
		streamingSTT = local.streaming
		log.Printf("[stt] using preloaded %s", local.name)
	}

	client := stt.NewClient(sttURL)
	if streamingSTT {
		m.chunker = stt.NewStreamingChunker(client, streamChunkWindow, m.onSegment)
	} else {
		m.chunker = stt.NewChunker(client, chunkWindow, m.onSegment)
	}
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
		m.closeSession()
		return m.fail("Couldn't start the selected audio device(s). Pick different sources in Audio settings.", err)
	}

	m.running = true
	emitListening(m.capturer.Active(), m.capturer.HasMic())
	return nil
}

// newNemotronServer resolves the optional install written by resources/nemotron/setup.ps1.
// The .ready marker is deliberately required so an interrupted installer download
// is never mistaken for a usable model installation.
func newNemotronServer() (*stt.Server, error) {
	install, err := resolveNemotronInstall()
	if err != nil {
		return nil, err
	}
	root := install.root
	ready := filepath.Join(root, ".ready")
	python := filepath.Join(root, "runtime", "Scripts", "python.exe")
	script := install.script
	modelDir := filepath.Join(root, "model")
	config := filepath.Join(modelDir, "config.json")
	weights := filepath.Join(modelDir, "model.safetensors")
	args := []string{
		script,
		"--model", modelDir,
		"--host", whisperHost,
		"--port", fmt.Sprintf("%d", whisperPort),
		"--language", "en-US",
	}
	return stt.NewCommandServer(
		"Nemotron 3.5 ASR",
		python,
		args,
		[]string{ready, python, script, config, weights},
		whisperHost,
		whisperPort,
		filepath.Join(dataDir(), "nemotron-server.log"),
		3*time.Minute,
	), nil
}

// newCPUWhisperServer resolves the bundled fallback without starting it.
func newCPUWhisperServer(settings store.Settings, allowFallback bool) (*stt.Server, string, error) {
	cpuBinPath, err := resolveResource(filepath.Join("resources", "whisper", "bin", "Release", "whisper-server.exe"))
	if err != nil {
		return nil, "", fmt.Errorf("resolve bundled whisper server: %w", err)
	}
	modelName := settings.WhisperModel
	if modelName == "" {
		modelName = "ggml-small.en-q5_1.bin"
	}
	modelPath, err := resolveResource(filepath.Join("resources", "whisper", "models", modelName))
	if err != nil && allowFallback {
		// The configured filename isn't present — commonly because the default
		// model changed but a stale name is still saved in Settings, or a
		// differently-named file was downloaded. Rather than hard-fail, fall back
		// to whatever model is installed so the app keeps working.
		if alt, altName, ok := anyInstalledModel(); ok {
			log.Printf("[stt] configured model %q not found; falling back to installed model %q", modelName, altName)
			modelPath = alt
			modelName = altName
			err = nil
		} else {
			return nil, "", fmt.Errorf("transcription model %q is missing and no fallback model is installed: %w", modelName, err)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("transcription model %q is missing: %w", modelName, err)
	}
	return stt.NewServer(cpuBinPath, modelPath, whisperHost, whisperPort, filepath.Join(dataDir(), "whisper-server.log")), modelName, nil
}

// beginLocalEngine starts (or joins) a preparation attempt for the configured
// local model. A generation token prevents a canceled load from publishing a
// stale process after Stop, Restart, or a model change.
func (m *MeetingService) beginLocalEngine(parent context.Context, settings store.Settings) <-chan struct{} {
	key := localEngineKey(settings)
	m.localMu.Lock()
	if m.localDone != nil && m.localKey == key && (m.localState == "loading" || m.localState == "ready") {
		done := m.localDone
		m.localMu.Unlock()
		return done
	}
	m.localGen++
	generation := m.localGen
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	m.localDone = done
	m.localCancel = cancel
	m.localResult = localEngineResult{}
	m.localErr = nil
	m.localState = "loading"
	m.localKey = key
	m.localMu.Unlock()
	emitRuntimeInfo(m.GetRuntimeInfo())

	go func() {
		result, err := m.loadLocalEngine(ctx, settings)
		m.localMu.Lock()
		if generation != m.localGen {
			m.localMu.Unlock()
			if result.server != nil {
				result.server.Stop()
			}
			close(done)
			return
		}
		m.localResult = result
		m.localErr = err
		m.localCancel = nil
		if err != nil || result.server == nil {
			m.localState = "error"
		} else {
			m.localState = "ready"
		}
		close(done)
		m.localMu.Unlock()
		emitRuntimeInfo(m.GetRuntimeInfo())
	}()
	return done
}

func (m *MeetingService) loadLocalEngine(ctx context.Context, settings store.Settings) (localEngineResult, error) {
	switch settings.SttEngine {
	case "nemotron":
		if !m.hasNVIDIAGPU() {
			return localEngineResult{}, errors.New("Nemotron requires a working NVIDIA GPU")
		}
		nemotron, err := m.newNemotron()
		if err != nil {
			return localEngineResult{}, fmt.Errorf("Nemotron is not fully installed: %w", err)
		}
		if err := nemotron.Start(ctx); err != nil {
			nemotron.Stop()
			return localEngineResult{}, fmt.Errorf("Nemotron: %w", err)
		}
		return localEngineResult{
			server: nemotron, streaming: true, name: "Nemotron 3.5 ASR Streaming",
			model: "NVIDIA Nemotron 3.5 ASR Streaming 0.6B · GPU",
		}, nil
	case "whisper":
		whisper, modelName, err := m.newCPUWhisper(settings, false)
		if err == nil {
			err = whisper.Start(ctx)
		}
		if err != nil {
			if whisper != nil {
				whisper.Stop()
			}
			return localEngineResult{}, fmt.Errorf("Whisper %s: %w", settings.WhisperModel, err)
		}
		return localEngineResult{
			server: whisper, name: "bundled CPU Whisper", model: "Whisper " + modelName + " · CPU",
		}, nil
	case "auto", "":
		// Continue below with the original Nemotron-first fallback behavior.
	default:
		return localEngineResult{}, fmt.Errorf("unsupported transcription engine %q", settings.SttEngine)
	}

	var nemotronErr error
	if m.hasNVIDIAGPU() {
		nemotron, err := m.newNemotron()
		if err != nil {
			nemotronErr = err
			log.Printf("[stt] NVIDIA GPU detected but Nemotron 3.5 ASR is not provisioned: %v", err)
		} else if err := nemotron.Start(ctx); err != nil {
			nemotron.Stop()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return localEngineResult{}, ctxErr
			}
			nemotronErr = err
			log.Printf("[stt] Nemotron 3.5 ASR failed to preload (%v); preparing CPU Whisper fallback", err)
		} else {
			log.Printf("[stt] Nemotron 3.5 ASR Streaming preloaded on NVIDIA GPU")
			return localEngineResult{
				server:    nemotron,
				streaming: true,
				name:      "Nemotron 3.5 ASR Streaming",
				model:     "NVIDIA Nemotron 3.5 ASR Streaming 0.6B · GPU",
			}, nil
		}
	} else {
		log.Printf("[stt] no available NVIDIA GPU detected; preloading CPU Whisper")
	}
	if err := ctx.Err(); err != nil {
		return localEngineResult{}, err
	}

	whisper, modelName, err := m.newCPUWhisper(settings, true)
	if err == nil {
		err = whisper.Start(ctx)
	}
	if err != nil {
		if whisper != nil {
			whisper.Stop()
		}
		if nemotronErr != nil {
			return localEngineResult{}, fmt.Errorf("Nemotron: %v; CPU Whisper: %w", nemotronErr, err)
		}
		return localEngineResult{}, fmt.Errorf("CPU Whisper: %w", err)
	}
	log.Printf("[stt] bundled CPU Whisper fallback preloaded")
	return localEngineResult{
		server: whisper,
		name:   "bundled CPU Whisper",
		model:  "Whisper " + modelName + " · CPU",
	}, nil
}

func (m *MeetingService) waitLocalEngine(ctx context.Context, settings store.Settings) (localEngineResult, error) {
	done := m.beginLocalEngine(ctx, settings)
	select {
	case <-ctx.Done():
		return localEngineResult{}, ctx.Err()
	case <-done:
	}
	m.localMu.Lock()
	defer m.localMu.Unlock()
	if m.localKey != localEngineKey(settings) {
		return localEngineResult{}, errors.New("transcription model changed while it was loading")
	}
	return m.localResult, m.localErr
}

// shutdownLocalEngine invalidates the current generation, then cancels/waits
// without holding localMu so the loader can finish cleanup without deadlocking.
func (m *MeetingService) shutdownLocalEngine() {
	m.localMu.Lock()
	m.localGen++
	cancel := m.localCancel
	done := m.localDone
	server := m.localResult.server
	m.localDone = nil
	m.localCancel = nil
	m.localResult = localEngineResult{}
	m.localErr = nil
	m.localState = "stopped"
	m.localMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	if server != nil {
		server.Stop()
	}
	emitRuntimeInfo(m.GetRuntimeInfo())
}

func localEngineKey(settings store.Settings) string {
	return settings.SttEngine + "\x00" + settings.WhisperModel
}

func transcriptionModelID(settings store.Settings) string {
	switch settings.SttEngine {
	case "nemotron", "external":
		return settings.SttEngine
	case "whisper":
		return "whisper:" + settings.WhisperModel
	default:
		return "auto"
	}
}

func configuredTranscriptionLabel(settings store.Settings) string {
	switch settings.SttEngine {
	case "nemotron":
		return "NVIDIA Nemotron 3.5 ASR Streaming 0.6B · GPU"
	case "whisper":
		return "Whisper " + settings.WhisperModel + " · CPU"
	case "external":
		return "External · " + strings.TrimRight(strings.TrimSpace(settings.SttBaseURL), "/")
	default:
		return "Automatic local model"
	}
}

// ListTranscriptionModels discovers the selectable local installations and the
// always-available external-server choice.
func (m *MeetingService) ListTranscriptionModels() ([]TranscriptionModelOption, error) {
	whisperModels, _ := installedWhisperModels()
	_, whisperBinErr := resolveResource(filepath.Join("resources", "whisper", "bin", "Release", "whisper-server.exe"))
	nemotronReason := nemotronUnavailableReason(m.hasNVIDIAGPU())
	nemotronAvailable := nemotronReason == ""
	whisperAvailable := whisperBinErr == nil && len(whisperModels) > 0

	auto := TranscriptionModelOption{
		ID: "auto", Label: "Automatic (recommended)", Kind: "automatic",
		Detail:    "Use Nemotron on a supported NVIDIA GPU, then fall back to installed CPU Whisper.",
		Available: nemotronAvailable || whisperAvailable,
	}
	if !auto.Available {
		auto.UnavailableReason = "No usable local Nemotron or Whisper installation was found."
	}
	options := []TranscriptionModelOption{auto, {
		ID: "nemotron", Label: "Nemotron 3.5 ASR Streaming", Kind: "local",
		Detail: "Low-latency NVIDIA GPU transcription.", Available: nemotronAvailable,
		UnavailableReason: nemotronReason,
	}}
	for _, name := range whisperModels {
		option := TranscriptionModelOption{
			ID: "whisper:" + name, Label: "Whisper · " + name, Kind: "local",
			Detail: "Bundled whisper.cpp CPU transcription.", Available: whisperBinErr == nil,
		}
		if whisperBinErr != nil {
			option.UnavailableReason = "The bundled whisper.cpp server is not installed."
		}
		options = append(options, option)
	}

	var settings store.Settings
	var err error
	if m.store != nil {
		settings, err = m.store.GetSettings()
	}
	if err == nil && settings.SttEngine == "whisper" {
		selectedID := transcriptionModelID(settings)
		found := false
		for _, option := range options {
			if option.ID == selectedID {
				found = true
				break
			}
		}
		if !found {
			options = append(options, TranscriptionModelOption{
				ID: selectedID, Label: "Missing Whisper model · " + settings.WhisperModel,
				Kind: "local", Detail: "This previously selected model is no longer installed.",
				UnavailableReason: "The model file could not be found.",
			})
		}
	}
	options = append(options, TranscriptionModelOption{
		ID: "external", Label: "External server", Kind: "external", Available: true,
		Detail: "Use an HTTP server with a whisper.cpp-compatible /inference endpoint.",
	})
	return options, nil
}

// ConfigureTranscription validates and persists a model selection, then
// reconciles the idle runtime. Local loading continues asynchronously.
func (m *MeetingService) ConfigureTranscription(config TranscriptionConfig) error {
	current, err := m.store.GetSettings()
	if err != nil {
		return err
	}
	next, err := settingsForTranscriptionConfig(current, config)
	if err != nil {
		return err
	}
	unchanged := current.SttEngine == next.SttEngine && current.WhisperModel == next.WhisperModel && strings.TrimSpace(current.SttBaseURL) == strings.TrimSpace(next.SttBaseURL)
	if unchanged {
		return nil
	}
	if err := m.ensureTranscriptionIdle(); err != nil {
		return err
	}
	if err := m.validateTranscriptionSettings(next); err != nil {
		return err
	}
	if err := m.store.SaveTranscriptionSettings(next.SttEngine, next.SttBaseURL, next.WhisperModel); err != nil {
		return err
	}
	m.shutdownLocalEngine()
	if next.SttEngine != "external" {
		m.beginLocalEngine(context.Background(), next)
	} else {
		emitRuntimeInfo(m.GetRuntimeInfo())
	}
	return nil
}

// StartTranscriptionModel loads the selected local model without starting a meeting.
func (m *MeetingService) StartTranscriptionModel() error {
	if err := m.ensureTranscriptionIdle(); err != nil {
		return err
	}
	settings, err := m.store.GetSettings()
	if err != nil {
		return err
	}
	if settings.SttEngine == "external" {
		return errors.New("external server processes cannot be started by Parley; use Test connection instead")
	}
	if err := m.validateTranscriptionSettings(settings); err != nil {
		return err
	}
	m.beginLocalEngine(context.Background(), settings)
	return nil
}

// StopTranscriptionModel releases the selected local model while Parley is idle.
func (m *MeetingService) StopTranscriptionModel() error {
	if err := m.ensureTranscriptionIdle(); err != nil {
		return err
	}
	settings, err := m.store.GetSettings()
	if err != nil {
		return err
	}
	if settings.SttEngine == "external" {
		return errors.New("external server processes cannot be stopped by Parley")
	}
	m.shutdownLocalEngine()
	return nil
}

// RestartTranscriptionModel replaces the current local process with a fresh load.
func (m *MeetingService) RestartTranscriptionModel() error {
	if err := m.ensureTranscriptionIdle(); err != nil {
		return err
	}
	settings, err := m.store.GetSettings()
	if err != nil {
		return err
	}
	if settings.SttEngine == "external" {
		return errors.New("external server processes cannot be restarted by Parley")
	}
	if err := m.validateTranscriptionSettings(settings); err != nil {
		return err
	}
	m.shutdownLocalEngine()
	m.beginLocalEngine(context.Background(), settings)
	return nil
}

// TestExternalTranscription verifies that an external HTTP server is reachable.
// Any HTTP response counts as reachable; the inference path is exercised by the
// first real transcription request.
func (m *MeetingService) TestExternalTranscription(baseURL string) error {
	clean, err := normalizeExternalURL(baseURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean+"/", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the external transcription server: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (m *MeetingService) ensureTranscriptionIdle() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("stop the active meeting before changing the transcription model")
	}
	return nil
}

func (m *MeetingService) validateTranscriptionSettings(settings store.Settings) error {
	switch settings.SttEngine {
	case "external":
		_, err := normalizeExternalURL(settings.SttBaseURL)
		return err
	case "nemotron":
		if !m.hasNVIDIAGPU() {
			return errors.New("Nemotron requires a working NVIDIA GPU")
		}
		_, err := m.newNemotron()
		if err != nil {
			return fmt.Errorf("Nemotron is not fully installed: %w", err)
		}
		return nil
	case "whisper":
		_, _, err := m.newCPUWhisper(settings, false)
		return err
	case "auto", "":
		if m.hasNVIDIAGPU() {
			if _, err := m.newNemotron(); err == nil {
				return nil
			}
		}
		_, _, err := m.newCPUWhisper(settings, true)
		return err
	default:
		return fmt.Errorf("unsupported transcription engine %q", settings.SttEngine)
	}
}

func settingsForTranscriptionConfig(current store.Settings, config TranscriptionConfig) (store.Settings, error) {
	modelID := strings.TrimSpace(config.ModelID)
	next := current
	next.SttBaseURL = strings.TrimSpace(config.ExternalURL)
	switch {
	case modelID == "auto":
		next.SttEngine = "auto"
	case modelID == "nemotron":
		next.SttEngine = "nemotron"
	case modelID == "external":
		next.SttEngine = "external"
		clean, err := normalizeExternalURL(config.ExternalURL)
		if err != nil {
			return store.Settings{}, err
		}
		next.SttBaseURL = clean
	case strings.HasPrefix(modelID, "whisper:"):
		name := strings.TrimSpace(strings.TrimPrefix(modelID, "whisper:"))
		if name == "" || filepath.Base(name) != name || !strings.HasSuffix(strings.ToLower(name), ".bin") {
			return store.Settings{}, errors.New("select an installed Whisper model")
		}
		next.SttEngine = "whisper"
		next.WhisperModel = name
	default:
		return store.Settings{}, fmt.Errorf("unsupported transcription model %q", modelID)
	}
	return next, nil
}

func normalizeExternalURL(raw string) (string, error) {
	clean := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(clean)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("enter a complete external transcription URL beginning with http:// or https://")
	}
	return clean, nil
}

func nemotronUnavailableReason(hasGPU bool) string {
	if !hasGPU {
		return "A working NVIDIA GPU was not detected."
	}
	if _, err := resolveNemotronInstall(); err != nil {
		return "The Nemotron runtime or model installation is incomplete."
	}
	return ""
}

func installedWhisperModels() ([]string, error) {
	dir, err := resolveResource(filepath.Join("resources", "whisper", "models"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".bin") {
			models = append(models, entry.Name())
		}
	}
	sort.Strings(models)
	return models, nil
}

// anyInstalledModel returns the first *.bin model present under
// resources/whisper/models, used as a fallback when the model filename saved in
// Settings doesn't match what's actually installed.
func anyInstalledModel() (path, name string, ok bool) {
	dir, err := resolveResource(filepath.Join("resources", "whisper", "models"))
	if err != nil {
		return "", "", false
	}
	models, err := installedWhisperModels()
	if err != nil {
		return "", "", false
	}
	if len(models) > 0 {
		return filepath.Join(dir, models[0]), models[0], true
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
	// Order matters: stop feeds, then flush and transcribe remaining audio. The
	// preloaded local server stays warm for the next meeting.
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
// terminate the app-lifetime transcription subprocess.
func (m *MeetingService) ServiceShutdown() error {
	log.Println("[meeting] service shutdown")
	err := m.Stop()
	m.shutdownLocalEngine()
	return err
}

func (m *MeetingService) engineTimeout() time.Duration {
	settings, err := m.store.GetSettings()
	if err != nil || settings.AnalysisTimeoutSec <= 0 {
		return 60 * time.Second
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
		snapshot := store.ContextSnapshot{}
		if hasProfile {
			snapshot = store.ContextSnapshot{
				Name: profile.Name, Summary: profile.Summary, People: profile.People, Notes: profile.Notes,
			}
		}
		id, err := m.store.CreateSession(title, profileID, "", snapshot)
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

// startEngine sets up the LLM analysis engine using the context snapshotted with
// this session. Legacy sessions fall back to their saved profile id. If no
// endpoint is configured, analysis is skipped (transcript still works).
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

	sessionID := m.sessionID.Load()
	bg := m.analysisContext(sessionID, settings.ActiveProfileID)

	apiKey, _ := m.store.GetConnectionAPIKey(conn.ID)
	log.Printf("[analysis] using LLM connection %q (%s, model %s)", conn.Name, conn.BaseURL, conn.Model)
	client := llm.NewClient(conn.BaseURL, apiKey, conn.Model)
	delay := time.Duration(settings.AnalysisIntervalSec) * time.Second
	timeout := time.Duration(settings.AnalysisTimeoutSec) * time.Second
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

func (m *MeetingService) analysisContext(sessionID, activeProfileID int64) analysis.Context {
	profileID := activeProfileID
	if sessionID != 0 {
		if b, err := m.store.GetSessionBundle(sessionID); err == nil {
			if b.ContextSnapshot.Captured {
				return analysis.Context{
					Summary: b.ContextSnapshot.Summary,
					People:  b.ContextSnapshot.People,
					Notes:   b.ContextSnapshot.Notes,
				}
			}
			profileID = b.Session.ProfileID
		}
	}
	if profileID != 0 {
		if p, err := m.store.GetProfile(profileID); err == nil {
			return analysis.Context{Summary: p.Summary, People: p.People, Notes: p.Notes}
		}
	}
	return analysis.Context{}
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
	return m.exportMarkdownFile(sessionID, "Export meeting notes", "", meetingexport.Markdown)
}

// ExportTranscriptMarkdown saves the exact pre-meeting context snapshot followed
// by the complete persisted transcript. It intentionally excludes analysis and
// live notes.
func (m *MeetingService) ExportTranscriptMarkdown(sessionID int64) (string, error) {
	return m.exportMarkdownFile(sessionID, "Export context and transcript", "-transcript", meetingexport.TranscriptMarkdown)
}

func (m *MeetingService) exportMarkdownFile(sessionID int64, dialogTitle, filenameSuffix string, render func(store.SessionBundle) string) (string, error) {
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
	hasExt := strings.HasSuffix(strings.ToLower(filename), ".md")
	if hasExt {
		filename = filename[:len(filename)-3]
	}
	filename += filenameSuffix
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("export requires the Wails application runtime")
	}
	path, err := app.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:    dialogTitle,
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
	if err := os.WriteFile(path, []byte(render(b)), 0o644); err != nil {
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

func emitRuntimeInfo(info RuntimeInfo) {
	if app := application.Get(); app != nil {
		app.Event.Emit("runtimeInfo", info)
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
