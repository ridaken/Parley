package analysis

import (
	"context"
	"encoding/json"
	"fmt"
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
		"\"suggestions\":[{\"kind\":\"question\",\"text\":\"By how much?\"}]," +
		"\"actionItems\":[{\"text\":\"Cut costs\",\"owner\":\"Sam\"}]}\n```"
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
	if len(res.ActionItems) != 1 || res.ActionItems[0].Text != "Cut costs" || res.ActionItems[0].Owner != "Sam" {
		t.Fatalf("action items wrong: %+v", res.ActionItems)
	}
}

func TestParseResultRejectsBadReplies(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"no braces":     "I'm not sure how to answer that.",
		"closing first": "}{ broken",
		"invalid json":  "here you go: {not: valid, json}",
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseResult(reply); err == nil {
				t.Fatalf("reply %q: expected an error, got nil", reply)
			}
		})
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

	eng.analyze(context.Background(), "Others: I think we slip to May.\n", priorView{}, nil)

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
	prompt := eng.buildUserPrompt("You: hi\n", priorView{}, eng.snapshotNotes())
	if !contains(prompt, "Acme") || !contains(prompt, "margins") {
		t.Fatalf("expected both notes in prompt:\n%s", prompt)
	}

	// Simulate a topic change: topic-scoped notes must expire, meeting ones stay.
	eng.mu.Lock()
	eng.dropTopicNotesLocked()
	eng.state.Current = Topic{Title: "Timeline"}
	eng.mu.Unlock()

	prompt = eng.buildUserPrompt("You: hi\n", priorView{title: "Pricing"}, eng.snapshotNotes())
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
	prompt := eng.buildUserPrompt("You: hi\n", priorView{}, eng.snapshotNotes())
	if !contains(prompt, "Acme") || !contains(prompt, "opex") {
		t.Fatalf("restored notes missing from prompt:\n%s", prompt)
	}
}

