// Package store persists settings and reusable context profiles in SQLite, with
// the LLM API key kept in the OS keychain rather than the database.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"
)

const (
	keyringService = "Parley"
	// keyringUser is the legacy single-connection key slot. Each LLM connection
	// now stores its key under connKeyringUser(id); the legacy slot is migrated
	// into the first connection on upgrade and otherwise unused.
	keyringUser = "llm-api-key"
)

// connKeyringUser is the OS-keychain account under which a given LLM
// connection's API key is stored, keeping every provider's key in the vault.
func connKeyringUser(id int64) string {
	return "llm-api-key:" + strconv.FormatInt(id, 10)
}

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
	AnalysisTimeoutSec  int             `json:"analysisTimeoutSec"`
	LoggingLevel        string          `json:"loggingLevel"` // "trace" | "error" | "none"
	ActiveProfileID     int64           `json:"activeProfileID"`
	HasAPIKey           bool            `json:"hasAPIKey"`
	CaptureSources      []CaptureSource `json:"captureSources"`
	// SttBaseURL, when set, points transcription at a remote /inference-compatible
	// server (e.g. http://host:8765) instead of launching a local engine.
	SttBaseURL string `json:"sttBaseURL"`
	// WhisperModel is the model filename under resources/whisper/models used by the
	// bundled engine. Defaults to ggml-small.en-q5_1.bin (quantized; accurate but
	// light on CPU).
	WhisperModel string `json:"whisperModel"`
	// ActiveLLMConnectionID selects which saved LLM connection drives analysis.
	ActiveLLMConnectionID int64 `json:"activeLLMConnectionID"`
}

