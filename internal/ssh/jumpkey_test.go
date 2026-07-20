package ssh

import (
	"testing"

	"ssh-tool/internal/store"
)

func u16(v uint16) *uint16 { return &v }
func sp(s string) *string   { return &s }

func TestJumpPrefixKey(t *testing.T) {
	// No jump -> empty key (never pooled).
	if k := JumpPrefixKey(&store.ResolvedSettings{Hostname: "h", Port: 22}); k != "" {
		t.Errorf("no-jump key = %q, want empty", k)
	}

	base := func(target string) *store.ResolvedSettings {
		return &store.ResolvedSettings{
			Hostname: target, Port: 22,
			JumpHost: &store.JumpHostSpec{Hostname: "bastion", Port: u16(22), Username: sp("jump")},
		}
	}

	// Same bastion, different targets -> SAME key (target isn't in the key).
	k1 := JumpPrefixKey(base("10.0.0.1"))
	k2 := JumpPrefixKey(base("10.0.0.2"))
	if k1 == "" || k1 != k2 {
		t.Fatalf("same bastion should key equal: %q vs %q", k1, k2)
	}

	// Different bastion host -> different key.
	other := base("10.0.0.1")
	other.JumpHost.Hostname = "bastion2"
	if JumpPrefixKey(other) == k1 {
		t.Error("different bastion host must not share a key")
	}

	// Different jump user -> different key.
	du := base("10.0.0.1")
	du.JumpHost.Username = sp("other")
	if JumpPrefixKey(du) == k1 {
		t.Error("different jump user must not share a key")
	}

	// A network profile changes the transport -> different key.
	np := base("10.0.0.1")
	np.NetworkProfileID = sp("prof-1")
	if JumpPrefixKey(np) == k1 {
		t.Error("network profile must change the key")
	}
	// Same profile, same bastion -> equal again.
	np2 := base("10.0.0.9")
	np2.NetworkProfileID = sp("prof-1")
	if JumpPrefixKey(np) != JumpPrefixKey(np2) {
		t.Error("same profile+bastion should key equal regardless of target")
	}
}
