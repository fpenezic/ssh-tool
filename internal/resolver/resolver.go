// Package resolver computes the merged ResolvedSettings a connection ships
// with, by walking its folder ancestor chain root->leaf and finally applying
// the connection's own overrides.
package resolver

import (
	"ssh-tool/internal/store"
)

// ResolveConnection: load + compute. Returns the merged settings to hand off
// to the SSH layer.
func ResolveConnection(db *store.DB, connectionID string) (*store.ResolvedSettings, error) {
	conn, err := db.GetConnection(connectionID)
	if err != nil {
		return nil, err
	}
	folders, err := db.ListFolders()
	if err != nil {
		return nil, err
	}
	rs := ResolveWith(*conn, folders)
	return &rs, nil
}

// ResolveWith is pure: same inputs -> same outputs. Used by tests exhaustively.
func ResolveWith(conn store.Connection, folders []store.Folder) store.ResolvedSettings {
	chain := ancestorChain(conn.FolderID, folders)
	var merged store.InheritableSettings
	for _, f := range chain {
		merged = mergeSettings(merged, f.Settings)
	}
	merged = mergeSettings(merged, conn.Overrides)
	return finalize(merged, conn.Hostname)
}

// ancestorChain walks from `start` up to root, then reverses so the returned
// slice is root-first.
func ancestorChain(start *string, folders []store.Folder) []store.Folder {
	idx := make(map[string]store.Folder, len(folders))
	for _, f := range folders {
		idx[f.ID] = f
	}
	var chain []store.Folder
	current := start
	for i := 0; i < 10_000 && current != nil; i++ {
		f, ok := idx[*current]
		if !ok {
			break
		}
		chain = append(chain, f)
		current = f.ParentID
	}
	// reverse
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// mergeSettings: child wins on scalars. Maps deep-merge. jump_host is atomic
// (whole replace per JumpHostOverride).
func mergeSettings(base, over store.InheritableSettings) store.InheritableSettings {
	out := store.InheritableSettings{
		Username:          firstNonNil(over.Username, base.Username),
		Port:              firstNonNilU16(over.Port, base.Port),
		AuthRef:           firstNonNil(over.AuthRef, base.AuthRef),
		JumpHost:          firstNonNilJH(over.JumpHost, base.JumpHost),
		ColorTag:          firstNonNil(over.ColorTag, base.ColorTag),
		BroadcastGroupID:  firstNonNil(over.BroadcastGroupID, base.BroadcastGroupID),
		KeepaliveInterval: firstNonNilU32(over.KeepaliveInterval, base.KeepaliveInterval),
		TerminalType:      firstNonNil(over.TerminalType, base.TerminalType),
		InitialCommand:    firstNonNil(over.InitialCommand, base.InitialCommand),
		AutoReconnect:     firstNonNilBool(over.AutoReconnect, base.AutoReconnect),
		Verbose:           firstNonNilBool(over.Verbose, base.Verbose),
		VncEnabled:        firstNonNilBool(over.VncEnabled, base.VncEnabled),
		VncPort:           firstNonNilU16(over.VncPort, base.VncPort),
		VncUseTunnel:      firstNonNilBool(over.VncUseTunnel, base.VncUseTunnel),
		VncDefault:        firstNonNilBool(over.VncDefault, base.VncDefault),
		NetworkProfileID:  firstNonNil(over.NetworkProfileID, base.NetworkProfileID),
		SSHOptions:        mergeMap(base.SSHOptions, over.SSHOptions),
		EnvVars:           mergeMap(base.EnvVars, over.EnvVars),
	}
	return out
}

func mergeMap(base, over map[string]string) map[string]string {
	if base == nil && over == nil {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func finalize(s store.InheritableSettings, hostname string) store.ResolvedSettings {
	var jh *store.JumpHostSpec
	if s.JumpHost != nil {
		switch s.JumpHost.Kind {
		case "none":
			jh = nil
		case "chain":
			jh = s.JumpHost.Chain
		}
	}
	port := uint16(22)
	if s.Port != nil {
		port = *s.Port
	}
	keepalive := uint32(0)
	if s.KeepaliveInterval != nil {
		keepalive = *s.KeepaliveInterval
	}
	term := "xterm-256color"
	if s.TerminalType != nil {
		term = *s.TerminalType
	}
	initialCmd := ""
	if s.InitialCommand != nil {
		initialCmd = *s.InitialCommand
	}
	vncPort := uint16(5900)
	if s.VncPort != nil && *s.VncPort != 0 {
		vncPort = *s.VncPort
	}
	ssh := s.SSHOptions
	if ssh == nil {
		ssh = map[string]string{}
	}
	env := s.EnvVars
	if env == nil {
		env = map[string]string{}
	}
	// An explicit "" override means "direct" - normalize to nil so
	// consumers only branch on non-nil.
	netProfile := s.NetworkProfileID
	if netProfile != nil && *netProfile == "" {
		netProfile = nil
	}
	return store.ResolvedSettings{
		Hostname:          hostname,
		Username:          s.Username,
		Port:              port,
		AuthRef:           s.AuthRef,
		JumpHost:          jh,
		SSHOptions:        ssh,
		EnvVars:           env,
		ColorTag:          s.ColorTag,
		BroadcastGroupID:  s.BroadcastGroupID,
		KeepaliveInterval: keepalive,
		TerminalType:      term,
		InitialCommand:    initialCmd,
		AutoReconnect:     s.AutoReconnect != nil && *s.AutoReconnect,
		Verbose:           s.Verbose != nil && *s.Verbose,
		VncEnabled:        s.VncEnabled != nil && *s.VncEnabled,
		VncPort:           vncPort,
		VncUseTunnel:      s.VncUseTunnel != nil && *s.VncUseTunnel,
		VncDefault:        s.VncDefault != nil && *s.VncDefault,
		NetworkProfileID:  netProfile,
	}
}

func firstNonNil(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}
func firstNonNilU16(a, b *uint16) *uint16 {
	if a != nil {
		return a
	}
	return b
}
func firstNonNilU32(a, b *uint32) *uint32 {
	if a != nil {
		return a
	}
	return b
}
func firstNonNilJH(a, b *store.JumpHostOverride) *store.JumpHostOverride {
	if a != nil {
		return a
	}
	return b
}
func firstNonNilBool(a, b *bool) *bool {
	if a != nil {
		return a
	}
	return b
}
