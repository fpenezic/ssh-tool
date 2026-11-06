package main

import (
	"context"
	"time"
)

// A Database tab is drawn connected only while its server answers. The
// handle's pool reconnects by itself on the next call, so nothing else
// notices a server that stopped (a restarted container, a failover, a
// dropped VPN) until the user clicks: dbWatch asks the server every
// dbAliveEvery and tells every window ("db_alive", with the handle id)
// when that changes, so a tree row in the main window follows a tab that
// lives in a detached one. The
// ping keeps the SSH session and the server login in use while the tab is
// open; closing the tab stops it.

const (
	dbAliveEvery   = 20 * time.Second
	dbAliveTimeout = 10 * time.Second
)

// DbAlive is the payload of db_alive: Error is "" while the server
// answers.
type DbAlive struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (a *App) dbWatch(id string, dh *dbHandle) {
	t := time.NewTicker(dbAliveEvery)
	defer t.Stop()
	last := ""
	first := true
	for {
		select {
		case <-dh.stop:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), dbAliveTimeout)
		err := dh.h.Ping(ctx)
		cancel()
		select {
		case <-dh.stop:
			return // closed while asking: not a server problem
		default:
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		a.dbMu.Lock()
		dh.down = msg
		a.dbMu.Unlock()
		if first || (msg == "") != (last == "") {
			EventsEmit("db_alive", DbAlive{ID: id, OK: msg == "", Error: msg})
		}
		first, last = false, msg
	}
}
