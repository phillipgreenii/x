package exitcode

import "testing"

// TestValuesPinned fails on any renumbering: every caller that branches on a
// shared code would break silently, so a change here must be a visible diff.
func TestValuesPinned(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"OK", OK, 0},
		{"Error", Error, 1},
		{"Usage", Usage, 2},
		{"Unavailable", Unavailable, 3},
		{"NotFound", NotFound, 4},
		{"Degraded", Degraded, 5},
		{"Timeout", Timeout, 6},
		{"Config", Config, 7},
		{"Denied", Denied, 8},
		{"Busy", Busy, 9},
		{"ShutdownOverrun", ShutdownOverrun, 10},
		{"Conflict", Conflict, 11},
		{"AppMin", AppMin, 20},
		{"AppMax", AppMax, 63},
	}
	seen := map[int]string{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
		if prev, dup := seen[c.got]; dup {
			t.Errorf("%s and %s share the value %d", prev, c.name, c.got)
		}
		seen[c.got] = c.name
	}
}

func TestSharedCodesAvoidReservedAndAppRange(t *testing.T) {
	for _, c := range []int{OK, Error, Usage, Unavailable, NotFound, Degraded, Timeout, Config, Denied, Busy, ShutdownOverrun, Conflict} {
		if IsAppSpecific(c) || IsSignal(c) || (c >= 64 && c <= 78) || c == 124 || c == 126 || c == 127 || c == 255 {
			t.Errorf("shared code %d is in a reserved or app-specific range", c)
		}
	}
}

func TestRangeHelpers(t *testing.T) {
	for code, want := range map[int]bool{19: false, 20: true, 63: true, 64: false} {
		if got := IsAppSpecific(code); got != want {
			t.Errorf("IsAppSpecific(%d) = %v, want %v", code, got, want)
		}
	}
	for code, want := range map[int]bool{128: false, 129: true, 130: true, 143: true, 254: true, 255: false, 1: false} {
		if got := IsSignal(code); got != want {
			t.Errorf("IsSignal(%d) = %v, want %v", code, got, want)
		}
	}
}
