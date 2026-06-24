package condense

import (
	"context"
	"errors"
	"testing"

	"github.com/tomvokac/parley/internal/llm"
)

// stubCompleter records the messages it was sent and returns a canned reply/error.
type stubCompleter struct {
	reply string
	err   error
	got   []llm.Message
}

func (s *stubCompleter) Complete(_ context.Context, messages []llm.Message) (string, error) {
	s.got = messages
	return s.reply, s.err
}

func TestNotesRejectsEmptyInputWithoutCallingModel(t *testing.T) {
	for _, in := range []string{"", "   \n\t  "} {
		c := &stubCompleter{reply: "should not be used"}
		_, err := Notes(context.Background(), c, in)
		if !errors.Is(err, ErrEmptyInput) {
			t.Fatalf("input %q: err = %v, want ErrEmptyInput", in, err)
		}
		if c.got != nil {
			t.Fatalf("input %q: model was called for empty input", in)
		}
	}
}

func TestNotesSendsSystemPromptAndTrimsResult(t *testing.T) {
	c := &stubCompleter{reply: "  - Acme is the client\n  "}
	out, err := Notes(context.Background(), c, "  The client is Acme.  ")
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}
	if out != "- Acme is the client" {
		t.Fatalf("result not trimmed: %q", out)
	}
	if len(c.got) != 2 || c.got[0].Role != "system" || c.got[0].Content != SystemPrompt {
		t.Fatalf("first message must be the condense system prompt: %+v", c.got)
	}
	if c.got[1].Role != "user" || c.got[1].Content != "The client is Acme." {
		t.Fatalf("user message should be the trimmed input: %+v", c.got)
	}
}

func TestNotesTreatsBlankReplyAsError(t *testing.T) {
	c := &stubCompleter{reply: "   \n  "}
	if _, err := Notes(context.Background(), c, "something"); !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("err = %v, want ErrEmptyResult", err)
	}
}

func TestNotesPropagatesModelError(t *testing.T) {
	want := errors.New("llm: status 500")
	c := &stubCompleter{err: want}
	if _, err := Notes(context.Background(), c, "something"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
