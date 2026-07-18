package store

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PutImage stores an image blob, content-addressed by MD5. If the same image
// already exists, returns its existing id (no duplicate row). Returns the
// row id either way.
//
// RDM exports tend to repeat the same PNG hundreds of times across many
// connections sharing a customer logo. Hashing on insert avoids bloating
// the DB.
func (d *DB) PutImage(data []byte, mimeType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty image")
	}
	sum := md5.Sum(data)
	hash := hex.EncodeToString(sum[:])

	var existingID string
	err := d.conn.QueryRow("SELECT id FROM images WHERE md5 = ?", hash).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if mimeType == "" {
		mimeType = "image/png"
	}
	id := newID()
	_, err = d.conn.Exec(
		`INSERT INTO images (id, md5, mime_type, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, hash, mimeType, data, time.Now().Unix(),
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetImage returns (mime, data, true) for the given image id, or
// (_, _, false) if not found.
func (d *DB) GetImage(id string) (string, []byte, bool, error) {
	var mime string
	var data []byte
	err := d.conn.QueryRow("SELECT mime_type, data FROM images WHERE id = ?", id).
		Scan(&mime, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return mime, data, true, nil
}

// ListImageIDs returns every image id in the store paired with a
// usage count (how many folders + connections reference it). Used
// by the icon picker so the user can pick from images already in
// the DB (e.g. RDM-imported logos) instead of re-uploading them.
type ImageSummary struct {
	ID       string `json:"id"`
	MIME     string `json:"mime"`
	UseCount int    `json:"use_count"`
}

func (d *DB) ListImageIDs() ([]ImageSummary, error) {
	rows, err := d.conn.Query(`
		SELECT i.id, i.mime_type,
		       (SELECT COUNT(*) FROM folders f WHERE f.icon_image_id = i.id)
		     + (SELECT COUNT(*) FROM connections c WHERE c.icon_image_id = i.id)
		     + (SELECT COUNT(*) FROM credential_refs cr WHERE cr.icon_image_id = i.id)
		  FROM images i
		 ORDER BY i.created_at DESC, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageSummary
	for rows.Next() {
		var s ImageSummary
		if err := rows.Scan(&s.ID, &s.MIME, &s.UseCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetFolderIcon updates the icon_image_id on a folder. Pass empty string to
// clear. Setting an uploaded image clears any built-in icon (they are
// mutually exclusive - one icon source at a time).
func (d *DB) SetFolderIcon(folderID, imageID string) error {
	var v interface{}
	if imageID != "" {
		v = imageID
	}
	_, err := d.conn.Exec("UPDATE folders SET icon_image_id = ?, icon_name = NULL, icon_color = NULL, updated_at = ? WHERE id = ?",
		v, time.Now().Unix(), folderID)
	return err
}

// SetConnectionIcon updates the icon_image_id on a connection. Pass empty
// string to clear. Setting an uploaded image clears any built-in icon.
func (d *DB) SetConnectionIcon(connID, imageID string) error {
	var v interface{}
	if imageID != "" {
		v = imageID
	}
	_, err := d.conn.Exec("UPDATE connections SET icon_image_id = ?, icon_name = NULL, icon_color = NULL, updated_at = ? WHERE id = ?",
		v, time.Now().Unix(), connID)
	return err
}

// SetFolderNamedIcon sets a built-in (lucide) icon + palette colour on a
// folder, clearing any uploaded image. Pass an empty name to clear the
// built-in icon (falls back to the default). color may be empty.
func (d *DB) SetFolderNamedIcon(folderID, name, color string) error {
	var n, c interface{}
	if name != "" {
		n = name
		if color != "" {
			c = color
		}
	}
	_, err := d.conn.Exec("UPDATE folders SET icon_name = ?, icon_color = ?, icon_image_id = NULL, updated_at = ? WHERE id = ?",
		n, c, time.Now().Unix(), folderID)
	return err
}

// SetConnectionNamedIcon sets a built-in icon + palette colour on a
// connection, clearing any uploaded image. Empty name clears it.
func (d *DB) SetConnectionNamedIcon(connID, name, color string) error {
	var n, c interface{}
	if name != "" {
		n = name
		if color != "" {
			c = color
		}
	}
	_, err := d.conn.Exec("UPDATE connections SET icon_name = ?, icon_color = ?, icon_image_id = NULL, updated_at = ? WHERE id = ?",
		n, c, time.Now().Unix(), connID)
	return err
}
