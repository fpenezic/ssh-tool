package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")
var ErrCycle = errors.New("cycle detected in folder hierarchy")
var ErrInUse = errors.New("credential in use")

// NewFolder is the create-input for a folder. Pointers default to zero.
type NewFolder struct {
	ParentID  *string
	Name      string
	SortOrder int64
	Settings  InheritableSettings
}

func newID() string { return uuid.New().String() }

func (d *DB) CreateFolder(in NewFolder) (*Folder, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("validation: folder name is empty")
	}
	id := newID()
	ts := now()
	settingsJSON, err := json.Marshal(in.Settings)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	_, err = d.conn.Exec(
		`INSERT INTO folders (id, parent_id, name, sort_order, settings_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.ParentID, in.Name, in.SortOrder, string(settingsJSON), ts, ts,
	)
	if err != nil {
		return nil, err
	}
	return d.GetFolder(id)
}

func (d *DB) GetFolder(id string) (*Folder, error) {
	row := d.conn.QueryRow(
		`SELECT id, parent_id, name, sort_order, settings_json, icon_image_id, icon_name, icon_color, created_at, updated_at
		 FROM folders WHERE id = ?`, id,
	)
	return scanFolder(row)
}

func (d *DB) ListFolders() ([]Folder, error) {
	rows, err := d.conn.Query(
		`SELECT id, parent_id, name, sort_order, settings_json, icon_image_id, icon_name, icon_color, created_at, updated_at
		 FROM folders ORDER BY sort_order, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// UpdateFolder applies a partial update. Nil fields mean "keep existing"
// except ClearParent which forces parent_id = NULL.
type UpdateFolder struct {
	ID          string
	ParentID    *string
	ClearParent bool
	Name        *string
	SortOrder   *int64
	Settings    *InheritableSettings
}

func (d *DB) UpdateFolder(in UpdateFolder) (*Folder, error) {
	existing, err := d.GetFolder(in.ID)
	if err != nil {
		return nil, err
	}

	newParent := existing.ParentID
	if in.ClearParent {
		newParent = nil
	} else if in.ParentID != nil {
		newParent = in.ParentID
	}
	if newParent != nil {
		if *newParent == in.ID {
			return nil, ErrCycle
		}
		if err := d.checkNoCycle(in.ID, *newParent); err != nil {
			return nil, err
		}
	}

	newName := existing.Name
	if in.Name != nil {
		newName = *in.Name
	}
	if newName == "" {
		return nil, fmt.Errorf("validation: folder name is empty")
	}
	newSort := existing.SortOrder
	if in.SortOrder != nil {
		newSort = *in.SortOrder
	}
	newSettings := existing.Settings
	if in.Settings != nil {
		newSettings = *in.Settings
	}

	settingsJSON, err := json.Marshal(newSettings)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(
		`UPDATE folders SET parent_id=?, name=?, sort_order=?, settings_json=?, updated_at=?
		 WHERE id=?`,
		newParent, newName, newSort, string(settingsJSON), now(), in.ID,
	)
	if err != nil {
		return nil, err
	}
	return d.GetFolder(in.ID)
}

func (d *DB) DeleteFolder(id string) error {
	res, err := d.conn.Exec("DELETE FROM folders WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// checkNoCycle walks ancestors of proposedParent; cycle if movingID appears.
func (d *DB) checkNoCycle(movingID, proposedParent string) error {
	current := proposedParent
	for i := 0; i < 10_000; i++ {
		if current == movingID {
			return ErrCycle
		}
		var parent sql.NullString
		err := d.conn.QueryRow(
			"SELECT parent_id FROM folders WHERE id = ?", current,
		).Scan(&parent)
		if err != nil || !parent.Valid {
			return nil
		}
		current = parent.String
	}
	return ErrCycle
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanFolder(s scanner) (*Folder, error) {
	var (
		f           Folder
		parentID    sql.NullString
		settingsRaw string
		iconID      sql.NullString
		iconName    sql.NullString
		iconColor   sql.NullString
	)
	err := s.Scan(&f.ID, &parentID, &f.Name, &f.SortOrder, &settingsRaw, &iconID, &iconName, &iconColor, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if parentID.Valid {
		f.ParentID = &parentID.String
	}
	if iconID.Valid {
		f.IconImageID = &iconID.String
	}
	if iconName.Valid {
		f.IconName = &iconName.String
	}
	if iconColor.Valid {
		f.IconColor = &iconColor.String
	}
	if err := json.Unmarshal([]byte(settingsRaw), &f.Settings); err != nil {
		return nil, fmt.Errorf("unmarshal folder settings: %w", err)
	}
	return &f, nil
}
