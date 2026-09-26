// Command ssh-tool-agent is a headless, read-only MCP server over an ssh-tool
// profile, for unattended LLM agents.
//
// It opens a profile directory (store.db + vault.enc) without any GUI, limits
// itself to the folders and connections named in its config, and exposes one
// capability: run a command that internal/cmdpolicy classifies as read-only on
// one or more of those hosts, without a PTY, and return stdout/stderr/exit
// code per host. Anything not read-only is refused with the reason - there is
// no approval prompt because there is nobody to ask.
//
// Point it at a profile of its own (an export of just the folders it needs),
// not at the desktop app's live profile: two processes writing one SQLite
// store is not supported.
//
// Spike. Not wired into the release build.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ssh-tool/internal/cmdpolicy"
	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// Config is the agent's JSON config file.
type Config struct {
	// DataDir is the profile directory. Empty = the platform default
	// (the desktop app's own profile - see the package comment).
	DataDir string `json:"data_dir"`
	// Folders are folder paths ("Proxmox", "Cloud/Hetzner") whose
	// connections and dynamic-inventory hosts are in scope, subfolders
	// included. "/" is the whole profile.
	Folders []string `json:"folders"`
	// Connections are single connections in scope by name or id.
	Connections []string `json:"connections"`
	// AllowSudo lets sudo/doas wrap a read-only command.
	AllowSudo bool `json:"allow_sudo"`
	// ExtraReadOnly are commands trusted as read-only with any arguments.
	ExtraReadOnly []string `json:"extra_readonly"`
	// TimeoutSeconds caps one command on one host. Default 60.
	TimeoutSeconds int `json:"timeout_seconds"`
	// MaxOutputBytes caps stdout and stderr per host. Default 64 KiB.
	MaxOutputBytes int `json:"max_output_bytes"`
}

func (c *Config) policy() cmdpolicy.Options {
	return cmdpolicy.Options{Extra: c.ExtraReadOnly, AllowSudo: c.AllowSudo}
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 60
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 64 << 10
	}
	if len(c.Folders) == 0 && len(c.Connections) == 0 {
		return nil, fmt.Errorf("%s: no folders or connections in scope", path)
	}
	return &c, nil
}

func main() {
	cfgPath := flag.String("config", "", "agent config file (JSON)")
	check := flag.String("check", "", "classify a command and exit")
	list := flag.Bool("list", false, "print the hosts in scope and exit")
	execCmd := flag.String("exec", "", "run a command on -hosts (or all in scope) and exit")
	hosts := flag.String("hosts", "", "comma-separated host names or ids for -exec")
	flag.Parse()
	log.SetOutput(os.Stderr) // stdout is the MCP channel
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("ssh-tool-agent: ")

	if *check != "" && *cfgPath == "" {
		printVerdict(*check, cmdpolicy.Classify(*check, cmdpolicy.Options{}))
		return
	}
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "usage: ssh-tool-agent -config agent.json [-list | -check CMD | -exec CMD [-hosts a,b]]")
		os.Exit(2)
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if *check != "" {
		printVerdict(*check, cmdpolicy.Classify(*check, cfg.policy()))
		return
	}

	a, err := open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer a.db.Close()

	switch {
	case *list:
		hs, err := a.scope()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(formatHosts(hs))
	case *execCmd != "":
		var names []string
		if *hosts != "" {
			names = strings.Split(*hosts, ",")
		}
		out, err := a.exec(*execCmd, names, "", 0)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(out)
	default:
		if err := a.serveStdio(); err != nil {
			log.Fatal(err)
		}
	}
}

func printVerdict(cmd string, v cmdpolicy.Verdict) {
	if v.ReadOnly {
		fmt.Printf("read-only: %s\n", cmd)
		return
	}
	fmt.Printf("refused: %s\n  %s\n", cmd, v.Reason)
	os.Exit(1)
}

type agent struct {
	cfg   *Config
	db    *store.DB
	vault *creds.Vault
}

// open loads the profile. The vault is unlocked from SSH_TOOL_VAULT_PASSPHRASE
// when set, else from the machine-bound auto-unlock sidecar; a profile with
// no vault still works for key-agent auth.
func open(cfg *Config) (*agent, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = store.DataDir()
	}
	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		return nil, err
	}
	v := creds.NewVault()
	vpath := filepath.Join(dir, "vault.enc")
	v.SetPath(vpath)
	if creds.FileExists(vpath) {
		if pass := os.Getenv("SSH_TOOL_VAULT_PASSPHRASE"); pass != "" {
			if err := v.Unlock(pass, false); err != nil {
				db.Close()
				return nil, fmt.Errorf("vault: %w", err)
			}
		} else if ok, err := v.AutoUnlock(); err != nil || !ok {
			db.Close()
			if err == nil {
				err = fmt.Errorf("locked (no SSH_TOOL_VAULT_PASSPHRASE and no auto-unlock sidecar)")
			}
			return nil, fmt.Errorf("vault: %w", err)
		}
	}
	log.Printf("profile %s open", dir)
	a := &agent{cfg: cfg, db: db, vault: v}
	a.wireNetworkProfiles()
	return a, nil
}
