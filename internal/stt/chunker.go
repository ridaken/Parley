package stt

import (
	"context"
	"log"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tomvokac/parley/internal/audio"
)

// Segment is a transcribed slice of one source's audio.
type Segment struct {
	Source  audio.Source `json:"source"`
	Text    string       `json:"text"`
	StartMs int64        `json:"startMs"`
	EndMs   int64        `json:"endMs"`
}

// silenceThreshold is the minimum peak |amplitude| (S16) for a chunk to be
// considered worth transcribing. Avoids feeding whisper near-silent buffers
// (which can hallucinate text).
const silenceThreshold = 350

const (
	lowEnergyThreshold = 80
	lowEnergyPeakLimit = 1200
)

var blankAudioRE = regexp.MustCompile(`(?i)\[blank_audio\]`)

// Chunker accumulates per-source samples and, on a fixed interval, transcribes
// each source's buffered audio and reports a Segment.
type Chunker struct {
	client    *Client
	window    time.Duration
	onSegment func(Segment)

	mu                sync.Mutex
	buf               map[audio.Source]*srcState
	finalFlushTimeout time.Duration

	cancel context.CancelFunc
	done   chan struct{}
}

type srcState struct {
	pending   []int16
	flushedMs int64 // total audio already windowed, for timeline positions
}

// NewChunker creates a chunker that flushes every window.
func NewChunker(client *Client, window time.Duration, onSegment func(Segment)) *Chunker {
	return &Chunker{
		client:    client,
		window:    window,
		onSegment: onSegment,
		buf:       make(map[audio.Source]*srcState),
	}
}

// Feed appends captured samples for a source (called from audio callbacks).
func (c *Chunker) Feed(src audio.Source, samples []int16) {
	c.mu.Lock()
	st := c.buf[src]
	if st == nil {
		st = &srcState{}
		c.buf[src] = st
	}
	st.pending = append(st.pending, samples...)
	c.mu.Unlock()
}

// Start begins the flush loop.
func (c *Chunker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.loop(ctx)
}

// Stop ends the loop and waits for in-flight transcriptions.
func (c *Chunker) Stop() {
	c.StopWithTimeout(30 * time.Second)
}

// StopWithTimeout ends the loop and gives the final flush the supplied deadline.
func (c *Chunker) StopWithTimeout(timeout time.Duration) {
	if c.cancel != nil {
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		c.mu.Lock()
		c.finalFlushTimeout = timeout
		c.mu.Unlock()
		c.cancel()
		<-c.done
	}
}

func (c *Chunker) loop(ctx context.Context) {
	defer close(c.done)
	t := time.NewTicker(c.window)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			timeout := c.finalFlushTimeout
			c.mu.Unlock()
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			fctx, cancel := context.WithTimeout(context.Background(), timeout)
			c.flush(fctx)
			cancel()
			return
		case <-t.C:
			c.flush(context.Background())
		}
	}
}

type flushJob struct {
	src            audio.Source
	samples        []int16
	startMs, endMs int64
}

func (c *Chunker) flush(ctx context.Context) {
	var jobs []flushJob
	maxSamples := int(c.window * time.Duration(audio.SampleRate) / time.Second)
	if maxSamples <= 0 {
		maxSamples = audio.SampleRate
	}

	c.mu.Lock()
	for src, st := range c.buf {
		if len(st.pending) == 0 {
			continue
		}
		pendingMs := int64(len(st.pending)) * 1000 / int64(audio.SampleRate)
		windowMs := c.window.Milliseconds()
		if windowMs > 0 && pendingMs > 2*windowMs {
			log.Printf("[stt] transcription backlog source=%s pending=%dms window=%dms", src, pendingMs, windowMs)
		}
		for len(st.pending) > 0 {
			n := len(st.pending)
			if n > maxSamples {
				n = maxSamples
			}
			samples := append([]int16(nil), st.pending[:n]...)
			st.pending = st.pending[n:]
			durMs := int64(len(samples)) * 1000 / int64(audio.SampleRate)
			start := st.flushedMs
			st.flushedMs += durMs
			jobs = append(jobs, flushJob{src: src, samples: samples, startMs: start, endMs: start + durMs})
		}
	}
	c.mu.Unlock()

	for _, j := range jobs {
		select {
		case <-ctx.Done():
			log.Printf("[stt] final flush stopped before source=%s start=%dms end=%dms: %v",
				j.src, j.startMs, j.endMs, ctx.Err())
			return
		default:
		}
		if shouldSkipAudio(j.samples) {
			continue // skip silent or low-energy windows
		}
		start := time.Now()
		audioMs := j.endMs - j.startMs
		text, err := c.client.Transcribe(ctx, audio.EncodeMonoWAV(audio.SampleRate, j.samples))
		if err != nil {
			log.Printf("[stt] transcribe failed source=%s start=%dms end=%dms audio=%dms duration=%s: %v",
				j.src, j.startMs, j.endMs, audioMs, time.Since(start).Round(time.Millisecond), err)
			continue
		}
		text = CleanTranscriptText(text)
		if text == "" {
			log.Printf("[stt] transcribe empty source=%s start=%dms end=%dms audio=%dms duration=%s",
				j.src, j.startMs, j.endMs, audioMs, time.Since(start).Round(time.Millisecond))
			continue
		}
		duration := time.Since(start).Round(time.Millisecond)
		if duration.Milliseconds() > audioMs {
			log.Printf("[stt] transcribed source=%s start=%dms end=%dms audio=%dms duration=%s lagging=true",
				j.src, j.startMs, j.endMs, audioMs, duration)
		} else {
			log.Printf("[stt] transcribed source=%s start=%dms end=%dms audio=%dms duration=%s",
				j.src, j.startMs, j.endMs, audioMs, duration)
		}
		c.onSegment(Segment{Source: j.src, Text: text, StartMs: j.startMs, EndMs: j.endMs})
	}
}

// CleanTranscriptText removes whisper.cpp filler tokens that are not useful
// transcript content. A result containing only filler is treated as empty.
func CleanTranscriptText(text string) string {
	text = blankAudioRE.ReplaceAllString(text, " ")
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func shouldSkipAudio(samples []int16) bool {
	peak := peakAmplitude(samples)
	if peak < silenceThreshold {
		return true
	}
	return peak < lowEnergyPeakLimit && rmsAmplitude(samples) < lowEnergyThreshold
}

func peakAmplitude(samples []int16) int {
	peak := 0
	for _, v := range samples {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	return peak
}

func rmsAmplitude(samples []int16) int {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, v := range samples {
		x := float64(v)
		sum += x * x
	}
	return int(math.Sqrt(sum / float64(len(samples))))
}
