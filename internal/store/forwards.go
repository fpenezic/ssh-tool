package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProxyBookmark is a named URL shortcut stored on a dynamic (SOCKS5) forward.
type ProxyBookmark struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PortForward is the persisted spec of a forward (not its running state).
// Auto-start ones get instantiated whenever the parent connection connects.
type PortForward struct {
	ID           string          `json:"id"`
	ConnectionID string          `json:"connection_id"`
	Kind         string          `json:"kind"` // local|remote|dynamic
	LocalAddr    *string         `json:"local_addr"`
	LocalPort    *uint16         `json:"local_port"`
	RemoteHost   *string         `json:"remote_host"`
	RemotePort   *uint16         `json:"remote_port"`
	AutoStart    bool            `json:"auto_start"`
	Description  string          `json:"description"`
	Bookmarks    []ProxyBookmark `json:"bookmarks"`
}

type NewPortForward struct {
	ConnectionID string
	Kind         string
	LocalAddr    *string
	LocalPort    *uint16
	RemoteHost   *string
	RemotePort   *uint16
	AutoStart    bool
	Description  string
}

func (d *DB) CreatePortForward(in NewPortForward) (*PortForward, error) {
	if in.Kind != "local" && in.Kind != "remote" && in.Kind != "dynamic" {
		return nil, fmt.Errorf("kind must be local|remote|dynamic, got %q", in.Kind)
	}
	if in.ConnectionID == "" {
		return nil, fmt.Errorf("connection_id required")
	}
	if in.Kind != "dynamic" && (in.RemoteHost == nil || in.RemotePort == nil) {
		return nil, fmt.Errorf("%s forward needs remote_host + remote_port", in.Kind)
	}
	id := newID()
	_, err := d.conn.Exec(
		`INSERT INTO port_forwards
		 (id, connection_id, kind, local_addr, local_port, remote_host, remote_port, auto_start, description)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ConnectionID, in.Kind,
		nullableStr(in.LocalAddr), nullableU16(in.LocalPort),
		nullableStr(in.RemoteHost), nullableU16(in.RemotePort),
		boolToInt(in.AutoStart), in.Description,
	)
	if err != nil {
		return nil, err
	}
	return d.GetPortForward(id)
}

func (d *DB) GetPortForward(id string) (*PortForward, error) {
	row := d.conn.QueryRow(
		`SELECT id, connection_id, kind, local_addr, local_port,
		        remote_host, remote_port, auto_start, description, bookmarks
		 FROM port_forwards WHERE id = ?`, id,
	)
	return scanPortForward(row)
}

func (d *DB) ListPortForwards(connectionID string) ([]PortForward, error) {
	rows, err := d.conn.Query(
		`SELECT id, connection_id, kind, local_addr, local_port,
		        remote_host, remote_port, auto_start, description, bookmarks
		 FROM port_forwards WHERE connection_id = ? ORDER BY description`,
		connectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortForward
	for rows.Next() {
		f, err := scanPortForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ListAllPortForwards returns every port-forward in the DB across all
// connections. Used by the global quick palette so it can offer
// "start tunnel" / "open bookmark" actions without firing one IPC per
// connection.
func (d *DB) ListAllPortForwards() ([]PortForward, error) {
	rows, err := d.conn.Query(
		`SELECT id, connection_id, kind, local_addr, local_port,
		        remote_host, remote_port, auto_start, description, bookmarks
		 FROM port_forwards ORDER BY connection_id, description`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortForward
	for rows.Next() {
		f, err := scanPortForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

type UpdatePortForward struct {
	ID              string
	LocalAddr       *string
	ClearLocalAddr  bool
	LocalPort       *uint16
	ClearLocalPort  bool
	RemoteHost      *string
	ClearRemoteHost bool
	RemotePort      *uint16
	ClearRemotePort bool
	AutoStart       *bool
	Description     *string
}

func (d *DB) UpdatePortForward(in UpdatePortForward) (*PortForward, error) {
	existing, err := d.GetPortForward(in.ID)
	if err != nil {
		return nil, err
	}
	la := existing.LocalAddr
	if in.ClearLocalAddr {
		la = nil
	} else if in.LocalAddr != nil {
		la = in.LocalAddr
	}
	lp := existing.LocalPort
	if in.ClearLocalPort {
		lp = nil
	} else if in.LocalPort != nil {
		lp = in.LocalPort
	}
	rh := existing.RemoteHost
	if in.ClearRemoteHost {
		rh = nil
	} else if in.RemoteHost != nil {
		rh = in.RemoteHost
	}
	rp := existing.RemotePort
	if in.ClearRemotePort {
		rp = nil
	} else if in.RemotePort != nil {
		rp = in.RemotePort
	}
	auto := existing.AutoStart
	if in.AutoStart != nil {
		auto = *in.AutoStart
	}
	desc := existing.Description
	if in.Description != nil {
		desc = *in.Description
	}

	_, err = d.conn.Exec(
		`UPDATE port_forwards SET
		   local_addr=?, local_port=?, remote_host=?, remote_port=?,
		   auto_start=?, description=?
		 WHERE id=?`,
		nullableStr(la), nullableU16(lp),
		nullableStr(rh), nullableU16(rp),
		boolToInt(auto), desc, in.ID,
	)
	if err != nil {
		return nil, err
	}
	return d.GetPortForward(in.ID)
}

// SetPortForwardBookmarks replaces the bookmarks list on a dynamic forward.
func (d *DB) SetPortForwardBookmarks(id string, bookmarks []ProxyBookmark) error {
	if bookmarks == nil {
		bookmarks = []ProxyBookmark{}
	}
	b, err := json.Marshal(bookmarks)
	if err != nil {
		return fmt.Errorf("marshal bookmarks: %w", err)
	}
	res, err := d.conn.Exec("UPDATE port_forwards SET bookmarks=? WHERE id=?", string(b), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) DeletePortForward(id string) error {
	res, err := d.conn.Exec("DELETE FROM port_forwards WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanPortForward(s scanner) (*PortForward, error) {
	var (
		f             PortForward
		la, rh        sql.NullString
		lp, rp        sql.NullInt64
		auto          int64
		descRaw       sql.NullString
		bookmarksJSON sql.NullString
	)
	err := s.Scan(
		&f.ID, &f.ConnectionID, &f.Kind, &la, &lp, &rh, &rp, &auto, &descRaw, &bookmarksJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if la.Valid {
		v := la.String
		f.LocalAddr = &v
	}
	if lp.Valid {
		v := uint16(lp.Int64)
		f.LocalPort = &v
	}
	if rh.Valid {
		v := rh.String
		f.RemoteHost = &v
	}
	if rp.Valid {
		v := uint16(rp.Int64)
		f.RemotePort = &v
	}
	f.AutoStart = auto != 0
	if descRaw.Valid {
		f.Description = strings.TrimSpace(descRaw.String)
	}
	if bookmarksJSON.Valid && bookmarksJSON.String != "" {
		_ = json.Unmarshal([]byte(bookmarksJSON.String), &f.Bookmarks)
	}
	if f.Bookmarks == nil {
		f.Bookmarks = []ProxyBookmark{}
	}
	return &f, nil
}

func nullableStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullableU16(p *uint16) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}
