//go:build linux && integration

package isolated

// wsagent_test.go — the workspace agent (xbin/wsagent; D231, D227, D228,
// D229, D232) end to end, on an xbind as an install runs one: this tree's
// binary with its release table and prebuilt backend, booted without --dev
// under --isolate with owner auth and the vault; hack/fakeopenai as the LLM
// connector's upstream and hack/fakeacp as the one coding agent (no real
// provider, no key that is one). People drive their agents the way its page
// and xbin's chrome do: the page's frame token on the agent's own routes,
// the person's own session on everything that decides.

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	wsKeptTile   = "apps/kept"   // owned by an org that keeps workspace agents out
	wsLedgerTile = "apps/ledger" // owned by an org that allows one model only
	wsAdmin      = "ada"
)

func TestWorkspaceAgent(t *testing.T) {
	e := startWsAgent(t)
	d := e.d

	// the workspace: a note with a backend in no org, and two orgs' tiles
	if err := d.WriteFiles(wsNoteTile, map[string]string{
		"go.mod":          "module ws/note\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": wsNote,
		"index.html":      "<!doctype html><html><head></head><body>note</body></html>\n",
		"API.md":          "# Note\n\nGET /note reads the note. PUT /note replaces it with the body.\n",
	}); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, wsNoteTile, map[string]string{"xbin.json": `{"title":"Note","runtime":"go","agent":"full","expose":{"roles":{"reader":"read","writer":"write"}}}` + "\n"})
	e.staticTile(wsKeptTile, "Kept")
	e.staticTile(wsLedgerTile, "Ledger")
	e.org("closed", "Closed", wsKeptTile, map[string]any{"workspaceAgent": "off"})
	e.org("finance", "Finance", wsLedgerTile, map[string]any{"models": []string{wsModelA}})
	// Coding agents here run on the LLM connector alone. A conversation that
	// has seen Finance's tile (its row on the map is enough) holds Finance's
	// data, and what it tells a tile agent goes to that session's model: on
	// a person's own sign-in nobody can say which, and the start is refused.
	// Held to the connector, the session starts on its default — the model
	// Finance allows (docs/protocol/delegation.md §Agent sessions).
	d.Must(t, "PUT", "/api/xbin/agents/policy", map[string]any{"credentials": "connector-only"}, 200)

	e.person(wsAdmin, "admin", nil)
	e.person("alice", "user", map[string]string{wsNoteTile: "terminal", wsKeptTile: "write", wsLedgerTile: "write"})
	e.person("bob", "user", nil)

	// nobody has an agent until an admin turns workspace agents on and says who gets one
	if r := d.Call(t, "GET", "/api/xbin/frame-token?component="+wsAgent, nil, e.as("alice")...); r.Status != 403 {
		t.Errorf("alice's frame token of the agent before anyone was given one: %d %s, want 403", r.Status, r)
	}
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone"}, 200, e.as(wsAdmin)...)

	alice := e.open("alice", map[string]any{"main": true})
	t.Cleanup(func() { alice.transcript(t) })

	var noteRequest string // alice's request of part 2, at xbind
	var ledger *wsConv     // alice's second session, of part 5

	t.Run("1 a person's agent reads the map", func(t *testing.T) {
		e.in(t)
		if alice.model != wsModelA {
			t.Errorf("alice's Main is on %q, want the connector's default %s", alice.model, wsModelA)
		}
		turn := alice.say(`ws: workspace_map`)
		m := turn.tool("workspace_map", 0)
		t.Logf("the map: %s", m)
		for _, want := range []string{wsNoteTile, wsLedgerTile} {
			if !strings.Contains(m, want) {
				t.Errorf("workspace_map doesn't list %s, a tile alice can see:\n%s", want, m)
			}
		}
		if strings.Contains(m, wsKeptTile) {
			t.Errorf("workspace_map names %s, a tile whose org keeps workspace agents out:\n%s", wsKeptTile, m)
		}
		if !strings.HasPrefix(turn.Done, "Done: This session:") {
			t.Errorf("the model's answer: %q, want the map's first line", turn.Done)
		}
	})

	t.Run("2 a change waits for its person's approval", func(t *testing.T) {
		e.in(t)
		before := e.seen("alice")
		if !strings.Contains(before, "note=first") || !strings.Contains(before, "writes=0") {
			t.Fatalf("the note before anything: %s", before)
		}
		turn := alice.say(`ws: tile_call {"tile":"` + wsNoteTile + `","method":"GET","path":"/note"} ;; tile_call {"tile":"` + wsNoteTile + `","method":"PUT","path":"/note","body":"\"by the agent\"","why":"set the note"}`)
		if got := turn.tool("tile_call", 0); !strings.Contains(got, "GET /note → 200") || !strings.Contains(got, "note=first read by user=alice agent="+wsAgent+" role=writer") {
			t.Errorf("the agent's read of the note: %q, want it made as alice, marked as her agent's", got)
		}
		if got := turn.tool("tile_call", 1); !strings.HasPrefix(got, "Pending — request ") {
			t.Fatalf("the agent's PUT: %q, want a pending request", got)
		}
		// the request: on the session's cards, in what waits for alice, and at xbind
		cards := alice.cards()
		if len(cards.Requests) != 1 || cards.Requests[0].Kind != "call" || cards.Requests[0].State != "pending" || cards.Requests[0].Ref == "" ||
			cards.Requests[0].Tile != wsNoteTile || cards.Requests[0].Method != "PUT" {
			t.Fatalf("the session's cards: %+v, want one pending call on %s", cards.Requests, wsNoteTile)
		}
		ref := cards.Requests[0].Ref
		noteRequest = ref
		if needs := alice.needs(); len(needs) != 1 || needs[0].Kind != "approval" || needs[0].Request != ref {
			t.Errorf("GET /ws/needs: %+v, want the approval of %s", needs, ref)
		}
		reqs := e.requests("alice")
		if len(reqs) != 1 || reqs[0].ID != ref || reqs[0].State != "pending" || reqs[0].Method != "PUT" || reqs[0].Target != "/api/"+wsNoteTile+"/note" ||
			reqs[0].Body != `"by the agent"` || reqs[0].Summary != "set the note" || reqs[0].Agent != wsAgent || reqs[0].Preview.Class != "tile-write" {
			t.Fatalf("alice's requests at xbind: %+v, want the exact call, pending", reqs)
		}
		if st := e.state("alice"); st["pendingRequests"] != float64(1) {
			t.Errorf("alice's delegation state: %v, want one pending request", st)
		}
		if got := e.seen("alice"); !strings.Contains(got, "note=first") || !strings.Contains(got, "writes=0") {
			t.Fatalf("the note changed before alice approved: %s", got)
		}

		// nobody but alice, in her own session, approves it
		approve := "/api/xbin/delegate/requests/" + ref + "/approve"
		if r := d.Call(t, "POST", approve, map[string]any{"grant": "once"}, e.as("bob")...); r.Status != 403 && r.Status != 404 {
			t.Errorf("bob approves alice's agent's request: %d %s, want 403 or 404", r.Status, r)
		}
		if r := d.Call(t, "POST", approve, map[string]any{"grant": "once"}, alice.fr); r.Status != 403 {
			t.Errorf("the agent's own page approves its request: %d %s, want 403", r.Status, r)
		}
		if got := e.request("alice", ref); got.State != "pending" {
			t.Fatalf("the request after two refused approvals: %s", got.State)
		}
		after := alice.last()
		d.Must(t, "POST", approve, map[string]any{"grant": "once"}, 200, e.as("alice")...) // what the chrome's Allow once sends
		approved := time.Now()

		// the backend is told, makes that one call with the ticket, and wakes the conversation
		told := alice.woken(after, time.Minute, "the approved call ran", "approved by your person", "PUT /note → 200", "written by user=alice agent="+wsAgent+" role=writer")
		wake := time.Since(approved)
		t.Logf("the update, %s after the approval: %s", wake.Round(10*time.Millisecond), told)
		if wake > wsWakeWithin {
			t.Errorf("alice's approval reached the conversation after %s, want within %s", wake.Round(time.Millisecond), wsWakeWithin)
		}
		if got := e.seen("alice"); !strings.Contains(got, `note="by the agent"`) || !strings.Contains(got, "writes=1") ||
			!strings.Contains(got, "last write by user=alice agent="+wsAgent+" role=writer") {
			t.Errorf("the note after the approved call: %s, want one write, alice's, marked as her agent's", got)
		}
		cards = alice.cards()
		// (via "app": the test signs in as the app does, POST /login, and holds that session's token)
		if r := cards.Requests[0]; r.State != "used" || r.By != "person" || r.Via != "app" || r.Grant != "once" || r.ResultStatus != 200 || r.Result != "PUT /note → 200" {
			t.Errorf("the request's card after the call: %+v, want used, by the person with her own sign-in, once, 200", r)
		}
		if needs := alice.needs(); len(needs) != 0 {
			t.Errorf("GET /ws/needs after the decision: %+v, want nothing", needs)
		}
		if got := e.request("alice", ref); got.State != "used" {
			t.Errorf("the request at xbind after the call: %s, want used (a ticket is spent once)", got.State)
		}
		// her log has the call, under its ticket, and her decision
		var call, decision bool
		rows := e.audit("alice")
		for _, r := range rows {
			if r.Method == "PUT" && r.Path == "/api/"+wsNoteTile+"/note" && r.Ticket == ref && r.Status == 200 && r.User == "alice" && r.Agent == wsAgent && !r.DryRun {
				call = true
			}
			if strings.HasPrefix(r.Class, "decision:") {
				decision = true
			}
		}
		if !call || !decision {
			t.Errorf("alice's log (GET /delegate/audit): the call under its ticket %v, her decision %v:\n%+v", call, decision, rows)
		}
		if r := d.Call(t, "GET", "/api/xbin/delegate/audit", nil, alice.fr); r.Status != 403 {
			t.Errorf("the agent's page reads the log: %d %s, want 403", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/delegate/audit?user=alice", nil, e.as("bob")...); r.Status != 403 {
			t.Errorf("bob reads alice's log: %d %s, want 403", r.Status, r)
		}
	})

	t.Run("3 a tile agent from an approved brief", func(t *testing.T) {
		e.in(t)
		if got := e.tileAgents("alice"); len(got) != 0 {
			t.Fatalf("alice's tile agents before any start: %+v", got)
		}
		turn := alice.say(`ws: tile_agent_brief {"tile":"` + wsNoteTile + `","goal":"List what is in the tile","done_when":"the files are listed"}`)
		if got := turn.tool("tile_agent_brief", 0); !strings.Contains(got, "Nothing has started") {
			t.Fatalf("tile_agent_brief: %q", got)
		}
		cards := alice.cards()
		if len(cards.Briefs) != 1 || cards.Briefs[0].State != "draft" || cards.Briefs[0].Tile != wsNoteTile {
			t.Fatalf("the brief's card: %+v, want one draft for %s", cards.Briefs, wsNoteTile)
		}
		brief := cards.Briefs[0].ID
		briefAPI := fmt.Sprintf("%s/ws/sessions/%s/briefs/%d", wsAPI, alice.run, brief)
		// alice picks the coding agent on the brief's card, among what xbind says the tile allows
		type startDefaults struct {
			Picked, Harness, HarnessName, Mode, Line string
			Asks, Ticket                             bool
			Defaults                                 struct {
				Harnesses []struct {
					ID, Name, Mode    string
					Asks, NeedsTicket bool
				}
			}
		}
		var sd startDefaults
		d.Must(t, "GET", briefAPI+"/start-defaults", nil, 200, alice.fr).Decode(t, &sd)
		offered, fakeName := false, "" // the coding agent's name is xbind's: the backend keeps no catalog of them
		for _, h := range sd.Defaults.Harnesses {
			if h.ID == "fake" && h.Mode == "ask" && h.Asks && h.NeedsTicket {
				offered, fakeName = true, h.Name
			}
		}
		if !offered || sd.Picked != "" || fakeName == "" {
			t.Fatalf("what the brief's start would run on: %+v, want the fake coding agent offered by name in its asking mode, behind a ticket, and nothing picked yet", sd)
		}
		// … and it is xbind's own answer, as alice's session reads the same catalog
		var provs []struct{ ID, Name string }
		d.Must(t, "GET", "/api/xbin/agent/providers", nil, 200, e.as("alice")...).Decode(t, &provs)
		if i := slices.IndexFunc(provs, func(p struct{ ID, Name string }) bool { return p.ID == "fake" }); i < 0 || provs[i].Name != fakeName {
			t.Errorf("the brief's card names the fake coding agent %q; xbind's providers: %+v", fakeName, provs)
		}
		if r := d.Call(t, "PUT", briefAPI, map[string]any{"harness": "no-such-agent"}, alice.fr); r.Status != 409 {
			t.Errorf("picking a coding agent the tile doesn't offer: %d %s, want 409", r.Status, r)
		}
		d.Must(t, "PUT", briefAPI, map[string]any{"harness": "fake"}, 200, alice.fr)
		sd = startDefaults{}
		d.Must(t, "GET", briefAPI+"/start-defaults", nil, 200, alice.fr).Decode(t, &sd)
		if sd.Picked != "fake" || sd.Harness != "fake" || sd.Mode != "ask" || !sd.Asks || !sd.Ticket || sd.HarnessName != fakeName || !strings.HasPrefix(sd.Line, fakeName+" · asks") {
			t.Fatalf("the start after alice's pick: %+v, want the fake coding agent by xbind's name for it (%q), asking, behind a ticket", sd, fakeName)
		}

		turn = alice.say(fmt.Sprintf(`ws: tile_agent_start {"brief": %d}`, brief))
		if got := turn.tool("tile_agent_start", 0); !strings.HasPrefix(got, "Pending — request ") {
			t.Fatalf("tile_agent_start: %q, want a pending request", got)
		}
		cards = alice.cards()
		if r := cards.Requests[0]; r.Kind != "session" || r.State != "pending" || r.Brief != brief || r.Ref == "" || r.CallID == "" {
			t.Fatalf("the start's card: %+v, want a pending session request of brief %d, with the call that filed it", r, brief)
		}
		ref, startReq, startCall := cards.Requests[0].Ref, cards.Requests[0].ID, cards.Requests[0].CallID
		req := e.request("alice", ref)
		if s := req.Preview.Session; req.Kind != "session" || s.Tile != wsNoteTile || s.Provider != "fake" || !s.Asks || s.Mode != "ask" ||
			!strings.Contains(s.Prompt, "Goal: List what is in the tile") {
			t.Fatalf("the start as xbind shows it to alice: %+v, want the fake coding agent in its asking mode with the brief as its first prompt", req)
		}
		if got := e.tileAgents("alice"); len(got) != 0 {
			t.Fatalf("a tile agent runs before alice approved its start: %+v", got)
		}
		started := alice.last()
		d.Must(t, "POST", "/api/xbin/delegate/requests/"+ref+"/approve", map[string]any{"grant": "once"}, 200, e.as("alice")...)
		approved := time.Now()

		// the session: alice's, in the tile, asking, marked as started by her agent
		var sess wsTileSession
		xbindtest.Eventually(t, time.Minute, "the tile agent asks alice for a permission", func() (bool, string) {
			got := e.tileAgents("alice")
			if len(got) != 1 {
				return false, fmt.Sprintf("%+v", got)
			}
			sess = got[0]
			return sess.Status == "waiting_permission" && sess.Pending == 1, fmt.Sprintf("%+v", sess)
		})
		if sess.Cwd != wsNoteTile || sess.Provider != "fake" || sess.Mode != "ask" || sess.Origin != "agent" {
			t.Errorf("the tile agent's session: %+v, want the fake coding agent in %s, mode ask, origin agent", sess, wsNoteTile)
		}
		var detail struct {
			Permissions []struct{ Pid string }
		}
		sessAPI := "/api/xbin/term/sessions/" + sess.ID
		d.Must(t, "GET", sessAPI, nil, 200, e.as("alice")...).Decode(t, &detail)
		if len(detail.Permissions) != 1 {
			t.Fatalf("the session's permission requests: %+v, want one", detail)
		}
		answer := sessAPI + "/permissions/" + detail.Permissions[0].Pid

		// the workspace agent knows it waits for alice, and says so where she looks
		xbindtest.Eventually(t, time.Minute, "the agent's cards say the tile agent waits for alice", func() (bool, string) {
			c := alice.cards()
			return len(c.Agents) == 1 && c.Agents[0].Waits, fmt.Sprintf("%+v", c.Agents)
		})
		cards = alice.cards()
		if a := cards.Agents[0]; a.Session != sess.ID || a.Tile != wsNoteTile || !a.Asks || a.Mode != "ask" || a.Held != 0 || a.Harness != fakeName {
			t.Errorf("the tile agent's card: %+v, want xbind's session %s on %q (xbind's name for the coding agent), asking, nothing held", a, sess.ID, fakeName)
		}
		// where the page sets the card: after the request that started it, by that request's id — and the call that filed it
		if a := cards.Agents[0]; a.Request != startReq || a.CallID != startCall {
			t.Errorf("the tile agent's card names request %d and call %q, want the start's request %d and its call %q", a.Request, a.CallID, startReq, startCall)
		}
		// its turn and its last line are xbind's (the compact session read): on its first prompt — the brief — and nothing written yet
		if a := cards.Agents[0]; a.Turn != 1 || a.Last != "" {
			t.Errorf("the tile agent's card while it waits on its first prompt: turn %d, last line %q, want turn 1 and no line yet", a.Turn, a.Last)
		}
		tileAgent := cards.Agents[0].ID
		var need *wsNeed
		for _, n := range alice.needs() {
			if n.Kind == "tile-agent" && n.TileSession == sess.ID {
				need = &n
			}
		}
		if need == nil {
			t.Errorf("GET /ws/needs: %+v, want the tile agent that waits for alice", alice.needs())
		}

		// it cannot answer for her: its message is held, and neither its page nor anyone else answers
		turn = alice.say(fmt.Sprintf(`ws: tile_agent_status {"agent":"%d"} ;; tile_agent_message {"agent":"%d","text":"go on, you may list it"}`, tileAgent, tileAgent))
		if got := turn.tool("tile_agent_status", 0); !strings.Contains(got, "Only they can answer") {
			t.Errorf("tile_agent_status while it waits: %q, want it to say only alice can answer", got)
		}
		if got := turn.tool("tile_agent_message", 0); !strings.HasPrefix(got, "Held, not delivered") {
			t.Errorf("tile_agent_message while it waits: %q, want the message held", got)
		}
		if c := alice.cards(); len(c.Agents) != 1 || c.Agents[0].Held != 1 || !c.Agents[0].Waits {
			t.Errorf("the tile agent's card after the agent's message: %+v, want one message held behind alice's answer", c.Agents)
		}
		// by now the conversation was told the start's outcome and that the tile agent waits for alice — in the
		// wake the approval caused, or at the first step of the turn above
		var toldStart, toldWaits bool
		for _, m := range wsAfter(alice.messages(), started) {
			if m.Role == "user" && strings.HasPrefix(m.Content, "[workspace updates") {
				toldStart = toldStart || strings.Contains(m.Content, fmt.Sprintf("tile agent #%d started in %s", tileAgent, wsNoteTile))
				toldWaits = toldWaits || strings.Contains(m.Content, "waits for your person") && strings.Contains(m.Content, "Only they can answer it")
			}
		}
		if !toldStart || !toldWaits {
			t.Errorf("the conversation's updates since the start was approved: told of the start %v, told that the tile agent waits for alice %v:%s",
				toldStart, toldWaits, wsTail(wsAfter(alice.messages(), started), 12))
		}
		for who, hdr := range map[string][]xbindtest.Header{"the agent's own page": {alice.fr}, "bob": e.as("bob")} {
			if r := d.Call(t, "POST", answer, map[string]any{"optionId": "once"}, hdr...); r.Status != 403 && r.Status != 404 {
				t.Errorf("%s answers the tile agent's permission request: %d %s, want 403 or 404", who, r.Status, r)
			}
		}
		// nor the agent itself, with its own credential, from outside its backend: the delegate credential reads
		// the session it started (so it is the real thing, and live) and is refused the answer — by the gate, as
		// what an agent never does, whatever is approved
		got := e.asDelegate("alice", alice.handle, "GET "+sessAPI, "POST "+answer+` {"optionId":"once"}`)
		if got[0].Status != 200 || !strings.Contains(got[0].Body, sess.ID) {
			t.Fatalf("the agent's credential reads the session it started: %d %s, want 200", got[0].Status, got[0].Body)
		}
		if got[1].Status != 403 || !strings.Contains(got[1].Body, `"refusal":"not-delegated"`) || !strings.Contains(got[1].Body, "answered by the person, never by their agent") {
			t.Errorf("the agent's own credential answers the tile agent's permission request: %d %s, want 403 not-delegated, saying the person answers", got[1].Status, got[1].Body)
		}
		if got := e.tileAgents("alice"); len(got) != 1 || got[0].Status != "waiting_permission" || got[0].Pending != 1 {
			t.Fatalf("the tile agent after the agent's message and two refused answers: %+v, want it still waiting for alice", got)
		}

		// alice answers it herself: the turn ends, the held message goes in, and her agent is told — at once: the
		// tile agent's ended turn wakes the conversation without waiting out any window, though the start's outcome
		// and the tile agent's question woke it moments ago
		after := alice.last()
		d.Must(t, "POST", answer, map[string]any{"optionId": "once"}, 200, e.as("alice")...)
		answered := time.Now()
		told := alice.woken(after, time.Minute, "the tile agent ended its turn", fmt.Sprintf("#%d", tileAgent), "ended its turn")
		wake := time.Since(answered)
		t.Logf("the update, %s after alice answered the tile agent (%s after the start was approved): %s", wake.Round(10*time.Millisecond), time.Since(approved).Round(time.Second), told)
		if wake > wsWakeWithin {
			t.Errorf("the tile agent's ended turn woke the conversation %s after alice answered it, want within %s", wake.Round(time.Millisecond), wsWakeWithin)
		}
		xbindtest.Eventually(t, time.Minute, "the tile agent answered the message that was held, and is idle", func() (bool, string) {
			c := alice.cards()
			got := e.tileAgents("alice")
			log := string(d.Call(t, "GET", sessAPI+"/events", nil, e.as("alice")...).Body)
			// the card's turn and last line are xbind's own: two prompts taken (the brief, the held message), and the last line it wrote
			return strings.Contains(log, "echo: go on, you may list it") && len(got) == 1 && got[0].Status == "idle" &&
					len(c.Agents) == 1 && c.Agents[0].Held == 0 && !c.Agents[0].Waits && c.Agents[0].Status == "idle" &&
					c.Agents[0].Turn == 2 && c.Agents[0].Last == "echo: go on, you may list it",
				fmt.Sprintf("%+v %+v", got, c.Agents)
		})
		turn = alice.say(fmt.Sprintf(`ws: tile_agent_status {"agent":"%d"} ;; tile_agent_result {"agent":"%d"} ;; tile_agents`, tileAgent, tileAgent))
		if got := turn.tool("tile_agent_status", 0); !strings.Contains(got, "ended its turn") {
			t.Errorf("tile_agent_status after alice answered: %q, want the ended turn", got)
		}
		res := turn.tool("tile_agent_result", 0)
		t.Logf("tile_agent_result: %s", res)
		if !strings.Contains(res, "ended its turn") || !strings.Contains(res, "echo: go on, you may list it") {
			t.Errorf("tile_agent_result: %q, want the ended turn and the tile agent's answer to the message that was held", res)
		}
		if needs := alice.needs(); len(needs) != 0 {
			t.Errorf("GET /ws/needs after alice answered: %+v, want nothing", needs)
		}
	})

	t.Run("4 an org's rule keeps the agent out, and it says why", func(t *testing.T) {
		e.in(t)
		turn := alice.say(`ws: tile_call {"tile":"` + wsKeptTile + `","method":"GET","path":"/"} ;; workspace_map`)
		if got := turn.tool("tile_call", 0); !strings.Contains(got, "is connected in this session") {
			t.Errorf("tile_call on a tile whose org keeps agents out: %q, want it refused as not connected", got)
		}
		const why = "Closed has the workspace agent turned off"
		if got := turn.tool("workspace_map", 0); !strings.Contains(got, "Not connected — Closed: "+why) || strings.Contains(got, wsKeptTile) {
			t.Errorf("workspace_map: %q, want the org's own reason for what is not connected, and the tile not named", got)
		}
		c := alice.cards()
		if len(c.Refusals) == 0 || c.Refusals[0].Kind != "policy" || c.Refusals[0].Tile != wsKeptTile || c.Refusals[0].Tool != "tile_call" ||
			c.Refusals[0].Reason != why {
			t.Errorf("the session's refusals: %+v, want the newest a policy refusal of tile_call on %s with the org's reason %q", c.Refusals, wsKeptTile, why)
		}
		// alice's page shows her the tile by name, with the same reason — the one read of xbind's its frame has
		var conn struct {
			Tiles []struct{ Tile, State, Reason, By string }
		}
		d.Must(t, "GET", "/api/xbin/agents/connection?session="+url.QueryEscape(alice.handle), nil, 200, alice.fr).Decode(t, &conn)
		found := false
		for _, tl := range conn.Tiles {
			if tl.Tile == wsKeptTile {
				found = tl.State == "blocked" && tl.By == "closed" && len(c.Refusals) > 0 && tl.Reason == c.Refusals[0].Reason
			}
		}
		if !found {
			t.Errorf("alice's connection view: %+v, want %s blocked by closed with the reason her agent's card gives", conn.Tiles, wsKeptTile)
		}
	})

	t.Run("5 a model the conversation's data doesn't allow", func(t *testing.T) {
		e.in(t)
		ledger = e.open("alice", map[string]any{"name": "Ledger", "model": wsModelA})
		t.Cleanup(func() { ledger.transcript(t) })
		if it := ledger.item(); it.Model != wsModelA || len(it.Tainted) != 0 || it.Main {
			t.Fatalf("a new session: %+v, want it on %s, holding nothing yet", it, wsModelA)
		}
		turn := ledger.say(`ws: tile_api_doc {"tile":"` + wsLedgerTile + `"}`)
		if got := turn.tool("tile_api_doc", 0); !strings.Contains(got, "This tile has a page and no API of its own") {
			t.Fatalf("tile_api_doc of %s: %q, want the tile's API.md", wsLedgerTile, got)
		}
		if it := ledger.item(); !slices.Contains(it.Tainted, "finance") {
			t.Errorf("the session after reading %s: tainted %v, want finance", wsLedgerTile, it.Tainted)
		}
		// the page asks first (the what-if), then is refused the change itself
		var whatIf struct {
			WhatIf struct {
				Allowed     bool
				TaintedOrgs []string
			}
			Offer struct{ NewSession struct{ Model string } }
		}
		d.Must(t, "GET", wsAPI+"/ws/sessions/"+ledger.run+"/connection?whatIf="+url.QueryEscape(wsModelB), nil, 200, ledger.fr).Decode(t, &whatIf)
		if whatIf.WhatIf.Allowed || !slices.Contains(whatIf.WhatIf.TaintedOrgs, "finance") || whatIf.Offer.NewSession.Model != wsModelB {
			t.Errorf("what if the session moved to %s: %+v, want it not allowed for finance's data, with a new session offered", wsModelB, whatIf)
		}
		r := d.Call(t, "PATCH", wsAPI+"/ws/sessions/"+ledger.run, map[string]any{"model": wsModelB}, ledger.fr)
		var refused struct {
			Code, Model, Reason string
			Orgs                []string
			Offer               struct{ NewSession struct{ Model string } }
		}
		r.Decode(t, &refused)
		if r.Status != 409 || refused.Code != "model_taint" || !slices.Contains(refused.Orgs, "finance") || refused.Model != wsModelB ||
			refused.Offer.NewSession.Model != wsModelB || !strings.Contains(refused.Reason, "Finance") {
			t.Errorf("PATCH {model: %s} on a session that holds Finance's data: %d %s, want 409 model_taint naming finance and its rule, with the offer of a new session on it", wsModelB, r.Status, r)
		}
		if got := ledger.item().Model; got != wsModelA {
			t.Errorf("the session's model after the refused change: %s, want %s", got, wsModelA)
		}
		// the way forward works: a new session on that model, which Finance's tile is not connected to
		fresh := e.open("alice", map[string]any{"name": "On B", "model": wsModelB})
		t.Cleanup(func() { fresh.transcript(t) })
		turn = fresh.say(`ws: workspace_map`)
		if got := turn.tool("workspace_map", 0); strings.Contains(got, wsLedgerTile) || !strings.Contains(got, "Finance") || !strings.Contains(got, wsNoteTile) {
			t.Errorf("the map of a session on %s: %q, want %s left out with Finance's reason, and %s listed", wsModelB, got, wsLedgerTile, wsNoteTile)
		}
		d.Must(t, "DELETE", wsAPI+"/ws/sessions/"+fresh.run, nil, 200, fresh.fr)
	})

	t.Run("5b the page lists the models a session could move to", func(t *testing.T) {
		e.in(t)
		var st struct {
			Models struct {
				Default string
				Models  []struct{ ID string }
			}
		}
		d.Must(t, "GET", wsAPI+"/ws/state", nil, 200, alice.fr).Decode(t, &st)
		if st.Models.Default != wsModelA {
			t.Errorf("GET /ws/state: the default model is %q, want %s", st.Models.Default, wsModelA)
		}
		// (the connector's list is no call: a holder is shown every model so
		// its person can pick — internal/boot/delegation.go holderCallPolicy)
		have := map[string]bool{}
		for _, m := range st.Models.Models {
			have[m.ID] = true
		}
		if !have[wsModelA] || !have[wsModelB] {
			t.Errorf("GET /ws/state lists the models %v, want %s and %s", st.Models.Models, wsModelA, wsModelB)
		}
	})

	t.Run("6 pause stops everything at once", func(t *testing.T) {
		e.in(t)
		before := e.seen("alice")
		var st map[string]any
		d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"paused": true}, 200, e.as("alice")...).Decode(t, &st)
		if st["paused"] != true {
			t.Fatalf("PUT /delegate/state {paused: true}: %v", st)
		}
		// her agent's page learns it as ws/api.js does: the backend is refused its own read of the state
		// (delegationError), so the page reads xbind's with its own credential
		var page struct {
			Delegation      *struct{ Paused bool }
			DelegationError string
		}
		r := d.Must(t, "GET", wsAPI+"/ws/state", nil, 200, alice.fr)
		if r.Decode(t, &page); page.Delegation != nil || !strings.Contains(page.DelegationError, "paused") {
			t.Errorf("GET /ws/state while paused: %s, want the backend refused its read of the state as paused", r)
		}
		var own struct {
			Paused   bool
			PausedAt string
		}
		if d.Must(t, "GET", "/api/xbin/delegate/state", nil, 200, alice.fr).Decode(t, &own); !own.Paused || own.PausedAt == "" {
			t.Errorf("the page's own read of GET /delegate/state while paused: %+v, want paused, with when", own)
		}
		// xbind runs no model turn for a paused agent: the backend answers her message without the model —
		// a line in the transcript naming the switch and who changes it where, nothing queued or sent, her
		// words kept (409) for her to send again
		read := `ws: tile_call {"tile":"` + wsNoteTile + `","method":"GET","path":"/note"}`
		lastBefore := alice.last()
		r = d.Call(t, "POST", wsAPI+"/runs/"+alice.run+"/message", map[string]any{"text": read}, alice.fr)
		var off struct {
			Code, Refusal, Error string
			Kept                 bool
		}
		if r.Decode(t, &off); r.Status != 409 || off.Code != "agent-off" || off.Refusal != "paused" || !off.Kept || !strings.Contains(off.Error, "Resume in the workspace agent's dock") {
			t.Errorf("a message while paused: %d %s, want 409 agent-off, paused, kept, saying where to resume", r.Status, r)
		}
		var view struct {
			Steps []struct{ Kind, Detail string }
		}
		d.Must(t, "GET", wsAPI+"/runs/"+alice.run+"/view", nil, 200, alice.fr).Decode(t, &view)
		if n := len(view.Steps); n == 0 || view.Steps[n-1].Kind != "off" || !strings.Contains(view.Steps[n-1].Detail, "Your agent is paused") {
			t.Errorf("the transcript's line for a message while paused: %+v, want the newest step of kind off", view.Steps)
		}
		time.Sleep(time.Second) // (a negative: nothing may reach the conversation or the model)
		if ms := wsAfter(alice.messages(), lastBefore); len(ms) != 0 {
			t.Errorf("a message while paused reached the conversation:%s", wsTail(ms, 4))
		}
		if got := e.seen("alice"); got != before {
			t.Errorf("the tile was reached while alice's agent was paused: %s, before: %s", got, before)
		}
		// only alice resumes it: its page may pause and nothing else
		if r := d.Call(t, "PUT", "/api/xbin/delegate/state", map[string]any{"paused": false}, alice.fr); r.Status != 403 {
			t.Errorf("the agent's page resumes it: %d %s, want 403", r.Status, r)
		}
		d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"paused": false}, 200, e.as("alice")...)
		// resumed, nothing runs by itself: sent again, it runs — once
		turn := alice.say(read)
		if got := turn.tool("tile_call", 0); !strings.Contains(got, "GET /note → 200") {
			t.Errorf("a read after alice resumed: %q, want it to run", got)
		}
		users := 0
		for _, m := range wsAfter(alice.messages(), lastBefore) {
			if m.Role == "user" {
				users++
			}
		}
		if users != 1 {
			t.Errorf("%d messages of alice's reached the conversation after she resumed, want only the one she sent again", users)
		}
	})

	t.Run("7 another person's agent is another partition", func(t *testing.T) {
		e.in(t)
		if ledger == nil || noteRequest == "" {
			t.Fatal("parts 2 and 5 left no request and no second session of alice's to keep from bob")
		}
		bob := e.open("bob", map[string]any{"main": true})
		t.Cleanup(func() { bob.transcript(t) })
		if bob.handle == alice.handle {
			t.Fatalf("bob's Main has alice's handle at xbind: %s", bob.handle)
		}
		ss := bob.sessions()
		if len(ss) != 1 || !ss[0].Main || ss[0].Handle != bob.handle {
			t.Errorf("bob's sessions: %+v, want his Main and nothing of alice's", ss)
		}
		turn := bob.say(`ws: workspace_map`)
		if got := turn.tool("workspace_map", 0); strings.Contains(got, wsNoteTile) || strings.Contains(got, wsLedgerTile) {
			t.Errorf("bob's agent's map lists a tile only alice can see:\n%s", got)
		}
		// alice's conversations aren't in bob's partition: her second session's id names nothing there
		for _, path := range []string{"/runs/" + ledger.run, "/runs/" + ledger.run + "/view", "/ws/sessions/" + ledger.run + "/cards"} {
			if r := d.Call(t, "GET", wsAPI+path, nil, bob.fr); r.Status != 404 && r.Status != 403 {
				t.Errorf("bob's page reads alice's session at %s: %d %s, want 404 or 403", path, r.Status, r)
			}
		}
		// ids are a partition's own (bob's Main may have the number of alice's): what bob reads at one is his
		var view struct {
			Run struct{ Owner string }
		}
		r := d.Must(t, "GET", wsAPI+"/runs/"+alice.run+"/view", nil, 200, alice.fr)
		if r.Decode(t, &view); view.Run.Owner != "alice" || !strings.Contains(string(r.Body), "by the agent") {
			t.Fatalf("alice's own view of her Main: owner %q, want hers, with what she asked in it", view.Run.Owner)
		}
		if r := d.Call(t, "GET", wsAPI+"/runs/"+alice.run+"/view", nil, bob.fr); r.Status == 200 {
			if r.Decode(t, &view); view.Run.Owner != "bob" || strings.Contains(string(r.Body), "by the agent") || strings.Contains(string(r.Body), wsNoteTile) {
				t.Errorf("bob's page reads alice's Main at its id: owner %q: %s", view.Run.Owner, r)
			}
		} else if r.Status != 404 && r.Status != 403 {
			t.Errorf("bob's page at the id of alice's Main: %d %s, want his own conversation, or 404 or 403", r.Status, r)
		}
		if got := bob.cards(); len(got.Requests) != 0 || len(got.Agents) != 0 || len(got.Briefs) != 0 {
			t.Errorf("bob's cards: %+v, want nothing of alice's requests, briefs and tile agents", got)
		}
		if needs := bob.needs(); len(needs) != 0 {
			t.Errorf("GET /ws/needs for bob: %+v, want nothing", needs)
		}
		// xbind keeps them apart too: the session's full view, alice's request, her tile agent
		conn := "/api/xbin/agents/connection?session=" + url.QueryEscape(alice.handle)
		d.Must(t, "GET", conn, nil, 200, alice.fr)
		if r := d.Call(t, "GET", conn, nil, bob.fr); r.Status != 404 {
			t.Errorf("bob's page reads the connection view of alice's session: %d %s, want 404", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/delegate/requests/"+noteRequest, nil, e.as("bob")...); r.Status != 404 && r.Status != 403 {
			t.Errorf("bob reads alice's request: %d %s, want 404 or 403", r.Status, r)
		}
		if got := e.requests("bob"); len(got) != 0 {
			t.Errorf("bob's requests at xbind: %+v, want none", got)
		}
		if got := e.tileAgents("bob"); len(got) != 0 {
			t.Errorf("bob's tile agents: %+v, want none (alice's is hers)", got)
		}
		parts := d.Must(t, "GET", "/api/xbin/partitions?tile="+wsAgent, nil, 200).String()
		if !strings.Contains(parts, "alice") || !strings.Contains(parts, "bob") {
			t.Errorf("the agent's partitions (GET /partitions?tile=%s): %s, want one of alice's and one of bob's", wsAgent, parts)
		}
	})

	t.Run("8 an admin's change set, applied in part", func(t *testing.T) {
		e.in(t)
		// not for someone who isn't an admin: alice can't turn it on, and her agent says so
		if r := d.Call(t, "PUT", "/api/xbin/delegate/state", map[string]any{"adminChanges": true}, e.as("alice")...); r.Status != 403 {
			t.Errorf("alice, no admin, turns admin changes on: %d %s, want 403", r.Status, r)
		}
		propose := `ws: admin_propose {"title":"Let two tiles use the note","changes":[` +
			`{"kind":"grant.add","args":{"from":"apps/welcome","target":"` + wsNoteTile + `","role":"reader"},"why":"welcome shows the note"},` +
			`{"kind":"grant.add","args":{"from":"` + wsLedgerTile + `","target":"` + wsNoteTile + `","role":"writer"},"why":"the ledger writes the note"}]}`
		if got := alice.say(propose).tool("admin_propose", 0); !strings.Contains(got, "isn't a workspace or org admin") {
			t.Errorf("admin_propose for alice: %q, want it to say she is no admin", got)
		}

		ada := e.open(wsAdmin, map[string]any{"main": true})
		t.Cleanup(func() { ada.transcript(t) })
		if got := ada.say(propose).tool("admin_propose", 0); !strings.Contains(got, "hasn't turned on") {
			t.Errorf("admin_propose before the admin opted in: %q, want it to say the preference is off", got)
		}
		var st map[string]any
		d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"adminChanges": true}, 200, e.as(wsAdmin)...).Decode(t, &st)
		if st["adminChanges"] != true {
			t.Fatalf("PUT /delegate/state {adminChanges: true} as an admin: %v", st)
		}
		grants := func() map[string]bool {
			var out struct {
				Grants []struct{ From, Target, Role string }
			}
			d.Must(t, "GET", "/api/xbin/grants", nil, 200).Decode(t, &out)
			have := map[string]bool{}
			for _, g := range out.Grants {
				have[g.From+" → "+g.Target+" as "+g.Role] = true
			}
			return have
		}
		const first, second = "apps/welcome → " + wsNoteTile + " as reader", wsLedgerTile + " → " + wsNoteTile + " as writer"

		turn := ada.say(propose)
		if got := turn.tool("admin_propose", 0); !strings.HasPrefix(got, "Change set ") || !strings.Contains(got, "Proposed; nothing is applied") {
			t.Fatalf("admin_propose: %q, want a proposed change set", got)
		}
		cards := ada.cards()
		if len(cards.Requests) != 1 || cards.Requests[0].Kind != "changeset" || cards.Requests[0].State != "proposed" || cards.Requests[0].Ref == "" {
			t.Fatalf("the change set's card: %+v, want one, proposed", cards.Requests)
		}
		ref := cards.Requests[0].Ref
		if needs := ada.needs(); len(needs) != 1 || needs[0].Kind != "changeset" || needs[0].Request != ref {
			t.Errorf("GET /ws/needs: %+v, want the change set %s", needs, ref)
		}
		type changeSet struct {
			ID, User, State string
			Items           []struct {
				N                         int
				Kind, Line, Result, Error string
			}
		}
		var sets struct{ Changesets []changeSet }
		d.Must(t, "GET", "/api/xbin/delegate/changesets", nil, 200, e.as(wsAdmin)...).Decode(t, &sets)
		if len(sets.Changesets) != 1 || sets.Changesets[0].ID != ref || sets.Changesets[0].State != "proposed" || len(sets.Changesets[0].Items) != 2 ||
			sets.Changesets[0].Items[0].N != 1 || sets.Changesets[0].Items[1].N != 2 || sets.Changesets[0].Items[0].Line == "" {
			t.Fatalf("the admin's change sets at xbind: %+v, want the one proposed, two items in xbind's own words", sets.Changesets)
		}
		if have := grants(); have[first] || have[second] {
			t.Fatalf("a proposed change set changed the grants: %v", have)
		}

		// only the admin, in their own session, decides it — and applies the part they tick
		approve := "/api/xbin/delegate/changesets/" + ref + "/approve"
		for who, hdr := range map[string][]xbindtest.Header{"the agent's own page": {ada.fr}, "alice": e.as("alice")} {
			if r := d.Call(t, "POST", approve, map[string]any{"items": []int{1, 2}}, hdr...); r.Status != 403 && r.Status != 404 {
				t.Errorf("%s approves the admin's change set: %d %s, want 403 or 404", who, r.Status, r)
			}
		}
		if r := d.Call(t, "POST", approve, map[string]any{"items": []int{}}, e.as(wsAdmin)...); r.Status != 400 {
			t.Errorf("approving none of it: %d %s, want 400 (nothing ticked applies nothing)", r.Status, r)
		}
		if have := grants(); have[first] || have[second] {
			t.Fatalf("a refused approval changed the grants: %v", have)
		}
		// The step-up: xbind applies an admin's change set only on a recent sign-in — under ten minutes old
		// (auth.EnrollFreshLogin) — or with their password again. Which of the two this run meets is the clock's:
		// xbind has no setting that shortens the limit and keeps sign-ins in memory only, so a real daemon's
		// session is stale only once ten minutes passed (in process the rule is covered with a back-dated
		// session: internal/broker/changeset_test.go). XBIN_E2E_STALE_SIGNIN=1 waits that long, so the stale
		// case runs here too; without it this part takes whichever case the run's own length makes true, and
		// checks xbind's choice against the sign-in's age either way.
		const recent = 10 * time.Minute
		if os.Getenv("XBIN_E2E_STALE_SIGNIN") != "" {
			t.Logf("waiting until %s's sign-in (%s ago) is older than %s", wsAdmin, time.Since(e.signedIn[wsAdmin]).Round(time.Second), recent)
			xbindtest.Eventually(t, recent+time.Minute, "the admin's sign-in is older than xbind's limit", func() (bool, string) {
				age := time.Since(e.signedIn[wsAdmin])
				return age > recent+3*time.Second, age.Round(time.Second).String()
			})
			e.agentPage(wsAdmin) // its backend may have stopped for idleness meanwhile: it answers again
		}
		after := ada.last()
		body := map[string]any{"items": []int{1}} // the first line ticked, the second left out
		age := time.Since(e.signedIn[wsAdmin])
		r := d.Call(t, "POST", approve, body, e.as(wsAdmin)...)
		steppedUp := r.Status == 403 && strings.Contains(r.String(), `"stepUp"`)
		switch {
		case steppedUp && age < recent-30*time.Second:
			t.Errorf("xbind asked for a recent sign-in %s after the admin signed in: %s", age.Round(time.Second), r)
		case !steppedUp && age > recent+time.Second:
			t.Fatalf("approving with a sign-in %s old: %d %s, want the step-up (403 {stepUp})", age.Round(time.Second), r.Status, r)
		}
		if steppedUp {
			// a stale sign-in: the password is asked for, nothing was applied, and the set is still open
			t.Logf("the admin's sign-in is %s old; xbind asked for the step-up: %s", age.Round(time.Second), r)
			if !strings.Contains(r.String(), `"stepUp":"password"`) || !strings.Contains(r.String(), "approve admin changes") {
				t.Errorf("the step-up: %s, want {stepUp: password} saying what it is for", r)
			}
			body["password"] = "not-" + wsPassword(wsAdmin)
			if r := d.Call(t, "POST", approve, body, e.as(wsAdmin)...); r.Status != 403 || !strings.Contains(r.String(), "wrong password") {
				t.Errorf("the step-up with a wrong password: %d %s, want 403", r.Status, r)
			}
			if have := grants(); have[first] || have[second] {
				t.Fatalf("a refused step-up changed the grants: %v", have)
			}
			d.Must(t, "GET", "/api/xbin/delegate/changesets", nil, 200, e.as(wsAdmin)...).Decode(t, &sets)
			if len(sets.Changesets) != 1 || sets.Changesets[0].State != "proposed" {
				t.Fatalf("the change set after a refused step-up: %+v, want it still proposed", sets.Changesets)
			}
			// the chrome asks for the password and sends the same choice again
			body["password"] = wsPassword(wsAdmin)
			r = d.Call(t, "POST", approve, body, e.as(wsAdmin)...)
		} else {
			t.Logf("the admin's sign-in is %s old, recent enough: no step-up (XBIN_E2E_STALE_SIGNIN=1 runs the stale case here)", age.Round(time.Second))
		}
		var applied changeSet
		if r.Decode(t, &applied); r.Status != 200 || applied.State != "applied" || len(applied.Items) != 2 ||
			applied.Items[0].Result != "applied" || applied.Items[1].Result != "rejected" {
			t.Fatalf("approving item 1 of 2: %d %s, want the set applied, item 1 applied and item 2 left out", r.Status, r)
		}
		if have := grants(); !have[first] || have[second] {
			t.Errorf("the grants after applying item 1 of 2: %v, want %q and not %q", have, first, second)
		}
		told := ada.woken(after, time.Minute, "the admin decided the change set", "your person decided", "1. [grant.add]", "applied", "left out by your person")
		t.Logf("the update: %s", told)
		if c := ada.cards(); len(c.Requests) != 1 || c.Requests[0].State != "applied" || c.Requests[0].Result != "1 of 1 changes applied" || c.Requests[0].By != "person" {
			t.Errorf("the change set's card after the decision: %+v, want applied, 1 of 1, by the person", c.Requests)
		}
		var change bool
		rows := e.audit(wsAdmin)
		for _, row := range rows {
			change = change || row.Class == "changeset:grant.add"
		}
		if !change {
			t.Errorf("the admin's log has no applied change (class changeset:grant.add):\n%+v", rows)
		}
	})
}

// state is person's GET /delegate/state.
func (e *wsEnv) state(person string) map[string]any {
	e.t.Helper()
	var out map[string]any
	e.d.Must(e.t, "GET", "/api/xbin/delegate/state", nil, 200, e.as(person)...).Decode(e.t, &out)
	return out
}
