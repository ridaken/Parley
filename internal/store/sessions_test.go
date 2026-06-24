package store

import "testing"

func TestSessionRoundTrip(t *testing.T) {
	s := openTemp(t)

	id, err := s.CreateSession("Standup", 0, "/tmp/audio")
	if err != nil || id == 0 {
		t.Fatalf("CreateSession: id=%d err=%v", id, err)
	}

	segs := []Segment{
		{Source: "You", Text: "Morning", StartMs: 0, EndMs: 1000},
		{Source: "Others", Text: "Hi there", StartMs: 1000, EndMs: 2000},
	}
	for _, seg := range segs {
		if err := s.AppendSegment(id, seg); err != nil {
			t.Fatalf("AppendSegment: %v", err)
		}
	}

	if err := s.SaveAnalysis(id, `{"current":{"title":"Greetings"}}`); err != nil {
		t.Fatalf("SaveAnalysis: %v", err)
	}

	note, err := s.AddLiveNote(id, LiveNote{Scope: "meeting", Text: "Acme = A-C-M-E"})
	if err != nil || note.ID == 0 || note.CreatedAt == "" {
		t.Fatalf("AddLiveNote: %+v err=%v", note, err)
	}

	if err := s.EndSession(id, "/tmp/audio"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	bundle, err := s.GetSessionBundle(id)
	if err != nil {
		t.Fatalf("GetSessionBundle: %v", err)
	}
	if len(bundle.Segments) != 2 || bundle.Segments[0].Text != "Morning" {
		t.Fatalf("segments = %+v", bundle.Segments)
	}
	if bundle.AnalysisJSON == "" {
		t.Fatalf("analysis not persisted")
	}
	if len(bundle.LiveNotes) != 1 || bundle.LiveNotes[0].Text != "Acme = A-C-M-E" {
		t.Fatalf("live notes = %+v", bundle.LiveNotes)
	}
	if bundle.Session.EndedAt == "" {
		t.Fatalf("ended_at not stamped")
	}

	list, err := s.ListSessions()
	if err != nil || len(list) != 1 || list[0].SegmentCount != 2 {
		t.Fatalf("ListSessions = %+v err=%v", list, err)
	}

	if err := s.DeleteSession(id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	list, _ = s.ListSessions()
	if len(list) != 0 {
		t.Fatalf("expected no sessions after delete, got %d", len(list))
	}
}
