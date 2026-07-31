package ssh

import "testing"

func TestBuildLogTailCommand(t *testing.T) {
	cases := []struct {
		name    string
		opts    LogTailOptions
		want    string
		wantErr bool
	}{
		{
			name: "journal with unit",
			opts: LogTailOptions{Kind: LogTailJournal, Unit: "nginx", Lines: 100},
			want: "journalctl -f -n 100 --no-pager -o short-iso -u nginx",
		},
		{
			name: "journal whole (no unit)",
			opts: LogTailOptions{Kind: LogTailJournal, Lines: 50},
			want: "journalctl -f -n 50 --no-pager -o short-iso",
		},
		{
			name: "journal default lines",
			opts: LogTailOptions{Kind: LogTailJournal, Unit: "ssh"},
			want: "journalctl -f -n 200 --no-pager -o short-iso -u ssh",
		},
		{
			name: "file tail",
			opts: LogTailOptions{Kind: LogTailFile, Path: "/var/log/syslog", Lines: 300},
			want: "tail -n 300 -F /var/log/syslog",
		},
		{
			name:    "file without path errors",
			opts:    LogTailOptions{Kind: LogTailFile},
			wantErr: true,
		},
		{
			name:    "unknown kind errors",
			opts:    LogTailOptions{Kind: "bogus"},
			wantErr: true,
		},
		{
			name: "unit with shell metachars is quoted",
			opts: LogTailOptions{Kind: LogTailJournal, Unit: "a; rm -rf /", Lines: 10},
			want: "journalctl -f -n 10 --no-pager -o short-iso -u 'a; rm -rf /'",
		},
		{
			name: "container docker default engine",
			opts: LogTailOptions{Kind: LogTailContainer, Container: "web", Lines: 100},
			want: "docker logs -f --tail 100 web",
		},
		{
			name: "container podman engine",
			opts: LogTailOptions{Kind: LogTailContainer, Engine: "podman", Container: "db", Lines: 50},
			want: "podman logs -f --tail 50 db",
		},
		{
			name: "container name with space is quoted",
			opts: LogTailOptions{Kind: LogTailContainer, Container: "my app", Lines: 20},
			want: "docker logs -f --tail 20 'my app'",
		},
		{
			name:    "container without name errors",
			opts:    LogTailOptions{Kind: LogTailContainer, Lines: 10},
			wantErr: true,
		},
		{
			name: "compose project",
			opts: LogTailOptions{Kind: LogTailCompose, Project: "myproj", Lines: 200},
			want: "docker compose -p myproj logs -f --tail 200",
		},
		{
			name: "compose podman engine and quoting",
			opts: LogTailOptions{Kind: LogTailCompose, Engine: "podman", Project: "a b", Lines: 10},
			want: "podman compose -p 'a b' logs -f --tail 10",
		},
		{
			name: "unknown engine falls back to docker",
			opts: LogTailOptions{Kind: LogTailContainer, Engine: "evil; rm", Container: "x", Lines: 10},
			want: "docker logs -f --tail 10 x",
		},
		{
			name:    "compose without project errors",
			opts:    LogTailOptions{Kind: LogTailCompose, Lines: 10},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildLogTailCommand(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("buildLogTailCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}
