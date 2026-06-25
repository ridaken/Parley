package main

import (
	"testing"
	"time"

	"github.com/tomvokac/parley/internal/store"
)

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
