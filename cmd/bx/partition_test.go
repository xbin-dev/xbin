package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// covers PD-44 06§7 — bx partition keep|switch read the request from the
// tile's /components row and send it back as from/to, so a request that
// changed meanwhile is refused rather than decided blind; switch previews
// (dryRun) before it deletes and sends the typed path; a tile with nothing
// to decide is refused before any act (a row without partition asks the
// route, which an xbind older than partitions lacks: exit 6); unknown flags
// are errors.
func TestBxPartition(t *testing.T) {
	var got []string
	old := false // an xbind older than partitioned tiles
	row := `{"component":{"path":"apps/docs","partition":{"state":"pending","user":false,"global":false,"request":{"user":true,"global":false,"declined":false}}},"apiDoc":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
		switch r.URL.Path {
		case "/api/xbin/components/apps/docs":
			_, _ = io.WriteString(w, row)
		case "/api/xbin/components/apps/plain":
			_, _ = io.WriteString(w, `{"component":{"path":"apps/plain"},"apiDoc":""}`)
		case "/api/xbin/partitions/mode":
			if old {
				http.NotFound(w, r) // Go's mux: no route
				return
			}
			if strings.Contains(string(b), `"tile":"apps/plain"`) {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"apps/plain has no partition mode switch request (it runs unpartitioned)"}`)
				return
			}
			fallthrough
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deletes": "all data in this tile",
				"wiped": map[string]int{"namespaces": 2, "bytes": 4096}, "keeps": []string{"the code"}})
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	if err := partitionCmd([]string{"keep", "apps/docs"}); err != nil {
		t.Fatal(err)
	}
	if err := partitionCmd([]string{"switch", "apps/docs", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if err := partitionCmd([]string{"switch", "--confirm", "apps/docs", "apps/docs", "--yes"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/xbin/components/apps/docs ",
		`POST /api/xbin/partitions/mode {"act":"keep","from":null,"tile":"apps/docs","to":{"user":true,"global":false}}`,
		"GET /api/xbin/components/apps/docs ",
		`POST /api/xbin/partitions/mode {"act":"switch","dryRun":true,"from":null,"tile":"apps/docs","to":{"user":true,"global":false}}`,
		"GET /api/xbin/components/apps/docs ",
		`POST /api/xbin/partitions/mode {"act":"switch","dryRun":true,"from":null,"tile":"apps/docs","to":{"user":true,"global":false}}`,
		`POST /api/xbin/partitions/mode {"act":"switch","confirm":"apps/docs","from":null,"tile":"apps/docs","to":{"user":true,"global":false},"yes":true}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}
	got = nil
	for _, args := range [][]string{
		{},
		{"frobnicate", "apps/docs"}, // an unknown subcommand (stop is F7b's: TestBxPartitionOps)
		{"keep"},
		{"keep", "apps/docs", "--yes"},
		{"switch", "apps/docs", "--force"},
		{"keep", "apps/plain"}, // nothing to decide: refused after the read and the route's dry answer
	} {
		if err := partitionCmd(args); err == nil || errors.Is(err, errNoPartitions) {
			t.Errorf("bx partition %v: %v", args, err)
		}
	}
	if len(got) != 2 || got[0] != "GET /api/xbin/components/apps/plain " ||
		got[1] != `POST /api/xbin/partitions/mode {"act":"keep","dryRun":true,"from":null,"tile":"apps/plain","to":null}` {
		t.Errorf("refusals sent %q", got)
	}
	// an xbind older than partitioned tiles: its rows carry no partition and
	// the route is missing — exit 6 (errNoPartitions), never "nothing to decide"
	old = true
	if err := partitionCmd([]string{"keep", "apps/plain"}); !errors.Is(err, errNoPartitions) {
		t.Errorf("against an older xbind: %v", err)
	}
	old = false
	// a declined request can't be kept again; it can still be switched
	row = strings.Replace(row, `"declined":false`, `"declined":true`, 1)
	if err := partitionCmd([]string{"keep", "apps/docs"}); err == nil || !strings.Contains(err.Error(), "already keeps") {
		t.Errorf("keeping a declined request: %v", err)
	}
}
