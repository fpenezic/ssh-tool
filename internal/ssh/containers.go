package ssh

// Container-engine discovery for the log tail: detect docker/podman on a host
// and list running containers + compose projects, so the UI can offer a picker.
// One-shot commands over the SSH client, same shape as ListInterfaces
// (tcpdump.go) - run a command, parse stdout. No streaming, no sudo (listing is
// read-only; if the daemon needs root the command simply returns nothing and
// the UI shows an empty list).

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ContainerInfo is one running container as the picker shows it.
type ContainerInfo struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

// DetectContainerEngine returns the first available container CLI on the host
// ("docker" preferred, then "podman"), or "" if neither is present. Both share
// the subcommands the log tail uses (logs -f, ps, compose).
func DetectContainerEngine(client *ssh.Client) (string, error) {
	for _, engine := range []string{"docker", "podman"} {
		sess, err := client.NewSession()
		if err != nil {
			return "", err
		}
		// command -v exits 0 and prints a path when the binary exists.
		out, runErr := sess.Output("command -v " + engine + " 2>/dev/null")
		sess.Close()
		if runErr == nil && strings.TrimSpace(string(out)) != "" {
			return engine, nil
		}
	}
	return "", nil
}

// ContainerAccess describes whether the login user can drive the engine
// directly, or whether the commands have to be elevated.
type ContainerAccess struct {
	// Engine is the detected binary ("docker" | "podman"), "" when neither
	// is installed.
	Engine string `json:"engine"`
	// Direct is true when the login user can talk to the daemon as-is:
	// rootful docker with the user in the `docker` group, rootless podman,
	// or a root login.
	Direct bool `json:"direct"`
	// NeedsSudo is true when the binary exists but the daemon refused the
	// user, AND the same command works under sudo. The listing and the tail
	// then both run elevated.
	NeedsSudo bool `json:"needs_sudo"`
	// Denied is true when the engine is installed but reachable neither
	// directly nor through sudo - the UI has to say so rather than show an
	// empty picker.
	Denied bool `json:"denied"`
	// Reason carries the daemon's own refusal (first stderr line) so the UI
	// can show why, e.g. the docker-group hint.
	Reason string `json:"reason"`
}

// ProbeContainerAccess works out how - or whether - the engine can be driven
// on this host.
//
// This exists because a permission failure is the ONE failure mode that looks
// identical to "no containers running": `docker ps` exits non-zero and prints
// to stderr, so a listing that swallows both shows an empty picker either way.
// A user not in the `docker` group is a common setup, not an edge case, and
// on those hosts the log tail itself would have worked - it already runs under
// the same sudo prefix journalctl uses.
func ProbeContainerAccess(client *ssh.Client, rootUser, sudoNoPwd bool) (*ContainerAccess, error) {
	engine, err := DetectContainerEngine(client)
	if err != nil {
		return nil, err
	}
	acc := &ContainerAccess{Engine: engine}
	if engine == "" {
		return acc, nil
	}

	// A cheap command that still requires talking to the daemon. `ps` would
	// do, but `version` is smaller and its failure text carries the same
	// permission message.
	probe := engine + " version --format '{{.Server.Version}}'"

	if ok, _ := runProbe(client, probe); ok {
		acc.Direct = true
		return acc, nil
	}
	// Capture WHY it failed before deciding what to do about it - the
	// daemon's own wording ("permission denied while trying to connect to
	// the Docker daemon socket", "Cannot connect to the Docker daemon") is
	// more useful to the user than anything we could invent.
	_, acc.Reason = runProbe(client, probe)

	// Root already failed above, so there is no elevation left to try.
	if rootUser {
		acc.Denied = true
		return acc, nil
	}
	// Only probe sudo when it will not block on a password prompt. With a
	// password-requiring sudo we cannot test non-interactively, so assume it
	// is worth trying: the tail's existing prompt then asks for the password
	// exactly as it does for journalctl.
	if sudoNoPwd {
		if ok, _ := runProbe(client, "sudo -n "+probe); ok {
			acc.NeedsSudo = true
			return acc, nil
		}
		acc.Denied = true
		return acc, nil
	}
	acc.NeedsSudo = true
	return acc, nil
}

