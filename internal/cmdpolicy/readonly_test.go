package cmdpolicy

import "testing"

// The read-only lines are the shapes an LLM agent actually sends: pipes,
// stderr silenced, `|| true`, a cd first, a loop, $(...). The old
// operator-splitting classifier refused most of them.
func TestClassifyReadOnly(t *testing.T) {
	for _, cmd := range []string{
		"uptime",
		"df -h 2>/dev/null | grep -v tmpfs",
		"systemctl status nginx --no-pager 2>&1 | head -20",
		"systemctl --failed",
		"systemctl list-units --type=service --state=running",
		"systemctl -t service list-units",
		"journalctl -u nginx -n 50 --no-pager",
		"journalctl -p err -b --since '1 hour ago' | tail -n 30",
		"cat /etc/os-release && uname -a",
		"cd /var/log && ls -la | sort -k5 -n | tail",
		"grep -c processor /proc/cpuinfo || true",
		"ps aux --sort=-%mem | head -n 10",
		"for u in nginx sshd; do systemctl is-active $u; done",
		"echo \"load: $(cut -d' ' -f1 /proc/loadavg)\"",
		"free -m; df -h /; uptime",
		"ls /etc/*.conf 2>/dev/null | wc -l",
		"find /var/log -name '*.log' -mtime -1 -size +10M",
		"sed -n '1,20p' /etc/fstab",
		"sed 's/#.*//' /etc/ssh/sshd_config | grep -v '^$'",
		"awk -F: '$3 >= 1000 {print $1}' /etc/passwd",
		"awk '{sum += $2} END {print sum}' file",
		"docker ps --format '{{.Names}}\\t{{.Status}}'",
		"docker logs --tail 100 web 2>&1 | grep -i error",
		"docker compose -f /srv/app/compose.yml ps",
		"docker stats --no-stream",
		"docker ps -f status=exited",
		"pvesm status",
		"qm list",
		"pct config 101",
		"pvesh get /nodes --output-format json",
		"proxmox-backup-client snapshot list --repository root@pam@pbs:store1",
		"proxmox-backup-manager datastore list",
		"zpool status -x",
		"zfs list -t snapshot -o name,creation -s creation | tail -5",
		"restic -r /srv/restic snapshots --latest 1",
		"ceph -s",
		"ceph osd pool ls",
		"ip -br addr",
		"ip route show",
		"ss -tlnp",
		"LANG=C date +%F",
		"timeout 10 curl -fsS http://localhost:9100/metrics | grep node_load",
		"curl -sI https://example.com",
		"git -C /srv/repo log --oneline -5",
		"git branch -a",
		"crontab -l 2>/dev/null",
		"iptables -nvL",
		"nginx -t",
		"top -bn1 | head -15",
		"ping -c 3 example.com",
		"cat /proc/mdstat; mdadm --detail /dev/md0",
		"smartctl -a /dev/sda",
		"dpkg -l | grep -i openssl",
		"rpm -qa | sort",
		"f=/var/log/syslog; tail -n 5 $f",
		"find /etc -name '*.bak' | xargs ls -la",
		"diff <(sort a) <(sort b)",
		"cat <<EOF\nhello\nEOF",
		"/usr/bin/uptime",
		"stat -c %y /var/backups/latest.tar.gz",
		"date -d @1700000000",
		"hostname -f",
		"sysctl -a 2>/dev/null | grep vm.swappiness",
		"kubectl -n kube-system get pods",
		"openssl x509 -noout -enddate -in /etc/ssl/certs/site.pem",
		"echo | openssl s_client -connect example.com:443 2>/dev/null | openssl x509 -noout -dates",
	} {
		if v := Classify(cmd, Options{}); !v.ReadOnly {
			t.Errorf("want read-only: %q\n  reason: %s", cmd, v.Reason)
		}
	}
}

