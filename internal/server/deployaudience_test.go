package server

import (
	"slices"
	"sort"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
)

// audienceServer is a server over a users store for the deployments event
// filter: alice an admin; on apps/crm wanda writes, rita reads, terry has
// terminal level; nora has no access at all.
func audienceServer(t *testing.T) (*Server, *users.Store) {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "wanda", Tiles: map[string]string{"apps/crm": users.LevelWrite}},
		{ID: "rita", Tiles: map[string]string{"apps/crm": users.LevelRead}},
		{ID: "terry", Tiles: map[string]string{"apps/crm": users.LevelTerminal}},
		{ID: "nora"},
	} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	return &Server{Auth: a}, st
}

// audiencePrincipals builds the subscribers as auth builds them from their
// credentials: humans carry their user and access, element principals the
// access of the user they carry. dev* and staging* are bound to non-primary
// deployments (the claim that WP-32 mints); every other tile principal to
// main.
func audiencePrincipals(t *testing.T, st *users.Store) map[string]auth.Principal {
	t.Helper()
	human := func(id string) auth.Principal {
		u, ok := st.Get(id)
		if !ok {
			t.Fatalf("no user %s", id)
		}
		acc, _ := st.Access(id)
		return auth.Principal{UserID: id, User: u, Access: acc, Via: "session"}
	}
	elem := func(tile, via, uid, dep string) auth.Principal {
		p := auth.Principal{Component: tile, UserID: uid, Via: via, Deployment: dep}
		if uid != "" {
			p.Access, _ = st.Access(uid)
		}
		return p
	}
	return map[string]auth.Principal{
		"owner":       {Owner: true, Via: "bearer"},
		"alice":       human("alice"),
		"wanda":       human("wanda"),
		"rita":        human("rita"),
		"terry":       human("terry"),
		"nora":        human("nora"),
		"termT":       elem("apps/crm", "terminal", "terry", ""), // terry's shell or agent session on the tile
		"termOwner":   elem("apps/crm", "terminal", "", ""),      // the bootstrap token's
		"termR":       elem("apps/crm", "terminal", "rita", ""),  // a session whose driver is down to read
		"frameR":      elem("apps/crm", "frame", "rita", ""),     // the primary's frame tokens
		"frameW":      elem("apps/crm", "frame", "wanda", ""),
		"frameOwner":  elem("apps/crm", "frame", "", ""),
		"inst":        elem("apps/crm", "instance", "", ""),
		"otherFrameW": elem("apps/other", "frame", "wanda", ""), // other tiles' principals
		"otherTermA":  elem("apps/other", "terminal", "alice", ""),
		"otherInst":   elem("apps/other", "instance", "", ""),
		"devFrameW":   elem("apps/crm", "frame", "wanda", "dev"), // dev's own principals
		"devInst":     elem("apps/crm", "instance", "", "dev"),
		"devFrameR":   elem("apps/crm", "frame", "rita", "dev"), // its user no longer writes
		"stagingInst": elem("apps/crm", "instance", "", "staging"),
		"otherDevFrm": elem("apps/other", "frame", "wanda", "dev"), // another tile's dev
	}
}

