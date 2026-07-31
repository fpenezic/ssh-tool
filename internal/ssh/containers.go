package ssh

// Container-engine discovery for the log tail: detect docker/podman on a host
// and list running containers + compose projects, so the UI can offer a picker.
// One-shot commands over the SSH client, same shape as ListInterfaces
// (tcpdump.go) - run a command, parse stdout. No streaming, no sudo (listing is
// read-only; if the daemon needs root the command simply returns nothing and
// the UI shows an empty list).

import (
	"encoding/json"
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

// ListContainers returns the running containers for the engine. Uses a
// tab-separated format so names/images with spaces don't split wrong.
func ListContainers(client *ssh.Client, engine string) ([]ContainerInfo, error) {
	engine = containerEngine(engine)
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	// {{.Names}} is a single name for `docker ps`; tabs separate the columns.
	out, err := sess.Output(engine + ` ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}' 2>/dev/null`)
	if err != nil {
		// A non-zero exit (daemon down, perms) yields an empty list, not an
		// error the UI must surface - the picker just shows nothing.
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
func ListComposeProjects(client *ssh.Client, engine string) ([]string, error) {
	engine = containerEngine(engine)

	// Primary: compose ls --format json -> [{"Name":"proj",...}].
	if sess, err := client.NewSession(); err == nil {
		out, runErr := sess.Output(engine + " compose ls --format json 2>/dev/null")
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
	out, err := sess.Output(engine + ` ps --format '{{.Label "com.docker.compose.project"}}' 2>/dev/null`)
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
