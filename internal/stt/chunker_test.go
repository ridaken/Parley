package stt

import (
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

func TestPeakAmplitude(t *testing.T) {
	if got := peakAmplitude([]int16{0, -10, 7, -32000, 5}); got != 32000 {
		t.Fatalf("peakAmplitude = %d, want 32000", got)
	}
	if got := peakAmplitude(nil); got != 0 {
		t.Fatalf("peakAmplitude(nil) = %d, want 0", got)
	}
}
