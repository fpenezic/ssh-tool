package ssh

import (
	"strings"
	"testing"
)

func TestFirstLinePicksTheDaemonsReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"permission denied while trying to connect to the Docker daemon socket\n",
			"permission denied while trying to connect to the Docker daemon socket",
		},
		// Leading blank lines must not win - the reason is the first line
		// with content.
		{"\n\nCannot connect to the Docker daemon\nmore\n", "Cannot connect to the Docker daemon"},
		{"first\nsecond", "first"},
		{"trailing\r\n", "trailing"},
		{"", ""},
		{"   \n\t\n", ""},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The listing has to reach the daemon exactly the way the tail will, or the
// picker shows nothing while the stream would have worked. And it must never
// use a form of sudo that can block on a password prompt - a one-shot listing
// has no channel to answer one.
func TestListingPrefixNeverBlocksOnAPassword(t *testing.T) {
	if got := listingPrefix(false); got != "" {
		t.Errorf("direct access needs no prefix, got %q", got)
	}
	got := listingPrefix(true)
	if got != "sudo -n " {
		t.Errorf("elevated listing = %q, want %q", got, "sudo -n ")
	}
	if !strings.Contains(got, "-n") {
		t.Error("the listing must use sudo -n so it cannot hang waiting for a password")
	}
}

// ContainerAccess must be able to express the three outcomes distinctly.
// Collapsing "denied" into "no engine" is what produced the empty picker.
func TestContainerAccessStatesAreDistinguishable(t *testing.T) {
	noEngine := ContainerAccess{}
	if noEngine.Engine != "" || noEngine.Direct || noEngine.NeedsSudo || noEngine.Denied {
		t.Error("the zero value must mean 'no engine installed'")
	}

	direct := ContainerAccess{Engine: "docker", Direct: true}
	if direct.NeedsSudo || direct.Denied {
		t.Error("direct access must not also claim sudo or denial")
	}

	elevated := ContainerAccess{Engine: "docker", NeedsSudo: true}
	if elevated.Direct || elevated.Denied {
		t.Error("a sudo host is neither direct nor denied")
	}

	denied := ContainerAccess{Engine: "docker", Denied: true, Reason: "permission denied"}
	if denied.Direct || denied.NeedsSudo {
		t.Error("a denied host must not look reachable")
	}
	// The reason is what tells the user how to fix it, so it must survive.
	if denied.Reason == "" {
		t.Error("a denial without a reason leaves the user with nothing to act on")
	}
}
