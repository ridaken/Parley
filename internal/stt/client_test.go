package stt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientUsesLongerStreamFinishBudget(t *testing.T) {
	client := NewClient("http://127.0.0.1:1")
	if client.http.Timeout != 0 {
		t.Fatalf("HTTP client timeout = %s, want request-context timeouts", client.http.Timeout)
	}
	if standardRequestTimeout != 2*time.Minute {
		t.Fatalf("standard request timeout = %s", standardRequestTimeout)
	}
	if streamFinishTimeout != 5*time.Minute {
		t.Fatalf("stream finish timeout = %s", streamFinishTimeout)
	}
	if streamFinishTimeout <= standardRequestTimeout {
		t.Fatal("stream finalization must have a longer budget than ordinary requests")
	}
}

func TestStreamFinishHonorsEarlierCallerDeadline(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"text":"late"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewClient(server.URL).StreamFinish(ctx, "You")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StreamFinish error = %v, want caller deadline", err)
	}
	select {
	case <-requestStarted:
	default:
		t.Fatal("stream finish request never reached the server")
	}
}
