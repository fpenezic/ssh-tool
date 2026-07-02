package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Workspace is a named snapshot of tabs + their layout + group metadata.
// LayoutJSON is opaque to the store layer - the frontend writes a
// shape it knows how to read on restore. We just persist the blob.
type Workspace struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	LayoutJSON   string `json:"layout_json"`
	LastOpenedAt *int64 `json:"last_opened_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func (d *DB) ListWorkspaces() ([]Workspace, error) {
	rows, err := d.conn.Query(`
		SELECT id, name, layout_json, last_opened_at, created_at, updated_at
		FROM workspaces
		ORDER BY (last_opened_at IS NULL), last_opened_at DESC, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Workspace, 0)
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.LayoutJSON, &w.LastOpenedAt, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *DB) GetWorkspace(id string) (*Workspace, error) {
	var w Workspace
	err := d.conn.QueryRow(`
		SELECT id, name, layout_json, last_opened_at, created_at, updated_at
		FROM workspaces WHERE id = ?
	`, id).Scan(&w.ID, &w.Name, &w.LayoutJSON, &w.LastOpenedAt, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("workspace %s not found", id)
		}
		return nil, err
	}
	return &w, nil
}

func (d *DB) CreateWorkspace(name, layoutJSON string) (*Workspace, error) {
	if name == "" {
		return nil, fmt.Errorf("workspace name is required")
	}
	if layoutJSON == "" {
		layoutJSON = "[]"
	}
	id := uuid.New().String()
	now := time.Now().Unix()
	_, err := d.conn.Exec(`
		INSERT INTO workspaces (id, name, layout_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, name, layoutJSON, now, now)
	if err != nil {
		return nil, err
	}
	return d.GetWorkspace(id)
}

func (d *DB) UpdateWorkspace(id, name, layoutJSON string) (*Workspace, error) {
	now := time.Now().Unix()
	res, err := d.conn.Exec(`
		UPDATE workspaces
		SET name = ?, layout_json = ?, updated_at = ?
		WHERE id = ?
	`, name, layoutJSON, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("workspace %s not found", id)
	}
	return d.GetWorkspace(id)
}

func (d *DB) DeleteWorkspace(id string) error {
	_, err := d.conn.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	return err
}

// TouchWorkspaceLastOpened bumps last_opened_at; used by the "Open"
// action so the picker can sort by recency.
func (d *DB) TouchWorkspaceLastOpened(id string) error {
	now := time.Now().Unix()
	_, err := d.conn.Exec(`UPDATE workspaces SET last_opened_at = ? WHERE id = ?`, now, id)
	return err
}
