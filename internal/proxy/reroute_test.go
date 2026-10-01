package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGen is a generation for rerouting: a socket, and whether it is retired.
type fakeGen struct {
	sock    string
	retired bool
}

func (g fakeGen) Sock() string  { return g.sock }
func (g fakeGen) Retired() bool { return g.retired }

// answering is a generation's socket that answers every request with name.
func answering(t *testing.T, name string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), name+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, name)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// cutting is a generation's socket that takes each connection and closes it
// unanswered: what a request meets when the generation's process ends with
// the request in its listen backlog, or before it answered.
func cutting(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "cut.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return sock
}

// gone is a generation's socket nobody listens on any more: a dial after
// its process closed it fails, and nothing of the request was sent.
func gone(t *testing.T) string { return filepath.Join(t.TempDir(), "gone.sock") }

// covers D173 — a request the proxy routed to a generation that xbind then
// retired (a swap replaced it, and its SIGTERM cut the request off before
// any answer) goes to the deployment's generation now, when resending it is
// safe: no body, and an idempotent method or nothing sent (a failed dial).
// Anything else, and any failure of a generation that wasn't retired (a
// crash), answers 502 as before, without asking for another generation.
func TestRerouteRetiredGeneration(t *testing.T) {
	next := answering(t, "g2")
	for _, tc := range []struct {
		name     string
		method   string
		body     string
		gen      fakeGen
		again    []generation // what each call of again answers, in order
		againErr error
		want     string // the answer's body, or "502" plus a substring of the error
		asked    int    // calls of again
	}{
		{name: "GET cut off by the retirement goes to the new generation",
			method: "GET", gen: fakeGen{cutting(t), true}, again: []generation{fakeGen{next, false}}, want: "g2", asked: 1},
		{name: "OPTIONS is idempotent too",
			method: "OPTIONS", gen: fakeGen{cutting(t), true}, again: []generation{fakeGen{next, false}}, want: "g2", asked: 1},
		{name: "a failed dial of a retired generation sent nothing: any method",
			method: "POST", gen: fakeGen{gone(t), true}, again: []generation{fakeGen{next, false}}, want: "g2", asked: 1},
		{name: "a crash is no retirement",
			method: "GET", gen: fakeGen{cutting(t), false}, again: []generation{fakeGen{next, false}}, want: "502 backend error", asked: 0},
		{name: "a POST may have reached the retired generation",
			method: "POST", gen: fakeGen{cutting(t), true}, again: []generation{fakeGen{next, false}}, want: "502 backend error", asked: 0},
		{name: "a body went to the retired generation",
			method: "PUT", body: "x", gen: fakeGen{gone(t), true}, again: []generation{fakeGen{next, false}}, want: "502 gone.sock", asked: 0},
		{name: "a generation retired in turn: the request follows again",
			method: "GET", gen: fakeGen{cutting(t), true},
			again: []generation{fakeGen{cutting(t), true}, fakeGen{next, false}}, want: "g2", asked: 2},
		{name: "the deployment can't answer now: its reason",
			method: "GET", gen: fakeGen{cutting(t), true}, againErr: errors.New("component apps/x is not enabled"),
			want: "502 component apps/x is not enabled", asked: 1},
		{name: "a deployment that never stops swapping: bounded",
			method: "GET", gen: fakeGen{cutting(t), true},
			again: []generation{fakeGen{cutting(t), true}, fakeGen{cutting(t), true}, fakeGen{cutting(t), true}, fakeGen{next, false}},
			want:  "502 backend error", asked: maxReroutes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			px := &Proxy{}
			var asked atomic.Int64
			tr := &rerouting{px: px, gen: tc.gen, again: func() (generation, error) {
				n := asked.Add(1)
				if tc.againErr != nil {
					return nil, tc.againErr
				}
				return tc.again[n-1], nil
			}}
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			w := httptest.NewRecorder()
			px.forward(w, httptest.NewRequest(tc.method, "/api/apps/x/v", body), tr, "v", "", nil)
			got := w.Body.String()
			if w.Code != http.StatusOK {
				got = fmt.Sprint(w.Code) + " " + got
			}
			if want, ok := strings.CutPrefix(tc.want, "502 "); ok {
				if w.Code != http.StatusBadGateway || !strings.Contains(got, want) {
					t.Errorf("answer %q, want a 502 naming %q", got, want)
				}
			} else if got != tc.want {
				t.Errorf("answer %q, want %q", got, tc.want)
			}
			if n := int(asked.Load()); n != tc.asked {
				t.Errorf("asked for the deployment's generation %d times, want %d", n, tc.asked)
			}
		})
	}
}
