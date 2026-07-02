package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Snippet is a short reusable command body the user fires into an
// active terminal. Global by default (ConnectionID nil); set
// ConnectionID to scope a snippet to a single connection.
type Snippet struct {
	ID           string   `json:"id"`
	ConnectionID *string  `json:"connection_id,omitempty"`
	Name         string   `json:"name"`
	Body         string   `json:"body"`
	Tags         []string `json:"tags"`
	UseCount     int64    `json:"use_count"`
	LastUsedAt   *int64   `json:"last_used_at,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

// SnippetInput is what the frontend sends for create / update.
type SnippetInput struct {
	ConnectionID *string  `json:"connection_id,omitempty"`
	Name         string   `json:"name"`
	Body         string   `json:"body"`
	Tags         []string `json:"tags"`
}

func (d *DB) ListSnippets(connectionID *string) ([]Snippet, error) {
	// Two queries combined: per-connection rows (when conn is given) plus
	// all global rows. Global rows come back even when connectionID is
	// nil. Order by last-used desc, then name.
	var rows *sql.Rows
	var err error
	if connectionID != nil {
		rows, err = d.conn.Query(`
			SELECT id, connection_id, name, body, tags_json, use_count,
			       last_used_at, created_at, updated_at
			FROM snippets
			WHERE connection_id IS NULL OR connection_id = ?
			ORDER BY (last_used_at IS NULL), last_used_at DESC, name
		`, *connectionID)
	} else {
		rows, err = d.conn.Query(`
			SELECT id, connection_id, name, body, tags_json, use_count,
			       last_used_at, created_at, updated_at
			FROM snippets
			ORDER BY (last_used_at IS NULL), last_used_at DESC, name
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Snippet, 0, 32)
	for rows.Next() {
		var s Snippet
		var tagsJSON string
		if err := rows.Scan(&s.ID, &s.ConnectionID, &s.Name, &s.Body, &tagsJSON,
			&s.UseCount, &s.LastUsedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if tagsJSON != "" {
			_ = json.Unmarshal([]byte(tagsJSON), &s.Tags)
		}
		if s.Tags == nil {
			s.Tags = []string{}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) GetSnippet(id string) (*Snippet, error) {
	var s Snippet
	var tagsJSON string
	err := d.conn.QueryRow(`
		SELECT id, connection_id, name, body, tags_json, use_count,
		       last_used_at, created_at, updated_at
		FROM snippets WHERE id = ?
	`, id).Scan(&s.ID, &s.ConnectionID, &s.Name, &s.Body, &tagsJSON,
		&s.UseCount, &s.LastUsedAt, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("snippet %s not found", id)
		}
		return nil, err
	}
	if tagsJSON != "" {
		_ = json.Unmarshal([]byte(tagsJSON), &s.Tags)
	}
	if s.Tags == nil {
		s.Tags = []string{}
	}
	return &s, nil
}

func (d *DB) CreateSnippet(in SnippetInput) (*Snippet, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("snippet name is required")
	}
	if in.Body == "" {
		return nil, fmt.Errorf("snippet body is required")
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	tagsJSON, _ := json.Marshal(in.Tags)
	id := uuid.New().String()
	now := time.Now().Unix()
	_, err := d.conn.Exec(`
		INSERT INTO snippets (id, connection_id, name, body, tags_json,
		                      use_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)
	`, id, in.ConnectionID, in.Name, in.Body, string(tagsJSON), now, now)
	if err != nil {
		return nil, err
	}
	return d.GetSnippet(id)
}

func (d *DB) UpdateSnippet(id string, in SnippetInput) (*Snippet, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("snippet name is required")
	}
	if in.Body == "" {
		return nil, fmt.Errorf("snippet body is required")
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	tagsJSON, _ := json.Marshal(in.Tags)
	now := time.Now().Unix()
	res, err := d.conn.Exec(`
		UPDATE snippets
		SET connection_id = ?, name = ?, body = ?, tags_json = ?, updated_at = ?
		WHERE id = ?
	`, in.ConnectionID, in.Name, in.Body, string(tagsJSON), now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("snippet %s not found", id)
	}
	return d.GetSnippet(id)
}

func (d *DB) DeleteSnippet(id string) error {
	_, err := d.conn.Exec(`DELETE FROM snippets WHERE id = ?`, id)
	return err
}

// RecordSnippetUse bumps use_count and last_used_at. Best-effort -
// errors are returned but the typical caller ignores them.
func (d *DB) RecordSnippetUse(id string) error {
	now := time.Now().Unix()
	_, err := d.conn.Exec(`
		UPDATE snippets SET use_count = use_count + 1, last_used_at = ?
		WHERE id = ?
	`, now, id)
	return err
}
