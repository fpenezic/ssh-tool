package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEvent is one row of the local audit log.
type AuditEvent struct {
	ID       int64             `json:"id"`
	TS       int64             `json:"ts"` // unix seconds
	Action   string            `json:"action"`
	Target   string            `json:"target"`
	Metadata map[string]string `json:"metadata"`
}

// AppendAudit inserts a single event. Failures are returned to the
// caller; the higher-level recordAudit helper in app.go swallows them
// to a log line so a write failure can never break the underlying op.
func (d *DB) AppendAudit(action, target string, metadata map[string]string) error {
	if metadata == nil {
		metadata = map[string]string{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = d.audit.Exec(
		`INSERT INTO audit_events (ts, action, target, metadata_json) VALUES (?, ?, ?, ?)`,
		time.Now().Unix(), action, target, string(raw),
	)
	return err
}

// AuditFilter narrows ListAudit results.
type AuditFilter struct {
	Action string // exact match if non-empty
	Limit  int    // 0 -> 500
	Before int64  // unix seconds upper bound (exclusive), 0 = no bound
}

// ListAudit returns events newest-first.
func (d *DB) ListAudit(f AuditFilter) ([]AuditEvent, error) {
	q := `SELECT id, ts, action, target, metadata_json FROM audit_events WHERE 1=1`
	args := []any{}
	if f.Action != "" {
		q += " AND action = ?"
		args = append(args, f.Action)
	}
	if f.Before > 0 {
		q += " AND ts < ?"
		args = append(args, f.Before)
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ?"
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	args = append(args, limit)

	rows, err := d.audit.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var meta string
		if err := rows.Scan(&e.ID, &e.TS, &e.Action, &e.Target, &meta); err != nil {
			return nil, err
		}
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &e.Metadata)
		}
		if e.Metadata == nil {
			e.Metadata = map[string]string{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeAuditBefore deletes events older than the given unix-second
// cutoff. Used by the optional retention slider in Settings.
func (d *DB) PurgeAuditBefore(cutoff int64) (int64, error) {
	res, err := d.audit.Exec(`DELETE FROM audit_events WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// auditHandle keeps this file's only direct dependency on database/sql
// minimal so renaming the DB handle field would surface here too.
var _ = (*sql.DB)(nil)
