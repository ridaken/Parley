package stt

import (
	"context"
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

// Chunker accumulates per-source samples and, on a fixed interval, transcribes
// each source's buffered audio and reports a Segment.
type Chunker struct {
	client    *Client
	window    time.Duration
	onSegment func(Segment)

	mu  sync.Mutex
	buf map[audio.Source]*srcState

	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup
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
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
	c.wg.Wait()
}

func (c *Chunker) loop(ctx context.Context) {
	defer close(c.done)
	t := time.NewTicker(c.window)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush(ctx) // final partial flush
			return
		case <-t.C:
			c.flush(ctx)
		}
	}
}

type flushJob struct {
	src              audio.Source
	samples          []int16
	startMs, endMs   int64
}

func (c *Chunker) flush(ctx context.Context) {
	var jobs []flushJob

	c.mu.Lock()
	for src, st := range c.buf {
		if len(st.pending) == 0 {
			continue
		}
		samples := st.pending
		st.pending = nil
		durMs := int64(len(samples)) * 1000 / int64(audio.SampleRate)
		start := st.flushedMs
		st.flushedMs += durMs
		jobs = append(jobs, flushJob{src: src, samples: samples, startMs: start, endMs: start + durMs})
	}
	c.mu.Unlock()

	for _, j := range jobs {
		if peakAmplitude(j.samples) < silenceThreshold {
			continue // skip silent windows
		}
		c.wg.Add(1)
		go func(j flushJob) {
			defer c.wg.Done()
			text, err := c.client.Transcribe(ctx, audio.EncodeMonoWAV(audio.SampleRate, j.samples))
			if err != nil || text == "" {
				return
			}
			c.onSegment(Segment{Source: j.src, Text: text, StartMs: j.startMs, EndMs: j.endMs})
		}(j)
	}
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
