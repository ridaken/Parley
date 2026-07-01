package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAnalysisFailureHonorsLevels(t *testing.T) {
	dir := t.TempDir()
	event := AnalysisFailure{
		Kind:     "parse",
		Error:    "bad JSON",
		Attempt:  1,
		Request:  []map[string]string{{"role": "user", "content": "prompt"}},
		Response: "raw reply",
	}
	if err := LogAnalysisFailure(dir, "trace", event); err != nil {
		t.Fatalf("trace log: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "analysis-failures.jsonl"))
	if err != nil {
		t.Fatalf("read trace log: %v", err)
	}
	if !strings.Contains(string(data), "prompt") || !strings.Contains(string(data), "raw reply") {
		t.Fatalf("trace log should include request/response: %s", data)
	}
	if strings.Contains(string(data), "maxAttempts") || strings.Contains(string(data), "skippedWindow") {
		t.Fatalf("trace log should omit empty legacy retry fields: %s", data)
	}

	dir = t.TempDir()
	if err := LogAnalysisFailure(dir, "error", event); err != nil {
		t.Fatalf("error log: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "analysis-failures.jsonl"))
	if err != nil {
		t.Fatalf("read error log: %v", err)
	}
	if strings.Contains(string(data), "prompt") || strings.Contains(string(data), "raw reply") {
		t.Fatalf("error log should omit request/response: %s", data)
	}

	dir = t.TempDir()
	if err := LogAnalysisFailure(dir, "none", event); err != nil {
		t.Fatalf("none log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "analysis-failures.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("none should not write a log, stat err=%v", err)
	}
}

func TestLogFrontendErrorHonorsLevels(t *testing.T) {
	dir := t.TempDir()
	if err := LogFrontendError(dir, "error", FrontendError{Message: "boom", Stack: "stack"}); err != nil {
		t.Fatalf("frontend log: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "app-errors.jsonl"))
	if err != nil {
		t.Fatalf("read frontend log: %v", err)
	}
	if !strings.Contains(string(data), "boom") || strings.Contains(string(data), "stack") {
		t.Fatalf("error-level frontend log should include message but omit stack: %s", data)
	}
}
