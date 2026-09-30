package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// covers 05§2 S1 — bx grant prints the approval warning POST /grants
// answers for a partitioned tile's grant on another's people's data, on
// stderr (stdout stays today's "ok"); an answer without one prints nothing
// more.
func TestBxGrantWarning(t *testing.T) {
	warn := "apps/q's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who can read apps/pg"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method == "POST" && strings.Contains(string(b), `"target":"apps/pg"`) {
			_, _ = io.WriteString(w, `{"ok":"true","warning":"`+warn+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":"true"}`)
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	for _, c := range []struct {
		args    []string
		errText string
	}{
		{[]string{"apps/q", "apps/pg:reader"}, "⚠ " + warn + "\n"},
		{[]string{"apps/q", "apps/x:reader"}, ""},
		{[]string{"--revoke", "apps/q", "apps/pg:reader"}, ""},
	} {
		var err error
		stderr := captureStderrW2(t, func() {
			out := captureStdoutF10(t, func() { err = cmdGrant(c.args) })
			if out != "ok\n" {
				t.Errorf("bx grant %v printed %q on stdout", c.args, out)
			}
		})
		if err != nil || stderr != c.errText {
			t.Errorf("bx grant %v: %v, stderr %q, want %q", c.args, err, stderr, c.errText)
		}
	}
}

// captureStderrW2 runs fn with os.Stderr redirected, returning what it wrote.
func captureStderrW2(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	os.Stderr = old
	_ = w.Close()
	return <-done
}
