package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// covers PD-13 06§7 — bx partition consent <from> <to> [--revoke] sends the
// edge to POST/DELETE /partitions/consents, consent ls and ledger read
// theirs; the route's refusal is the error; an xbind without the routes
// exits 6. bx grants reads pending rows whatever else they carry (approvers
// is a list) and shows the approval warning; bx grant prints it after
// approving.
func TestBxPartitionConsent(t *testing.T) {
	var got []string
	old := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b)))
		switch {
		case old && strings.HasPrefix(r.URL.Path, "/api/xbin/partitions/"):
			http.NotFound(w, r) // Go's mux: no route
		case r.URL.Path == "/api/xbin/partitions/consents" && strings.Contains(string(b), `"to":"apps/x"`):
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"apps/x doesn't keep each person's data apart: there is nothing to allow"}`)
		case r.URL.Path == "/api/xbin/partitions/consents":
			_, _ = io.WriteString(w, `{"policy":{"partitionConsent":true},"consents":[{"from":"apps/q","to":"apps/pg","at":"2026-09-30T12:00:00Z","via":"session"}],"asked":[]}`)
		case r.URL.Path == "/api/xbin/partitions/ledger":
			_, _ = io.WriteString(w, `{"days":7,"rows":[{"tile":"apps/q","day":"2026-09-30","kind":"edge","target":"apps/pg","count":3}]}`)
		case r.URL.Path == "/api/xbin/grants":
			_, _ = io.WriteString(w, `{"grants":[{"from":"apps/x","target":"apps/pg","role":"reader","approvedBy":"owner","approvedAt":1727700000}],"pending":[{"from":"apps/q","target":"apps/pg","role":"reader","approvers":["workspace-admin"],`+
				`"warning":"apps/q's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who can read apps/pg"}]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	for _, args := range [][]string{
		{"consent", "apps/q", "apps/pg"},
		{"consent", "apps/q", "apps/pg", "--revoke"},
		{"consent", "ls"},
		{"ledger", "apps/q", "--days", "7"},
	} {
		if err := partitionCmd(args); err != nil {
			t.Fatalf("bx partition %v: %v", args, err)
		}
	}
	want := []string{
		`POST /api/xbin/partitions/consents {"from":"apps/q","to":"apps/pg"}`,
		`DELETE /api/xbin/partitions/consents {"from":"apps/q","to":"apps/pg"}`,
		"GET /api/xbin/partitions/consents ",
		"GET /api/xbin/partitions/ledger?days=7&tile=apps%2Fq ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}
	got = nil
	if err := partitionCmd([]string{"consent", "apps/q", "apps/x"}); err == nil || !strings.Contains(err.Error(), "nothing to allow") {
		t.Errorf("a refused consent: %v", err)
	}
	for _, args := range [][]string{{"consent"}, {"consent", "apps/q"}, {"consent", "apps/q", "apps/q"}, {"consent", "a", "b", "--force"}, {"ledger", "--days", "0"}} {
		if err := partitionCmd(args); err == nil || errors.Is(err, errNoPartitions) {
			t.Errorf("bx partition %v: %v", args, err)
		}
	}
	old = true
	if err := partitionCmd([]string{"consent", "ls"}); !errors.Is(err, errNoPartitions) {
		t.Errorf("against an older xbind: %v", err)
	}
	old = false

	// bx grants decodes rows that carry approvedAt (a number) and approvers
	// (a list); bx grant warns
	if err := cmdGrants(); err != nil {
		t.Errorf("bx grants with approvedAt and approvers: %v", err)
	}
	if w := grantWarning("apps/q", "apps/pg", "reader"); !strings.Contains(w, "every person who can read apps/pg") {
		t.Errorf("bx grant's warning: %q", w)
	}
	if w := grantWarning("apps/q", "apps/pg", "writer"); w != "" {
		t.Errorf("another role's warning: %q", w)
	}
}
