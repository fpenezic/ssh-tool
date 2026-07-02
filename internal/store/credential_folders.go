package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (d *DB) CreateCredentialFolder(name string, parentID *string) (*CredentialFolder, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("validation: folder name is empty")
	}
	id := newID()
	ts := now()
	_, err := d.conn.Exec(
		`INSERT INTO credential_folders (id, parent_id, name, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		id, parentID, name, ts, ts,
	)
	if err != nil {
		return nil, err
	}
	return d.GetCredentialFolder(id)
}

func (d *DB) GetCredentialFolder(id string) (*CredentialFolder, error) {
	row := d.conn.QueryRow(
		`SELECT id, parent_id, name, sort_order, created_at, updated_at
		 FROM credential_folders WHERE id = ?`, id,
	)
	return scanCredentialFolder(row)
}

func (d *DB) ListCredentialFolders() ([]CredentialFolder, error) {
	rows, err := d.conn.Query(
		`SELECT id, parent_id, name, sort_order, created_at, updated_at
		 FROM credential_folders ORDER BY sort_order, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialFolder
	for rows.Next() {
		f, err := scanCredentialFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (d *DB) UpdateCredentialFolder(id string, name *string, parentID *string, clearParent bool) (*CredentialFolder, error) {
	existing, err := d.GetCredentialFolder(id)
	if err != nil {
		return nil, err
	}
	newName := existing.Name
	if name != nil {
		newName = strings.TrimSpace(*name)
	}
	if newName == "" {
		return nil, fmt.Errorf("validation: folder name is empty")
	}
	var newParent *string
	if clearParent {
		newParent = nil
	} else if parentID != nil {
		newParent = parentID
	} else {
		newParent = existing.ParentID
	}
	if newParent != nil {
		if err := d.checkNoCredentialFolderCycle(id, *newParent); err != nil {
			return nil, err
		}
	}
	_, err = d.conn.Exec(
		`UPDATE credential_folders SET name=?, parent_id=?, updated_at=? WHERE id=?`,
		newName, newParent, now(), id,
	)
	if err != nil {
		return nil, err
	}
	return d.GetCredentialFolder(id)
}

func (d *DB) checkNoCredentialFolderCycle(movingID, proposedParent string) error {
	current := proposedParent
	for i := 0; i < 10_000; i++ {
		if current == movingID {
			return ErrCycle
		}
		var parent sql.NullString
		err := d.conn.QueryRow(
			"SELECT parent_id FROM credential_folders WHERE id = ?", current,
		).Scan(&parent)
		if err != nil || !parent.Valid {
			return nil
		}
		current = parent.String
	}
	return ErrCycle
}

func (d *DB) DeleteCredentialFolder(id string) error {
	res, err := d.conn.Exec("DELETE FROM credential_folders WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanCredentialFolder(s scanner) (*CredentialFolder, error) {
	var f CredentialFolder
	var parentID sql.NullString
	err := s.Scan(&f.ID, &parentID, &f.Name, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if parentID.Valid {
		f.ParentID = &parentID.String
	}
	return &f, nil
}
