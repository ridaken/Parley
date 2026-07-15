package export

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tomvokac/parley/internal/analysis"
	"github.com/tomvokac/parley/internal/store"
)

func TestMarkdownFullState(t *testing.T) {
	state := analysis.State{
		Summary: "- The team agreed to move launch planning forward.\n- Budget remains unresolved.",
		Current: analysis.Topic{
			Title:   "Launch",
			Summary: "The current discussion is about release timing.",
			Assertions: []analysis.Assertion{
				{Speaker: "Others", Text: "The beta can start in July."},
			},
		},
		Past: []analysis.Topic{
			{
				Title:   "Budget",
				Summary: "The team reviewed Q3 spend.",
				Assertions: []analysis.Assertion{
					{Speaker: "You", Text: "Hosting cost needs a cap."},
				},
			},
		},
		ActionItems: []analysis.ActionItem{
			{Text: "Send the launch plan", Owner: "Dana"},
			{Text: "Price the LLM host"},
		},
	}
	md := Markdown(bundle(t, state))

	for _, want := range []string{
		"# Planning - Jun 24, 2026",
		"## Summary",
		"- Budget remains unresolved.",
		"## Action items",
		"- [ ] Send the launch plan - Dana",
		"- [ ] Price the LLM host - unassigned",
		"## Topics covered",
		"### Launch",
		"**Others:** The beta can start in July.",
		"### Budget",
		"**You:** Hosting cost needs a cap.",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "transcript") {
		t.Fatalf("raw transcript should not be exported:\n%s", md)
	}
}

func TestMarkdownEmptyState(t *testing.T) {
	md := Markdown(store.SessionBundle{Session: store.Session{Title: "Empty", StartedAt: "2026-06-24T12:00:00Z"}})
	for _, want := range []string{
		"_No summary yet._",
		"_No action items captured._",
		"_No topics captured._",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestMarkdownDerivesSummaryFromTopicOutline(t *testing.T) {
	state := analysis.State{
		Past:    []analysis.Topic{{Title: "Authentication", Summary: "JWT issuance was agreed."}},
		Current: analysis.Topic{Title: "Origins", Points: []string{"Portal access is required.", "Admin access is unresolved."}},
	}
	md := Markdown(bundle(t, state))
	for _, want := range []string{
		"- **Authentication:** JWT issuance was agreed.",
		"- **Origins:** Portal access is required. Admin access is unresolved.",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("derived outline summary missing %q:\n%s", want, md)
		}
	}
}

func TestMarkdownPreservesSpecialCharacters(t *testing.T) {
	state := analysis.State{
		Summary: "- Discussed R&D, C++, and budget < $5k.",
		Current: analysis.Topic{
			Title:   "R&D / C++",
			Summary: "Use symbols as spoken: <, >, &, and #.",
			Assertions: []analysis.Assertion{
				{Text: "Keep #1 priority unchanged."},
			},
		},
	}
	md := Markdown(bundle(t, state))
	for _, want := range []string{"R&D, C++", "budget < $5k", "Use symbols as spoken", "Keep #1 priority unchanged."} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestTranscriptMarkdownIncludesCapturedContextAndEverySegment(t *testing.T) {
	b := store.SessionBundle{
		Session: store.Session{Title: "Architecture", StartedAt: "2026-06-24T15:04:05Z"},
		ContextSnapshot: store.ContextSnapshot{
			Captured: true,
			Summary:  "Review authentication design",
			People:   "Dana; Lee",
			Notes:    "JWTs are issued by the portal",
		},
		Segments: []store.Segment{
			{Source: "You", Text: "Which origins are allowed?", StartMs: 3_000},
			{Source: "Others", Text: "Portal and admin.", StartMs: 65_000},
		},
		AnalysisJSON: `{"summary":"must not appear"}`,
		LiveNotes:    []store.LiveNote{{Text: "must not appear either"}},
	}
	md := TranscriptMarkdown(b)
	for _, want := range []string{
		"## Meeting context", "### Summary / agenda", "Review authentication design",
		"### People", "Dana; Lee", "### Notes", "JWTs are issued by the portal",
		"## Transcript", "[00:03] **You:** Which origins are allowed?", "[01:05] **Others:** Portal and admin.",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("transcript markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "must not appear") {
		t.Fatalf("transcript export included analysis or live notes:\n%s", md)
	}
}

func TestTranscriptMarkdownDistinguishesNoContextFromLegacySession(t *testing.T) {
	noContext := TranscriptMarkdown(store.SessionBundle{ContextSnapshot: store.ContextSnapshot{Captured: true}})
	if !strings.Contains(noContext, "No pre-meeting context was provided") {
		t.Fatalf("new no-context session message missing:\n%s", noContext)
	}
	legacy := TranscriptMarkdown(store.SessionBundle{})
	if !strings.Contains(legacy, "unavailable for this legacy session") {
		t.Fatalf("legacy context message missing:\n%s", legacy)
	}
}

func bundle(t *testing.T, state analysis.State) store.SessionBundle {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return store.SessionBundle{
		Session: store.Session{
			Title:     "Planning",
			StartedAt: "2026-06-24T15:04:05Z",
		},
		AnalysisJSON: string(data),
		Segments: []store.Segment{
			{Source: "Others", Text: "This should not appear."},
		},
	}
}
