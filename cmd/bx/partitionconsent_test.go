package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// covers PD-13 06§7 — bx partition consent <from> <to> [--revoke] sends the
// edge to POST/DELETE /partitions/consents, consent ls and ledger read
// theirs; the route's refusal is the error; an xbind without the routes
// exits 6. bx grants reads rows whatever else they carry (approvedAt is a
// number, approvers a list) and shows the approval warning.
func TestBxPartitionConsent(t *testing.T) {
	var got []string
	old, revoked := false, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b)))
		switch {
		case old && strings.HasPrefix(r.URL.Path, "/api/xbin/partitions/"):
			http.NotFound(w, r) // Go's mux: no route
		case r.URL.Path == "/api/xbin/partitions/consents" && strings.Contains(string(b), `"to":"apps/x"`):
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"apps/x doesn't keep each person's data apart: there is nothing to allow"}`)
		case r.URL.Path == "/api/xbin/partitions/consents" && r.Method == "DELETE":
			_, _ = io.WriteString(w, `{"policy":{"partitionConsent":true},"consents":[],"asked":[],"revoked":`+strconv.FormatBool(revoked)+`}`)
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
	if err := partitionCmd([]string{"consent", "ls"}); !errors.Is(err, errNoPartitions) ||
		!strings.Contains(err.Error(), "no GET /api/xbin/partitions/consents") || strings.Contains(err.Error(), "no partitioned tiles") {
		t.Errorf("against an older xbind: %v", err)
	}
	old = false

	// revoking says whether there was a consent to take back (the answer's
	// revoked), never that something was stopped when nothing was
	revoked = false
	out := captureStdoutF10(t, func() {
		if err := partitionCmd([]string{"consent", "apps/q", "apps/pg", "--revoke"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "nothing to take back") || strings.Contains(out, "stopped") {
		t.Errorf("revoking what wasn't given printed %q", out)
	}
	revoked = true
	if out := captureStdoutF10(t, func() { _ = partitionCmd([]string{"consent", "apps/q", "apps/pg", "--revoke"}) }); !strings.Contains(out, "can no longer use your data") {
		t.Errorf("revoking printed %q", out)
	}

	// bx grants decodes rows that carry approvedAt (a number) and approvers
	// (a list), and prints the warning
	out = captureStdoutF10(t, func() {
		if err := cmdGrants(); err != nil {
			t.Errorf("bx grants with approvedAt and approvers: %v", err)
		}
	})
	if !strings.Contains(out, "⚠ apps/q's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who can read apps/pg") ||
		!strings.Contains(out, "apps/x") {
		t.Errorf("bx grants printed %q", out)
	}
}

// captureStdoutF10 runs fn with os.Stdout redirected, returning what it wrote.
func captureStdoutF10(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}
