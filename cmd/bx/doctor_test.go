package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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

// covers P17 — bx doctor flags a tile whose name holds '+' (no new name may
// since 2026-09-28; one that predates the rule keeps working but can't get
// deployments), and only that tile.
func TestDoctorFlagsPlusNames(t *testing.T) {
	f := newDLFake().on("GET /api/xbin/components", 200, `[{"path":"notes+ideas"},{"path":"apps/x"}]`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	out, _, _ := dlExec(t, t.TempDir(), []string{"XBIN_URL=" + srv.URL, "XBIN_TOKEN=dl-token"}, "doctor")
	if !strings.Contains(out, "✗ notes+ideas: its name holds '+', which names a tile deployment in URLs") ||
		!strings.Contains(out, "can't get deployments") {
		t.Errorf("bx doctor didn't flag notes+ideas:\n%s", out)
	}
	if strings.Contains(out, "apps/x: its name holds") {
		t.Errorf("bx doctor flagged apps/x:\n%s", out)
	}
}