// LLMConnection is a saved, named LLM endpoint the user can switch between
// (e.g. a local llama-server, a cloud provider). The API key is kept in the OS
// keychain, never in this struct or the database.
type LLMConnection struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"baseURL"`
	Model     string `json:"model"`
	HasAPIKey bool   `json:"hasAPIKey"`
	UpdatedAt string `json:"updatedAt"`
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
);

CREATE TABLE IF NOT EXISTS sessions (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  title         TEXT NOT NULL DEFAULT '',
  started_at    TEXT NOT NULL DEFAULT '',
  ended_at      TEXT NOT NULL DEFAULT '',
  profile_id    INTEGER NOT NULL DEFAULT 0,
  audio_dir     TEXT NOT NULL DEFAULT '',
  analysis_json TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS transcript_segments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id INTEGER NOT NULL,
  seq        INTEGER NOT NULL,
  source     TEXT    NOT NULL,
  text       TEXT    NOT NULL,
  start_ms   INTEGER NOT NULL,
  end_ms     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_segments_session ON transcript_segments(session_id, seq);

CREATE TABLE IF NOT EXISTS live_notes (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  INTEGER NOT NULL,
  scope       TEXT    NOT NULL,          -- 'meeting' | 'topic'
  topic_title TEXT    NOT NULL DEFAULT '',
  text        TEXT    NOT NULL,
  created_at  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_live_notes_session ON live_notes(session_id);

CREATE TABLE IF NOT EXISTS llm_connections (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  base_url   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);`)
	if err != nil {
		return err
	}
	// Added after initial release — tolerate pre-existing databases.
	if err := s.addColumn("settings", "capture_sources", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumn("settings", "stt_base_url", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumn("settings", "whisper_model", "TEXT NOT NULL DEFAULT 'ggml-small.en-q5_1.bin'"); err != nil {
		return err
	}
	if err := s.addColumn("settings", "active_llm_connection_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumn("settings", "analysis_timeout_sec", "INTEGER NOT NULL DEFAULT 30"); err != nil {
		return err
	}
	if err := s.addColumn("settings", "logging_level", "TEXT NOT NULL DEFAULT 'trace'"); err != nil {
		return err
	}
	if err := s.addColumn("sessions", "status", "TEXT NOT NULL DEFAULT 'complete'"); err != nil {
		return err
	}
	return s.seedLLMConnections()
}

// seedLLMConnections creates the first saved LLM connection from the legacy
// single-endpoint settings (and migrates the legacy keychain key) the first time
// the new connections table is empty, so upgrading users keep their provider.
func (s *Store) seedLLMConnections() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM llm_connections`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var baseURL, model string
	_ = s.db.QueryRow(`SELECT llm_base_url, llm_model FROM settings WHERE id = 1`).Scan(&baseURL, &model)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080/v1"
	}
	if model == "" {
		model = "local-model"
	}
	res, err := s.db.Exec(
		`INSERT INTO llm_connections (name, base_url, model, updated_at) VALUES (?, ?, ?, ?)`,
		"Default", baseURL, model, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.Exec(`UPDATE settings SET active_llm_connection_id = ? WHERE id = 1`, id); err != nil {
		return err
	}
	// Carry the legacy single key over to the new per-connection slot.
	if key, err := keyring.Get(keyringService, keyringUser); err == nil && key != "" {
		_ = keyring.Set(keyringService, connKeyringUser(id), key)
	}
	return nil
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
	row := s.db.QueryRow(`SELECT llm_base_url, llm_model, analysis_interval_sec, analysis_timeout_sec, logging_level, active_profile_id, capture_sources, stt_base_url, whisper_model, active_llm_connection_id FROM settings WHERE id = 1`)
	if err := row.Scan(&st.LLMBaseURL, &st.LLMModel, &st.AnalysisIntervalSec, &st.AnalysisTimeoutSec, &st.LoggingLevel, &st.ActiveProfileID, &sourcesJSON, &st.SttBaseURL, &st.WhisperModel, &st.ActiveLLMConnectionID); err != nil {
		return Settings{}, err
	}
	if st.AnalysisIntervalSec <= 0 {
		st.AnalysisIntervalSec = 15
	}
	if st.AnalysisTimeoutSec <= 0 {
		st.AnalysisTimeoutSec = 30
	}
	if st.WhisperModel == "" {
		st.WhisperModel = "ggml-small.en-q5_1.bin"
	}
	st.LoggingLevel = normalizeLoggingLevel(st.LoggingLevel)
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
		`UPDATE settings SET llm_base_url = ?, llm_model = ?, analysis_interval_sec = ?, analysis_timeout_sec = ?, logging_level = ?, active_profile_id = ?, capture_sources = ?, stt_base_url = ?, whisper_model = ?, active_llm_connection_id = ? WHERE id = 1`,
		st.LLMBaseURL, st.LLMModel, st.AnalysisIntervalSec, st.AnalysisTimeoutSec, normalizeLoggingLevel(st.LoggingLevel), st.ActiveProfileID, string(sourcesJSON), st.SttBaseURL, st.WhisperModel, st.ActiveLLMConnectionID,
	)
	return err
}

func normalizeLoggingLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "none":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "trace"
	}
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

// GetConnectionAPIKey reads a connection's API key from the OS keychain.
func (s *Store) GetConnectionAPIKey(id int64) (string, error) {
	key, err := keyring.Get(keyringService, connKeyringUser(id))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return key, err
}

// SetConnectionAPIKey stores (or, if empty, clears) a connection's API key.
func (s *Store) SetConnectionAPIKey(id int64, key string) error {
	if key == "" {
		err := keyring.Delete(keyringService, connKeyringUser(id))
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(keyringService, connKeyringUser(id), key)
}

// ListLLMConnections returns all saved LLM connections, newest-updated first,
// each flagged with whether it has a stored API key.
func (s *Store) ListLLMConnections() ([]LLMConnection, error) {
	rows, err := s.db.Query(`SELECT id, name, base_url, model, updated_at FROM llm_connections ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	conns := []LLMConnection{}
	for rows.Next() {
		var c LLMConnection
		if err := rows.Scan(&c.ID, &c.Name, &c.BaseURL, &c.Model, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if key, err := s.GetConnectionAPIKey(c.ID); err == nil && key != "" {
			c.HasAPIKey = true
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

// GetLLMConnection returns a single connection by id.
func (s *Store) GetLLMConnection(id int64) (LLMConnection, error) {
	var c LLMConnection
	row := s.db.QueryRow(`SELECT id, name, base_url, model, updated_at FROM llm_connections WHERE id = ?`, id)
	if err := row.Scan(&c.ID, &c.Name, &c.BaseURL, &c.Model, &c.UpdatedAt); err != nil {
		return LLMConnection{}, err
	}
	if key, err := s.GetConnectionAPIKey(c.ID); err == nil && key != "" {
		c.HasAPIKey = true
	}
	return c, nil
}

// GetActiveLLMConnection returns the connection selected for analysis, falling
// back to the most recently updated one if the active id is unset/stale.
func (s *Store) GetActiveLLMConnection() (LLMConnection, error) {
	var id int64
	_ = s.db.QueryRow(`SELECT active_llm_connection_id FROM settings WHERE id = 1`).Scan(&id)
	if id != 0 {
		if c, err := s.GetLLMConnection(id); err == nil {
			return c, nil
		}
	}
	conns, err := s.ListLLMConnections()
	if err != nil {
		return LLMConnection{}, err
	}
	if len(conns) == 0 {
		return LLMConnection{}, errors.New("no LLM connection configured")
	}
	return conns[0], nil
}

// SetActiveLLMConnection records which connection should drive analysis.
func (s *Store) SetActiveLLMConnection(id int64) error {
	_, err := s.db.Exec(`UPDATE settings SET active_llm_connection_id = ? WHERE id = 1`, id)
	return err
}

// SaveLLMConnection inserts (ID == 0) or updates a connection, returning the
// saved row. A newly inserted connection becomes active if none was set.
func (s *Store) SaveLLMConnection(c LLMConnection) (LLMConnection, error) {
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if c.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO llm_connections (name, base_url, model, updated_at) VALUES (?, ?, ?, ?)`,
			c.Name, c.BaseURL, c.Model, c.UpdatedAt,
		)
		if err != nil {
			return LLMConnection{}, err
		}
		c.ID, _ = res.LastInsertId()
		var active int64
		_ = s.db.QueryRow(`SELECT active_llm_connection_id FROM settings WHERE id = 1`).Scan(&active)
		if active == 0 {
			_ = s.SetActiveLLMConnection(c.ID)
		}
		return c, nil
	}
	_, err := s.db.Exec(
		`UPDATE llm_connections SET name = ?, base_url = ?, model = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.BaseURL, c.Model, c.UpdatedAt, c.ID,
	)
	return c, err
}

// DeleteLLMConnection removes a connection and its stored key. If it was the
// active one, the most recently updated remaining connection becomes active.
func (s *Store) DeleteLLMConnection(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM llm_connections WHERE id = ?`, id); err != nil {
		return err
	}
	_ = s.SetConnectionAPIKey(id, "") // drop its key from the keychain

	var active int64
	_ = s.db.QueryRow(`SELECT active_llm_connection_id FROM settings WHERE id = 1`).Scan(&active)
	if active == id {
		next := int64(0)
		if conns, err := s.ListLLMConnections(); err == nil && len(conns) > 0 {
			next = conns[0].ID
		}
		return s.SetActiveLLMConnection(next)
	}
	return nil
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
