package main

import "testing"

func TestPBSReachabilityLogValue(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		value *bool
		want  string
	}{{nil, "unknown"}, {&yes, "true"}, {&no, "false"}} {
		if got := pbsReachabilityLabel(c.value); got != c.want {
			t.Fatalf("got %q want %q", got, c.want)
		}
	}
}
