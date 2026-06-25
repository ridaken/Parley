package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteReturnsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.MaxTokens != defaultMaxTokens {
			t.Fatalf("max_tokens = %d, want %d", req.MaxTokens, defaultMaxTokens)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "pong"}},
			},
			"usage": map[string]any{
				"prompt_tokens":     7,
				"completion_tokens": 3,
				"total_tokens":      10,
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", "m")
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "ping"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "pong" {
		t.Fatalf("got %q, want pong", got)
	}
}

func TestCompleteParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: responseMessage{Role: "assistant", Content: "pong"}}},
			Usage:   Usage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16},
		})
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "m").Complete(context.Background(), []Message{{Role: "user", Content: "ping"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "pong" {
		t.Fatalf("got %q, want pong", got)
	}
}

func TestCompleteJSONRequestsJSONMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
			t.Fatalf("response_format not requested: %+v", req.ResponseFormat)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":true}`}}},
		})
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "m").CompleteJSON(context.Background(), []Message{{Role: "user", Content: "json"}})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("got %q", got)
	}
}

func TestCompleteJSONFallsBackWhenUnsupported(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if req.ResponseFormat == nil {
				t.Fatal("first call should request response_format")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`unsupported response_format`))
			return
		}
		if req.ResponseFormat != nil {
			t.Fatalf("fallback call should omit response_format: %+v", req.ResponseFormat)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":true}`}}},
		})
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "m").CompleteJSON(context.Background(), []Message{{Role: "user", Content: "json"}})
	if err != nil {
		t.Fatalf("CompleteJSON fallback: %v", err)
	}
	if got != `{"ok":true}` || calls != 2 {
		t.Fatalf("got %q after %d calls", got, calls)
	}
}

func TestCompleteFallsBackToReasoningContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":              "assistant",
					"content":           "",
					"reasoning_content": `{"currentTopicTitle":"Recovered"}`,
				},
			}},
		})
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "m").Complete(context.Background(), []Message{{Role: "user", Content: "ping"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != `{"currentTopicTitle":"Recovered"}` {
		t.Fatalf("got %q", got)
	}
}

func TestCompleteParsesContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "text", "text": "pong"},
					},
				},
			}},
		})
	}))
	defer srv.Close()

	got, err := NewClient(srv.URL, "", "m").Complete(context.Background(), []Message{{Role: "user", Content: "ping"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "pong" {
		t.Fatalf("got %q, want pong", got)
	}
}

func TestCompleteErrorsOnEmptyAssistantContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": ""},
			}},
		})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "", "m").Complete(context.Background(), []Message{{Role: "user", Content: "ping"}})
	if err == nil || !strings.Contains(err.Error(), "empty assistant content") || !strings.Contains(err.Error(), "finish_reason=stop") {
		t.Fatalf("expected empty content error with finish reason, got %v", err)
	}
}

func TestCompleteSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestCompleteSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "model not loaded"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestPingSucceedsOnHealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "pong"}}},
		})
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "", "m").Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "", "m").Ping(context.Background()); err == nil {
		t.Fatal("expected Ping to surface a server error")
	}
}

func TestBaseURLTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://x/v1/", "", "m")
	if c.baseURL != "http://x/v1" {
		t.Fatalf("baseURL = %q, want http://x/v1", c.baseURL)
	}
}
