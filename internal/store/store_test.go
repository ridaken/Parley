package store

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSettingsDefaultsAndUpdate(t *testing.T) {
	s := openTemp(t)

	got, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.LLMBaseURL == "" || got.AnalysisIntervalSec == 0 || got.AnalysisTimeoutSec == 0 {
		t.Fatalf("expected sane defaults, got %+v", got)
	}
	if got.LoggingLevel != "trace" {
		t.Fatalf("logging level default = %q, want trace", got.LoggingLevel)
	}
	if got.SttEngine != "auto" {
		t.Fatalf("transcription engine default = %q, want auto", got.SttEngine)
	}

	got.LLMModel = "qwen2.5"
	got.AnalysisIntervalSec = 20
	got.AnalysisTimeoutSec = 45
	got.LoggingLevel = "error"
	got.SttEngine = "whisper"
	got.WhisperModel = "ggml-base.en.bin"
	if err := s.SaveSettings(got); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	again, _ := s.GetSettings()
	if again.LLMModel != "qwen2.5" || again.AnalysisIntervalSec != 20 || again.AnalysisTimeoutSec != 45 || again.LoggingLevel != "error" || again.SttEngine != "whisper" || again.WhisperModel != "ggml-base.en.bin" {
		t.Fatalf("settings not persisted: %+v", again)
	}
}

func TestLegacyRemoteSettingsMigrateToExternalEngine(t *testing.T) {
	s := openTemp(t)
	if _, err := s.db.Exec(`UPDATE settings SET stt_engine = '', stt_base_url = 'http://stt.local:8765' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE settings
		SET stt_engine = CASE WHEN TRIM(stt_base_url) <> '' THEN 'external' ELSE 'auto' END
		WHERE TRIM(stt_engine) = ''`); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.SttEngine != "external" {
		t.Fatalf("legacy remote engine = %q, want external", got.SttEngine)
	}
}

func TestProfileCRUD(t *testing.T) {
	s := openTemp(t)

	saved, err := s.SaveProfile(Profile{Name: "Sync", Summary: "Weekly", People: "A,B"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if saved.ID == 0 || saved.UpdatedAt == "" {
		t.Fatalf("expected id+timestamp, got %+v", saved)
	}

	saved.Notes = "added notes"
	if _, err := s.SaveProfile(saved); err != nil {
		t.Fatalf("update: %v", err)
	}

	list, err := s.ListProfiles()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListProfiles = %d (%v)", len(list), err)
	}
	if list[0].Notes != "added notes" {
		t.Fatalf("update not persisted: %+v", list[0])
	}

	if err := s.DeleteProfile(saved.ID); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	list, _ = s.ListProfiles()
	if len(list) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(list))
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	// Use an in-memory keychain, reset per test, so API-key paths don't touch
	// (or depend on) the real OS keychain and don't leak keys across tests.
	keyring.MockInit()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
