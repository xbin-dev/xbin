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

// covers D127j — bx doctor flags a tile whose name holds '+' (no new name may
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

// covers D166's upgrade check — bx doctor renders GET /go-build-versions: a
// tile still linking older versions is a problem naming the lines to add
// and what changed, a dismissed one a note, a tile it couldn't compare a
// note with the error's first line, a shared go.work the go command
// refused one note.
func TestDoctorGoBuildVersions(t *testing.T) {
	f := newDLFake().on("GET /api/xbin/components", 200, `[{"path":"apps/b","runtime":"go"}]`).
		on("GET /api/xbin/go-build-versions", 200, `{"since":"v0.3.65","done":true,"running":false,
			"workspaceError":"the workspace's shared go.work doesn't load: go: module ../c listed in go.work file requires go >= 1.27\nmore","tiles":[
			{"tile":"apps/b","require":["modernc.org/sqlite v1.39.1"],"minimal":true,"changes":[
				{"module":"modernc.org/libc","had":"v1.66.10","now":"v1.55.3"},
				{"module":"golang.org/x/exp","had":"v0.0.0-2025"},
				{"module":"example.com/n","now":"v0.1.0"}],"dismissed":false},
			{"tile":"apps/c","require":["golang.org/x/net v0.46.0"],"minimal":false,"changes":[],"dismissed":true}],
			"errors":[{"tile":"apps/d","error":"built with the workspace's go.work: go: module . requires go >= 1.25.0\nmore"}]}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	out, _, _ := dlExec(t, t.TempDir(), []string{"XBIN_URL=" + srv.URL, "XBIN_TOKEN=dl-token"}, "doctor")
	for _, want := range []string{
		"✗ apps/b builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require modernc.org/sqlite v1.39.1` to its go.mod to keep what it had — modernc.org/libc v1.66.10 → v1.55.3, golang.org/x/exp v0.0.0-2025 → not linked, example.com/n (new) v0.1.0",
		"· (dismissed) apps/c builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require golang.org/x/net v0.46.0` to its go.mod to keep what it had (the raw differing lines: fewer may do)",
		"· apps/d: the Go build versions check couldn't compare its builds: built with the workspace's go.work: go: module . requires go >= 1.25.0\n",
		"· the Go build versions check couldn't compare the tiles it has no baseline of: the workspace's shared go.work doesn't load: go: module ../c listed in go.work file requires go >= 1.27 (POST /api/xbin/go-build-versions/check once that is fixed)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bx doctor lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗ apps/c") {
		t.Errorf("a dismissed tile counted as a problem:\n%s", out)
	}
}
