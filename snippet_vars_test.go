package main

import "testing"

func TestExpandSnippetVars(t *testing.T) {
	cases := []struct {
		name string
		body string
		vars map[string]string
		want string
	}{
		{
			name: "no placeholders unchanged",
			body: "systemctl status nginx",
			vars: map[string]string{"x": "y"},
			want: "systemctl status nginx",
		},
		{
			name: "simple substitution",
			body: "journalctl -u ${svc}",
			vars: map[string]string{"svc": "ssh"},
			want: "journalctl -u ssh",
		},
		{
			name: "default used when var missing",
			body: "journalctl --since ${since:-1h}",
			vars: nil,
			want: "journalctl --since -1h",
		},
		{
			name: "provided value beats default",
			body: "journalctl --since ${since:-1h}",
			vars: map[string]string{"since": "10min ago"},
			want: "journalctl --since 10min ago",
		},
		{
			name: "explicit empty value honoured",
			body: "cmd ${flag}",
			vars: map[string]string{"flag": ""},
			want: "cmd ",
		},
		{
			name: "missing var no default left verbatim",
			body: "echo ${unknown}",
			vars: map[string]string{},
			want: "echo ${unknown}",
		},
		{
			name: "multiple vars",
			body: "journalctl -u ${svc} --since ${since:-1h}",
			vars: map[string]string{"svc": "docker", "since": "today"},
			want: "journalctl -u docker --since today",
		},
		{
			name: "same var twice",
			body: "${u}@${u}",
			vars: map[string]string{"u": "root"},
			want: "root@root",
		},
		{
			name: "unterminated placeholder verbatim",
			body: "echo ${broken",
			vars: map[string]string{"broken": "x"},
			want: "echo ${broken",
		},
		{
			name: "empty default",
			body: "cmd${maybe:}end",
			vars: nil,
			want: "cmdend",
		},
		{
			name: "colon in default value preserved",
			body: "ssh ${target:user@host:22}",
			vars: nil,
			want: "ssh user@host:22",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandSnippetVars(tc.body, tc.vars)
			if got != tc.want {
				t.Fatalf("expandSnippetVars(%q, %v) = %q, want %q", tc.body, tc.vars, got, tc.want)
			}
		})
	}
}
