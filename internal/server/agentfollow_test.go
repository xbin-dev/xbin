package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
)

// A follow from an idle session's last seq (a client reconnecting after it
// caught up) answers at once: its head does not wait for the agent's next
// event. The iOS app waits for the head before it reads the stream; it
// hung until its 60 s request timeout, holding a connection, every time it
// followed a session that had nothing new to say.
func TestAgentFollowHeadAtOnce(t *testing.T) {
	bx, fake := realAgentBins(t)
	t.Setenv(agent.FakeEnv, fake)
	h, s := impServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := term.NewManager(root, nil)
	m.BxPath = bx
	m.Tokens = s.Auth
	s.Term, s.Hub = m, events.NewHub()
	srv := httptest.NewServer(h)
	defer srv.Close()

	alice := s.Auth.NewSession("alice", "")
	do := func(ctx context.Context, method, path, body string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, method, srv.URL+"/api/xbin"+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: alice})
		return http.DefaultClient.Do(r)
	}
	resp, err := do(context.Background(), "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake"}`)
	if err != nil {
		t.Fatal(err)
	}
	var info struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if resp.StatusCode != 200 || info.ID == "" {
		t.Fatalf("open: %d", resp.StatusCode)
	}
	defer m.Kill(info.ID)

	// Wait until the session has settled: an idle status, nothing more coming.
	var next uint64
	deadline := time.Now().Add(20 * time.Second)
	for {
		evs, n, _, err := m.AgentEvents(info.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		idle := false
		for _, e := range evs {
			if e.Type == agent.EvStatus && strings.Contains(string(e.Data), `"idle"`) {
				idle = true
			}
		}
		if idle {
			next = n
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake agent never went idle: %d events", len(evs))
		}
		time.Sleep(100 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	resp, err = do(ctx, "GET", "/term/sessions/"+info.ID+"/events?follow=1&since="+strconv.FormatUint(next, 10), "")
	if err != nil {
		t.Fatalf("the follow's head did not come (%v after %s): it waits for the next event", err, time.Since(start).Round(time.Millisecond))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("follow: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
