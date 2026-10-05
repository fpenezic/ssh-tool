package main

import (
	"errors"
	"fmt"
	"log"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// Local port conflicts for tunnels. The SSH layer reports a taken port as
// *PortInUseError with the id of the forward of ours holding it; this file
// turns that into "used by the tunnel X on connection Y", warns at edit time
// when another saved tunnel or a running program has the port, and tells the
// user when an auto-port tunnel could not get its previous port back.

// forwardLabel names a saved forward for a message: `"web ui" on db-01`, or
// `a tunnel on db-01` when it has no description.
func (a *App) forwardLabel(id string) string {
	spec, err := a.db.GetPortForward(id)
	if err != nil || spec == nil {
		return "another tunnel"
	}
	return a.specLabel(spec)
}

func (a *App) specLabel(spec *store.PortForward) string {
	conn := "another connection"
	if c, err := a.db.GetConnection(spec.ConnectionID); err == nil && c != nil {
		conn = c.Name
	}
	if spec.Description != "" {
		return fmt.Sprintf("%q on %s", spec.Description, conn)
	}
	return "a tunnel on " + conn
}

// forwardErr rewrites a port conflict for the user; anything else passes
// through unchanged.
func (a *App) forwardErr(err error, selfID string) error {
	var pe *sshlayer.PortInUseError
	if !errors.As(err, &pe) {
		return err
	}
	switch {
	case pe.Reserved:
		return fmt.Errorf("local port %d is reserved by Windows (excluded port range)", pe.Port)
	case pe.HolderID == selfID:
		return fmt.Errorf("this tunnel is already running on port %d", pe.Port)
	case pe.HolderID != "":
		return fmt.Errorf("local port %d is already used by %s", pe.Port, a.forwardLabel(pe.HolderID))
	}
	return fmt.Errorf("local port %d is in use by another program", pe.Port)
}

// startForwardFor starts a saved forward and reports what the user must
// know about it. background is for starts nobody clicked (auto-start on
// connect, restore after reconnect): their failure would otherwise only
// reach the log, so it goes out as a notice too.
func (a *App) startForwardFor(sess *sshlayer.Session, spec *store.PortForward, background bool) (*sshlayer.ForwardStatus, error) {
	st, err := startForward(a.forwards, sess, spec)
	if err != nil {
		err = a.forwardErr(err, spec.ID)
		if background {
			log.Printf("forward %s did not start: %v", spec.ID, err)
			a.forwardNotice("err", fmt.Sprintf("Tunnel %s did not start: %v", a.specLabel(spec), err))
		}
		return nil, err
	}
	if st.MovedFrom != 0 {
		a.forwardNotice("info", fmt.Sprintf("Tunnel %s: port %d was taken, now on %d", a.specLabel(spec), st.MovedFrom, st.LocalPort))
	}
	return st, nil
}

type forwardNoticePayload struct {
	Level   string `json:"level"` // "err" | "info"
	Message string `json:"message"`
}

func (a *App) forwardNotice(level, msg string) {
	EventsEmit("forward_notice", forwardNoticePayload{Level: level, Message: msg})
}

// PortCheckResult is what the tunnel editor shows under the local port.
type PortCheckResult struct {
	InUse    bool     `json:"in_use"`
	Reserved bool     `json:"reserved"`
	Holder   string   `json:"holder,omitempty"`   // running tunnel of ours, by label
	SavedOn  []string `json:"saved_on,omitempty"` // other saved tunnels with the same port
}

// ForwardsPortCheck reports, for the local listen addr:port being edited,
// whether it is taken right now and which other saved tunnels use the same
// port. excludeID is the forward being edited, so it never warns about
// itself. Remote forwards listen on the server and are left out.
func (a *App) ForwardsPortCheck(addr string, port uint16, excludeID string) PortCheckResult {
	var out PortCheckResult
	if port == 0 {
		return out
	}
	probe := a.forwards.CheckLocalPort(addr, port)
	if probe.HolderID != excludeID {
		out.InUse, out.Reserved = probe.InUse, probe.Reserved
		if probe.HolderID != "" {
			out.Holder = a.forwardLabel(probe.HolderID)
		}
	}
	all, err := a.db.ListAllPortForwards()
	if err != nil {
		return out
	}
	for i := range all {
		f := &all[i]
		if f.ID == excludeID || f.Kind == "remote" || f.LocalPort == nil || *f.LocalPort != port {
			continue
		}
		fa := ""
		if f.LocalAddr != nil {
			fa = *f.LocalAddr
		}
		if !sshlayer.SameBind(fa, addr) {
			continue
		}
		out.SavedOn = append(out.SavedOn, a.specLabel(f))
	}
	return out
}

// ForwardsStartFreePort starts a saved forward on an OS-assigned port for
// this run only, for when its fixed port is taken. The saved port is kept.
func (a *App) ForwardsStartFreePort(forwardID, sessionID string) (*sshlayer.ForwardStatus, error) {
	spec, err := a.db.GetPortForward(forwardID)
	if err != nil {
		return nil, err
	}
	sess, ok := a.pool.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not connected")
	}
	cp := *spec
	cp.LocalPort = nil
	return a.startForwardFor(sess, &cp, false)
}
