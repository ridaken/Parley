// Package analysis turns a rolling transcript into live meeting insight: the
// current topic, assertions made, past topics, and suggested questions.
package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// ActionItem is a task/follow-up surfaced during the meeting, optionally owned by
// someone. Unlike Assertion, action items are meeting-level: they accumulate for
// the whole session and survive topic changes.
type ActionItem struct {
	Text  string `json:"text"`
	Owner string `json:"owner"` // "" when no owner was stated
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
	// ActionItems are meeting-level: they accumulate across the whole session and
	// are not tied to (or cleared with) the current topic.
	ActionItems []ActionItem `json:"actionItems"`
}

// Context is the user-supplied background that grounds the analysis.
type Context struct {
	Summary string
	People  string
	Notes   string
}

// Note scopes for live, mid-meeting context.
const (
	ScopeMeeting = "meeting" // applies for the whole session
	ScopeTopic   = "topic"   // applies only while its topic is current
)

// LiveNote is context the user injects during the meeting. Meeting-scoped notes
// (names, themes, standing corrections) ride along on every analysis; topic-scoped
// notes correct the immediate discussion and expire when the topic changes.
type LiveNote struct {
	Scope      string
	TopicTitle string // topic active when a topic-scoped note was added
	Text       string
}

const (
	maxTranscriptLines = 600
	promptWindowLines  = 60
	maxPastTopics      = 30
	maxActionItems     = 100
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
	liveNotes   []LiveNote

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

// AddLiveNote records a live context note. For topic-scoped notes it tags the
// note with the current topic title (so it can expire on the next topic change)
// and returns the stored note so the caller can persist/display it.
func (e *Engine) AddLiveNote(scope, text string) LiveNote {
	text = strings.TrimSpace(text)
	if scope != ScopeMeeting {
		scope = ScopeTopic
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	n := LiveNote{Scope: scope, Text: text}
	if scope == ScopeTopic {
		n.TopicTitle = e.state.Current.Title
	}
	if text != "" {
		e.liveNotes = append(e.liveNotes, n)
	}
	return n
}

// Restore seeds the engine from a previously saved session so analysis resumes
// where it left off: prior insight is shown immediately and re-feeding history
// is avoided (analyzedLen is advanced past the restored transcript).
func (e *Engine) Restore(state State, notes []LiveNote, transcript []struct{ Speaker, Text string }) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = state
	e.liveNotes = notes
	e.transcript = e.transcript[:0]
	for _, h := range transcript {
		e.transcript = append(e.transcript, line{speaker: h.Speaker, text: h.Text})
	}
	if len(e.transcript) > maxTranscriptLines {
		e.transcript = e.transcript[len(e.transcript)-maxTranscriptLines:]
	}
	e.analyzedLen = len(e.transcript)
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
	// Snapshot the prior analysis under the lock (copy the slices — the goroutine
	// reads them after we unlock) so the model can refine rather than restart.
	prior := priorView{
		title:       e.state.Current.Title,
		summary:     e.state.Current.Summary,
		assertions:  append([]Assertion(nil), e.state.Current.Assertions...),
		actionItems: append([]ActionItem(nil), e.state.ActionItems...),
	}
	notes := e.liveNotesForPromptLocked()
	e.analyzedLen = len(e.transcript)
	e.mu.Unlock()

	e.busy.Store(true)
	go func() {
		defer e.busy.Store(false)
		e.analyze(ctx, window, prior, notes)
	}()
}

// liveNotesForPromptLocked returns the notes currently in effect: all
// meeting-scoped notes plus any topic-scoped notes whose topic is still current.
func (e *Engine) liveNotesForPromptLocked() []LiveNote {
	cur := e.state.Current.Title
	out := make([]LiveNote, 0, len(e.liveNotes))
	for _, n := range e.liveNotes {
		if n.Scope == ScopeTopic && !strings.EqualFold(n.TopicTitle, cur) {
			continue
		}
		out = append(out, n)
	}
	return out
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
	ActionItems         []ActionItem `json:"actionItems"`
}

// priorView is the engine's last analysis, fed back into the prompt so the model
// refines and extends it instead of re-deriving everything from the recent window.
type priorView struct {
	title       string
	summary     string
	assertions  []Assertion
	actionItems []ActionItem
}

func (e *Engine) analyze(ctx context.Context, window string, prior priorView, notes []LiveNote) {
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: e.buildUserPrompt(window, prior, notes)},
	}
	reply, err := e.client.Complete(ctx, messages)
	if err != nil {
		log.Printf("[analysis] llm error: %v", err)
		return
	}
	res, err := parseResult(reply)
	if err != nil {
		log.Printf("[analysis] parse error: %v (reply: %.200q)", err, reply)
		return
	}

	e.mu.Lock()
	if res.TopicChanged && e.state.Current.Title != "" &&
		!strings.EqualFold(e.state.Current.Title, res.CurrentTopicTitle) {
		e.state.Past = append(e.state.Past, e.state.Current)
		if len(e.state.Past) > maxPastTopics {
			e.state.Past = e.state.Past[len(e.state.Past)-maxPastTopics:]
		}
		e.dropTopicNotesLocked() // topic-scoped corrections expire with their topic
	}
	// Keep the established title stable while the discussion stays on the same
	// subject. The model tends to paraphrase an unchanged topic on every pass,
	// which churns the UI (and would wrongly expire topic-scoped notes keyed on
	// the title). Only adopt the model's new title when it flags a real change.
	title := res.CurrentTopicTitle
	if !res.TopicChanged && e.state.Current.Title != "" {
		title = e.state.Current.Title
	}
	e.state.Current = Topic{
		Title:      title,
		Summary:    res.CurrentTopicSummary,
		Assertions: res.Assertions,
	}
	e.state.Suggestions = res.Suggestions
	// Action items are meeting-level: merge regardless of topicChanged so they
	// accumulate across the whole session and never drop on a topic rollover.
	e.mergeActionItemsLocked(res.ActionItems)
	snapshot := e.cloneStateLocked()
	e.mu.Unlock()

	if e.emit != nil {
		e.emit(snapshot)
	}
}

