package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// setMode runs the rest of the test as a process of mode m (user: its
// person) and restores the legacy mode — and every hook a mode sets — after.
func setMode(t *testing.T, m agentMode, user string) {
	t.Helper()
	oldMode, oldUser := runMode, runUser
	oldIn, oldOut, oldSlots := confIn, confOut, slots
	oldTeam := teamStore.Load()
	oldClasses, oldRaw := classStore.Load(), confClassesRaw.Load()
	runMode, runUser = m, user
	pidMu.Lock()
	oldPID := pidSeen
	pidSeen = ""
	pidMu.Unlock()
	t.Cleanup(func() {
		runMode, runUser = oldMode, oldUser
		confIn, confOut, slots = oldIn, oldOut, oldSlots
		teamStore.Store(oldTeam)
		classStore.Store(oldClasses)
		confClassesRaw.Store(oldRaw)
		pidMu.Lock()
		pidSeen = oldPID
		pidMu.Unlock()
	})
}

func TestModeOf(t *testing.T) {
	for _, c := range []struct {
		in   string
		mode agentMode
		user string
		bad  bool
	}{
		{"", modeLegacy, "", false},
		{"global", modeGlobal, "", false},
		{"user:alice", modeUser, "alice", false},
		{"user:a.b-c_d@example.org", modeUser, "a.b-c_d@example.org", false},
		{"user:", modeLegacy, "", true},
		{"user:al ice", modeLegacy, "", true},
		{"org:acme", modeLegacy, "", true}, // a kind this code doesn't know: refuse, never serve everyone
		{"Global", modeLegacy, "", true},
	} {
		m, u, err := modeOf(c.in)
		if m != c.mode || u != c.user || (err != nil) != c.bad {
			t.Errorf("modeOf(%q) = %v %q %v, want %v %q bad=%v", c.in, m, u, err, c.mode, c.user, c.bad)
		}
	}
}

func TestDetectMode(t *testing.T) {
	setMode(t, modeLegacy, "")
	t.Setenv("XBIN_PARTITION", "user:bob")
	if err := detectMode(); err != nil || !userMode() || runUser != "bob" || partitionKey() != "user:bob" {
		t.Fatalf("user:bob → %v %q %v", runMode, runUser, err)
	}
	t.Setenv("XBIN_PARTITION", "global")
	if err := detectMode(); err != nil || !globalMode() || !partitioned() || partitionKey() != "global" || agentHome() != "global" {
		t.Fatalf("global → %v %v", runMode, err)
	}
	t.Setenv("XBIN_PARTITION", "")
	if err := detectMode(); err != nil || partitioned() || partitionKey() != "" || agentHome() != "" {
		t.Fatalf("unset → %v %v", runMode, err)
	}
	t.Setenv("XBIN_PARTITION", "team:x")
	if err := detectMode(); err == nil {
		t.Fatal("an unknown partition kind was accepted")
	}
}

// TestLegacyModeUnchanged is the legacy golden's guard: with XBIN_PARTITION
// unset nothing partition-shaped is wired — no conf, team or slots, the
// route gate is today's admin role, no route is rewritten, and no id is
// learned. (Every other test in this package runs in legacy mode too: that
// they pass unchanged is the golden itself.)
func TestLegacyModeUnchanged(t *testing.T) {
	setMode(t, modeLegacy, "")
	startMode(newTestDB(t))
	if confIn != nil || confOut != nil || slots != nil || teamStore.Load() != nil {
		t.Fatalf("legacy mode wired partition plumbing: in=%v out=%v slots=%v team=%v", confIn, confOut, slots, teamStore.Load())
	}
	h := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }
	for pattern := range userRoutes {
		if got := partitionRoute(pattern, h); reflect.ValueOf(got).Pointer() != reflect.ValueOf(h).Pointer() {
			t.Errorf("legacy mode rewrote %s", pattern)
		}
	}
	// the gate: an attributed-looking call without the admin role is refused, as ever
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("X-XBin-From", "apps/agent")
	req.Header.Set("X-XBin-User", "alice")
	req.Header.Set("X-XBin-Role", "writer")
	req.Header.Set("X-XBin-Partition", "user:alice")
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	agentRole(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("legacy gate let a writer through: %d", rec.Code)
	}
	req.Header.Set("X-XBin-Partition-Id", "u-0123456789abcdef0123456789abcdef")
	notePartitionID(req)
	if partitionID() != "" || agentHome() != "" {
		t.Errorf("legacy mode learned a partition id %q", partitionID())
	}
	if _, ok := confSetting("config"); ok {
		t.Error("legacy mode reads settings from conf")
	}
	if err := confRefuses("config"); err != nil {
		t.Errorf("legacy mode refuses a settings write: %v", err)
	}
	if gateLimit(Config{MaxActiveRuns: 6}) != 6 {
		t.Error("legacy mode caps its gate")
	}
}

func TestNotePartitionID(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	setMode(t, modeUser, "alice")
	ag := newTestAgent(t, newTestDB(t))
	old := agent
	agent = ag
	t.Cleanup(func() { agent = old })
	if agentHome() != "user:alice" {
		t.Fatalf("before any call: home %q, want the partition key", agentHome())
	}
	call := func(part, id string) {
		req := httptest.NewRequest("GET", "/me", nil)
		req.Header.Set("X-XBin-Partition", part)
		req.Header.Set("X-XBin-Partition-Id", id)
		notePartitionID(req)
	}
	call("user:bob", "u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") // not this partition's: ignored
	if partitionID() != "" {
		t.Fatalf("learned another partition's id %q", partitionID())
	}
	call("user:alice", "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if partitionID() != "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || agentHome() != "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("id %q home %q", partitionID(), agentHome())
	}
	// kept across a restart: a resumed run labels its sandboxes before any call
	pidMu.Lock()
	pidSeen = ""
	pidMu.Unlock()
	if partitionID() != "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("the id didn't survive: %q", partitionID())
	}
}
