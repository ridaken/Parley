package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomvokac/parley/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeManagedSTTServer struct {
	url        string
	started    chan struct{}
	release    chan struct{}
	startErr   error
	startOnce  sync.Once
	startCalls atomic.Int32
	stopCalls  atomic.Int32
}

func newFakeManagedSTTServer() *fakeManagedSTTServer {
	return &fakeManagedSTTServer{
		url:     "http://127.0.0.1:18765",
		started: make(chan struct{}),
	}
}

func (s *fakeManagedSTTServer) Start(ctx context.Context) error {
	s.startCalls.Add(1)
	s.startOnce.Do(func() { close(s.started) })
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.startErr
}

func (s *fakeManagedSTTServer) Stop()       { s.stopCalls.Add(1) }
func (s *fakeManagedSTTServer) URL() string { return s.url }

func openMeetingTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "parley.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionTitleUsesActiveContextName(t *testing.T) {
	now := time.Date(2026, time.June, 25, 14, 30, 0, 0, time.UTC)
	got := sessionTitle(store.Profile{Name: "  Q3 Planning Sync  "}, true, now)
	if got != "Q3 Planning Sync" {
		t.Fatalf("sessionTitle = %q, want context name", got)
	}
}

func TestSessionTitleFallsBackToTimestamp(t *testing.T) {
	now := time.Date(2026, time.June, 25, 14, 30, 0, 0, time.UTC)
	for name, profile := range map[string]store.Profile{
		"no profile":    {},
		"blank profile": {Name: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			hasProfile := name != "no profile"
			got := sessionTitle(profile, hasProfile, now)
			if got != "Meeting Jun 25 2026, 2:30 PM" {
				t.Fatalf("sessionTitle = %q", got)
			}
		})
	}
}

func TestExportSessionIDFallsBackToLastStoppedMeeting(t *testing.T) {
	m := NewMeetingService(nil)
	m.lastSessionID.Store(42)

	if got := m.exportSessionID(0); got != 42 {
		t.Fatalf("exportSessionID(0) = %d, want last stopped session", got)
	}

	m.sessionID.Store(99)
	if got := m.exportSessionID(0); got != 99 {
		t.Fatalf("exportSessionID(0) = %d, want active session", got)
	}
	if got := m.exportSessionID(7); got != 7 {
		t.Fatalf("exportSessionID(7) = %d, want explicit session", got)
	}
}

