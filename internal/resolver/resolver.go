// Package resolver computes the merged ResolvedSettings a connection ships
// with, by walking its folder ancestor chain root->leaf and finally applying
// the connection's own overrides.
package resolver

import (
	"strings"

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
	if err := ExpandJumpRefs(&rs, conn.ID, folders, DBLookup(db)); err != nil {
		return nil, err
	}
	return &rs, nil
}

// ResolveWithRefs is ResolveWith plus the bastion-connection expansion, for
// connections that have no row of their own (dynamic hosts, quick connect)
// but inherit a jump chain that may reference saved connections.
func ResolveWithRefs(db *store.DB, conn store.Connection, folders []store.Folder) (store.ResolvedSettings, error) {
	rs := ResolveWith(conn, folders)
	err := ExpandJumpRefs(&rs, conn.ID, folders, DBLookup(db))
	return rs, err
}

// DBLookup reads referenced connections straight from the store.
func DBLookup(db *store.DB) ConnLookup {
	return func(id string) (*store.Connection, error) {
		if strings.HasPrefix(id, DynRefPrefix) {
			return lookupDynamic(id)
		}
		c, err := db.GetConnection(id)
		if err != nil || c == nil {
			return nil, errDeletedRef(id)
		}
		return c, nil
	}
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
		OTPRef:            firstNonNil(over.OTPRef, base.OTPRef),
		JumpHost:          firstNonNilJH(over.JumpHost, base.JumpHost),
		ColorTag:          firstNonNil(over.ColorTag, base.ColorTag),
		BroadcastGroupID:  firstNonNil(over.BroadcastGroupID, base.BroadcastGroupID),
		KeepaliveInterval: firstNonNilU32(over.KeepaliveInterval, base.KeepaliveInterval),
		TerminalType:      firstNonNil(over.TerminalType, base.TerminalType),
		InitialCommand:    firstNonNil(over.InitialCommand, base.InitialCommand),
		InitialCommandLineDelayMs: firstNonNilU32(
			over.InitialCommandLineDelayMs, base.InitialCommandLineDelayMs),
		AutoReconnect:      firstNonNilBool(over.AutoReconnect, base.AutoReconnect),
		Verbose:            firstNonNilBool(over.Verbose, base.Verbose),
		ProbeLiveness:      firstNonNilBool(over.ProbeLiveness, base.ProbeLiveness),
		VncEnabled:         firstNonNilBool(over.VncEnabled, base.VncEnabled),
		VncPort:            firstNonNilU16(over.VncPort, base.VncPort),
		VncUseTunnel:       firstNonNilBool(over.VncUseTunnel, base.VncUseTunnel),
		VncDefault:         firstNonNilBool(over.VncDefault, base.VncDefault),
		NetworkProfileID:   firstNonNil(over.NetworkProfileID, base.NetworkProfileID),
		TunnelOnly:         firstNonNilBool(over.TunnelOnly, base.TunnelOnly),
		TunnelAutostart:    firstNonNilBool(over.TunnelAutostart, base.TunnelAutostart),
		KafkaEnabled:       firstNonNilBool(over.KafkaEnabled, base.KafkaEnabled),
		KafkaBootstrap:     firstNonNil(over.KafkaBootstrap, base.KafkaBootstrap),
		KafkaUseTunnel:     firstNonNilBool(over.KafkaUseTunnel, base.KafkaUseTunnel),
		KafkaDefault:       firstNonNilBool(over.KafkaDefault, base.KafkaDefault),
		KafkaReadOnly:      firstNonNilBool(over.KafkaReadOnly, base.KafkaReadOnly),
		KafkaTLS:           firstNonNilBool(over.KafkaTLS, base.KafkaTLS),
		KafkaTLSSkipVerify: firstNonNilBool(over.KafkaTLSSkipVerify, base.KafkaTLSSkipVerify),
		KafkaSASL:          firstNonNil(over.KafkaSASL, base.KafkaSASL),
		KafkaCredentialRef: firstNonNil(over.KafkaCredentialRef, base.KafkaCredentialRef),
		DbEnabled:          firstNonNilBool(over.DbEnabled, base.DbEnabled),
		DbEngine:           firstNonNil(over.DbEngine, base.DbEngine),
		DbAddr:             firstNonNil(over.DbAddr, base.DbAddr),
		DbName:             firstNonNil(over.DbName, base.DbName),
		DbUseTunnel:        firstNonNilBool(over.DbUseTunnel, base.DbUseTunnel),
		DbDefault:          firstNonNilBool(over.DbDefault, base.DbDefault),
		DbReadOnly:         firstNonNilBool(over.DbReadOnly, base.DbReadOnly),
		DbTLS:              firstNonNil(over.DbTLS, base.DbTLS),
		DbCredentialRef:    firstNonNil(over.DbCredentialRef, base.DbCredentialRef),
		// The connection's own list (folders do not set one).
		DbTargets: over.DbTargets,
		DbLabel:   over.DbLabel,
		// The default among them; a target since removed falls back to
		// the connection's own.
		DbDefaultTarget: over.DbDefaultTarget,
		SSHOptions:      mergeMap(base.SSHOptions, over.SSHOptions),
		EnvVars:         mergeMap(base.EnvVars, over.EnvVars),
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
	// 0 is a meaningful value here (send the lines back to back), so unlike
	// port or keepalive there is no non-zero default to fall back to.
	var initialCmdDelay uint32
	if s.InitialCommandLineDelayMs != nil {
		initialCmdDelay = *s.InitialCommandLineDelayMs
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
	// Same for the credential: an explicit "" on a connection stops the
	// folder's credential from being inherited ("None" in the editor), so
	// the connection's own password or the connect-time prompt is used.
	authRef := s.AuthRef
	if authRef != nil && *authRef == "" {
		authRef = nil
	}
	otpRef := s.OTPRef
	if otpRef != nil && *otpRef == "" {
		otpRef = nil
	}
	kafkaBootstrap := ""
	if s.KafkaBootstrap != nil {
		kafkaBootstrap = strings.TrimSpace(*s.KafkaBootstrap)
	}
	kafkaSASL := ""
	if s.KafkaSASL != nil {
		kafkaSASL = *s.KafkaSASL
	}
	kafkaCred := s.KafkaCredentialRef
	if kafkaCred != nil && *kafkaCred == "" {
		kafkaCred = nil
	}
	dbCred := s.DbCredentialRef
	if dbCred != nil && *dbCred == "" {
		dbCred = nil
	}
	dbEngine := strOr(s.DbEngine, "postgres")
	dbTLS := strOr(s.DbTLS, "disable")
	return store.ResolvedSettings{
		// A pasted hostname can carry a newline or spaces; every dial and
		// probe goes through here, so older rows work too.
		Hostname:                  strings.TrimSpace(hostname),
		OTPRef:                    otpRef,
		Username:                  s.Username,
		Port:                      port,
		AuthRef:                   authRef,
		JumpHost:                  jh,
		SSHOptions:                ssh,
		EnvVars:                   env,
		ColorTag:                  s.ColorTag,
		BroadcastGroupID:          s.BroadcastGroupID,
		KeepaliveInterval:         keepalive,
		TerminalType:              term,
		InitialCommand:            initialCmd,
		InitialCommandLineDelayMs: initialCmdDelay,
		AutoReconnect:             s.AutoReconnect != nil && *s.AutoReconnect,
		Verbose:                   s.Verbose != nil && *s.Verbose,
		// Default true: probe unless a folder explicitly opted out. The global
		// switch gates whether probing runs at all; this only lets a folder
		// exclude itself.
		ProbeLiveness:      s.ProbeLiveness == nil || *s.ProbeLiveness,
		VncEnabled:         s.VncEnabled != nil && *s.VncEnabled,
		VncPort:            vncPort,
		VncUseTunnel:       s.VncUseTunnel != nil && *s.VncUseTunnel,
		VncDefault:         s.VncDefault != nil && *s.VncDefault,
		NetworkProfileID:   netProfile,
		TunnelOnly:         s.TunnelOnly != nil && *s.TunnelOnly,
		TunnelAutostart:    s.TunnelAutostart != nil && *s.TunnelAutostart,
		KafkaEnabled:       s.KafkaEnabled != nil && *s.KafkaEnabled,
		KafkaBootstrap:     kafkaBootstrap,
		KafkaUseTunnel:     s.KafkaUseTunnel == nil || *s.KafkaUseTunnel,
		KafkaDefault:       s.KafkaDefault != nil && *s.KafkaDefault,
		KafkaReadOnly:      s.KafkaReadOnly != nil && *s.KafkaReadOnly,
		KafkaTLS:           s.KafkaTLS != nil && *s.KafkaTLS,
		KafkaTLSSkipVerify: s.KafkaTLSSkipVerify != nil && *s.KafkaTLSSkipVerify,
		KafkaSASL:          kafkaSASL,
		KafkaCredentialRef: kafkaCred,
		DbEnabled:          s.DbEnabled != nil && *s.DbEnabled,
		DbEngine:           dbEngine,
		DbAddr:             strOr(s.DbAddr, ""),
		DbName:             strOr(s.DbName, ""),
		DbUseTunnel:        s.DbUseTunnel == nil || *s.DbUseTunnel,
		DbDefault:          s.DbDefault != nil && *s.DbDefault,
		DbReadOnly:         s.DbReadOnly != nil && *s.DbReadOnly,
		DbTLS:              dbTLS,
		DbCredentialRef:    dbCred,
		DbTargets:          s.DbTargets,
		DbLabel:            strOr(s.DbLabel, ""),
		DbDefaultTarget:    dbDefaultTarget(s),
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

// strOr is the trimmed value of an optional string, def when unset or
// blank.
func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	if v := strings.TrimSpace(*p); v != "" {
		return v
	}
	return def
}

// dbDefaultTarget is the listed database marked default, "" for the
// connection's own (also when the marked one is no longer listed).
func dbDefaultTarget(s store.InheritableSettings) string {
	if s.DbDefaultTarget == nil || *s.DbDefaultTarget == "" {
		return ""
	}
	for _, t := range s.DbTargets {
		if t.ID == *s.DbDefaultTarget {
			return t.ID
		}
	}
	return ""
}
