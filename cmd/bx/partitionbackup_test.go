package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// covers 11§4 — bx restore/backups --partition: a restore checks the
// archive first (a dry run) and sends the version it checked with the typed
// "<tile> user:<id>" (--yes types it); backups lists a person's partition's
// versions; bx restore --confirm passes the switch's date, and a restore
// without it sends today's body; unknown flags are errors.
func TestBxPartitionBackups(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b)))
		switch r.URL.Path {
		case "/api/xbin/partitions/backups":
			_ = json.NewEncoder(w).Encode(map[string]any{"partition": "user:alice", "partitionId": "u-1",
				"versions": []map[string]any{{"version": "v002", "time": "t", "size": 9}}})
		case "/api/xbin/partitions/restore":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "partition": "user:alice", "version": "v002"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	if err := cmdRestore([]string{"apps/pk", "--partition", "--yes", "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdBackups([]string{"apps/pk", "--partition", "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdRestore([]string{"apps/pk", "--confirm", "2026-09-30"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdRestore([]string{"apps/pk"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/xbin/partitions/restore {"dryRun":true,"tile":"apps/pk","user":"alice"}`,
		`POST /api/xbin/partitions/restore {"confirm":"apps/pk user:alice","tile":"apps/pk","user":"alice","version":"v002"}`,
		`GET /api/xbin/partitions/backups?tile=apps%2Fpk&user=alice `,
		`POST /api/xbin/restore {"component":"apps/pk","confirm":"2026-09-30","file":"","version":""}`,
		`POST /api/xbin/restore {"component":"apps/pk","file":"","version":""}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}
	for _, args := range [][]string{
		{"apps/pk", "--partition", "--force"},
		{"--partition"},
		{"apps/pk", "--partition", "--user"},
	} {
		if err := cmdRestore(args); err == nil {
			t.Errorf("bx restore %v: no error", args)
		}
	}
	if err := cmdBackups([]string{"apps/pk", "--partition", "--to", "alice"}); err == nil {
		t.Error("bx backups --to: no error")
	}
}

// covers 11§4 — bx backup reports a partitioned tile's people's
// partitions: one not backed up fails it (the tile's archive written all
// the same), a plaintext-vault workspace's skipped ones are a warning;
// bx restore --confirm needs a value, and an xbind that predates it (its
// 400 names the old body) is explained.
func TestBxBackupPartitionsReport(t *testing.T) {
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/xbin/restore" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"need {component, version?, file?}"}`)
			return
		}
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	for _, c := range []struct {
		answer string
		fails  bool
	}{
		{`{"ok":"true","version":"v1"}`, false},
		{`{"ok":"true","version":"v1","partitions":{"archived":2}}`, false},
		{`{"ok":"true","version":"v1","partitions":{"archived":0,"skipped":"2 people's partitions aren't archived"}}`, false},
		{`{"ok":"true","version":"v1","partitions":{"archived":1,"failed":["user:bob (u-1): can't be mounted"]}}`, true},
	} {
		answer = c.answer
		if err := cmdBackup([]string{"apps/pk"}); (err != nil) != c.fails {
			t.Errorf("bx backup answered %s: %v", c.answer, err)
		}
	}
	if err := cmdRestore([]string{"apps/pk", "--confirm"}); err == nil || !strings.Contains(err.Error(), "--confirm needs") {
		t.Errorf("--confirm without a value: %v", err)
	}
	if err := cmdRestore([]string{"apps/pk", "--confirm", "2026-09-30"}); err == nil || !strings.Contains(err.Error(), "predates bx restore --confirm") {
		t.Errorf("an xbind without --confirm: %v", err)
	}
	if err := cmdRestore([]string{"apps/pk"}); err == nil || strings.Contains(err.Error(), "predates") {
		t.Errorf("a plain restore's 400: %v", err)
	}
}
