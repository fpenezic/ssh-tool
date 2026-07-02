package rdm

import (
	"os"
	"testing"
)

// TestParseSampleExport just checks that the parser doesn't choke on a real
// export. Driven by RDM_EXPORT env var so CI doesn't need the file.
func TestParseSampleExport(t *testing.T) {
	path := os.Getenv("RDM_EXPORT")
	if path == "" {
		t.Skip("RDM_EXPORT not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("parsed %d entries", len(f.Connections))

	folders := 0
	ssh := 0
	other := 0
	withVPN := 0
	withGateways := 0
	for _, e := range f.Connections {
		if IsFolderType(e.ConnectionType) {
			folders++
		} else if IsSSHType(e.ConnectionType) {
			ssh++
			if e.VPN != nil && e.VPN.VPNGroupName != "" {
				withVPN++
			}
			if e.Terminal != nil && e.Terminal.UseSSHGateway && len(e.Terminal.SSHGateways) > 0 {
				withGateways++
			}
		} else {
			other++
		}
	}
	t.Logf("folders=%d ssh=%d other=%d via_vpn=%d via_gateway=%d",
		folders, ssh, other, withVPN, withGateways)
}
