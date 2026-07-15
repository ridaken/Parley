package stt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerDoesNotStartWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := NewCommandServer(
		"canceled test",
		filepath.Join(t.TempDir(), "missing.exe"),
		nil,
		nil,
		"127.0.0.1",
		18129,
		"",
		time.Minute,
	)
	if err := srv.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context.Canceled before file/process work", err)
	}
}

// TestServerRedirectsOutputToLog verifies the whisper server starts headlessly
// and its stdout/stderr land in the log file (never a console). Skips if the
// bundled binary/model aren't present.
func TestServerRedirectsOutputToLog(t *testing.T) {
	bin := filepath.Join("..", "..", "resources", "whisper", "bin", "Release", "whisper-server.exe")
	model := filepath.Join("..", "..", "resources", "whisper", "models", "ggml-base.en.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("whisper binary not present")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skip("whisper model not present")
	}

	logPath := filepath.Join(t.TempDir(), "whisper.log")
	srv := NewServer(bin, model, "127.0.0.1", 18099, logPath)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "whisper") {
		t.Fatalf("expected whisper output in log file, got %d bytes", len(data))
	}
}

func TestServerReportsChildExitWithoutWaitingForStartupTimeout(t *testing.T) {
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("test"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}

	// A Go test binary exits immediately when Server supplies whisper's unknown
	// -m/--host/--port flags. This exercises process supervision without needing
	// a second fixture executable.
	srv := NewServer(os.Args[0], model, "127.0.0.1", 18109, filepath.Join(t.TempDir(), "whisper.log"))
	started := time.Now()
	err := srv.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited before becoming ready") {
		t.Fatalf("expected early child-exit error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("child exit took %v to surface; expected immediate failure", elapsed)
	}
}

func TestCommandServerReportsNamedChildExit(t *testing.T) {
	srv := NewCommandServer(
		"Nemotron test",
		os.Args[0],
		[]string{"-definitely-not-a-go-test-flag"},
		[]string{os.Args[0]},
		"127.0.0.1",
		18119,
		filepath.Join(t.TempDir(), "nemotron.log"),
		30*time.Second,
	)
	started := time.Now()
	err := srv.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Nemotron test server exited before becoming ready") {
		t.Fatalf("expected named early child-exit error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("child exit took %v to surface; expected immediate failure", elapsed)
	}
}