// receivers lists, sorted, the principals whose filter passes e.
func receivers(s *Server, ps map[string]auth.Principal, e events.Event) []string {
	var got []string
	for name, p := range ps {
		if s.eventFilter(p)(e) {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	return got
}

func union(sets ...[]string) []string {
	var out []string
	for _, s := range sets {
		for _, n := range s {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func minus(a, b []string) []string {
	var out []string
	for _, n := range a {
		if !slices.Contains(b, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// covers T7 D127h SC-EVENTS PO-5 — the deployments event reaches the audience
// 11-contract §3.4 gives each form, on the pr precedent: a fact about the
// primary reaches the tile's readers (a reader form only those the full form
// misses); the full forms of record and deploy, and work-tree, reach the
// write audience (admins, humans with write, the tile's terminal and agent
// sessions while their driver writes); a deploy onto the primary also
// reaches the own principals of the deployment it came from; anything naming
// a non-primary deployment reaches only the write audience and that
// deployment's own principals. The primary's frame tokens never get a full
// form or a non-primary fact, other tiles' principals never a non-primary
// fact, and an event the filter can't read, or a reader form carrying any
// other key, reaches the write audience alone. Today's types keep today's
// delivery.
func TestDeploymentEventsFiltered(t *testing.T) {
	s, st := audienceServer(t)
	ps := audiencePrincipals(t, st)
	W := []string{"owner", "alice", "wanda", "terry", "termT", "termOwner"}
	R := union(W, []string{"rita", "termR", "frameR", "frameW", "frameOwner", "inst",
		"otherFrameW", "otherTermA", "otherDevFrm", "devFrameW", "devInst", "devFrameR", "stagingInst"})
	devOwn := []string{"devFrameW", "devInst"}
	everyone := make([]string, 0, len(ps))
	for n := range ps {
		everyone = append(everyone, n)
	}
	crm := func(data any) events.Event {
		return events.Event{Type: "deployments", Component: "apps/crm", Data: data}
	}
	type m = map[string]any
	for _, tc := range []struct {
		name string
		e    events.Event
		want []string
	}{
		// the primary's rows
		{"record, full form", crm(m{"op": "record", "seq": 19, "by": "user:ana", "session": "s1",
			"what": []string{"liveReload", "primary"}}), W},
		{"record, reader form", crm(m{"op": "record", "what": []string{"liveReload"}}), minus(R, W)},
		{"deploy onto the primary from dev, full form", crm(m{"op": "deploy", "id": 43, "deployment": "main",
			"how": "promote", "from": "dev", "checkpoint": "c:3f2a1c9", "result": "running", "phase": "build",
			"by": "user:ana", "session": "s1"}), union(W, devOwn)},
		{"reload now, full form naming the primary as from", crm(m{"op": "deploy", "id": 46, "deployment": "main",
			"how": "reload-now", "from": "main", "checkpoint": "c:7b19e02", "result": "running", "phase": "build",
			"by": "user:ana"}), W},
		{"roll back the primary, full form", crm(m{"op": "deploy", "id": 44, "deployment": "main",
			"how": "rollback", "checkpoint": "c:77aa01b", "result": "ok", "phase": "swap", "by": "user:ana"}), W},
		{"deploy onto the primary, reader form", crm(m{"op": "deploy", "deployment": "main",
			"checkpoint": "c:3f2a1c9", "result": "running", "phase": "build", "by": "user:ana"}), minus(R, W)},
		{"work-tree", crm(m{"op": "work-tree", "changed": 3}), W},
		{"the primary's data op", crm(m{"op": "data", "deployment": "main", "busy": "restoring", "state": "original"}), R},
		// non-primary rows, keyed on the principals' bound deployment
		{"reload of dev", crm(m{"op": "reload", "deployment": "dev"}), union(W, devOwn)},
		{"build of dev, with compiler output", crm(m{"op": "build", "deployment": "dev", "phase": "error",
			"text": "x.go:1: nope"}), union(W, devOwn)},
		{"deploy onto dev", crm(m{"op": "deploy", "id": 45, "deployment": "dev", "how": "deploy",
			"checkpoint": "c:1111111", "result": "ok", "phase": "swap", "by": "user:ana"}), union(W, devOwn)},
		{"deploy onto dev, shaped like a reader form", crm(m{"op": "deploy", "deployment": "dev",
			"checkpoint": "c:1111111", "result": "ok", "phase": "swap", "by": "user:ana"}), union(W, devOwn)},
		{"data of dev", crm(m{"op": "data", "deployment": "dev", "busy": "", "state": "seeded"}), union(W, devOwn)},
		{"status of dev", crm(m{"op": "status", "deployment": "dev", "level": "error", "message": "db down",
			"ts": 1790000000, "transient": false}), union(W, devOwn)},
		{"would notify, dev", crm(m{"op": "notify", "deployment": "dev", "to": "user:bob", "title": "hi",
			"at": "2026-09-27T10:00:00Z"}), union(W, devOwn)},
		{"the runner's struct data for dev", crm(struct {
			Op         string `json:"op"`
			Deployment string `json:"deployment"`
		}{"reload", "dev"}), union(W, devOwn)},
		{"another tile's dev", events.Event{Type: "deployments", Component: "apps/other",
			Data: m{"op": "reload", "deployment": "dev"}}, []string{"alice", "otherTermA", "owner"}},
		// fail closed
		{"an unknown op about the primary", crm(m{"op": "future", "deployment": "main"}), W},
		{"a record reader form with another key", crm(m{"op": "record", "what": []string{"deployments"},
			"names": []string{"dev"}}), W},
		{"a deploy reader form naming from", crm(m{"op": "deploy", "deployment": "main", "from": "dev",
			"checkpoint": "c:3f2a1c9", "result": "ok", "phase": "swap", "by": "user:ana"}), union(W, devOwn)},
		{"no deployment", crm(m{"op": "reload"}), W},
		{"data that isn't an object", crm("apps/crm+dev"), W},
		{"no data", crm(nil), W},
		// today's types: unchanged
		{"reload", events.Event{Type: "reload", Component: "apps/crm"}, everyone},
		{"build-error", events.Event{Type: "build-error", Component: "apps/crm", Text: "x.go:1: nope"}, everyone},
		{"status", events.Event{Type: "status", Component: "apps/crm", Data: m{"level": "warn"}}, everyone},
	} {
		sort.Strings(tc.want)
		if got := receivers(s, ps, tc.e); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n  got  %v\n  want %v\n  missing %v, extra %v", tc.name, got, tc.want,
				minus(tc.want, got), minus(got, tc.want))
		}
	}
}

// reassigned is a Policy whose apps/crm primary is blue.
type reassigned struct{ NoopPolicy }

func (reassigned) Primary(tile string) string {
	if tile == "apps/crm" {
		return "blue"
	}
	return ""
}

// covers T7 D127h D127j — the filter keys on the tile's primary as the Policy
// names it (PrimaryPolicy), not on the name main: once blue is the primary,
// a reader form of a deploy onto blue reaches readers, and a fact about main
// reaches only the write audience and main's own principals, the claimless
// frame and instance tokens whose user, if any, writes (the name rule).
func TestDeploymentEventsFilteredReassignedPrimary(t *testing.T) {
	s, st := audienceServer(t)
	s.InstallPolicy(reassigned{})
	ps := audiencePrincipals(t, st)
	W := []string{"owner", "alice", "wanda", "terry", "termT", "termOwner"}
	crm := func(data any) events.Event {
		return events.Event{Type: "deployments", Component: "apps/crm", Data: data}
	}
	type m = map[string]any
	readers := receivers(s, ps, crm(m{"op": "record", "what": []string{"primary"}}))
	for _, tc := range []struct {
		name string
		e    events.Event
		want []string
	}{
		{"deploy onto blue, reader form", crm(m{"op": "deploy", "deployment": "blue", "checkpoint": "c:2222222",
			"result": "ok", "phase": "swap", "by": "user:ana"}), readers},
		{"blue's data op", crm(m{"op": "data", "deployment": "blue", "state": "original"}), union(readers, W)},
		{"main's data op", crm(m{"op": "data", "deployment": "main", "state": "seeded"}),
			union(W, []string{"frameW", "frameOwner", "inst"})},
		{"deploy onto main, reader-shaped", crm(m{"op": "deploy", "deployment": "main", "checkpoint": "c:2222222",
			"result": "ok", "phase": "swap", "by": "user:ana"}), union(W, []string{"frameW", "frameOwner", "inst"})},
		{"deploy onto blue from main", crm(m{"op": "deploy", "id": 9, "deployment": "blue", "how": "promote",
			"from": "main", "checkpoint": "c:2222222", "result": "ok", "phase": "swap", "by": "user:ana"}),
			union(W, []string{"frameW", "frameOwner", "inst"})},
	} {
		sort.Strings(tc.want)
		if got := receivers(s, ps, tc.e); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n  got  %v\n  want %v", tc.name, got, tc.want)
		}
	}
	if slices.Contains(readers, "nora") || slices.Contains(readers, "wanda") || !slices.Contains(readers, "rita") {
		t.Errorf("reader form audience: %v", readers)
	}
}

// covers T7 — levels are the subscriber's current ones, read at each
// delivery, not those its principal held at connect time: a writer demoted to
// read gets the reader form instead of the full one, and stops counting as
// dev's own principal through her frame token; an admin demoted to a user
// without access, and a disabled user, receive nothing.
func TestDeploymentEventsFilteredCurrentLevel(t *testing.T) {
	s, st := audienceServer(t)
	ps := audiencePrincipals(t, st) // snapshots taken now
	full := events.Event{Type: "deployments", Component: "apps/crm", Data: map[string]any{"op": "record", "seq": 3,
		"by": "user:alice", "what": []string{"liveReload"}}}
	reader := events.Event{Type: "deployments", Component: "apps/crm", Data: map[string]any{"op": "record",
		"what": []string{"liveReload"}}}
	dev := events.Event{Type: "deployments", Component: "apps/crm", Data: map[string]any{"op": "reload",
		"deployment": "dev"}}
	pass := func(name string, e events.Event) bool { return s.eventFilter(ps[name])(e) }
	if !pass("wanda", full) || pass("wanda", reader) || !pass("devFrameW", dev) || !pass("alice", full) || !pass("terry", full) {
		t.Fatal("before the changes: wanda, alice and terry are writers, wanda's dev frame an own principal")
	}
	if _, err := st.Upsert(users.User{ID: "wanda", Tiles: map[string]string{"apps/crm": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "terry", Tiles: map[string]string{"apps/crm": users.LevelTerminal},
		Disabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		who  string
		e    events.Event
		want bool
	}{
		{"wanda", full, false}, {"wanda", reader, true}, {"devFrameW", dev, false}, {"frameW", reader, true},
		{"alice", full, false}, {"alice", reader, false}, {"otherTermA", reader, false},
		{"terry", full, false}, {"terry", reader, false}, {"termT", full, false},
	} {
		if got := pass(tc.who, tc.e); got != tc.want {
			t.Errorf("%s after the change, %v: got %v, want %v", tc.who, tc.e.Data, got, tc.want)
		}
	}
}
