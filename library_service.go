package main

import (
	"context"
	"fmt"
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

// TestConnection verifies the configured LLM endpoint/model/key respond.
func (l *LibraryService) TestConnection() error {
	s, err := l.store.GetSettings()
	if err != nil {
		return err
	}
	if s.LLMBaseURL == "" {
		return fmt.Errorf("no LLM endpoint configured")
	}
	key, _ := l.store.GetAPIKey()
	client := llm.NewClient(s.LLMBaseURL, key, s.LLMModel)
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
