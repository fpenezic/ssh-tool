package inventory

import (
	"reflect"
	"testing"
)

func TestAnsibleJumpQuotedValues(t *testing.T) {
	cases := map[string][]string{
		`-o ProxyCommand="ssh -o 'BatchMode=yes' -W %h:%p bastion.example.com"`: {"bastion.example.com"},
		`-o ProxyCommand='ssh -W %h:%p jump@bastion.example.com'`:                {"jump@bastion.example.com"},
		`-o ProxyJump="bastion.example.com"`:                                     {"bastion.example.com"},
	}
	for args, want := range cases {
		if got := AnsibleParseJumpHosts(args); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", args, got, want)
		}
	}
}
