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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "pong"}},
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

func TestBaseURLTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://x/v1/", "", "m")
	if c.baseURL != "http://x/v1" {
		t.Fatalf("baseURL = %q, want http://x/v1", c.baseURL)
	}
}
