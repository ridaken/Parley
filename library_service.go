package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tomvokac/parley/internal/llm"
	"github.com/tomvokac/parley/internal/store"
)

// LibraryService exposes settings and context-profile management to the frontend.
type LibraryService struct {
	store *store.Store
}

// NewLibraryService constructs the service.
func NewLibraryService(s *store.Store) *LibraryService {
	return &LibraryService{store: s}
}

// GetSettings returns persisted settings (HasAPIKey indicates a stored key).
func (l *LibraryService) GetSettings() (store.Settings, error) {
	return l.store.GetSettings()
}

// SaveSettings persists settings (excluding the API key).
func (l *LibraryService) SaveSettings(s store.Settings) error {
	return l.store.SaveSettings(s)
}

// SetAPIKey stores or clears the LLM API key in the OS keychain.
func (l *LibraryService) SetAPIKey(key string) error {
	return l.store.SetAPIKey(key)
}

// TestConnection verifies the active LLM connection's endpoint/model/key respond.
func (l *LibraryService) TestConnection() error {
	conn, err := l.store.GetActiveLLMConnection()
	if err != nil {
		return err
	}
	return l.testConn(conn)
}

// ListLLMConnections returns all saved LLM connections (newest-updated first).
func (l *LibraryService) ListLLMConnections() ([]store.LLMConnection, error) {
	return l.store.ListLLMConnections()
}

// SaveLLMConnection inserts or updates a connection and returns the saved row.
func (l *LibraryService) SaveLLMConnection(c store.LLMConnection) (store.LLMConnection, error) {
	return l.store.SaveLLMConnection(c)
}

// DeleteLLMConnection removes a connection (and its stored key).
func (l *LibraryService) DeleteLLMConnection(id int64) error {
	return l.store.DeleteLLMConnection(id)
}

// SetActiveLLMConnection selects which connection drives analysis.
func (l *LibraryService) SetActiveLLMConnection(id int64) error {
	return l.store.SetActiveLLMConnection(id)
}

// SetConnectionAPIKey stores or clears a connection's API key in the keychain.
func (l *LibraryService) SetConnectionAPIKey(id int64, key string) error {
	return l.store.SetConnectionAPIKey(id, key)
}

// TestLLMConnection verifies a specific saved connection responds. The UI saves
// edits (and key) before calling this, so it tests the persisted values.
func (l *LibraryService) TestLLMConnection(id int64) error {
	conn, err := l.store.GetLLMConnection(id)
	if err != nil {
		return err
	}
	return l.testConn(conn)
}

func (l *LibraryService) testConn(conn store.LLMConnection) error {
	if conn.BaseURL == "" {
		return fmt.Errorf("no LLM endpoint configured")
	}
	key, _ := l.store.GetConnectionAPIKey(conn.ID)
	client := llm.NewClient(conn.BaseURL, key, conn.Model)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return client.Ping(ctx)
}

// ListProfiles returns all saved context profiles.
func (l *LibraryService) ListProfiles() ([]store.Profile, error) {
	return l.store.ListProfiles()
}

// SaveProfile inserts or updates a context profile.
func (l *LibraryService) SaveProfile(p store.Profile) (store.Profile, error) {
	return l.store.SaveProfile(p)
}

// DeleteProfile removes a context profile.
func (l *LibraryService) DeleteProfile(id int64) error {
	return l.store.DeleteProfile(id)
}

// condenseSystemPrompt instructs the model to tighten user-supplied meeting
// background into the densest form that still grounds the live analysis. It must
// preserve concrete facts and add nothing, so the result is safe to feed back as
// context for every analysis pass.
const condenseSystemPrompt = `You compress meeting-prep notes for an AI meeting assistant that will read them as background on every analysis pass. Rewrite the provided context to be as short as possible while preserving EVERY concrete fact: people's names and their spellings, roles, organizations, acronyms and what they stand for, project/product names, dates, figures, goals, decisions already made, and open questions. Remove redundancy, repetition, filler, pleasantries, and anything not useful as background. Use terse bullet points or short phrases. Do NOT invent, infer, or add any information that is not present in the input. Output only the condensed notes as plain text — no preamble, no commentary, no markdown headers.`

// CondenseContext uses the active LLM connection to compress user-supplied
// meeting context (typically the free-form notes, where pasted documents land)
// into a denser form that preserves the concrete facts. It returns the condensed
// text for the UI to preview; it never mutates a saved profile itself.
func (l *LibraryService) CondenseContext(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("nothing to condense")
	}

	conn, err := l.store.GetActiveLLMConnection()
	if err != nil || conn.BaseURL == "" {
		return "", fmt.Errorf("no LLM connection is configured — set one in Settings before condensing")
	}

	key, _ := l.store.GetConnectionAPIKey(conn.ID)
	client := llm.NewClient(conn.BaseURL, key, conn.Model)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	reply, err := client.Complete(ctx, []llm.Message{
		{Role: "system", Content: condenseSystemPrompt},
		{Role: "user", Content: text},
	})
	if err != nil {
		return "", err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return "", fmt.Errorf("the model returned an empty result")
	}
	return reply, nil
}
