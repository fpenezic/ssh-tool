package store

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
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

// ----- Stats -----
//
// Everything below aggregates over the WHOLE audit.db, not over a
// ListAudit page. That distinction is the entire point: ListAudit is
// capped (default 500) because it feeds a table the user scrolls, so
// computing stats from those rows would silently report "activity in
// the last 500 events" while labelling it "last 30 days". The counts
// here are SQL aggregates and the session pairing walks every row in
// the window.

// AuditCount is one action (or host, or day) with its tally.
type AuditCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// AuditHostStat is one SSH target with its connect tally and total
// time connected. Seconds only covers sessions we could pair; see
// Unpaired.
type AuditHostStat struct {
	Host     string `json:"host"`
	Name     string `json:"name"`
	Connects int64  `json:"connects"`
	Seconds  int64  `json:"seconds"`
	Unpaired int64  `json:"unpaired"`
	LastTS   int64  `json:"lastTs"`
}

// AuditStats is the whole Insights payload for one time window.
type AuditStats struct {
	Since      int64           `json:"since"` // 0 = all time
	Until      int64           `json:"until"`
	Total      int64           `json:"total"`
	FirstTS    int64           `json:"firstTs"`
	Actions    []AuditCount    `json:"actions"`
	Hosts      []AuditHostStat `json:"hosts"`
	Daily      []AuditCount    `json:"daily"`  // key = YYYY-MM-DD, local time
	Hourly     []int64         `json:"hourly"` // 24 buckets, local time
	Failures   []AuditCount    `json:"failures"`
	Gates      []AuditCount    `json:"gates"` // LLM approval gate tallies
	LLMActions int64           `json:"llmActions"`
	// LLMLoggingOff is set when LLM activity logging was switched off
	// at some point inside the window, which makes Gates/LLMActions a
	// floor rather than a full count.
	LLMLoggingOff   bool  `json:"llmLoggingOff"`
	LLMLoggingOffTS int64 `json:"llmLoggingOffTs"`
	Connects        int64 `json:"connects"`
	SessionSecs     int64 `json:"sessionSecs"`
	Unpaired        int64 `json:"unpaired"`
	LongestSecs     int64 `json:"longestSecs"`
}

// auditStatsRows is the raw window we walk more than once.
type auditStatsRow struct {
	ts     int64
	action string
	target string
	meta   map[string]string
}

// AuditStats aggregates the audit log over [since, now]. since <= 0
// means all time. Host/session numbers are derived by pairing
// ssh.connect* with ssh.disconnect on session_id.
func (d *DB) AuditStats(since int64) (*AuditStats, error) {
	now := time.Now()
	out := &AuditStats{Since: since, Until: now.Unix()}

	// Total + first event: a separate cheap aggregate so the UI can
	// say "no data in this window, but there is data before it"
	// rather than looking broken on a fresh install.
	where, args := "", []any{}
	if since > 0 {
		where = " WHERE ts >= ?"
		args = append(args, since)
	}
	var first sql.NullInt64
	err := d.audit.QueryRow(
		`SELECT COUNT(*), MIN(ts) FROM audit_events`+where, args...,
	).Scan(&out.Total, &first)
	if err != nil {
		return nil, err
	}
	if first.Valid {
		out.FirstTS = first.Int64
	}

	// Per-action tally, biggest first. Straight SQL - no reason to
	// pull rows for this one.
	actionRows, err := d.audit.Query(
		`SELECT action, COUNT(*) c FROM audit_events`+where+
			` GROUP BY action ORDER BY c DESC, action ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer actionRows.Close()
	for actionRows.Next() {
		var c AuditCount
		if err := actionRows.Scan(&c.Key, &c.Count); err != nil {
			return nil, err
		}
		out.Actions = append(out.Actions, c)
		// Anything that records a failure is worth surfacing on its
		// own, not buried mid-list. Matching on the suffix keeps new
		// *.failed actions included without touching this code.
		if strings.HasSuffix(c.Key, ".failed") || c.Key == "share.violation" || c.Key == "share.deny" {
			out.Failures = append(out.Failures, c)
		}
	}
	if err := actionRows.Err(); err != nil {
		return nil, err
	}

	// The rest needs metadata, so walk the window once in ts order.
	rows, err := d.audit.Query(
		`SELECT ts, action, target, metadata_json FROM audit_events`+where+
			` ORDER BY ts ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []auditStatsRow
	for rows.Next() {
		var r auditStatsRow
		var meta string
		if err := rows.Scan(&r.ts, &r.action, &r.target, &meta); err != nil {
			return nil, err
		}
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &r.meta)
		}
		if r.meta == nil {
			r.meta = map[string]string{}
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out.Hourly = make([]int64, 24)
	daily := map[string]int64{}
	gates := map[string]int64{}
	for _, r := range all {
		t := time.Unix(r.ts, 0)
		out.Hourly[t.Hour()]++
		daily[t.Format("2006-01-02")]++
		// Every mcp_* row carries the gate that let it through, which
		// is the only place the audit log says whether a human
		// approved what the LLM did or it ran unattended.
		if strings.HasPrefix(r.action, "mcp_") {
			out.LLMActions++
			g := r.meta["gate"]
			if g == "" {
				g = "unknown"
			}
			gates[g]++
		}
		// A window that contains an "LLM logging -> off" flip cannot
		// report a complete count of what the LLM did, and saying so
		// matters more than the number: an unmarked gap reads as a
		// quiet period rather than as missing evidence.
		if r.action == "settings.toggle" && r.target == "mcp_audit_enabled" &&
			strings.HasPrefix(r.meta["to"], "off") {
			out.LLMLoggingOff = true
			if r.ts > out.LLMLoggingOffTS {
				out.LLMLoggingOffTS = r.ts
			}
		}
	}
	for k, v := range gates {
		out.Gates = append(out.Gates, AuditCount{Key: k, Count: v})
	}
	// Fixed risk order, not by count: the panel reads as a risk
	// ranking, so denied/yolo must not move around as tallies shift.
	gateRank := map[string]int{"denied": 0, "yolo": 1, "auto": 2, "approved": 3}
	sort.Slice(out.Gates, func(i, j int) bool {
		ri, oki := gateRank[out.Gates[i].Key]
		rj, okj := gateRank[out.Gates[j].Key]
		if !oki {
			ri = 99
		}
		if !okj {
			rj = 99
		}
		if ri != rj {
			return ri < rj
		}
		return out.Gates[i].Key < out.Gates[j].Key
	})
	for k, v := range daily {
		out.Daily = append(out.Daily, AuditCount{Key: k, Count: v})
	}
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Key < out.Daily[j].Key })

	out.Hosts, out.Connects, out.SessionSecs, out.Unpaired, out.LongestSecs = pairSessions(all)
	return out, nil
}

