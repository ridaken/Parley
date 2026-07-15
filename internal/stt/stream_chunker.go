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

const (
	// Keep transcript rows readable while still bounding how long an unpunctuated
	// utterance can grow (for example, dictation without sentence punctuation).
	streamTranscriptMaxWords = 25

	// RNNT decoding can retain the last token until it sees an utterance boundary.
	// Closing the stream after three silent 320 ms frames flushes that token while
	// keeping brief, natural pauses inside the same utterance.
	streamSilenceFinalizeAfter = 960 * time.Millisecond
)

// StreamingChunker feeds short audio frames into persistent cache-aware model
// streams. Model token deltas are coalesced into sentences (or bounded word
// groups when punctuation is absent) so the UI stays fresh without storing every
// word as a separate row.
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
	silenceMs   int64
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

		voiced := !shouldSkipAudio(job.samples)
		c.mu.Lock()
		st := c.buf[job.src]
		if !st.started {
			if !voiced {
				c.mu.Unlock()
				continue
			}
			st.started = true
			st.lastEmitMs = job.startMs
			st.silenceMs = 0
		} else if voiced {
			st.silenceMs = 0
		} else {
			st.silenceMs += job.endMs - job.startMs
		}
		finalizeAfterPause := !voiced && st.silenceMs >= streamSilenceFinalizeAfter.Milliseconds()
		c.mu.Unlock()

		started := time.Now()
		delta, err := c.client.StreamFeed(ctx, string(job.src), audio.EncodeMonoWAV(audio.SampleRate, job.samples))
		if err != nil {
			log.Printf("[stt] streaming feed failed source=%s start=%dms end=%dms: %v", job.src, job.startMs, job.endMs, err)
		} else {
			c.acceptDelta(job.src, delta, job.endMs, false)
		}
		if elapsed := time.Since(started); elapsed > c.window {
			log.Printf("[stt] streaming frame lag source=%s audio=%s duration=%s", job.src, c.window, elapsed.Round(time.Millisecond))
		}
		if finalizeAfterPause {
			c.finishStream(ctx, job.src, job.endMs)
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
		c.mu.Lock()
		endMs := c.buf[src].flushedMs
		c.mu.Unlock()
		c.finishStream(ctx, src, endMs)
	}
}

func (c *StreamingChunker) finishStream(ctx context.Context, src audio.Source, endMs int64) {
	delta, err := c.client.StreamFinish(ctx, string(src))
	if err != nil {
		log.Printf("[stt] streaming finish failed source=%s: %v", src, err)
	}

	c.mu.Lock()
	if st := c.buf[src]; st != nil {
		st.started = false
		st.silenceMs = 0
	}
	c.mu.Unlock()

	// Flush text already returned by earlier feeds even when the finish request
	// itself fails. On success, delta also contains the decoder's withheld tail.
	c.acceptDelta(src, delta, endMs, true)
}

func (c *StreamingChunker) acceptDelta(src audio.Source, delta string, endMs int64, final bool) {
	c.mu.Lock()
	st := c.buf[src]
	if st == nil {
		c.mu.Unlock()
		return
	}
	st.pendingText.WriteString(delta)
	ready, remainder := splitTranscript(st.pendingText.String(), final)
	st.pendingText.Reset()
	if remainder != "" {
		st.pendingText.WriteString(remainder)
	}
	startMs := st.lastEmitMs
	if len(ready) > 0 || final {
		st.lastEmitMs = endMs
	}
	c.mu.Unlock()

	if len(ready) == 0 {
		return
	}
	totalWords := 0
	for _, text := range ready {
		totalWords += len(strings.Fields(text))
	}
	emittedWords := 0
	for _, text := range ready {
		emittedWords += len(strings.Fields(text))
		segmentEndMs := endMs
		if totalWords > 0 {
			segmentEndMs = startMs + (endMs-startMs)*int64(emittedWords)/int64(totalWords)
		}
		c.onSegment(Segment{Source: src, Text: text, StartMs: startMs, EndMs: segmentEndMs})
		startMs = segmentEndMs
	}
}

func splitTranscript(text string, final bool) (ready []string, remainder string) {
	hadTrailingSpace := strings.TrimRight(text, " \t\r\n") != text
	words := strings.Fields(CleanTranscriptText(text))
	for len(words) > 0 {
		boundary := 0
		for i, word := range words {
			if endsSentence(word) {
				boundary = i + 1
				break
			}
			if i+1 == streamTranscriptMaxWords {
				boundary = streamTranscriptMaxWords
				break
			}
		}
		if boundary == 0 {
			if !final {
				break
			}
			boundary = len(words)
		}
		ready = append(ready, strings.Join(words[:boundary], " "))
		words = words[boundary:]
	}
	remainder = strings.Join(words, " ")
	if remainder != "" && hadTrailingSpace {
		remainder += " "
	}
	return ready, remainder
}

func endsSentence(word string) bool {
	word = strings.TrimRight(word, "\"'”’)]}")
	return strings.HasSuffix(word, ".") || strings.HasSuffix(word, "?") || strings.HasSuffix(word, "!")
}
