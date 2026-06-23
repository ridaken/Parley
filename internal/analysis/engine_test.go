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

	eng.analyze(context.Background(), "Others: I think we slip to May.\n", "")

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

	eng.analyze(context.Background(), "Others: line one\n", "")
	<-done // Topic A

	eng.analyze(context.Background(), "Others: line two\n", "Topic A")
	s := <-done // Topic B, A archived

	if s.Current.Title != "Topic B" {
		t.Fatalf("current = %q", s.Current.Title)
	}
	if len(s.Past) != 1 || s.Past[0].Title != "Topic A" {
		t.Fatalf("past = %+v", s.Past)
	}
}