func TestServiceStartupPreloadsAndReusesNemotron(t *testing.T) {
	s := openMeetingTestStore(t)
	m := NewMeetingService(s)
	nemotron := newFakeManagedSTTServer()
	nemotron.release = make(chan struct{})
	m.hasNVIDIAGPU = func() bool { return true }
	m.newNemotron = func() (managedSTTServer, error) { return nemotron, nil }
	m.newCPUWhisper = func(store.Settings) (managedSTTServer, string, error) {
		return nil, "", errors.New("CPU fallback should not be used")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- m.ServiceStartup(ctx, application.ServiceOptions{}) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("ServiceStartup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServiceStartup blocked on model loading")
	}
	select {
	case <-nemotron.started:
	case <-time.After(time.Second):
		t.Fatal("startup did not begin loading Nemotron")
	}
	select {
	case <-m.localDone:
		t.Fatal("preload completed before the fake model was released")
	default:
	}

	close(nemotron.release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	settings, _ := s.GetSettings()
	first, err := m.waitLocalEngine(waitCtx, settings)
	if err != nil {
		t.Fatalf("waitLocalEngine: %v", err)
	}
	second, err := m.waitLocalEngine(waitCtx, settings)
	if err != nil {
		t.Fatalf("second waitLocalEngine: %v", err)
	}
	if first.server != nemotron || second.server != nemotron || !first.streaming {
		t.Fatalf("preloaded result was not reused: first=%+v second=%+v", first, second)
	}
	if calls := nemotron.startCalls.Load(); calls != 1 {
		t.Fatalf("Nemotron Start calls = %d, want one app-lifetime load", calls)
	}
	m.running = true
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stops := nemotron.stopCalls.Load(); stops != 0 {
		t.Fatalf("meeting Stop released preloaded Nemotron %d times", stops)
	}

	if err := m.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if stops := nemotron.stopCalls.Load(); stops != 1 {
		t.Fatalf("Nemotron Stop calls = %d, want shutdown-only release", stops)
	}
}

func TestLocalEnginePreloadFallsBackToCPU(t *testing.T) {
	m := NewMeetingService(nil)
	nemotron := newFakeManagedSTTServer()
	nemotron.startErr = errors.New("CUDA failed")
	whisper := newFakeManagedSTTServer()
	m.hasNVIDIAGPU = func() bool { return true }
	m.newNemotron = func() (managedSTTServer, error) { return nemotron, nil }
	m.newCPUWhisper = func(store.Settings) (managedSTTServer, string, error) {
		return whisper, "ggml-small.en-q5_1.bin", nil
	}

	result, err := m.waitLocalEngine(context.Background(), store.Settings{})
	if err != nil {
		t.Fatalf("waitLocalEngine: %v", err)
	}
	if result.server != whisper || result.streaming {
		t.Fatalf("fallback result = %+v, want non-streaming CPU Whisper", result)
	}
	if nemotron.startCalls.Load() != 1 || whisper.startCalls.Load() != 1 {
		t.Fatalf("start calls: Nemotron=%d CPU=%d", nemotron.startCalls.Load(), whisper.startCalls.Load())
	}
	if nemotron.stopCalls.Load() != 1 {
		t.Fatalf("failed Nemotron was not cleaned up")
	}
	m.shutdownLocalEngine()
	if whisper.stopCalls.Load() != 1 {
		t.Fatalf("CPU fallback was not released at shutdown")
	}
}

func TestGetRuntimeInfoReportsResolvedLocalModel(t *testing.T) {
	s := openMeetingTestStore(t)
	m := NewMeetingService(s)
	done := make(chan struct{})
	close(done)
	m.localDone = done
	m.localResult = localEngineResult{
		server: newFakeManagedSTTServer(),
		model:  "Whisper ggml-small.en-q5_1.bin · CPU",
	}

	got := m.GetRuntimeInfo()
	if got.AppVersion != appVersion {
		t.Fatalf("AppVersion = %q, want %q", got.AppVersion, appVersion)
	}
	if got.TranscriptionStatus != "ready" {
		t.Fatalf("TranscriptionStatus = %q, want ready", got.TranscriptionStatus)
	}
	if got.TranscriptionModel != "Whisper ggml-small.en-q5_1.bin · CPU" {
		t.Fatalf("TranscriptionModel = %q", got.TranscriptionModel)
	}
}

func TestGetRuntimeInfoReportsRemoteServer(t *testing.T) {
	s := openMeetingTestStore(t)
	settings, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	settings.SttBaseURL = " http://transcription.local:8765/ "
	if err := s.SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	got := NewMeetingService(s).GetRuntimeInfo()
	if got.TranscriptionStatus != "ready" {
		t.Fatalf("TranscriptionStatus = %q, want ready", got.TranscriptionStatus)
	}
	if got.TranscriptionModel != "Remote model · http://transcription.local:8765" {
		t.Fatalf("TranscriptionModel = %q", got.TranscriptionModel)
	}
}

func TestServiceShutdownCancelsInProgressPreload(t *testing.T) {
	s := openMeetingTestStore(t)
	m := NewMeetingService(s)
	nemotron := newFakeManagedSTTServer()
	nemotron.release = make(chan struct{})
	m.hasNVIDIAGPU = func() bool { return true }
	m.newNemotron = func() (managedSTTServer, error) { return nemotron, nil }
	m.newCPUWhisper = func(store.Settings) (managedSTTServer, string, error) {
		return nil, "", errors.New("shutdown should prevent fallback startup")
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := m.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		t.Fatalf("ServiceStartup: %v", err)
	}
	select {
	case <-nemotron.started:
	case <-time.After(time.Second):
		t.Fatal("preload did not start")
	}
	cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- m.ServiceShutdown() }()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("ServiceShutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServiceShutdown did not cancel the in-progress model load")
	}
	if m.localResult.server != nil {
		t.Fatal("canceled preload retained a server")
	}
}
