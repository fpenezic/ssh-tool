package inventory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParseAnsibleINI_Basic(t *testing.T) {
	ini := `
# comment
[webservers]
web1.example.com ansible_user=deploy ansible_port=2222
web2.example.com

[webservers:vars]
env=prod

[dbservers]
db1.example.com ansible_host=10.0.0.5

[all:children]
webservers
dbservers
`
	hosts, err := parseAnsibleINI([]byte(ini))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]ansibleHost{}
	for _, h := range hosts {
		byName[h.Name] = h
	}

	web1, ok := byName["web1.example.com"]
	if !ok {
		t.Fatal("web1 missing")
	}
	if web1.Vars["ansible_user"] != "deploy" || web1.Vars["ansible_port"] != "2222" {
		t.Errorf("web1 vars wrong: %v", web1.Vars)
	}
	if web1.Vars["env"] != "prod" {
		t.Errorf("web1 should inherit env=prod from group vars; got %v", web1.Vars)
	}
	// Membership in webservers + all (via children).
	wantGroups := []string{"all", "webservers"}
	got := append([]string{}, web1.Groups...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, wantGroups) {
		t.Errorf("web1 groups want %v got %v", wantGroups, got)
	}

	db1 := byName["db1.example.com"]
	if db1.Vars["ansible_host"] != "10.0.0.5" {
		t.Errorf("db1 ansible_host wrong: %q", db1.Vars["ansible_host"])
	}
}

func TestParseAnsibleINI_HostInMultipleGroups(t *testing.T) {
	ini := `
[webservers]
host1

[loadbalancers]
host1
`
	hosts, err := parseAnsibleINI([]byte(ini))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	got := append([]string{}, hosts[0].Groups...)
	sort.Strings(got)
	want := []string{"loadbalancers", "webservers"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups: want %v got %v", want, got)
	}
}

func TestParseAnsibleINI_QuotedValues(t *testing.T) {
	ini := `
[hosts]
h1 ansible_ssh_common_args="-o ProxyJump=bastion.example.com"
h2 ansible_ssh_common_args='-o "ProxyCommand=ssh -W %h:%p bastion"'
`
	hosts, err := parseAnsibleINI([]byte(ini))
	if err != nil {
		t.Fatalf("%v", err)
	}
	byName := map[string]ansibleHost{}
	for _, h := range hosts {
		byName[h.Name] = h
	}
	h1 := byName["h1"]
	if !strings.Contains(h1.Vars["ansible_ssh_common_args"], "ProxyJump=bastion.example.com") {
		t.Errorf("quoted value not preserved: %q", h1.Vars["ansible_ssh_common_args"])
	}
}

func TestParseAnsibleYAML_Nested(t *testing.T) {
	doc := `
all:
  vars:
    env: prod
  children:
    webservers:
      hosts:
        web1.example.com:
          ansible_user: deploy
          ansible_port: 2222
        web2.example.com: {}
      vars:
        role: web
    dbservers:
      hosts:
        db1.example.com:
          ansible_host: 10.0.0.5
`
	hosts, err := parseAnsibleYAML([]byte(doc))
	if err != nil {
		t.Fatalf("%v", err)
	}
	byName := map[string]ansibleHost{}
	for _, h := range hosts {
		byName[h.Name] = h
	}
	web1 := byName["web1.example.com"]
	if web1.Vars["ansible_user"] != "deploy" || web1.Vars["ansible_port"] != "2222" {
		t.Errorf("web1 vars wrong: %v", web1.Vars)
	}
	if web1.Vars["env"] != "prod" {
		t.Errorf("web1 should inherit env from all; got %v", web1.Vars)
	}
	if web1.Vars["role"] != "web" {
		t.Errorf("web1 should inherit role from webservers; got %v", web1.Vars)
	}
	// Tags reflect full nesting.
	got := append([]string{}, web1.Groups...)
	sort.Strings(got)
	want := []string{"all", "webservers"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("web1 groups: want %v got %v", want, got)
	}
}

func TestAnsibleParseJumpHosts(t *testing.T) {
	cases := []struct {
		args string
		want []string
	}{
		{"-J root@192.168.107.250", []string{"root@192.168.107.250"}},
		{"-J bastion1,bastion2", []string{"bastion1", "bastion2"}},
		{"-o ProxyJump=bastion.example.com", []string{"bastion.example.com"}},
		{"-o ProxyJump=user@bastion:22", []string{"user@bastion:22"}},
		{"-o ProxyJump=bastion1.example.com,bastion2.example.com", []string{"bastion1.example.com", "bastion2.example.com"}},
		{`-o "ProxyCommand=ssh -W %h:%p bastion.example.com"`, []string{"bastion.example.com"}},
		{`-o 'ProxyCommand=ssh user@bastion -W %h:%p'`, []string{"user@bastion"}},
		{"-o StrictHostKeyChecking=no", nil},
		{"", nil},
		{`-o "ProxyCommand=corkscrew proxy 8080 %h %p"`, nil}, // non-ssh proxy → unparseable
	}
	for _, c := range cases {
		t.Run(c.args, func(t *testing.T) {
			got := AnsibleParseJumpHosts(c.args)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("args=%q want %v got %v", c.args, c.want, got)
			}
		})
	}
}

func TestAnsibleFetch_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.ini")
	body := `
[webservers]
web1 ansible_user=deploy
web2

[webservers:vars]
env=prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := Ansible{}.Fetch(context.Background(), map[string]any{
		"path": path,
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries got %d", len(entries))
	}
	if entries[0].Name != "web1" {
		t.Errorf("expected web1 first (sorted), got %q", entries[0].Name)
	}
	if entries[0].Kind != KindServer {
		t.Errorf("expected KindServer, got %q", entries[0].Kind)
	}
}

func TestAnsibleFetch_HostPatternFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.ini")
	body := `
[hosts]
web1
web2
db1
`
	_ = os.WriteFile(path, []byte(body), 0o600)
	entries, err := Ansible{}.Fetch(context.Background(), map[string]any{
		"path":         path,
		"host_pattern": "web*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 (web*), got %d: %+v", len(entries), entries)
	}
}

func TestAnsibleFetch_GroupPatternFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.ini")
	body := `
[prod_web]
w1
[dev_web]
w2
[prod_db]
d1
`
	_ = os.WriteFile(path, []byte(body), 0o600)
	entries, err := Ansible{}.Fetch(context.Background(), map[string]any{
		"path":          path,
		"group_pattern": "prod_*",
	})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	want := []string{"d1", "w1"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("want %v got %v", want, names)
	}
}

func TestAnsibleFetch_NameFromAnsibleHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.ini")
	body := `
[hosts]
inv_name ansible_host=10.0.0.5
`
	_ = os.WriteFile(path, []byte(body), 0o600)
	entries, err := Ansible{}.Fetch(context.Background(), map[string]any{
		"path":      path,
		"name_from": "ansible_host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "10.0.0.5" {
		t.Fatalf("expected name=10.0.0.5, got %+v", entries)
	}
	if entries[0].Hostname != "10.0.0.5" {
		t.Errorf("hostname should still be ansible_host: %q", entries[0].Hostname)
	}
	if entries[0].ExternalID != "inv_name" {
		t.Errorf("external_id should be inventory name for stable identity: %q", entries[0].ExternalID)
	}
}
