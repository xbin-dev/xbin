package main

import "testing"

func TestSingleUIDMap(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Single-uid mode: only container-root mapped (the eval-box failure).
		{"single-uid xbin", "         0        999          1\n", true},
		{"single-uid dev", "0 1000 1", true},
		// Range mode: root + a wide delegated sub-id row → not flagged.
		{"range", "         0       1000          1\n         1     100000      65535\n", false},
		{"range compact", "0 999 1\n1 100000 65536", false},
		// A single wide row (unusual) is still range-capable.
		{"wide only", "0 0 65536", false},
		// Empty / unreadable → not flagged (avoid false positives).
		{"empty", "", false},
		{"blank lines", "\n\n", false},
	}
	for _, c := range cases {
		if got := singleUIDMap(c.in); got != c.want {
			t.Errorf("%s: singleUIDMap(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
