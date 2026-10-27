package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/limits"
)

// covers D203 — bx limits' arguments: flags anywhere, kinds by bx name or
// API name, sizes in the kind's unit, raises by percentage of the current
// limit (+50 % without one), --scope in place of the tile; every mistake a
// usage error (exit 2) before any request.
func TestBxLimitsArgs(t *testing.T) {
	a, err := parseLimitsArgs("limits", []string{"apps/x", "--restart", "memory", "+25%", "--force"}, "--restart", "--force", "--scope")
	if err != nil || !reflect.DeepEqual(a.pos, []string{"apps/x", "memory", "+25%"}) || !a.restart || !a.force {
		t.Fatalf("%+v %v", a, err)
	}
	if tg, rest, err := a.target("limits", 1, 2); err != nil || tg.tile != "apps/x" || !reflect.DeepEqual(rest, []string{"memory", "+25%"}) {
		t.Errorf("target: %+v %v %v", tg, rest, err)
	}
	a, err = parseLimitsArgs("limits", []string{"--scope=apps/cal", "disk", "80G"}, "--scope")
	if tg, rest, _ := a.target("limits", 1, 2); err != nil || tg.scope != "apps/cal" || tg.rowTile() != "apps/cal" || len(rest) != 2 || tg.query() != "scope=apps%2Fcal" {
		t.Errorf("--scope: %+v %+v %v", a, tg, err)
	}
	if tg := (limitsTarget{scope: "workspace"}); tg.rowTile() != "" {
		t.Errorf("the workspace scope has no row: %q", tg.rowTile())
	}
	for _, c := range []struct {
		k       limits.Kind
		current int64
		s       string
		want    int64
	}{
		{limits.MemMiB, 2048, "", 3072},
		{limits.MemMiB, 2048, "+25%", 2560},
		{limits.MemMiB, 2048, "100%", 4096},
		{limits.MemMiB, 2048, "6G", 6144},
		{limits.MemMiB, 0, "1536M", 1536},
		{limits.DiskGiB, 50, "+50%", 75},
		{limits.DiskGiB, 50, "1.5T", 1536},
		{limits.SbxVCPUs, 4, "+25%", 5},
		{limits.SbxVCPUs, 4, "8", 8},
	} {
		if got, err := raiseValue(c.k, c.current, c.s); err != nil || got != c.want {
			t.Errorf("raise %s %d by %q = %d %v, want %d", c.k, c.current, c.s, got, err, c.want)
		}
	}
	for _, c := range []struct {
		k limits.Kind
		s string
	}{{limits.MemMiB, "4096"}, {limits.MemMiB, "+0%"}, {limits.MemMiB, "+x%"}, {limits.SbxVCPUs, "4G"}} {
		if _, err := raiseValue(c.k, 2048, c.s); err == nil {
			t.Errorf("raise %s by %q: accepted", c.k, c.s)
		}
	}
	if _, err := raiseValue(limits.MemMiB, 0, "+50%"); err == nil {
		t.Error("a percentage of an unknown limit: accepted")
	}
	body, err := orgLimitsBody([]string{"memory=8G", "disk=500G", "sandbox-vcpus=8"}, []string{"vm-memory"})
	if err != nil || !reflect.DeepEqual(body, map[string]any{"memMiB": int64(8192), "diskGiB": int64(500), "sbxVCPUs": int64(8), "vmMemMiB": nil}) {
		t.Errorf("org body: %v %v", body, err)
	}
	for _, set := range [][]string{{"memory"}, {"cpu=2"}, {"memory=lots"}} {
		if _, err := orgLimitsBody(set, nil); err == nil {
			t.Errorf("--set %v: accepted", set)
		}
	}
	for _, args := range [][]string{
		{"raise"}, {"raise", "apps/x"}, {"raise", "apps/x", "cpu"}, {"raise", "apps/x", "memory", "4096"},
		{"set", "apps/x", "memory"}, {"reset", "apps/x"}, {"show"}, {"show", "apps/x", "extra"},
		{"ls", "apps/x"}, {"ls", "--nope"}, {"frob"}, {"raise", "apps/x", "memory", "--scope"},
		{"dismiss", "apps/x", "memory", "--json"},
	} {
		var e *dcError
		if err := cmdLimits(args); !errors.As(err, &e) || e.code != exitUsage {
			t.Errorf("bx limits %v: %v, want a usage error", args, err)
		}
	}
}

