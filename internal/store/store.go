// Package store persists settings and reusable context profiles in SQLite, with
// the LLM API key kept in the OS keychain rather than the database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"
)

const (
	keyringService = "Parley"
	keyringUser    = "llm-api-key"
)

// CaptureSource is a user-selected audio device and the role its audio plays.
type CaptureSource struct {
	ID    string `json:"id"`    // device hex token; "" = default
	Name  string `json:"name"`  // display name
	Kind  string `json:"kind"`  // "input" | "loopback"
	Label string `json:"label"` // "You" | "Others" | "Room"
}

// Settings holds app-wide configuration (the API key is never included here).
type Settings struct {
	LLMBaseURL          string          `json:"llmBaseURL"`
	LLMModel            string          `json:"llmModel"`
	AnalysisIntervalSec int             `json:"analysisIntervalSec"`
	ActiveProfileID     int64           `json:"activeProfileID"`
	HasAPIKey           bool            `json:"hasAPIKey"`
	CaptureSources      []CaptureSource `json:"captureSources"`
}

// Profile is reusable context the user supplies to ground the analysis.
type Profile struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	People    string `json:"people"`
	Notes     string `json:"notes"`
	UpdatedAt string `json:"updatedAt"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS settings (
  id                     INTEGER PRIMARY KEY CHECK (id = 1),
  llm_base_url           TEXT    NOT NULL DEFAULT 'http://127.0.0.1:8080/v1',
  llm_model              TEXT    NOT NULL DEFAULT 'local-model',
  analysis_interval_sec  INTEGER NOT NULL DEFAULT 15,
  active_profile_id      INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO settings (id) VALUES (1);

CREATE TABLE IF NOT EXISTS profiles (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  summary    TEXT NOT NULL DEFAULT '',
  people     TEXT NOT NULL DEFAULT '',
  notes      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);`)
	if err != nil {
		return err
	}
	// Added after initial release — tolerate pre-existing databases.
	return s.addColumn("settings", "capture_sources", "TEXT NOT NULL DEFAULT '[]'")
}

// addColumn adds a column, ignoring the error if it already exists.
func (s *Store) addColumn(table, column, decl string) error {
	_, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

// GetSettings returns the persisted settings and whether an API key is stored.
func (s *Store) GetSettings() (Settings, error) {
	var st Settings
	var sourcesJSON string
	row := s.db.QueryRow(`SELECT llm_base_url, llm_model, analysis_interval_sec, active_profile_id, capture_sources FROM settings WHERE id = 1`)
	if err := row.Scan(&st.LLMBaseURL, &st.LLMModel, &st.AnalysisIntervalSec, &st.ActiveProfileID, &sourcesJSON); err != nil {
		return Settings{}, err
	}
	st.CaptureSources = []CaptureSource{}
	if sourcesJSON != "" {
		_ = json.Unmarshal([]byte(sourcesJSON), &st.CaptureSources)
	}
	if key, err := s.GetAPIKey(); err == nil && key != "" {
		st.HasAPIKey = true
	}
	return st, nil
}

// SaveSettings persists settings (excluding the API key).
func (s *Store) SaveSettings(st Settings) error {
	sources := st.CaptureSources
	if sources == nil {
		sources = []CaptureSource{}
	}
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE settings SET llm_base_url = ?, llm_model = ?, analysis_interval_sec = ?, active_profile_id = ?, capture_sources = ? WHERE id = 1`,
		st.LLMBaseURL, st.LLMModel, st.AnalysisIntervalSec, st.ActiveProfileID, string(sourcesJSON),
	)
	return err
}

// GetAPIKey reads the LLM API key from the OS keychain ("" if unset).
func (s *Store) GetAPIKey() (string, error) {
	key, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return key, err
}

// SetAPIKey stores (or, if empty, clears) the LLM API key in the OS keychain.
func (s *Store) SetAPIKey(key string) error {
	if key == "" {
		err := keyring.Delete(keyringService, keyringUser)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(keyringService, keyringUser, key)
}

// ListProfiles returns all saved profiles, most recently updated first.
func (s *Store) ListProfiles() ([]Profile, error) {
	rows, err := s.db.Query(`SELECT id, name, summary, people, notes, updated_at FROM profiles ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	profiles := []Profile{}
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.Name, &p.Summary, &p.People, &p.Notes, &p.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// GetProfile returns a single profile by id.
func (s *Store) GetProfile(id int64) (Profile, error) {
	var p Profile
	row := s.db.QueryRow(`SELECT id, name, summary, people, notes, updated_at FROM profiles WHERE id = ?`, id)
	err := row.Scan(&p.ID, &p.Name, &p.Summary, &p.People, &p.Notes, &p.UpdatedAt)
	return p, err
}

// SaveProfile inserts (ID == 0) or updates a profile, returning the saved row.
func (s *Store) SaveProfile(p Profile) (Profile, error) {
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if p.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO profiles (name, summary, people, notes, updated_at) VALUES (?, ?, ?, ?, ?)`,
			p.Name, p.Summary, p.People, p.Notes, p.UpdatedAt,
		)
		if err != nil {
			return Profile{}, err
		}
		p.ID, _ = res.LastInsertId()
		return p, nil
	}
	_, err := s.db.Exec(
		`UPDATE profiles SET name = ?, summary = ?, people = ?, notes = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Summary, p.People, p.Notes, p.UpdatedAt, p.ID,
	)
	return p, err
}

// DeleteProfile removes a profile.
func (s *Store) DeleteProfile(id int64) error {
	_, err := s.db.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	return err
}
