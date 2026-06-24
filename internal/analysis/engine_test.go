package analysis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tomvokac/parley/internal/llm"
)

func TestParseResult(t *testing.T) {
	// Model wraps JSON in prose/markdown — parser must still extract it.
	reply := "Sure!\n```json\n{\"currentTopicTitle\":\"Budget\",\"topicChanged\":true," +
		"\"assertions\":[{\"speaker\":\"Others\",\"text\":\"Q3 is over budget\"}]," +
		"\"suggestions\":[{\"kind\":\"question\",\"text\":\"By how much?\"}]}\n```"
	res, err := parseResult(reply)
	if err != nil {
		t.Fatalf("parseResult: %v", err)
	}
	if res.CurrentTopicTitle != "Budget" || !res.TopicChanged {
		t.Fatalf("unexpected: %+v", res)
	}
	if len(res.Assertions) != 1 || res.Assertions[0].Speaker != "Others" {
		t.Fatalf("assertions wrong: %+v", res.Assertions)
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0].Kind != "question" {
		t.Fatalf("suggestions wrong: %+v", res.Suggestions)
	}
}

func TestEngineEmitsAnalysis(t *testing.T) {
	// Mock OpenAI-compatible endpoint returning a fixed analysis JSON.
	content := `{"currentTopicTitle":"Project timeline","currentTopicSummary":"Discussing the launch date.","topicChanged":false,"assertions":[{"speaker":"Others","text":"We slip to May."}],"suggestions":[{"kind":"clarification","text":"Which features are cut?"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL, "", "test-model")
	got := make(chan State, 1)
	eng := NewEngine(client, 3*time.Second, Context{Summary: "test"}, func(s State) {
		select {
		case got <- s:
		default:
		}
	})

	eng.analyze(context.Background(), "Others: I think we slip to May.\n", "", nil)

	select {
	case s := <-got:
		if s.Current.Title != "Project timeline" {
			t.Fatalf("title = %q", s.Current.Title)
		}
		if len(s.Current.Assertions) != 1 || s.Current.Assertions[0].Text != "We slip to May." {
			t.Fatalf("assertions = %+v", s.Current.Assertions)
		}
		if len(s.Suggestions) != 1 || s.Suggestions[0].Kind != "clarification" {
			t.Fatalf("suggestions = %+v", s.Suggestions)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for analysis emit")
	}
}

func TestLiveNoteScoping(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	eng.state.Current = Topic{Title: "Pricing"}

	eng.AddLiveNote(ScopeMeeting, "Client is Acme")
	eng.AddLiveNote(ScopeTopic, "Topic is margins, not revenue")

	// Both notes apply while "Pricing" is current.
	prompt := eng.buildUserPrompt("You: hi\n", "", eng.snapshotNotes())
	if !contains(prompt, "Acme") || !contains(prompt, "margins") {
		t.Fatalf("expected both notes in prompt:\n%s", prompt)
	}

	// Simulate a topic change: topic-scoped notes must expire, meeting ones stay.
	eng.mu.Lock()
	eng.dropTopicNotesLocked()
	eng.state.Current = Topic{Title: "Timeline"}
	eng.mu.Unlock()

	prompt = eng.buildUserPrompt("You: hi\n", "Pricing", eng.snapshotNotes())
	if !contains(prompt, "Acme") {
		t.Fatalf("meeting note should persist:\n%s", prompt)
	}
	if contains(prompt, "margins") {
		t.Fatalf("topic note should have expired:\n%s", prompt)
	}
}

func (e *Engine) snapshotNotes() []LiveNote {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.liveNotesForPromptLocked()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRestoreSeedsStateWithoutReanalyzing(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)

	state := State{
		Current: Topic{Title: "Budget", Summary: "Q3 numbers"},
		Past:    []Topic{{Title: "Intro"}},
	}
	notes := []LiveNote{
		{Scope: ScopeMeeting, Text: "Client is Acme"},
		{Scope: ScopeTopic, TopicTitle: "Budget", Text: "focus on opex"},
	}
	history := []struct{ Speaker, Text string }{
		{"You", "Let's talk budget"},
		{"Others", "Sure"},
	}

	eng.Restore(state, notes, history)

	if eng.state.Current.Title != "Budget" || len(eng.state.Past) != 1 {
		t.Fatalf("state not restored: %+v", eng.state)
	}
	// analyzedLen must equal the restored transcript length so the next tick does
	// not regenerate topics already present (no new content yet).
	if eng.analyzedLen != len(history) || len(eng.transcript) != len(history) {
		t.Fatalf("analyzedLen=%d transcript=%d, want %d", eng.analyzedLen, len(eng.transcript), len(history))
	}
	// Both notes are in effect while "Budget" is current.
	prompt := eng.buildUserPrompt("You: hi\n", "", eng.snapshotNotes())
	if !contains(prompt, "Acme") || !contains(prompt, "opex") {
		t.Fatalf("restored notes missing from prompt:\n%s", prompt)
	}
}

func TestEngineArchivesPastTopic(t *testing.T) {
	replies := []string{
		`{"currentTopicTitle":"Topic A","topicChanged":false,"assertions":[],"suggestions":[]}`,
		`{"currentTopicTitle":"Topic B","topicChanged":true,"assertions":[],"suggestions":[]}`,
	}
	var idx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := replies[idx%len(replies)]
		idx++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	client := llm.NewClient(srv.URL, "", "m")
	done := make(chan State, 2)
	eng := NewEngine(client, 3*time.Second, Context{}, func(s State) { done <- s })

	eng.analyze(context.Background(), "Others: line one\n", "", nil)
	<-done // Topic A

	eng.analyze(context.Background(), "Others: line two\n", "Topic A", nil)
	s := <-done // Topic B, A archived

	if s.Current.Title != "Topic B" {
		t.Fatalf("current = %q", s.Current.Title)
	}
	if len(s.Past) != 1 || s.Past[0].Title != "Topic A" {
		t.Fatalf("past = %+v", s.Past)
	}
}
