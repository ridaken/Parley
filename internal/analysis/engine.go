// Package analysis turns a rolling transcript into live meeting insight: the
// current topic, assertions made, past topics, and suggested questions.
package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tomvokac/parley/internal/llm"
)

// Assertion is a point/claim made on a topic, attributed to a speaker.
type Assertion struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Suggestion is a question or clarification the listener could raise now.
type Suggestion struct {
	Kind string `json:"kind"` // "question" | "clarification"
	Text string `json:"text"`
}

// Topic groups a title, a short summary, and the assertions made about it.
type Topic struct {
	Title      string      `json:"title"`
	Summary    string      `json:"summary"`
	Assertions []Assertion `json:"assertions"`
}

// State is the full analysis snapshot pushed to the UI.
type State struct {
	Current     Topic        `json:"current"`
	Past        []Topic      `json:"past"`
	Suggestions []Suggestion `json:"suggestions"`
}

// Context is the user-supplied background that grounds the analysis.
type Context struct {
	Summary string
	People  string
	Notes   string
}

const (
	maxTranscriptLines = 600
	promptWindowLines  = 60
	maxPastTopics      = 30
)

type line struct {
	speaker string
	text    string
}

// Engine periodically analyses the transcript and emits updated State.
type Engine struct {
	client   *llm.Client
	interval time.Duration
	bg       Context
	emit     func(State)

	mu          sync.Mutex
	transcript  []line
	state       State
	analyzedLen int

	busy   atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

// NewEngine creates an engine. emit is called with each new analysis State.
func NewEngine(client *llm.Client, interval time.Duration, bg Context, emit func(State)) *Engine {
	if interval < 3*time.Second {
		interval = 3 * time.Second
	}
	return &Engine{client: client, interval: interval, bg: bg, emit: emit}
}

// Feed adds a transcribed line.
func (e *Engine) Feed(speaker, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	e.mu.Lock()
	e.transcript = append(e.transcript, line{speaker: speaker, text: text})
	if len(e.transcript) > maxTranscriptLines {
		e.transcript = e.transcript[len(e.transcript)-maxTranscriptLines:]
	}
	e.mu.Unlock()
}

// Start begins the analysis loop.
func (e *Engine) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	go e.loop(ctx)
}

// Stop ends the loop and waits for any in-flight analysis.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
	}
}

func (e *Engine) loop(ctx context.Context) {
	defer close(e.done)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.maybeAnalyze(ctx)
		}
	}
}

func (e *Engine) maybeAnalyze(ctx context.Context) {
	if e.busy.Load() {
		return // previous analysis still running
	}
	e.mu.Lock()
	if len(e.transcript) == e.analyzedLen {
		e.mu.Unlock()
		return // no new content
	}
	window := e.recentWindowLocked()
	prevTitle := e.state.Current.Title
	e.analyzedLen = len(e.transcript)
	e.mu.Unlock()

	e.busy.Store(true)
	go func() {
		defer e.busy.Store(false)
		e.analyze(ctx, window, prevTitle)
	}()
}

func (e *Engine) recentWindowLocked() string {
	start := 0
	if len(e.transcript) > promptWindowLines {
		start = len(e.transcript) - promptWindowLines
	}
	var b strings.Builder
	for _, l := range e.transcript[start:] {
		fmt.Fprintf(&b, "%s: %s\n", l.speaker, l.text)
	}
	return b.String()
}

type llmResult struct {
	CurrentTopicTitle   string       `json:"currentTopicTitle"`
	CurrentTopicSummary string       `json:"currentTopicSummary"`
	TopicChanged        bool         `json:"topicChanged"`
	Assertions          []Assertion  `json:"assertions"`
	Suggestions         []Suggestion `json:"suggestions"`
}

func (e *Engine) analyze(ctx context.Context, window, prevTitle string) {
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: e.buildUserPrompt(window, prevTitle)},
	}
	reply, err := e.client.Complete(ctx, messages)
	if err != nil {
		fmt.Printf("[analysis] llm error: %v\n", err)
		return
	}
	res, err := parseResult(reply)
	if err != nil {
		fmt.Printf("[analysis] parse error: %v (reply: %.200q)\n", err, reply)
		return
	}

	e.mu.Lock()
	if res.TopicChanged && e.state.Current.Title != "" &&
		!strings.EqualFold(e.state.Current.Title, res.CurrentTopicTitle) {
		e.state.Past = append(e.state.Past, e.state.Current)
		if len(e.state.Past) > maxPastTopics {
			e.state.Past = e.state.Past[len(e.state.Past)-maxPastTopics:]
		}
	}
	e.state.Current = Topic{
		Title:      res.CurrentTopicTitle,
		Summary:    res.CurrentTopicSummary,
		Assertions: res.Assertions,
	}
	e.state.Suggestions = res.Suggestions
	snapshot := e.cloneStateLocked()
	e.mu.Unlock()

	if e.emit != nil {
		e.emit(snapshot)
	}
}

func (e *Engine) cloneStateLocked() State {
	past := make([]Topic, len(e.state.Past))
	copy(past, e.state.Past)
	return State{Current: e.state.Current, Past: past, Suggestions: e.state.Suggestions}
}

func (e *Engine) buildUserPrompt(window, prevTitle string) string {
	var b strings.Builder
	b.WriteString("MEETING CONTEXT\n")
	if e.bg.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", e.bg.Summary)
	}
	if e.bg.People != "" {
		fmt.Fprintf(&b, "People: %s\n", e.bg.People)
	}
	if e.bg.Notes != "" {
		fmt.Fprintf(&b, "Notes: %s\n", e.bg.Notes)
	}
	if e.bg.Summary == "" && e.bg.People == "" && e.bg.Notes == "" {
		b.WriteString("(none provided)\n")
	}
	fmt.Fprintf(&b, "\nPREVIOUS TOPIC TITLE: %s\n", orNone(prevTitle))
	b.WriteString("\nRECENT TRANSCRIPT. Speaker labels: \"You\" = the listener; \"Others\" = remote/other participants; \"Room\" = an in-person/mixed capture where individuals (possibly including the listener) are not separable:\n")
	b.WriteString(window)
	b.WriteString("\nReturn the JSON object now.")
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none yet)"
	}
	return s
}

const systemPrompt = `You monitor a live meeting transcript and maintain structured notes for the listener.
Respond with ONLY a single minified JSON object (no markdown, no prose) of this shape:
{"currentTopicTitle": string, "currentTopicSummary": string (1-2 sentences), "topicChanged": boolean (true only if the current topic clearly differs from the PREVIOUS TOPIC TITLE provided), "assertions": [{"speaker": string (use the speaker label exactly as it appears in the transcript), "text": string}], "suggestions": [{"kind": "question"|"clarification", "text": string}]}.
- assertions: the key claims/points/decisions stated about the current topic (max 6, most recent/important first).
- suggestions: sharp questions the listener could ask right now, or things that need clarification (max 4).
Be concise and specific. If the transcript is too sparse to tell, use an empty array and a best-effort title.`

// parseResult extracts the first JSON object from an LLM reply and unmarshals it.
func parseResult(reply string) (llmResult, error) {
	start := strings.Index(reply, "{")
	end := strings.LastIndex(reply, "}")
	if start < 0 || end < 0 || end < start {
		return llmResult{}, fmt.Errorf("no JSON object found")
	}
	var res llmResult
	if err := json.Unmarshal([]byte(reply[start:end+1]), &res); err != nil {
		return llmResult{}, err
	}
	return res, nil
}
