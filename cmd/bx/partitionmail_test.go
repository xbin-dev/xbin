package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// covers 06§7 — bx partition mail ls|ack speaks the partition mail routes
// with the credential it runs with (a person's terminal: their partition's
// inbox): ls pages with --after/--limit, ack sends the ids; a refusal is the
// route's own error; an xbind without the routes exits 6.
func TestBxPartitionMail(t *testing.T) {
	var got []string
	old := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b)))
		switch {
		case old:
			http.NotFound(w, r) // Go's mux: no route
		case r.Header.Get("Authorization") == "Bearer admin":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"partition mail is sent and read only by a partitioned tile's global instance"}`)
		case r.Method == "GET":
			_, _ = io.WriteString(w, `{"items":[{"id":"0123456789abcdef01234567","from":"global","topic":"dm","data":{"text":"hi"},`+
				`"at":"2026-09-30T10:00:00Z","expires":"2026-10-07T10:00:00Z"}],"more":true}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	for _, args := range [][]string{
		{"mail", "ls"},
		{"mail", "ls", "--after", "0123456789abcdef01234567", "--limit", "5", "--json"},
		{"mail", "ack", "0123456789abcdef01234567", "0123456789abcdef01234568"},
	} {
		if err := partitionCmd(args); err != nil {
			t.Fatalf("bx partition %v: %v", args, err)
		}
	}
	want := []string{
		`GET /api/xbin/partitions/mail `,
		`GET /api/xbin/partitions/mail?after=0123456789abcdef01234567&limit=5 `,
		`POST /api/xbin/partitions/mail/ack {"ids":["0123456789abcdef01234567","0123456789abcdef01234568"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var out bytes.Buffer
	printMailLs(&out, []mailLsItem{{ID: "0123456789abcdef01234567", From: "global", Topic: "dm", Data: []byte(`{"text":"hi"}`),
		At: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), Expires: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}}, true)
	if s := out.String(); !strings.Contains(s, "global") || !strings.Contains(s, `{"text":"hi"}`) ||
		!strings.Contains(s, "bx partition mail ls --after 0123456789abcdef01234567") {
		t.Errorf("ls:\n%s", s)
	}
	for _, args := range [][]string{{"mail"}, {"mail", "rm"}, {"mail", "ack"}, {"mail", "ls", "--limit", "x"}} {
		if err := partitionCmd(args); err == nil {
			t.Errorf("bx partition %v: no error", args)
		}
	}
	t.Setenv("XBIN_TOKEN", "admin")
	if err := partitionCmd([]string{"mail", "ls"}); err == nil || !strings.Contains(err.Error(), "read only by") || errors.Is(err, errNoPartitions) {
		t.Errorf("an admin's ls: %v", err)
	}
	old = true
	if err := partitionCmd([]string{"mail", "ls"}); !errors.Is(err, errNoPartitions) || !strings.Contains(err.Error(), "partition-mail/1") {
		t.Errorf("an older xbind: %v", err)
	}
}
