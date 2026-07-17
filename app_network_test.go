package main

import (
	"encoding/json"
	"testing"
)

// A NetBird / Tailscale profile must keep its kind (and provider
// fields) when the policy toggle patches mode/paused. The old code
// round-tripped through a typed wg.Profile that has no Kind field, so
// every always/auto or pause toggle silently turned the profile into a
// broken WireGuard one. patchPolicyJSON is the kind-agnostic fix.
func TestPatchPolicyJSONPreservesKind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // expected kind after patch
	}{
		{
			name: "netbird",
			in:   `{"kind":"netbird","management_url":"https://nb.example.com","device_name":"laptop","setup_key_credential_id":"cred1","mode":"always"}`,
			want: kindNetbird,
		},
		{
			name: "tailscale",
			in:   `{"kind":"tailscale","control_url":"https://hs.example.com","hostname":"laptop","auth_key_credential_id":"cred2"}`,
			want: kindTailscale,
		},
		{
			name: "wireguard (no kind field)",
			in:   `{"id":"p1","name":"wg","addresses":["10.0.0.2/32"],"peers":[{"public_key":"abc"}]}`,
			want: kindWireguard,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := patchPolicyJSON(tc.in, "auto", true)
			if err != nil {
				t.Fatalf("patchPolicyJSON: %v", err)
			}
			pol := parsePolicy(out)
			if pol.Kind != tc.want {
				t.Errorf("kind after patch = %q, want %q\nout: %s", pol.Kind, tc.want, out)
			}
			if pol.Mode != "auto" {
				t.Errorf("mode = %q, want auto", pol.Mode)
			}
			if !pol.Paused {
				t.Errorf("paused = false, want true")
			}
		})
	}
}

// Provider-specific fields (management URL, device name, credential
// ids, hostname) must survive a policy patch untouched - losing them
// is what forced a full profile re-create after the bug.
func TestPatchPolicyJSONKeepsProviderFields(t *testing.T) {
	in := `{"kind":"netbird","management_url":"https://nb.example.com","device_name":"laptop","setup_key_credential_id":"cred1"}`
	out, err := patchPolicyJSON(in, "always", false)
	if err != nil {
		t.Fatalf("patchPolicyJSON: %v", err)
	}
	var cfg NetbirdConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("unmarshal patched config: %v", err)
	}
	if cfg.ManagementURL != "https://nb.example.com" {
		t.Errorf("management_url lost: %q", cfg.ManagementURL)
	}
	if cfg.DeviceName != "laptop" {
		t.Errorf("device_name lost: %q", cfg.DeviceName)
	}
	if cfg.SetupKeyCredentialID != "cred1" {
		t.Errorf("setup_key_credential_id lost: %q", cfg.SetupKeyCredentialID)
	}
}
