package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomvokac/parley/internal/audio"
)

func mockStreamingServer(t *testing.T, feedText, finishText string, feedCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("stream_id") == "" {
			http.Error(w, "missing stream_id", http.StatusBadRequest)
			return
		}
		text := finishText
		if r.FormValue("action") == "feed" {
			feedCalls.Add(1)
			text = feedText
		}
		_ = json.NewEncoder(w).Encode(inferenceResponse{Text: text})
	}))
}

func TestStreamingChunkerCoalescesDeltasAtOneSecondCadence(t *testing.T) {
	var calls atomic.Int32
	server := mockStreamingServer(t, "word ", "", &calls)
	defer server.Close()

	segments := make(chan Segment, 1)
	window := 320 * time.Millisecond
	chunker := NewStreamingChunker(NewClient(server.URL), window, func(segment Segment) {
		segments <- segment
	})
	frameSamples := int(window * time.Duration(audio.SampleRate) / time.Second)
	chunker.Feed(audio.You, loud(4*frameSamples))
	chunker.flush(context.Background())

	if calls.Load() != 4 {
		t.Fatalf("feed calls = %d, want 4", calls.Load())
	}
	select {
	case segment := <-segments:
		if segment.Text != "word word word word" {
			t.Fatalf("coalesced text = %q", segment.Text)
		}
		if segment.StartMs != 0 || segment.EndMs != 1280 {
			t.Fatalf("segment timing = %d/%d, want 0/1280", segment.StartMs, segment.EndMs)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming chunker did not emit a coalesced segment")
	}
}

func TestStreamingChunkerDoesNotOpenStreamForLeadingSilence(t *testing.T) {
	var calls atomic.Int32
	server := mockStreamingServer(t, "hello ", "", &calls)
	defer server.Close()

	window := 320 * time.Millisecond
	chunker := NewStreamingChunker(NewClient(server.URL), window, func(Segment) {})
	frameSamples := int(window * time.Duration(audio.SampleRate) / time.Second)
	chunker.Feed(audio.You, make([]int16, frameSamples))
	chunker.flush(context.Background())
	if calls.Load() != 0 {
		t.Fatalf("leading silence opened a model stream; calls = %d", calls.Load())
	}

	chunker.Feed(audio.You, loud(frameSamples))
	chunker.flush(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("voiced audio did not open model stream; calls = %d", calls.Load())
	}
}

func TestStreamingChunkerFlushesTailOnFinish(t *testing.T) {
	var calls atomic.Int32
	server := mockStreamingServer(t, "hello ", "world.", &calls)
	defer server.Close()

	segments := make(chan Segment, 1)
	window := 320 * time.Millisecond
	chunker := NewStreamingChunker(NewClient(server.URL), window, func(segment Segment) {
		segments <- segment
	})
	frameSamples := int(window * time.Duration(audio.SampleRate) / time.Second)
	chunker.Feed(audio.Others, loud(frameSamples))
	chunker.flush(context.Background())
	chunker.finishStreams(context.Background())

	select {
	case segment := <-segments:
		if segment.Text != "hello world." {
			t.Fatalf("final text = %q", segment.Text)
		}
		if segment.Source != audio.Others || segment.EndMs != 320 {
			t.Fatalf("unexpected final segment: %+v", segment)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming tail was not emitted")
	}
}