// dropTopicNotesLocked removes all topic-scoped live notes, keeping meeting-scoped
// ones. Called when the topic rolls over so stale corrections can't mislead the
// model about the new topic.
func (e *Engine) dropTopicNotesLocked() {
	kept := e.liveNotes[:0]
	for _, n := range e.liveNotes {
		if n.Scope == ScopeMeeting {
			kept = append(kept, n)
		}
	}
	e.liveNotes = kept
}

// mergeActionItemsLocked folds the model's per-pass action items into the
// meeting-level accumulating list. Items are keyed by normalized text so the same
// item re-emitted across passes isn't duplicated; an owner is backfilled when one
// is stated later, but an existing non-empty owner is never overwritten (avoids
// churn from model noise). The list is capped at maxActionItems (oldest dropped).
func (e *Engine) mergeActionItemsLocked(incoming []ActionItem) {
	if len(incoming) == 0 {
		return
	}
	index := make(map[string]int, len(e.state.ActionItems))
	for i, a := range e.state.ActionItems {
		index[normalizeActionItem(a.Text)] = i
	}
	for _, in := range incoming {
		text := strings.TrimSpace(in.Text)
		if text == "" {
			continue
		}
		owner := strings.TrimSpace(in.Owner)
		key := normalizeActionItem(text)
		if i, ok := index[key]; ok {
			if e.state.ActionItems[i].Owner == "" && owner != "" {
				e.state.ActionItems[i].Owner = owner
			}
			continue
		}
		index[key] = len(e.state.ActionItems)
		e.state.ActionItems = append(e.state.ActionItems, ActionItem{Text: text, Owner: owner})
	}
	if len(e.state.ActionItems) > maxActionItems {
		e.state.ActionItems = e.state.ActionItems[len(e.state.ActionItems)-maxActionItems:]
	}
}

