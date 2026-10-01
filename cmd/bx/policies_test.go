package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// bx policies reads GET /workspace-policies and turns one switch with a
// one-key PUT; a bad name or state is refused before any request.
func TestBxPolicies(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": 1, "partitionConsent": true, "credentialResetConfirm": false})
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")

	for _, args := range [][]string{
		{},
		{"--json"},
		{"set", "partition-consent", "on"},
		{"set", "credential-reset-confirm", "off"},
	} {
		if err := cmdPolicies(args); err != nil {
			t.Fatalf("bx policies %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"set", "partition-consent", "maybe"},
		{"set", "nope", "on"},
		{"set", "partition-consent"},
		{"ls", "x"},
		{"get"},
	} {
		if err := cmdPolicies(args); err == nil {
			t.Errorf("bx policies %v: no error", args)
		}
	}
	want := []string{
		"GET /api/xbin/workspace-policies ",
		"GET /api/xbin/workspace-policies ",
		`PUT /api/xbin/workspace-policies {"partitionConsent":true}`,
		`PUT /api/xbin/workspace-policies {"credentialResetConfirm":false}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}

	// D180: the command is an alias of bx settings now — one line on stderr
	// says so, stdout stays the answer
	r, w, _ := os.Pipe()
	stderr := os.Stderr
	os.Stderr = w
	err := moreCmds["policies"]([]string{"--json"})
	os.Stderr = stderr
	w.Close()
	note, _ := io.ReadAll(r)
	if err != nil || strings.Count(string(note), "\n") != 1 || !strings.Contains(string(note), "bx settings set partition-consent") {
		t.Fatalf("the alias's note: %q (%v)", note, err)
	}
}
