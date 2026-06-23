package store

import (
	"path/filepath"
	"testing"
)

func TestSettingsDefaultsAndUpdate(t *testing.T) {
	s := openTemp(t)

	got, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.LLMBaseURL == "" || got.AnalysisIntervalSec == 0 {
		t.Fatalf("expected sane defaults, got %+v", got)
	}

	got.LLMModel = "qwen2.5"
	got.AnalysisIntervalSec = 20
	if err := s.SaveSettings(got); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	again, _ := s.GetSettings()
	if again.LLMModel != "qwen2.5" || again.AnalysisIntervalSec != 20 {
		t.Fatalf("settings not persisted: %+v", again)
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
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