// covers D203 — bx limits' requests: each command sends what the API
// takes (approve: the manifest's pending asks at their value, capped at the
// maximum; raise: a percentage of the current limit; set, reset, dismiss;
// bx org limits --set/--rm), and an xbind without tile limits (its mux's
// 404) exits 6.
func TestBxLimitsRequests(t *testing.T) {
	row := `{"tile":{"tile":"apps/x","scope":"apps/x","scopeKey":"apps~x","rootsScope":true,"limits":{
		"memMiB":{"effective":2048,"default":2048,"max":16384,"enforced":true,"ask":4096,"pending":true},
		"diskGiB":{"effective":50,"default":50,"max":900,"enforced":true},
		"sbxVCPUs":{"effective":4,"default":4,"max":6,"enforced":true,"ask":8,"pending":true}}}}`
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(b)))
		switch {
		case r.URL.Path == "/api/xbin/limits/dismiss":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/xbin/orgs":
			_, _ = io.WriteString(w, `{"orgs":[{"id":"acme","limits":{"memMiB":4096}}]}`)
		case strings.HasSuffix(r.URL.Path, "/limits") && r.Method == "PUT":
			_, _ = io.WriteString(w, `{"limits":{"memMiB":8192}}`)
		case r.URL.Path == "/api/xbin/limits" && r.URL.RawQuery == "":
			_, _ = io.WriteString(w, `{"tiles":[`+strings.TrimSuffix(strings.TrimPrefix(row, `{"tile":`), "}")+`]}`)
		default:
			_, _ = io.WriteString(w, row)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	var out bytes.Buffer
	oOut := dcOut
	dcOut = &out
	defer func() { dcOut = oOut }()

	for _, args := range [][]string{
		{"approve", "apps/x"},
		{"raise", "apps/x", "memory"},
		{"raise", "apps/x", "disk", "+100%", "--restart"},
		{"set", "--scope", "workspace", "disk", "80G", "--force"},
		{"reset", "apps/x", "sandbox-vcpus"},
		{"dismiss", "apps/x", "memMiB"},
		{"ls", "--pending"},
	} {
		if err := cmdLimits(args); err != nil {
			t.Fatalf("bx limits %v: %v", args, err)
		}
	}
	if err := cmdOrgLimits([]string{"acme", "--set", "memory=8G", "--rm", "disk"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdOrgLimits([]string{"acme"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/xbin/limits?tile=apps%2Fx",
		`POST /api/xbin/limits {"kind":"memMiB","source":"manifest-ask","tile":"apps/x","value":4096}`,
		`POST /api/xbin/limits {"kind":"sbxVCPUs","source":"manifest-ask","tile":"apps/x","value":6}`,
		"GET /api/xbin/limits?tile=apps%2Fx",
		`POST /api/xbin/limits {"kind":"memMiB","source":"raise","tile":"apps/x","value":3072}`,
		"GET /api/xbin/limits?tile=apps%2Fx",
		`POST /api/xbin/limits {"kind":"diskGiB","restart":true,"source":"raise","tile":"apps/x","value":100}`,
		`POST /api/xbin/limits {"force":true,"kind":"diskGiB","scope":"workspace","source":"admin-set","value":80}`,
		"DELETE /api/xbin/limits?tile=apps%2Fx&kind=sbxVCPUs",
		"POST /api/xbin/limits/dismiss?tile=apps%2Fx&kind=memMiB",
		"GET /api/xbin/limits",
		`PUT /api/xbin/orgs/acme/limits {"diskGiB":null,"memMiB":8192}`,
		"GET /api/xbin/orgs",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(out.String(), "apps/x                           memory          2 GiB (default 2 GiB) · asks 4 GiB: pending") ||
		!strings.Contains(out.String(), "memory          4 GiB per tile") {
		t.Errorf("output:\n%s", out.String())
	}

	// An older xbind: its mux answers 404 with no JSON error → exit 6.
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	t.Setenv("XBIN_URL", old.URL)
	for _, args := range [][]string{{"ls"}, {"show", "apps/x"}, {"reset", "apps/x", "memory"}} {
		var e *dcError
		if err := cmdLimits(args); !errors.As(err, &e) || e.code != 6 || e.msg != noLimitsMsg {
			t.Errorf("bx limits %v on an old xbind: %v", args, err)
		}
	}
	var e *dcError
	if err := cmdOrgLimits([]string{"acme", "--set", "memory=8G"}); !errors.As(err, &e) || e.code != 6 {
		t.Errorf("bx org limits on an old xbind: %v", err)
	}
}
