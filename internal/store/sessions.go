package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Segment is one transcribed slice of a session's audio. It mirrors stt.Segment
// but lives here so the store has no dependency on the capture/STT packages.
type Segment struct {
	Source  string `json:"source"`
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

// LiveNote is a piece of context the user injects mid-meeting. A "meeting"-scoped
// note applies for the whole session (names, themes); a "topic"-scoped note only
// applies while its topic is current and expires when the topic rolls over.
type LiveNote struct {
	ID         int64  `json:"id"`
	Scope      string `json:"scope"`      // "meeting" | "topic"
	TopicTitle string `json:"topicTitle"` // topic active when a "topic" note was added
	Text       string `json:"text"`
	CreatedAt  string `json:"createdAt"`
}

// ContextSnapshot is the exact reusable context selected when a session was
// created. Captured distinguishes a new session with no selected context from a
// legacy session created before snapshots existed.
type ContextSnapshot struct {
	Captured bool   `json:"captured"`
	Name     string `json:"name"`
	Summary  string `json:"summary"`
	People   string `json:"people"`
	Notes    string `json:"notes"`
}

// Session is a (possibly multi-part) recorded meeting.
type Session struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	StartedAt    string `json:"startedAt"`
	EndedAt      string `json:"endedAt"`
	Status       string `json:"status"`
	ProfileID    int64  `json:"profileID"`
	AudioDir     string `json:"audioDir"`
	SegmentCount int    `json:"segmentCount"` // populated by ListSessions for the browser
}

// SessionBundle is the full persisted state of a meeting, used to reload/resume.
// AnalysisJSON is the analysis.State serialised as JSON; the caller (which knows
// that type) unmarshals it, keeping this package free of the analysis import.
type SessionBundle struct {
	Session         Session         `json:"session"`
	Segments        []Segment       `json:"segments"`
	AnalysisJSON    string          `json:"analysisJSON"`
	LiveNotes       []LiveNote      `json:"liveNotes"`
	ContextSnapshot ContextSnapshot `json:"contextSnapshot"`
}

// CreateSession inserts a new session row and returns its id.
func (s *Store) CreateSession(title string, profileID int64, audioDir string, snapshot ContextSnapshot) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	snapshot.Captured = true
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO sessions (title, started_at, profile_id, audio_dir, status, context_snapshot_json) VALUES (?, ?, ?, ?, ?, ?)`,
		title, now, profileID, audioDir, "recording", string(snapshotJSON),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndSession stamps a session's end time and (if non-empty) its audio dir.
func (s *Store) EndSession(id int64, audioDir string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if audioDir == "" {
		_, err := s.db.Exec(`UPDATE sessions SET ended_at = ?, status = 'complete' WHERE id = ?`, now, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = ?, audio_dir = ?, status = 'complete' WHERE id = ?`, now, audioDir, id)
	return err
}

// SetSessionStatus records lifecycle state such as recording/finalizing/complete.
func (s *Store) SetSessionStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, status, id)
	return err
}

// SetSessionTitle renames a session.
func (s *Store) SetSessionTitle(id int64, title string) error {
	_, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, title, id)
	return err
}

// SetSessionAudioDir records the audio directory for the active part.
func (s *Store) SetSessionAudioDir(id int64, audioDir string) error {
	_, err := s.db.Exec(`UPDATE sessions SET audio_dir = ? WHERE id = ?`, audioDir, id)
	return err
}

// AppendSegment stores a transcript segment, assigning the next sequence number.
func (s *Store) AppendSegment(sessionID int64, seg Segment) error {
	var seq int64
	row := s.db.QueryRow(`SELECT COALESCE(MAX(seq), -1) + 1 FROM transcript_segments WHERE session_id = ?`, sessionID)
	if err := row.Scan(&seq); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO transcript_segments (session_id, seq, source, text, start_ms, end_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, seq, seg.Source, seg.Text, seg.StartMs, seg.EndMs,
	)
	return err
}

// SaveAnalysis overwrites the persisted analysis snapshot for a session. The
// analysis State is a full snapshot recomputed each cadence, so storing it as a
// single JSON blob keeps save/load exact without per-row churn.
func (s *Store) SaveAnalysis(sessionID int64, analysisJSON string) error {
	_, err := s.db.Exec(`UPDATE sessions SET analysis_json = ? WHERE id = ?`, analysisJSON, sessionID)
	return err
}

// AddLiveNote persists a live context note and returns it with its id/timestamp.
func (s *Store) AddLiveNote(sessionID int64, note LiveNote) (LiveNote, error) {
	note.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO live_notes (session_id, scope, topic_title, text, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, note.Scope, note.TopicTitle, note.Text, note.CreatedAt,
	)
	if err != nil {
		return LiveNote{}, err
	}
	note.ID, _ = res.LastInsertId()
	return note, nil
}

// ListSessions returns all sessions, newest first, with their segment counts.
func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`
SELECT s.id, s.title, s.started_at, s.ended_at, s.status, s.profile_id, s.audio_dir,
       (SELECT COUNT(*) FROM transcript_segments t WHERE t.session_id = s.id)
FROM sessions s
ORDER BY s.started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.Title, &x.StartedAt, &x.EndedAt, &x.Status, &x.ProfileID, &x.AudioDir, &x.SegmentCount); err != nil {
			return nil, err
		}
		sessions = append(sessions, x)
	}
	return sessions, rows.Err()
}

// GetSessionBundle loads a session's full state (segments, analysis, live notes).
func (s *Store) GetSessionBundle(id int64) (SessionBundle, error) {
	var b SessionBundle
	var snapshotJSON string
	row := s.db.QueryRow(
		`SELECT id, title, started_at, ended_at, status, profile_id, audio_dir, analysis_json, context_snapshot_json FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&b.Session.ID, &b.Session.Title, &b.Session.StartedAt, &b.Session.EndedAt,
		&b.Session.Status, &b.Session.ProfileID, &b.Session.AudioDir, &b.AnalysisJSON, &snapshotJSON); err != nil {
		return SessionBundle{}, err
	}
	if snapshotJSON != "" {
		_ = json.Unmarshal([]byte(snapshotJSON), &b.ContextSnapshot)
	}

	segs, err := s.segments(id)
	if err != nil {
		return SessionBundle{}, err
	}
	b.Segments = segs
	b.Session.SegmentCount = len(segs)

	notes, err := s.liveNotes(id)
	if err != nil {
		return SessionBundle{}, err
	}
	b.LiveNotes = notes
	return b, nil
}

func (s *Store) segments(sessionID int64) ([]Segment, error) {
	rows, err := s.db.Query(
		`SELECT source, text, start_ms, end_ms FROM transcript_segments WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	segs := []Segment{}
	for rows.Next() {
		var x Segment
		if err := rows.Scan(&x.Source, &x.Text, &x.StartMs, &x.EndMs); err != nil {
			return nil, err
		}
		segs = append(segs, x)
	}
	return segs, rows.Err()
}

func (s *Store) liveNotes(sessionID int64) ([]LiveNote, error) {
	rows, err := s.db.Query(
		`SELECT id, scope, topic_title, text, created_at FROM live_notes WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := []LiveNote{}
	for rows.Next() {
		var x LiveNote
		if err := rows.Scan(&x.ID, &x.Scope, &x.TopicTitle, &x.Text, &x.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, x)
	}
	return notes, rows.Err()
}

// DeleteSession removes a session and all its child rows.
func (s *Store) DeleteSession(id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM transcript_segments WHERE session_id = ?`,
			`DELETE FROM live_notes WHERE session_id = ?`,
			`DELETE FROM sessions WHERE id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
