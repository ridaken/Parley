package stt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
