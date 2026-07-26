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
