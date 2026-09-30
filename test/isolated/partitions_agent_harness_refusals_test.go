//go:build linux && integration

package isolated

// partitions_agent_harness_refusals_test.go — the refusals case of
// TestPartitionsAgentHarness: the owner's ruling 90 §I15 (coding agents
// only in a person's own conversations) as the agent's backend keeps it
// (W6-A; the template's API.md "Partitioned instances" → "Coding agents
// only in your own conversations"), through xbind:
//
//   - a coding agent's conversation of alice's own is never published or
//     exported (so never copied): 409;
//   - in her partition a coding agent starts only in a sandbox homed there:
//     the team's sandbox is refused (403);
//   - the global instance starts none — POST /ask with `harness` (the owner
//     token's, and alice's shared chat) and the owner token's POST /runs
//     answer 409 — nor
//     signs one in (authenticate, the relayed login terminal: 409); its
//     agent's subagent_spawn with `harness` (a coding class, a team
//     sandbox bound) is refused and no adapter starts;
//   - a coding agent's conversation at the global instance, as a tree
//     before §I15 could hold one (planted through a builder's mail topic),
//     is never hosted, copied to a person's partition, nor un-shared by its
//     owner (409, members kept) — though a member may still leave it, and it
//     then stays there with its owner.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// The words the refusals say (the agent's harness_partition.go).
const (
	phNotAtGlobal = "coding agents work only in a person's own conversations — not in the shared space"
	phCantMove    = "a coding agent's conversation stays in the space it was started in"
	phUnshare     = "un-sharing it would move it to its owner's own space"
	phNotHomed    = "isn't a sandbox of your own space"
)

