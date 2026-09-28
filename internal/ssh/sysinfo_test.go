package ssh

import (
	"fmt"
	"strings"
	"testing"
)

func TestParsePs(t *testing.T) {
	out := "48211 nginx 72.0 4.1 php-fpm: pool www\n" +
		"99999 deploy 50.0 0.0 ps -eo pid=,user:32=,pcpu=,pmem=,args= --sort=-pcpu\n" +
		" 1022 app 18.3 31.0 java -Xmx4g -jar app.jar\n" +
		"877 nginx 3.0 0.4 nginx: worker process\n" +
		"612 root 1.0 0.2 rsyslogd -n\n" +
		"1 root 0.0 0.1 /usr/lib/systemd/systemd --switched-root\n" +
		"2 root 0.0 0.0 [kthreadd]\n"
	p := parsePs(out, topProcLimit)
	if len(p) != 6 {
		t.Fatalf("got %d rows, want 6 (7 lines minus the probe's own ps)", len(p))
	}
	if p[0].PID != 48211 || p[0].Command != "php-fpm: pool www" || p[0].CPU != 72 {
		t.Errorf("first = %+v", p[0])
	}
	for _, r := range p {
		if strings.HasPrefix(r.Command, "ps -eo") {
			t.Error("the probe's own ps must be dropped")
		}
	}
}

func TestParsePsCapsAtLimit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 15; i++ {
		b.WriteString(fmt.Sprintf("%d root 1.0 0.1 worker-%d\n", 100+i, i))
	}
	if got := len(parsePs(b.String(), topProcLimit)); got != topProcLimit {
		t.Errorf("got %d rows, want the cap of %d", got, topProcLimit)
	}
}

func TestParseUnits(t *testing.T) {
	out := "php8.3-fpm.service loaded failed failed The PHP 8.3 FastCGI Process Manager\n" +
		"nginx.service loaded active running A high performance web server\n" +
		"\n"
	u := parseUnits(out)
	if len(u) != 2 || u[0].Unit != "php8.3-fpm.service" || u[0].Active != "failed" ||
		u[1].Description != "A high performance web server" {
		t.Fatalf("units = %+v", u)
	}
}

func TestUnitNameGuard(t *testing.T) {
	for _, bad := range []string{"a;rm -rf /", "x y", "$(id)", "a'b"} {
		if unitNameRe.MatchString(bad) {
			t.Errorf("%q passed the unit name check", bad)
		}
	}
	for _, good := range []string{"nginx.service", "getty@tty1.service", "sys-devices-x\\x2d1.device"} {
		if !unitNameRe.MatchString(good) {
			t.Errorf("%q rejected", good)
		}
	}
}

func TestParseServerStatsFailedUnits(t *testing.T) {
	base := strings.Repeat(statsSep+"\n", 9)
	if s := parseServerStats(base + "2\n"); s.FailedUnits != 2 {
		t.Errorf("failed = %d, want 2", s.FailedUnits)
	}
	if s := parseServerStats(base); s.FailedUnits != -1 {
		t.Errorf("no systemctl must stay -1, got %d", s.FailedUnits)
	}
}
