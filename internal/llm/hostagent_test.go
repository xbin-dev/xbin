package llm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The host's workspaceAgent key: a bool or an object, read strictly.
func TestHostAgentKey(t *testing.T) {
	ok := map[string]HostAgent{
		`null`:                                 {},
		`false`:                                {Set: true},
		`true`:                                 {Set: true, Enabled: true},
		`{"enabled": false}`:                   {Set: true},
		`{"enabled": true}`:                    {Set: true, Enabled: true},
		`{"enabled": true, "who": "admins"}`:   {Set: true, Enabled: true, Who: "admins"},
		`{"enabled": true, "who": "everyone"}`: {Set: true, Enabled: true, Who: "everyone"},
		` {"who": "admins", "enabled": true} `: {Set: true, Enabled: true, Who: "admins"},
	}
	for in, want := range ok {
		var h HostAgent
		if err := json.Unmarshal([]byte(in), &h); err != nil || h != want {
			t.Errorf("%s: %+v %v, want %+v", in, h, err, want)
		}
	}
	for _, in := range []string{`"on"`, `1`, `{}`, `{"who": "admins"}`, `{"enabled": true, "who": "me"}`, `{"enabled": true, "who": "chosen"}`,
		`{"enabled": false, "who": "everyone"}`, `{"enabled": true, "whom": "admins"}`, `{"enabled": "yes"}`, `[]`} {
		var h HostAgent
		if err := json.Unmarshal([]byte(in), &h); err == nil {
			t.Errorf("%s: want an error, got %+v", in, h)
		}
	}
}

// In the host's file: read with the rest, an invalid one stops the load,
// and a change is seen as the connector sees one.
func TestHostAgentInHostFile(t *testing.T) {
	dir := t.TempDir()
	p := writeHost(t, dir, `{
	  // the connector's settings, and beside them
	  "workspaceAgent": {"enabled": true, "who": "admins"}
	}`, "")
	h, err := loadHost(p)
	if err != nil || h.WorkspaceAgent != (HostAgent{Set: true, Enabled: true, Who: "admins"}) {
		t.Fatalf("loadHost: %+v %v", h, err)
	}
	if _, err := loadHost(writeHost(t, dir, `{"workspaceAgent": {"enabled": true, "who": "people"}}`, "")); err == nil || !strings.Contains(err.Error(), "workspaceAgent") {
		t.Fatalf("a bad key stops the file: %v", err)
	}
	if h, err := loadHost(writeHost(t, dir, `{}`, "")); err != nil || h.WorkspaceAgent.Set {
		t.Fatalf("no key: %+v %v", h, err)
	}

	now := time.Now()
	writeHost(t, dir, `{"workspaceAgent": false}`, "")
	changed := make(chan struct{}, 4)
	s, err := New(Options{Dir: t.TempDir(), HostFile: p, Now: func() time.Time { return now }, HostChanged: func() { changed <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true}) {
		t.Fatalf("in force: %+v", got)
	}
	writeHost(t, dir, `{"workspaceAgent": {"enabled": true, "who": "everyone"}}`, "")
	future := now.Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	now = now.Add(hostCheckGap + time.Second)
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true, Enabled: true, Who: "everyone"}) {
		t.Fatalf("reloaded: %+v", got)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("HostChanged was not called after the reload")
	}
	// a changed file that is invalid is refused: the last good one stays
	writeHost(t, dir, `{"workspaceAgent": "yes"}`, "")
	later := future.Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	now = now.Add(hostCheckGap + time.Second)
	if got := s.HostWorkspaceAgent(); got != (HostAgent{Set: true, Enabled: true, Who: "everyone"}) {
		t.Fatalf("a refused reload keeps the last good one: %+v", got)
	}
}
