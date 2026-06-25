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

// mockWhisper returns a server that replies with a fixed transcription and counts
// how many inference requests it received.
func mockWhisper(t *testing.T, text string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		_ = json.NewEncoder(w).Encode(inferenceResponse{Text: text})
	}))
}

func loud(n int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = 6000
	}
	return s
}

func TestChunkerTranscribesAndTimesWindow(t *testing.T) {
	srv := mockWhisper(t, "hello world", nil)
	defer srv.Close()

	segs := make(chan Segment, 4)
	// Large window so only the final flush (on Stop) runs — deterministic.
	c := NewChunker(NewClient(srv.URL), time.Hour, func(s Segment) { segs <- s })
	c.Start()

	c.Feed(audio.You, loud(audio.SampleRate)) // exactly 1 second of audio
	c.Stop()

	select {
	case seg := <-segs:
		if seg.Source != audio.You || seg.Text != "hello world" {
			t.Fatalf("unexpected segment: %+v", seg)
		}
		if seg.StartMs != 0 || seg.EndMs != 1000 {
			t.Fatalf("timeline wrong: start=%d end=%d (want 0/1000)", seg.StartMs, seg.EndMs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no segment produced by final flush")
	}
}

func TestChunkerSkipsSilence(t *testing.T) {
	var calls atomic.Int32
	srv := mockWhisper(t, "should not happen", &calls)
	defer srv.Close()

	segs := make(chan Segment, 1)
	c := NewChunker(NewClient(srv.URL), time.Hour, func(s Segment) { segs <- s })
	c.Start()

	c.Feed(audio.You, make([]int16, audio.SampleRate)) // all-zero = silence
	c.Stop()

	if calls.Load() != 0 {
		t.Fatalf("silent chunk should not be transcribed; got %d calls", calls.Load())
	}
	select {
	case seg := <-segs:
		t.Fatalf("unexpected segment from silence: %+v", seg)
	default:
	}
}

func TestChunkerDoesNotOverlapSlowTranscriptions(t *testing.T) {
	var current atomic.Int32
	var maxConcurrent atomic.Int32
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := current.Add(1)
		for {
			max := maxConcurrent.Load()
			if now <= max || maxConcurrent.CompareAndSwap(max, now) {
				break
			}
		}
		calls.Add(1)
		time.Sleep(100 * time.Millisecond)
		current.Add(-1)
		_ = json.NewEncoder(w).Encode(inferenceResponse{Text: "hello"})
	}))
	defer srv.Close()

	c := NewChunker(NewClient(srv.URL), 20*time.Millisecond, func(Segment) {})
	c.Start()
	c.Feed(audio.You, loud(audio.SampleRate))
	time.Sleep(30 * time.Millisecond)
	c.Feed(audio.Others, loud(audio.SampleRate))
	time.Sleep(250 * time.Millisecond)
	c.Stop()

	if calls.Load() < 2 {
		t.Fatalf("expected at least 2 transcription calls, got %d", calls.Load())
	}
	if maxConcurrent.Load() != 1 {
		t.Fatalf("transcriptions overlapped: max concurrent = %d", maxConcurrent.Load())
	}
}

func TestChunkerContinuesAfterTranscriptionError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(inferenceResponse{Text: "recovered"})
	}))
	defer srv.Close()

	segs := make(chan Segment, 1)
	c := NewChunker(NewClient(srv.URL), time.Hour, func(s Segment) { segs <- s })

	c.Feed(audio.You, loud(audio.SampleRate))
	c.flush(context.Background())
	select {
	case seg := <-segs:
		t.Fatalf("failed transcription should not emit a segment: %+v", seg)
	default:
	}

	c.Feed(audio.You, loud(audio.SampleRate))
	c.flush(context.Background())
	select {
	case seg := <-segs:
		if seg.Text != "recovered" {
			t.Fatalf("unexpected recovered segment: %+v", seg)
		}
		if seg.StartMs != 1000 || seg.EndMs != 2000 {
			t.Fatalf("timeline should advance past failed audio: start=%d end=%d", seg.StartMs, seg.EndMs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("chunker did not recover after a transcription error")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestPeakAmplitude(t *testing.T) {
	if got := peakAmplitude([]int16{0, -10, 7, -32000, 5}); got != 32000 {
		t.Fatalf("peakAmplitude = %d, want 32000", got)
	}
	if got := peakAmplitude(nil); got != 0 {
		t.Fatalf("peakAmplitude(nil) = %d, want 0", got)
	}
}
