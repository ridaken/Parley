package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/tomvokac/parley/internal/analysis"
	"github.com/tomvokac/parley/internal/audio"
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
	State         string   `json:"state"` // idle | starting | listening | error
	Message       string   `json:"message"`
	MicAvailable  bool     `json:"micAvailable"`
	ActiveSources []string `json:"activeSources"`
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

	recMu      sync.Mutex
	recorders  map[audio.Source]*audio.MonoWAVWriter
	sessionDir string
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

// Start launches the whisper server, begins capture, and starts transcribing.
func (m *MeetingService) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}
	emitStatus("starting", "Launching transcription engine…", nil)

	binPath, err := resolveResource(filepath.Join("resources", "whisper", "bin", "Release", "whisper-server.exe"))
	if err != nil {
		return m.fail("whisper server binary not found", err)
	}
	modelPath, err := resolveResource(filepath.Join("resources", "whisper", "models", "ggml-base.en.bin"))
	if err != nil {
		return m.fail("whisper model not found", err)
	}

	m.server = stt.NewServer(binPath, modelPath, whisperHost, whisperPort, filepath.Join(dataDir(), "whisper-server.log"))
	if err := m.server.Start(context.Background()); err != nil {
		return m.fail("could not start transcription engine", err)
	}

	client := stt.NewClient(m.server.URL())
	m.chunker = stt.NewChunker(client, chunkWindow, m.onSegment)
	m.chunker.Start()

	m.startEngine()

	m.sessionDir = filepath.Join(recordingsDir(), time.Now().Format("2006-01-02_15-04-05"))
	if err := os.MkdirAll(m.sessionDir, 0o755); err != nil {
		fmt.Printf("[rec] could not create session dir: %v\n", err)
	}

	m.capturer, err = audio.NewCapturer(func(src audio.Source, samples []int16) {
		m.chunker.Feed(src, samples)
		m.record(src, samples)
	})
	if err != nil {
		m.chunker.Stop()
		m.server.Stop()
		return m.fail("could not initialise audio", err)
	}
	if err := m.capturer.Start(m.captureSpecs()); err != nil {
		m.capturer.Stop()
		m.chunker.Stop()
		if m.engine != nil {
			m.engine.Stop()
			m.engine = nil
		}
		m.server.Stop()
		return m.fail("could not start audio capture", err)
	}

	m.running = true
	emitStatus("listening", "Listening", m.capturer.Active())
	return nil
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
	m.capturer.Stop()
	m.chunker.Stop()
	if m.engine != nil {
		m.engine.Stop()
		m.engine = nil
	}
	m.server.Stop()

	m.recMu.Lock()
	for _, w := range m.recorders {
		_ = w.Close()
	}
	m.recorders = make(map[audio.Source]*audio.MonoWAVWriter)
	m.recMu.Unlock()

	m.running = false
	emitStatus("idle", "Stopped", nil)
	return nil
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
	if m.engine != nil {
		m.engine.Feed(string(seg.Source), seg.Text)
	}
}

// startEngine sets up the LLM analysis engine from current settings + the active
// context profile. If no endpoint is configured, analysis is skipped (transcript
// still works).
func (m *MeetingService) startEngine() {
	settings, err := m.store.GetSettings()
	if err != nil || settings.LLMBaseURL == "" {
		application.Get().Event.Emit("analysis", analysis.State{})
		fmt.Println("[analysis] no LLM endpoint configured — analysis disabled")
		return
	}

	var bg analysis.Context
	if settings.ActiveProfileID != 0 {
		if p, err := m.store.GetProfile(settings.ActiveProfileID); err == nil {
			bg = analysis.Context{Summary: p.Summary, People: p.People, Notes: p.Notes}
		}
	}

	apiKey, _ := m.store.GetAPIKey()
	client := llm.NewClient(settings.LLMBaseURL, apiKey, settings.LLMModel)
	interval := time.Duration(settings.AnalysisIntervalSec) * time.Second

	application.Get().Event.Emit("analysis", analysis.State{}) // clear previous session
	m.engine = analysis.NewEngine(client, interval, bg, func(s analysis.State) {
		application.Get().Event.Emit("analysis", s)
	})
	m.engine.Start()
}

func (m *MeetingService) fail(msg string, err error) error {
	emitStatus("error", msg, nil)
	return fmt.Errorf("%s: %w", msg, err)
}

func emitStatus(state, message string, active []audio.Source) {
	labels := make([]string, 0, len(active))
	mic := false
	for _, a := range active {
		labels = append(labels, string(a))
		if a == audio.You {
			mic = true
		}
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