func TestClassifyNotReadOnly(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf /tmp/x",
		"echo hi > /tmp/x",
		"echo hi >> ~/.bashrc",
		"ls &> out.txt",
		"ls >&out.txt",
		"cat file | tee copy",
		"sed -i 's/a/b/' file",
		"sed -ni 's/a/b/p' file",
		"sed --in-place=.bak s/a/b/ file",
		"sed 's/a/b/w out' file",
		"sed -e '1e id' file",
		"sed -f script.sed file",
		"awk '{system(\"id\")}' file",
		"awk '{print > \"/tmp/x\"}' file",
		"awk '{print | \"sh\"}' file",
		"find / -name x -delete",
		"find / -name x -exec rm {} ;",
		"sort -o file file",
		"tail -f /var/log/syslog",
		"journalctl -f",
		"journalctl --vacuum-time=1d",
		"systemctl restart nginx",
		"systemctl stop nginx; systemctl status nginx",
		"docker logs -f web",
		"docker rm -f web",
		"docker exec web id",
		"docker stats",
		"kubectl delete pod x",
		"kubectl logs -f x",
		"ls $(rm -rf /tmp/x)",
		"ls `touch /tmp/x`",
		"cat <(rm x)",
		"ls >(cat)",
		"sleep 100 &",
		"PATH=/tmp/evil ls",
		"PATH=/tmp/evil; ls",
		"export PATH=/tmp/evil",
		"PS4='$(id)'; set -x; ls",
		"GIT_PAGER='rm x' git log",
		"git -c core.pager='sh -c id' log",
		"git branch newbranch",
		"git tag v1",
		"git config user.name x",
		"git stash",
		"git log --output=/tmp/x",
		"f() { ls; }; f",
		"eval ls",
		"bash -c ls",
		"$CMD",
		"/tmp/cat /etc/passwd",
		"sudo ls",
		"date -s '2020-01-01'",
		"date 010100002020",
		"hostname evil",
		"mount /dev/sdb1 /mnt",
		"ip link set eth0 down",
		"ip addr add 10.0.0.1/24 dev eth0",
		"sysctl -w net.ipv4.ip_forward=1",
		"sysctl net.ipv4.ip_forward=1",
		"curl -o /tmp/x https://example.com",
		"curl -d a=b https://example.com",
		"curl -X POST https://example.com",
		"curl -fsSLo x https://example.com",
		"crontab -r",
		"crontab file",
		"iptables -F",
		"iptables -A INPUT -j DROP",
		"nginx -s reload",
		"nginx",
		"smartctl -t long /dev/sda",
		"mdadm --stop /dev/md0",
		"qm stop 100",
		"pct destroy 101",
		"pvesh set /nodes/x",
		"zfs destroy pool/x",
		"zpool scrub tank",
		"ceph osd out 1",
		"ceph -w",
		"restic forget --prune",
		"proxmox-backup-client backup root.pxar:/",
		"find . | xargs sed -i s/a/b/",
		"find . | xargs rm",
		"timeout 5 rm x",
		"env PATH=/tmp ls",
		"env -S 'rm x'",
		"uniq in out",
		"xxd in out",
		"openssl req -new -keyout k.pem",
		"wget https://example.com",
		"unknowncmd --status",
		"top",
		"ping example.com",
		"dmesg -c",
		"ss -K dst 10.0.0.1",
		"tree -o out",
		"ifconfig eth0 down",
	} {
		if v := Classify(cmd, Options{}); v.ReadOnly {
			t.Errorf("want NOT read-only: %q", cmd)
		}
	}
}

func TestClassifySudo(t *testing.T) {
	opt := Options{AllowSudo: true}
	for _, cmd := range []string{"sudo zpool status", "sudo -n journalctl -u x -n 5", "sudo -l"} {
		if v := Classify(cmd, opt); !v.ReadOnly {
			t.Errorf("want read-only with sudo allowed: %q (%s)", cmd, v.Reason)
		}
	}
	for _, cmd := range []string{"sudo rm x", "sudo -s", "sudo -i", "sudo -e /etc/hosts", "sudo PATH=/tmp ls"} {
		if v := Classify(cmd, opt); v.ReadOnly {
			t.Errorf("want NOT read-only even with sudo allowed: %q", cmd)
		}
	}
}

func TestClassifyExtra(t *testing.T) {
	if v := Classify("mytool --report | head", Options{Extra: []string{"mytool"}}); !v.ReadOnly {
		t.Fatalf("extra command refused: %s", v.Reason)
	}
	if v := Classify("mytool --report > out", Options{Extra: []string{"mytool"}}); v.ReadOnly {
		t.Fatal("extra command bypassed the redirect check")
	}
}

func TestClassifyReason(t *testing.T) {
	v := Classify("ls | sed -i s/a/b/ f", Options{})
	if v.ReadOnly || v.Reason != "sed -i edits files in place" {
		t.Fatalf("reason = %q", v.Reason)
	}
}

// A variable must not smuggle a flag past a rule that looks for one.
func TestClassifyVariables(t *testing.T) {
	for _, cmd := range []string{
		"for f in /var/log/*.log; do tail -n1 $f; done",
		"tail -n 5 \"$HOME/.bash_history\"",
		"d=/etc; ls $d | head",
	} {
		if v := Classify(cmd, Options{}); !v.ReadOnly {
			t.Errorf("want read-only: %q (%s)", cmd, v.Reason)
		}
	}
	for _, cmd := range []string{
		"x=-f; tail $x /var/log/syslog",
		"x=-i; sed $x s/a/b/ f",
		"for o in -f; do tail $o x; done",
		"tail $(echo -f) x",
		"tail $UNKNOWN x",
		"x=rm; $x file",
	} {
		if v := Classify(cmd, Options{}); v.ReadOnly {
			t.Errorf("want NOT read-only: %q", cmd)
		}
	}
}