// runProbe runs a command and reports success plus the first stderr line.
func runProbe(client *ssh.Client, cmd string) (bool, string) {
	sess, err := client.NewSession()
	if err != nil {
		return false, ""
	}
	defer sess.Close()
	var stderr strings.Builder
	sess.Stderr = &stderr
	runErr := sess.Run(cmd)
	if runErr == nil {
		return true, ""
	}
	return false, firstLine(stderr.String())
}

// firstLine trims a command's stderr down to its first non-empty line, which
// is where both docker and podman put the actual reason.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if ln != "" {
			return ln
		}
	}
	return ""
}

// listingPrefix returns the elevation prefix for a one-shot container command.
//
// Always -n: a listing has no channel to answer a password prompt, so sudo
// must never be allowed to block waiting for one. On a host where sudo does
// need a password the listing degrades to empty, while the tail itself still
// prompts through its existing password path.
func listingPrefix(elevate bool) string {
	if !elevate {
		return ""
	}
	return "sudo -n "
}

// ListContainers returns the running containers for the engine. Uses a
// tab-separated format so names/images with spaces don't split wrong.
func ListContainers(client *ssh.Client, engine string, elevate bool) ([]ContainerInfo, error) {
	engine = containerEngine(engine)
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	prefix := listingPrefix(elevate)
	// stderr is captured rather than discarded: a permission failure and an
	// empty host both produce no stdout, and the daemon's message is the only
	// thing that tells them apart.
	var stderr strings.Builder
	sess.Stderr = &stderr
	// {{.Names}} is a single name for `docker ps`; tabs separate the columns.
	out, err := sess.Output(prefix + engine + ` ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}'`)
	if err != nil {
		if reason := firstLine(stderr.String()); reason != "" {
			return nil, fmt.Errorf("%s", reason)
		}
		return nil, nil
	}
	var list []ContainerInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		info := ContainerInfo{Name: parts[0]}
		if len(parts) > 1 {
			info.Image = parts[1]
		}
		if len(parts) > 2 {
			info.Status = parts[2]
		}
		if info.Name != "" {
			list = append(list, info)
		}
	}
	return list, nil
}

// ListComposeProjects returns the distinct compose project names on the host.
// Tries `compose ls --format json`; falls back to scanning the compose project
// label on running containers where `compose ls` is unavailable.
func ListComposeProjects(client *ssh.Client, engine string, elevate bool) ([]string, error) {
	engine = containerEngine(engine)
	prefix := listingPrefix(elevate)

	// Primary: compose ls --format json -> [{"Name":"proj",...}].
	if sess, err := client.NewSession(); err == nil {
		out, runErr := sess.Output(prefix + engine + " compose ls --format json 2>/dev/null")
		sess.Close()
		if runErr == nil {
			var rows []struct {
				Name string `json:"Name"`
			}
			if json.Unmarshal(out, &rows) == nil && len(rows) > 0 {
				names := make([]string, 0, len(rows))
				for _, r := range rows {
					if strings.TrimSpace(r.Name) != "" {
						names = append(names, r.Name)
					}
				}
				if len(names) > 0 {
					return dedupeSorted(names), nil
				}
			}
		}
	}

	// Fallback: the compose project label on running containers.
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	out, err := sess.Output(prefix + engine + ` ps --format '{{.Label "com.docker.compose.project"}}' 2>/dev/null`)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if name != "" {
			names = append(names, name)
		}
	}
	return dedupeSorted(names), nil
}

func dedupeSorted(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	// Small lists; a simple insertion sort keeps it dependency-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
