package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
	"ssh-tool/internal/wg"
)

// wireNetworkProfiles lets hosts pinned to a network profile dial DIRECTLY
// when the profile allows it (mode auto, or paused) - the same thing the app
// does when the host answers without the tunnel. The agent never brings a
// tunnel up itself: a WireGuard identity running on two machines at once
// knocks the other one off, and the app's presence guard that prevents that
// is not here. A profile in "always" mode is therefore refused.
func (a *agent) wireNetworkProfiles() {
	sshlayer.FirstHopDialerHook = func(s *store.ResolvedSettings) (sshlayer.ContextDialer, error) {
		row, err := a.db.GetNetworkProfile(*s.NetworkProfileID)
		if err != nil {
			return nil, err
		}
		var pol struct {
			Mode   string `json:"mode"`
			Paused bool   `json:"paused"`
		}
		_ = json.Unmarshal([]byte(row.ConfigJSON), &pol)
		if !pol.Paused && pol.Mode != wg.ModeAuto {
			return nil, fmt.Errorf("network profile %q always uses its tunnel, and the agent does not start tunnels", row.Name)
		}
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			if h, ok := ctx.Value(sshlayer.DialPathKey{}).(*string); ok {
				*h = "direct"
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}, nil
	}
}