// normalizeActionItem produces the identity key for de-duplicating action items:
// lowercased, whitespace-collapsed, and stripped of trailing sentence punctuation.
func normalizeActionItem(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ".!?")
}

func (e *Engine) cloneStateLocked() State {
	past := make([]Topic, len(e.state.Past))
	copy(past, e.state.Past)
	ai := make([]ActionItem, len(e.state.ActionItems))
	copy(ai, e.state.ActionItems)
	return State{Current: e.state.Current, Past: past, Suggestions: e.state.Suggestions, ActionItems: ai}
}

func (e *Engine) buildUserPrompt(window string, prior priorView, notes []LiveNote) string {
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

	var standing, topical []string
	for _, n := range notes {
		if n.Scope == ScopeMeeting {
			standing = append(standing, n.Text)
		} else {
			topical = append(topical, n.Text)
		}
	}
	if len(standing) > 0 {
		b.WriteString("\nSTANDING CORRECTIONS (provided live by the listener; apply to the whole meeting — e.g. correct names, acronyms, themes):\n")
		for _, t := range standing {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	if len(topical) > 0 {
		b.WriteString("\nNOTE ON CURRENT TOPIC (provided live by the listener; corrects the immediate discussion only — trust over the transcript if they conflict):\n")
		for _, t := range topical {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}

	fmt.Fprintf(&b, "\nPREVIOUS TOPIC TITLE: %s\n", orNone(prior.title))

	if prior.summary != "" || len(prior.assertions) > 0 || len(prior.actionItems) > 0 {
		b.WriteString("\nCURRENT UNDERSTANDING SO FAR (your prior analysis — refine and extend it, don't restart):\n")
		if prior.summary != "" {
			fmt.Fprintf(&b, "Summary: %s\n", prior.summary)
		}
		if len(prior.assertions) > 0 {
			b.WriteString("Assertions:\n")
			for _, a := range prior.assertions {
				fmt.Fprintf(&b, "- %s: %s\n", a.Speaker, a.Text)
			}
		}
		if len(prior.actionItems) > 0 {
			b.WriteString("Action items tracked so far (meeting-wide; do NOT re-list these unless adding an owner):\n")
			for _, a := range prior.actionItems {
				owner := a.Owner
				if owner == "" {
					owner = "unassigned"
				}
				fmt.Fprintf(&b, "- %s [owner: %s]\n", a.Text, owner)
			}
		}
	}

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
{"currentTopicTitle": string, "currentTopicSummary": string (1-2 sentences), "topicChanged": boolean, "assertions": [{"speaker": string (use the speaker label exactly as it appears in the transcript), "text": string}], "suggestions": [{"kind": "question"|"clarification", "text": string}], "actionItems": [{"text": string, "owner": string}]}.
You are given your CURRENT UNDERSTANDING SO FAR (your prior analysis). Update and merge it rather than starting over: refine the summary, keep still-valid assertions, add new ones from the recent transcript, and do not drop a still-valid assertion just because it isn't repeated in the recent window. Avoid restating duplicates.
- topicChanged: set true ONLY when the discussion has genuinely moved to a different subject than the PREVIOUS TOPIC TITLE. While the conversation is still about that subject — even as new points, details, or sub-points come up — set it false. A topic spans the whole discussion of a subject, not each individual statement; do not split a continuing discussion into many topics.
- currentTopicTitle: when topicChanged is false, reuse the PREVIOUS TOPIC TITLE EXACTLY as given — do not reword, rephrase, shorten, or "improve" it. Only write a new title when topicChanged is true.
- assertions: the key claims/points/decisions stated about the current topic (max 6, most recent/important first).
- suggestions: sharp questions the listener could ask right now, or things that need clarification (max 4).
- actionItems: tasks, follow-ups, or commitments stated in the RECENT TRANSCRIPT, with owner set to the responsible person's name/label if stated (else ""). List only items visible in this window — earlier ones are already tracked for you under CURRENT UNDERSTANDING SO FAR; re-list a tracked item only to add an owner that was just stated.
Be concise and specific. If the transcript is too sparse to tell, use empty arrays and a best-effort title.`

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