// pairSessions walks events in ts order and matches each ssh.connect
// (or ssh.connect.dynamic) to the ssh.disconnect carrying the same
// session_id.
//
// An unpaired connect is a real thing, not a bug: the app can be
// killed, or crash, before SshDisconnect runs, and a session open
// right now has no disconnect yet either. Those count as connects
// with NO duration - deliberately not as zero-second sessions, which
// would drag the average toward zero and make an active host look
// idle. They are reported separately as Unpaired so the UI can say
// so out loud instead of quietly under-reporting time.
func pairSessions(all []auditStatsRow) (hosts []AuditHostStat, connects, total, unpaired, longest int64) {
	type open struct {
		ts   int64
		host string
		name string
	}
	pending := map[string]open{}
	agg := map[string]*AuditHostStat{}

	touch := func(host, name string, ts int64) *AuditHostStat {
		h, ok := agg[host]
		if !ok {
			h = &AuditHostStat{Host: host, Name: name}
			agg[host] = h
		}
		if h.Name == "" && name != "" {
			h.Name = name
		}
		if ts > h.LastTS {
			h.LastTS = ts
		}
		return h
	}

	for _, r := range all {
		switch r.action {
		case "ssh.connect", "ssh.connect.dynamic":
			host := r.meta["host"]
			if host == "" {
				host = r.target
			}
			if host == "" {
				host = "(unknown)"
			}
			connects++
			h := touch(host, r.meta["name"], r.ts)
			h.Connects++
			if sid := r.meta["session_id"]; sid != "" {
				pending[sid] = open{ts: r.ts, host: host, name: r.meta["name"]}
			} else {
				// No session_id means nothing can ever pair with it.
				h.Unpaired++
				unpaired++
			}
		case "ssh.disconnect":
			sid := r.meta["session_id"]
			if sid == "" {
				continue
			}
			o, ok := pending[sid]
			if !ok {
				// A disconnect whose connect fell outside the window
				// (or predates the log). Ignored rather than counted
				// from zero - we have no start to measure from.
				continue
			}
			delete(pending, sid)
			d := r.ts - o.ts
			if d < 0 {
				d = 0
			}
			// Prefer the name off the disconnect event: ssh.connect
			// records host/port/user but no name, while
			// ssh.disconnect carries name. Passing only o.name here
			// would leave every host row unnamed.
			name := r.meta["name"]
			if name == "" {
				name = o.name
			}
			h := touch(o.host, name, r.ts)
			h.Seconds += d
			total += d
			if d > longest {
				longest = d
			}
		}
	}
	// Whatever is still open never got its disconnect in this window.
	for _, o := range pending {
		if h, ok := agg[o.host]; ok {
			h.Unpaired++
		}
		unpaired++
	}

	for _, h := range agg {
		hosts = append(hosts, *h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Seconds != hosts[j].Seconds {
			return hosts[i].Seconds > hosts[j].Seconds
		}
		if hosts[i].Connects != hosts[j].Connects {
			return hosts[i].Connects > hosts[j].Connects
		}
		return hosts[i].Host < hosts[j].Host
	})
	return hosts, connects, total, unpaired, longest
}