func phRefusals(t *testing.T, e *phEnv, box, team agSandbox, own int64) {
	d := e.d
	fake := map[string]string{"provider": "fake"}

	// her own coding agent's conversation doesn't leave her partition
	phRefused(t, "publishing her coding agent's conversation",
		e.agc(t, "alice", "POST", fmt.Sprintf("/runs/%d/publish", own), map[string]any{
			"share": map[string]string{"visibility": "team", "teamRole": "viewer"}, "keep": true}, false), 409, phCantMove)
	phRefused(t, "exporting it (a copy's first step)", e.agc(t, "alice", "GET", fmt.Sprintf("/runs/%d/export", own), nil, false), 409, phCantMove)
	e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d", own), nil, false) // still hers, where it was

	// in her partition, only a sandbox homed there
	phRefused(t, "her coding agent in the team's sandbox", e.agc(t, "alice", "POST", "/ask", map[string]any{"text": "perm", "harness": fake,
		"sandbox": map[string]string{"ref": team.Ref}}, false), 403, phNotHomed)

	// the global instance: none started, none signed in
	phRefused(t, "the owner token's POST /ask with a coding agent", e.agc(t, "", "POST", "/ask", map[string]any{"text": "perm", "harness": fake,
		"sandbox": map[string]string{"ref": team.Ref}}, false), 409, phNotAtGlobal)
	phRefused(t, "alice's shared chat with a coding agent", e.agc(t, "alice", "POST", "/ask", map[string]any{"text": "perm", "harness": fake,
		"sandbox": map[string]string{"ref": team.Ref},
		"share":   map[string]any{"members": []map[string]string{{"user": "bob", "role": "participant"}}}}, true), 409, phNotAtGlobal)
	// (a person's POST /runs there is refused whatever it holds: a shared chat is POST /ask's)
	phRefused(t, "the owner token's POST /runs with a coding agent", e.agc(t, "", "POST", "/runs", map[string]any{"goal": "x",
		"harness": fake, "sandbox": map[string]string{"ref": team.Ref}}, false), 409, phNotAtGlobal)

	// the global instance's agent asks for a coding agent (a coding class, the
	// team's sandbox bound): refused, no adapter
	var g struct{ ID int64 }
	e.agm(t, 200, "", "POST", "/ask", map[string]any{"text": "harness spawn", "class": "coding",
		"sandbox": map[string]string{"ref": team.Ref}}, false).Decode(t, &g)
	if g.ID <= 0 || g.ID >= pa2to40 {
		t.Fatalf("the global instance's conversation: %+v", g)
	}
	var said string
	xbindtest.Eventually(t, 3*time.Minute, "the global instance's spawn is refused, and its agent says so", func() (bool, string) {
		r := e.run(t, "", g.ID)
		n, last := r.answers("The coding agent said:")
		said = last
		return n > 0 && r.Run.Status != "running", fmt.Sprintf("status %s, %+v", r.Run.Status, r.Messages)
	})
	if !strings.Contains(said, "coding agents work only in a person's own conversations") {
		t.Errorf("the spawn's refusal at the global instance, as its agent read it: %q", said)
	}
	var execs struct {
		Execs []struct{ ClientID, State string }
	}
	e.target().As(t, agTile).Call("GET", "/sandboxes/"+team.ID+"/execs", nil, 200, &execs)
	for _, x := range execs.Execs {
		if strings.HasPrefix(x.ClientID, "harness:") {
			t.Errorf("BUG: a coding agent's adapter in the team's sandbox after the global instance's spawn: %+v", x)
		}
	}
	for _, who := range []string{"", "alice"} {
		phRefused(t, fmt.Sprintf("%q signing a coding agent in at the global instance", who),
			e.agc(t, who, "POST", fmt.Sprintf("/runs/%d/harness/authenticate", g.ID), map[string]any{"method": "fake-api-key", "apiKey": "k"}, who != ""),
			409, phNotAtGlobal)
	}
	sub := fmt.Sprintf("/runs/%d/harness/terminal?login=1&xbin-partition=global", g.ID)
	if conn, r, err := d.Dial(t, e.agentPath(t, sub, "alice")); err == nil {
		conn.Close()
		t.Errorf("BUG: a sign-in terminal at the global instance opened")
	} else if r.Status != 409 || !strings.Contains(r.String(), phNotAtGlobal) {
		t.Errorf("a sign-in terminal at the global instance: %d %s (want 409)", r.Status, r)
	}

	// a coding agent's conversation at the global instance (from before
	// §I15): alice's, shared with bob
	var p struct{ ID int64 }
	e.agm(t, 200, "alice", "POST", "/ask", map[string]any{"text": "quick",
		"share": map[string]any{"members": []map[string]string{{"user": "bob", "role": "participant"}}}}, true).Decode(t, &p)
	xbindtest.Eventually(t, 3*time.Minute, "the shared conversation answers", func() (bool, string) {
		var r agRun
		e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d", p.ID), nil, true).Decode(t, &r)
		n, _ := r.answers("Quick answer.")
		return n > 0 && r.Run.Status != "running", r.Run.Status
	})
	d.Must(t, "POST", "/api/xbin/partitions/mail", map[string]any{"to": "global", "topic": "e2e/plant-harness", "data": p.ID}, 200,
		xbindtest.FrameHeader(e.pageTok(t, agTile, "alice")))
	xbindtest.Eventually(t, 2*time.Minute, "the planted coding agent's conversation", func() (bool, string) {
		var v hView
		e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d/view", p.ID), nil, true).Decode(t, &v)
		return v.Run.Engine == "harness", v.Run.Engine
	})
	type sharing struct {
		Owner, Visibility, TeamRole string
		Members                     []struct{ User, Role string }
	}
	var mem sharing
	members := func() {
		t.Helper()
		mem = sharing{}
		e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d/members", p.ID), nil, true).Decode(t, &mem)
	}
	members()
	seen := map[string]any{"owner": mem.Owner, "visibility": mem.Visibility, "teamRole": mem.TeamRole, "members": map[string]string{}}
	for _, m := range mem.Members {
		seen["members"].(map[string]string)[m.User] = m.Role
	}
	phRefused(t, "hosting a coding agent's conversation", e.agc(t, "alice", "POST", "/hosting", map[string]any{"conversation": p.ID, "seen": seen}, false),
		409, phCantMove)
	phRefused(t, "alice copying it to her own space", e.agc(t, "alice", "POST", "/copy", map[string]any{"from": p.ID}, false), 409, phCantMove)
	phRefused(t, "bob copying it to his own space", e.agc(t, "bob", "POST", "/copy", map[string]any{"from": p.ID}, false), 409, phCantMove)
	phRefused(t, "its owner un-sharing it (taking bob out)",
		e.agc(t, "alice", "DELETE", fmt.Sprintf("/runs/%d/members/bob", p.ID), nil, true), 409, phUnshare)
	members()
	if len(mem.Members) != 1 || mem.Members[0].User != "bob" {
		t.Errorf("after the refused un-share: members %+v (want bob kept)", mem.Members)
	}
	// a member's own leave is theirs: it stays at the global instance with its owner
	e.agm(t, 200, "bob", "DELETE", fmt.Sprintf("/runs/%d/members/bob", p.ID), nil, true)
	members()
	if len(mem.Members) != 0 || mem.Owner != "alice" {
		t.Errorf("after bob left: %+v (want alice's, shared with no one, at the global instance)", mem)
	}
	var r agRun
	e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d", p.ID), nil, true).Decode(t, &r)
	if n, _ := r.answers("Quick answer."); n == 0 {
		t.Errorf("the conversation at the global instance after bob left: %+v", r.Messages)
	}
	if st := e.agc(t, "bob", "GET", fmt.Sprintf("/runs/%d", p.ID), nil, true).Status; st != 404 {
		t.Errorf("bob reading it after he left: %d, want 404", st)
	}
}
