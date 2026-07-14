package stt

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tomvokac/parley/internal/audio"
)

const streamTranscriptEmitInterval = time.Second

// StreamingChunker feeds short audio frames into persistent cache-aware model
// streams. Model token deltas are coalesced into roughly one-second transcript
// segments so the UI stays fresh without storing every word as a separate row.
type StreamingChunker struct {
	client    *Client
	window    time.Duration
	onSegment func(Segment)

	mu                sync.Mutex
	buf               map[audio.Source]*streamState
	finalFlushTimeout time.Duration
	cancel            context.CancelFunc
	done              chan struct{}
}

type streamState struct {
	pending     []int16
	flushedMs   int64
	started     bool
	lastEmitMs  int64
	pendingText strings.Builder
}

// NewStreamingChunker creates a cache-aware chunker with the model's audio-frame cadence.
func NewStreamingChunker(client *Client, window time.Duration, onSegment func(Segment)) *StreamingChunker {
	return &StreamingChunker{
		client:    client,
		window:    window,
		onSegment: onSegment,
		buf:       make(map[audio.Source]*streamState),
	}
}

// Feed appends captured PCM samples for one independently cached audio source.
func (c *StreamingChunker) Feed(src audio.Source, samples []int16) {
	c.mu.Lock()
	st := c.buf[src]
	if st == nil {
		st = &streamState{}
		c.buf[src] = st
	}
	st.pending = append(st.pending, samples...)
	c.mu.Unlock()
}

// Start begins the short-frame flush loop.
func (c *StreamingChunker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.loop(ctx)
}

// Stop ends streaming and flushes model state.
func (c *StreamingChunker) Stop() {
	c.StopWithTimeout(30 * time.Second)
}

// StopWithTimeout ends streaming and bounds the final model flush.
func (c *StreamingChunker) StopWithTimeout(timeout time.Duration) {
	if c.cancel == nil {
		return
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c.mu.Lock()
	c.finalFlushTimeout = timeout
	c.mu.Unlock()
	c.cancel()
	<-c.done
}

func (c *StreamingChunker) loop(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(c.window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			timeout := c.finalFlushTimeout
			c.mu.Unlock()
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			finalCtx, cancel := context.WithTimeout(context.Background(), timeout)
			c.flush(finalCtx)
			c.finishStreams(finalCtx)
			cancel()
			return
		case <-ticker.C:
			c.flush(context.Background())
		}
	}
}

func (c *StreamingChunker) flush(ctx context.Context) {
	maxSamples := int(c.window * time.Duration(audio.SampleRate) / time.Second)
	if maxSamples <= 0 {
		maxSamples = audio.SampleRate
	}

	var jobs []flushJob
	c.mu.Lock()
	for src, st := range c.buf {
		for len(st.pending) > 0 {
			n := len(st.pending)
			if n > maxSamples {
				n = maxSamples
			}
			samples := append([]int16(nil), st.pending[:n]...)
			st.pending = st.pending[n:]
			durationMs := int64(len(samples)) * 1000 / int64(audio.SampleRate)
			start := st.flushedMs
			st.flushedMs += durationMs
			jobs = append(jobs, flushJob{src: src, samples: samples, startMs: start, endMs: st.flushedMs})
		}
	}
	c.mu.Unlock()
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].startMs == jobs[j].startMs {
			return jobs[i].src < jobs[j].src
		}
		return jobs[i].startMs < jobs[j].startMs
	})

	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.mu.Lock()
		st := c.buf[job.src]
		if !st.started {
			if shouldSkipAudio(job.samples) {
				c.mu.Unlock()
				continue
			}
			st.started = true
			st.lastEmitMs = job.startMs
		}
		c.mu.Unlock()

		started := time.Now()
		delta, err := c.client.StreamFeed(ctx, string(job.src), audio.EncodeMonoWAV(audio.SampleRate, job.samples))
		if err != nil {
			log.Printf("[stt] streaming feed failed source=%s start=%dms end=%dms: %v", job.src, job.startMs, job.endMs, err)
			continue
		}
		c.acceptDelta(job.src, delta, job.endMs, false)
		if elapsed := time.Since(started); elapsed > c.window {
			log.Printf("[stt] streaming frame lag source=%s audio=%s duration=%s", job.src, c.window, elapsed.Round(time.Millisecond))
		}
	}
}

func (c *StreamingChunker) finishStreams(ctx context.Context) {
	c.mu.Lock()
	var sources []audio.Source
	for src, st := range c.buf {
		if st.started {
			sources = append(sources, src)
		}
	}
	c.mu.Unlock()

	for _, src := range sources {
		delta, err := c.client.StreamFinish(ctx, string(src))
		if err != nil {
			log.Printf("[stt] streaming finish failed source=%s: %v", src, err)
		}
		c.mu.Lock()
		endMs := c.buf[src].flushedMs
		c.mu.Unlock()
		c.acceptDelta(src, delta, endMs, true)
	}
}

func (c *StreamingChunker) acceptDelta(src audio.Source, delta string, endMs int64, final bool) {
	c.mu.Lock()
	st := c.buf[src]
	st.pendingText.WriteString(delta)
	raw := st.pendingText.String()
	trimmed := strings.TrimSpace(raw)
	shouldEmit := final || (trimmed != "" && (endMs-st.lastEmitMs >= streamTranscriptEmitInterval.Milliseconds() || endsSentence(trimmed)))
	if !shouldEmit {
		c.mu.Unlock()
		return
	}
	text := CleanTranscriptText(raw)
	st.pendingText.Reset()
	startMs := st.lastEmitMs
	st.lastEmitMs = endMs
	c.mu.Unlock()

	if text != "" {
		c.onSegment(Segment{Source: src, Text: text, StartMs: startMs, EndMs: endMs})
	}
}

func endsSentence(text string) bool {
	return strings.HasSuffix(text, ".") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "!")
}
