// Package diagnostics writes local JSONL debug records for failures that need
// more context than the human-readable app log should carry.
package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	LevelTrace = "trace"
	LevelError = "error"
	LevelNone  = "none"
)

// AnalysisFailure is a structured record for a failed live-analysis pass.
type AnalysisFailure struct {
	Type           string    `json:"type"`
	Timestamp      time.Time `json:"timestamp"`
	SessionID      int64     `json:"sessionID,omitempty"`
	SessionTitle   string    `json:"sessionTitle,omitempty"`
	ConnectionName string    `json:"connectionName,omitempty"`
	BaseURL        string    `json:"baseURL,omitempty"`
	Model          string    `json:"model,omitempty"`
	Kind           string    `json:"kind"`
	Error          string    `json:"error"`
	Attempt        int       `json:"attempt"`
	MaxAttempts    int       `json:"maxAttempts"`
	SkippedWindow  bool      `json:"skippedWindow"`
	TargetLen      int       `json:"targetLen"`
	ElapsedMs      int64     `json:"elapsedMs,omitempty"`
	Request        any       `json:"request,omitempty"`
	Response       string    `json:"response,omitempty"`
	ErrorDetails   any       `json:"errorDetails,omitempty"`
}

// FrontendError is a browser/WebView exception forwarded from the React app.
type FrontendError struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Source    string    `json:"source,omitempty"`
	Stack     string    `json:"stack,omitempty"`
}

func NormalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelError:
		return LevelError
	case LevelNone:
		return LevelNone
	default:
		return LevelTrace
	}
}

func LogAnalysisFailure(dir, level string, event AnalysisFailure) error {
	level = NormalizeLevel(level)
	if level == LevelNone {
		return nil
	}
	event.Type = "analysis_failure"
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if level != LevelTrace {
		event.Request = nil
		event.Response = ""
	}
	return appendJSONL(filepath.Join(dir, "analysis-failures.jsonl"), event)
}

func LogFrontendError(dir, level string, event FrontendError) error {
	level = NormalizeLevel(level)
	if level == LevelNone {
		return nil
	}
	event.Type = "frontend_error"
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if level != LevelTrace {
		event.Stack = ""
	}
	return appendJSONL(filepath.Join(dir, "app-errors.jsonl"), event)
}

func appendJSONL(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
