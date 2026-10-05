package main

import (
	"reflect"
	"testing"
)

func TestReconnectDelays(t *testing.T) {
	if got := reconnectDelays(5, 16); !reflect.DeepEqual(got, []int64{1, 2, 4, 8, 16}) {
		t.Errorf("default = %v", got)
	}
	if got := reconnectDelays(7, 10); !reflect.DeepEqual(got, []int64{1, 2, 4, 8, 10, 10, 10}) {
		t.Errorf("capped = %v", got)
	}
	if got := reconnectDelays(1, 300); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("one attempt = %v", got)
	}
}
