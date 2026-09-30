package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// covers 06§7 — bx partition ls|stop|reset|purge|limits|share-log|credential
// send the routes' bodies (stop and reset name the caller's own partition
// by default, --user another's; reset sends the typed confirmation with
// --yes), and against an xbind without the partitions API they answer
// errNoPartitions (exit 6).
func TestBxPartitionOps(t *testing.T) {
	var got []string
	old := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b)))
		if old && r.URL.Path != "/api/xbin/whoami" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/xbin/whoami":
			_, _ = io.WriteString(w, `{"kind":"user","id":"alice"}`)
		case "/api/xbin/partitions":
			_, _ = io.WriteString(w, `{"features":["partitions/1"],"tile":"apps/docs","state":"partitioned","limits":{"maxRunning":6,"partitionBytes":0},
				"partitions":[{"partition":"user:alice","state":"active","running":true,"bytes":2048}],"tiles":[{"tile":"apps/docs","state":"partitioned"}],
				"credentials":[{"id":"c1","kind":"invite","by":"bob","until":"2026-10-01T00:00:00Z"}],
				"orphans":[{"tile":"apps/docs","partition":"u-0123","user":"zed","reason":"user-deleted","since":"2026-09-01"}]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"deleted":{"namespaces":1},"purged":[],"until":"2026-10-02T00:00:00Z","kind":"invite","decision":"allowed"}`)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	for _, args := range [][]string{
		{"ls"}, {"ls", "apps/docs"},
		{"stop", "apps/docs"}, {"stop", "apps/docs", "--user", "bob"},
		{"reset", "apps/docs", "--yes"},
		{"purge", "apps/docs", "--partition", "u-0123", "--yes"}, {"reviewed", "apps/docs", "on"},
		{"limits", "apps/docs"}, {"limits", "apps/docs", "--max-running", "4"},
		{"share-log", "apps/docs", "--days", "3"}, {"share-log", "apps/docs", "--stop"},
		{"credential", "c1", "allow"},
	} {
		if err := partitionCmd(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, want := range []string{
		`POST /api/xbin/partitions/stop {"partition":"user:alice","tile":"apps/docs"}`,
		`POST /api/xbin/partitions/stop {"partition":"user:bob","tile":"apps/docs"}`,
		`POST /api/xbin/partitions/reset {"confirm":"apps/docs user:alice","partition":"user:alice","tile":"apps/docs"}`,
		`POST /api/xbin/partitions/purge {"partition":"u-0123","tile":"apps/docs"}`,
		`POST /api/xbin/partitions/limits {"maxRunning":4,"tile":"apps/docs"}`,
		`POST /api/xbin/partitions/share-log {"days":3,"tile":"apps/docs"}`,
		`DELETE /api/xbin/partitions/share-log {"tile":"apps/docs"}`,
		`POST /api/xbin/partitions/credential-confirm {"allow":true,"id":"c1"}`,
		`POST /api/xbin/partitions/reviewed {"on":true,"tile":"apps/docs"}`,
	} {
		if !slices.Contains(got, want) {
			t.Errorf("no %s in\n%s", want, strings.Join(got, "\n"))
		}
	}
	// purge without --yes lists what it would delete, deletes nothing, fails
	got = nil
	if err := partitionCmd([]string{"purge", "apps/docs"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("purge without --yes: %v", err)
	}
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "POST /api/xbin/partitions/purge") }) {
		t.Errorf("purge without --yes posted: %q", got)
	}
	// bx logs' partition flags ask xbind for the global instance's or a shared person's log
	for _, c := range []struct {
		args []string
		want string
	}{{[]string{"--global", "apps/docs"}, "xbin-partition=global"}, {[]string{"apps/docs", "--user", "bob"}, "user=bob"}} {
		rest, q, err := takePartitionLogFlags(c.args)
		if err != nil || len(rest) != 1 || rest[0] != "apps/docs" || q.Encode() != c.want {
			t.Errorf("bx logs %v: %v %v %v", c.args, rest, q.Encode(), err)
		}
	}
	if _, _, err := takePartitionLogFlags([]string{"--global", "--user", "bob", "apps/docs"}); err == nil {
		t.Error("bx logs --global --user: both")
	}
	if err := partitionCmd([]string{"stop"}); err == nil {
		t.Error("stop without a tile")
	}
	if err := partitionCmd([]string{"ls", "--bogus"}); err == nil {
		t.Error("an unknown flag")
	}
	old = true
	if err := partitionCmd([]string{"ls"}); !errors.Is(err, errNoPartitions) {
		t.Errorf("an xbind without the partitions API: %v", err)
	}
}
