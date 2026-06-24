// Package condense compresses user-supplied meeting background into the densest
// form that still grounds the live analysis. It lives apart from the cgo-bound
// main package so the logic (prompt, validation, error handling) is unit-testable
// without an audio toolchain.
package condense

import (
	"context"
	"errors"
	"strings"

	"github.com/tomvokac/parley/internal/llm"
)

// SystemPrompt instructs the model to tighten meeting-prep notes while preserving
// every concrete fact, so the result is safe to feed back as context on every
// analysis pass.
const SystemPrompt = `You compress meeting-prep notes for an AI meeting assistant that will read them as background on every analysis pass. Rewrite the provided context to be as short as possible while preserving EVERY concrete fact: people's names and their spellings, roles, organizations, acronyms and what they stand for, project/product names, dates, figures, goals, decisions already made, and open questions. Remove redundancy, repetition, filler, pleasantries, and anything not useful as background. Use terse bullet points or short phrases. Do NOT invent, infer, or add any information that is not present in the input. Output only the condensed notes as plain text — no preamble, no commentary, no markdown headers.`

// ErrEmptyInput is returned when there is nothing to condense.
var ErrEmptyInput = errors.New("nothing to condense")

// ErrEmptyResult is returned when the model replies with no usable text.
var ErrEmptyResult = errors.New("the model returned an empty result")

// Completer is the slice of *llm.Client that this package needs, so tests can
// substitute a stub without a live endpoint.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

// Notes asks the model to condense the supplied text. It trims the input and
// result, returning ErrEmptyInput / ErrEmptyResult rather than calling or
// trusting the model with empty content, and surfaces any LLM error verbatim.
func Notes(ctx context.Context, c Completer, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrEmptyInput
	}
	reply, err := c.Complete(ctx, []llm.Message{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: text},
	})
	if err != nil {
		return "", err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return "", ErrEmptyResult
	}
	return reply, nil
}
