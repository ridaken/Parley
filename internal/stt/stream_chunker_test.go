package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestStreamingChunkerCoalescesDeltasAtWordLimit(t *testing.T) {
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
		t.Fatalf("short unpunctuated text emitted too early: %+v", segment)
	default:
	}

	chunker.Feed(audio.You, loud((streamTranscriptMaxWords-5)*frameSamples))
	chunker.flush(context.Background())
	if calls.Load() != streamTranscriptMaxWords-1 {
		t.Fatalf("feed calls = %d, want %d", calls.Load(), streamTranscriptMaxWords-1)
	}
	select {
	case segment := <-segments:
		t.Fatalf("text emitted before the word limit: %+v", segment)
	default:
	}

	chunker.Feed(audio.You, loud(frameSamples))
	chunker.flush(context.Background())
	if calls.Load() != streamTranscriptMaxWords {
		t.Fatalf("feed calls = %d, want %d", calls.Load(), streamTranscriptMaxWords)
	}
	select {
	case segment := <-segments:
		wantText := strings.TrimSpace(strings.Repeat("word ", streamTranscriptMaxWords))
		if segment.Text != wantText {
			t.Fatalf("coalesced text = %q", segment.Text)
		}
		wantEndMs := int64(streamTranscriptMaxWords) * window.Milliseconds()
		if segment.StartMs != 0 || segment.EndMs != wantEndMs {
			t.Fatalf("segment timing = %d/%d, want 0/%d", segment.StartMs, segment.EndMs, wantEndMs)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming chunker did not emit a coalesced segment")
	}
}

func TestStreamingChunkerEmitsCompleteSentence(t *testing.T) {
	segments := make(chan Segment, 1)
	chunker := NewStreamingChunker(nil, 320*time.Millisecond, func(segment Segment) {
		segments <- segment
	})
	chunker.buf[audio.You] = &streamState{}

	chunker.acceptDelta(audio.You, "Hello ", 320, false)
	chunker.acceptDelta(audio.You, "everyone, ", 640, false)
	chunker.acceptDelta(audio.You, "how are you ", 960, false)
	select {
	case segment := <-segments:
		t.Fatalf("partial sentence emitted too early: %+v", segment)
	default:
	}

	chunker.acceptDelta(audio.You, "today? ", 1280, false)
	select {
	case segment := <-segments:
		if segment.Text != "Hello everyone, how are you today?" {
			t.Fatalf("sentence text = %q", segment.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("completed sentence was not emitted")
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

func TestStreamingChunkerFlushesWithheldTailAfterPause(t *testing.T) {
	var calls atomic.Int32
	var finishes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text := "today?"
		if r.FormValue("action") == "feed" {
			call := calls.Add(1)
			if call == 1 {
				text = "Hello everyone, how are you "
			} else if finishes.Load() > 0 {
				text = "Thank you all for coming."
			} else {
				text = ""
			}
		} else {
			finishes.Add(1)
		}
		_ = json.NewEncoder(w).Encode(inferenceResponse{Text: text})
	}))
	defer server.Close()

	segments := make(chan Segment, 1)
	window := 320 * time.Millisecond
	chunker := NewStreamingChunker(NewClient(server.URL), window, func(segment Segment) {
		segments <- segment
	})
	frameSamples := int(window * time.Duration(audio.SampleRate) / time.Second)
	chunker.Feed(audio.You, loud(frameSamples))
	chunker.Feed(audio.You, make([]int16, 3*frameSamples))
	chunker.flush(context.Background())

	if calls.Load() != 4 {
		t.Fatalf("feed calls = %d, want voiced frame plus three silent frames", calls.Load())
	}
	if finishes.Load() != 1 {
		t.Fatalf("finish calls = %d, want pause to finish the stream once", finishes.Load())
	}
	select {
	case segment := <-segments:
		if segment.Text != "Hello everyone, how are you today?" {
			t.Fatalf("pause-finalized text = %q", segment.Text)
		}
		if segment.EndMs != 1280 {
			t.Fatalf("pause-finalized end = %d, want 1280", segment.EndMs)
		}
	case <-time.After(time.Second):
		t.Fatal("withheld final word was not emitted after sustained silence")
	}

	chunker.mu.Lock()
	started := chunker.buf[audio.You].started
	chunker.mu.Unlock()
	if started {
		t.Fatal("stream remained open after pause finalization")
	}

	chunker.Feed(audio.You, loud(frameSamples))
	chunker.flush(context.Background())
	select {
	case segment := <-segments:
		if segment.Text != "Thank you all for coming." {
			t.Fatalf("new utterance text = %q", segment.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("speech after the pause did not start a fresh stream")
	}
}

func TestSplitTranscriptHandlesSentenceQuotesAndRemainder(t *testing.T) {
	ready, remainder := splitTranscript("First sentence. Second sentence! “Are you ready?” trailing ", false)
	want := []string{"First sentence.", "Second sentence!", "“Are you ready?”"}
	if len(ready) != len(want) {
		t.Fatalf("ready = %#v, want %#v", ready, want)
	}
	for i := range want {
		if ready[i] != want[i] {
			t.Fatalf("ready[%d] = %q, want %q", i, ready[i], want[i])
		}
	}
	if remainder != "trailing " {
		t.Fatalf("remainder = %q, want trailing space preserved", remainder)
	}
}