func TestEngineKeepsStableTitleWhenTopicUnchanged(t *testing.T) {
	// The model establishes a title, then on the next pass rewords it but reports
	// topicChanged:false. The engine must keep the original wording and not archive.
	replies := []string{
		`{"currentTopicTitle":"Budget planning","currentTopicSummary":"a","topicChanged":false,"assertions":[],"suggestions":[]}`,
		`{"currentTopicTitle":"Planning the budget","currentTopicSummary":"b","topicChanged":false,"assertions":[],"suggestions":[]}`,
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

	done := make(chan State, 2)
	eng := NewEngine(llm.NewClient(srv.URL, "", "m"), 3*time.Second, Context{}, func(s State) { done <- s })

	eng.analyze(context.Background(), "Others: line one\n", priorView{}, nil)
	<-done // establishes "Budget planning"

	eng.analyze(context.Background(), "Others: line two\n", priorView{title: "Budget planning"}, nil)
	s := <-done
	if s.Current.Title != "Budget planning" {
		t.Fatalf("title churned to %q, want stable %q", s.Current.Title, "Budget planning")
	}
	if s.Current.Summary != "b" {
		t.Fatalf("summary should still update: %q", s.Current.Summary)
	}
	if len(s.Past) != 0 {
		t.Fatalf("unchanged topic must not be archived: %+v", s.Past)
	}
}

func TestFeedThenAnalyzeEmitsAndThenSkipsWithoutNewContent(t *testing.T) {
	content := `{"currentTopicTitle":"Onboarding","currentTopicSummary":"New hire setup.","topicChanged":true,"assertions":[{"speaker":"You","text":"Need a laptop."}],"suggestions":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	emits := make(chan State, 4)
	eng := NewEngine(llm.NewClient(srv.URL, "", "m"), 3*time.Second, Context{}, func(s State) { emits <- s })

	eng.Feed("You", "I need a laptop for the new hire")
	eng.Feed("Others", "I'll order one")
	eng.maybeAnalyze(context.Background())

	select {
	case s := <-emits:
		if s.Current.Title != "Onboarding" {
			t.Fatalf("title = %q", s.Current.Title)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no analysis emitted after Feed + maybeAnalyze")
	}

	// analyzedLen now equals the transcript length, so another tick with no new
	// lines must be a no-op (avoids re-billing the LLM on a quiet meeting).
	eng.maybeAnalyze(context.Background())
	select {
	case <-emits:
		t.Fatal("unexpected re-analysis with no new content")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestFeedIgnoresBlankLines(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	eng.Feed("You", "   ")
	eng.Feed("You", "")
	eng.mu.Lock()
	n := len(eng.transcript)
	eng.mu.Unlock()
	if n != 0 {
		t.Fatalf("blank lines should be dropped, transcript has %d", n)
	}
}

func TestRecentWindowCapsAtPromptWindow(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	for i := 0; i < promptWindowLines+10; i++ {
		eng.Feed("You", "a line of talk")
	}
	eng.mu.Lock()
	window := eng.recentWindowLocked()
	eng.mu.Unlock()

	lines := 0
	for _, r := range window {
		if r == '\n' {
			lines++
		}
	}
	if lines != promptWindowLines {
		t.Fatalf("window has %d lines, want %d", lines, promptWindowLines)
	}
}

func TestMergeActionItemsAccumulatesDedupsAndBackfillsOwner(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)

	eng.mergeActionItemsLocked([]ActionItem{{Text: "Send the budget deck"}})
	eng.mergeActionItemsLocked([]ActionItem{
		{Text: "send the budget deck.", Owner: "Sam"}, // same item (normalized) + owner
		{Text: "Book the room"},                       // genuinely new
		{Text: "   "},                                 // blank, ignored
	})

	if len(eng.state.ActionItems) != 2 {
		t.Fatalf("want 2 items after dedup, got %d: %+v", len(eng.state.ActionItems), eng.state.ActionItems)
	}
	if eng.state.ActionItems[0].Owner != "Sam" {
		t.Fatalf("owner not backfilled: %+v", eng.state.ActionItems[0])
	}

	// A later, differing owner must not overwrite an owner already set.
	eng.mergeActionItemsLocked([]ActionItem{{Text: "Send the budget deck", Owner: "Alex"}})
	if eng.state.ActionItems[0].Owner != "Sam" {
		t.Fatalf("non-empty owner was overwritten: %+v", eng.state.ActionItems[0])
	}
}

func TestActionItemsCapAtMax(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	for i := 0; i < maxActionItems+10; i++ {
		eng.mergeActionItemsLocked([]ActionItem{{Text: fmt.Sprintf("task %d", i)}})
	}
	if len(eng.state.ActionItems) != maxActionItems {
		t.Fatalf("want cap %d, got %d", maxActionItems, len(eng.state.ActionItems))
	}
	last := eng.state.ActionItems[len(eng.state.ActionItems)-1]
	if last.Text != fmt.Sprintf("task %d", maxActionItems+9) {
		t.Fatalf("newest item should be retained, got %q", last.Text)
	}
	if eng.state.ActionItems[0].Text == "task 0" {
		t.Fatalf("oldest item should have been dropped")
	}
}

func TestActionItemsSurviveTopicChange(t *testing.T) {
	replies := []string{
		`{"currentTopicTitle":"Topic A","topicChanged":false,"assertions":[],"suggestions":[],"actionItems":[{"text":"Email the vendor","owner":"Dana"}]}`,
		`{"currentTopicTitle":"Topic B","topicChanged":true,"assertions":[{"speaker":"You","text":"new point"}],"suggestions":[],"actionItems":[]}`,
	}
	var idx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": replies[idx%len(replies)]}}},
		})
		idx++
	}))
	defer srv.Close()

	done := make(chan State, 2)
	eng := NewEngine(llm.NewClient(srv.URL, "", "m"), 3*time.Second, Context{}, func(s State) { done <- s })

	eng.analyze(context.Background(), "Others: a\n", priorView{}, nil)
	<-done
	eng.analyze(context.Background(), "Others: b\n", priorView{title: "Topic A"}, nil)
	s := <-done

	if s.Current.Title != "Topic B" || len(s.Past) != 1 {
		t.Fatalf("topic change should archive Topic A: cur=%q past=%+v", s.Current.Title, s.Past)
	}
	if len(s.ActionItems) != 1 || s.ActionItems[0].Text != "Email the vendor" || s.ActionItems[0].Owner != "Dana" {
		t.Fatalf("action item must survive topic change: %+v", s.ActionItems)
	}
}

func TestBuildUserPromptIncludesUnderstanding(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	prior := priorView{
		title:      "Budget",
		summary:    "Discussing Q3 spend.",
		assertions: []Assertion{{Speaker: "Others", Text: "we are over budget"}},
		actionItems: []ActionItem{
			{Text: "Send deck"},
			{Text: "Approve PO", Owner: "Lee"},
		},
	}
	prompt := eng.buildUserPrompt("You: hi\n", prior, nil)
	for _, want := range []string{
		"CURRENT UNDERSTANDING SO FAR",
		"Discussing Q3 spend.",
		"- Others: we are over budget",
		"Send deck [owner: unassigned]",
		"Approve PO [owner: Lee]",
	} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRestoreRehydratesActionItems(t *testing.T) {
	eng := NewEngine(nil, 3*time.Second, Context{}, nil)
	eng.Restore(State{
		Current:     Topic{Title: "Budget"},
		ActionItems: []ActionItem{{Text: "Send deck", Owner: "Sam"}},
	}, nil, nil)
	if len(eng.state.ActionItems) != 1 || eng.state.ActionItems[0].Owner != "Sam" {
		t.Fatalf("action items not rehydrated: %+v", eng.state.ActionItems)
	}
}

func TestStartStopIsClean(t *testing.T) {
	// Unreachable endpoint is fine: with no transcript fed, the loop never calls
	// the LLM. This exercises Start/loop/Stop teardown without deadlocking.
	eng := NewEngine(llm.NewClient("http://127.0.0.1:0", "", "m"), 3*time.Second, Context{}, func(State) {})
	eng.Start()
	eng.Stop()
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

	eng.analyze(context.Background(), "Others: line one\n", priorView{}, nil)
	<-done // Topic A

	eng.analyze(context.Background(), "Others: line two\n", priorView{title: "Topic A"}, nil)
	s := <-done // Topic B, A archived

	if s.Current.Title != "Topic B" {
		t.Fatalf("current = %q", s.Current.Title)
	}
	if len(s.Past) != 1 || s.Past[0].Title != "Topic A" {
		t.Fatalf("past = %+v", s.Past)
	}
}
