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

// covers PD-44 06§7 — bx partition keep|switch read the request from the
// tile's /components row and send it back as from/to, so a request that
// changed meanwhile is refused rather than decided blind; switch previews
// (dryRun) before it deletes and sends the typed path; a tile with nothing
// to decide is refused before any act; unknown flags are errors.
func TestBxPartition(t *testing.T) {
	var got []string
	row := `{"component":{"path":"apps/docs","partition":{"state":"pending","user":false,"global":false,"request":{"user":true,"global":false,"declined":false}}},"apiDoc":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(b)))
		switch r.URL.Path {
		case "/api/xbin/components/apps/docs":
			_, _ = io.WriteString(w, row)
		case "/api/xbin/components/apps/plain":
			_, _ = io.WriteString(w, `{"component":{"path":"apps/plain"},"apiDoc":""}`)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deletes": "all data in this tile",
				"wiped": map[string]int{"namespaces": 2, "bytes": 4096}, "keeps": []string{"the code"}})
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	if err := cmdPartition([]string{"keep", "apps/docs"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdPartition([]string{"switch", "apps/docs", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdPartition([]string{"switch", "--confirm", "apps/docs", "apps/docs", "--yes"}); err != nil {
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
		{"stop", "apps/docs"},
		{"keep"},
		{"keep", "apps/docs", "--yes"},
		{"switch", "apps/docs", "--force"},
		{"keep", "apps/plain"}, // nothing to decide: refused after the read alone
	} {
		if err := cmdPartition(args); err == nil {
			t.Errorf("bx partition %v: no error", args)
		}
	}
	if len(got) != 1 || got[0] != "GET /api/xbin/components/apps/plain " {
		t.Errorf("refusals sent %q", got)
	}
	// a declined request can't be kept again; it can still be switched
	row = strings.Replace(row, `"declined":false`, `"declined":true`, 1)
	if err := cmdPartition([]string{"keep", "apps/docs"}); err == nil || !strings.Contains(err.Error(), "already keeps") {
		t.Errorf("keeping a declined request: %v", err)
	}
}
