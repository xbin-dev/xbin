package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// standing is GET /delegate/tile's answer, as the test reads it.
type standing struct {
	Tile, Title, Kind, Runtime, Level, Agent string
	Orgs                                     []string
	MCP                                      bool
	Owner                                    *server.StandingOwner
	Wiring, Unmet                            bool
	Slots                                    []server.StandingSlot
	Uses                                     []server.StandingUse
	Provides                                 []server.StandingProvide
	Exposes                                  []server.StandingExpose
	Cron                                     []server.StandingJob
	Log                                      server.StandingLog
	Sessions                                 []json.RawMessage
}

func (w *csWorld) getJSON(t *testing.T, path string, cred func(*http.Request), out any) int {
	t.Helper()
	code, body := w.do("GET", path, "", cred)
	if code == 200 && out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			t.Fatalf("%s: %v %s", path, err, body)
		}
	}
	return code
}

// freshAgent is a new conversation of user's agent: its credential and
// its handle (what the conversation is tainted with is read from it).
func (w *csWorld) freshAgent(t *testing.T, user string, scope *delegation.SessionScope) (func(*http.Request), string) {
	t.Helper()
	h, err := w.store.NewAgentSession(user, "apps/q", scope, "local/test")
	if err != nil {
		t.Fatal(err)
	}
	return func(r *http.Request) { agentOf(user)(r); r.Header.Set(server.HeaderAgentSession, h.ID) }, h.ID
}

func (w *csWorld) taintOf(t *testing.T, user, handle string) []string {
	t.Helper()
	s, _ := w.store.AgentSession(user, "apps/q", handle)
	return s.Tainted
}

// covers D246 (review M2) — GET /delegate/tile returns nothing this
// agent's own credential wouldn't get from the delegate reads it replaces:
// for each agent level, the admin opt-in off and on, and a person who owns
// the tile, administers the workspace, administers its org or only reads
// it, every field is in the union of — all read AS THE AGENT — the map's
// row, GET /components (the owner), GET /components/<path>, GET /code/file
// (the manifest), GET /bindings and GET /grants (admin reads: an admin who
// opted in), GET /logs, the account lookup (a name); the scheduled jobs,
// which no delegate read gives, only at GET /logs' gate and as the
// person's terminal on the tile lists them. A bound provider the
// conversation can't see (another org's rule, outside its scope) is
// "another tile"; one it can see taints it, as reading it would; the gate
// refuses what the reads refuse.
func TestTileStandingIsUnionOfReads(t *testing.T) {
	w := accessWorld(t)
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"writer"}],
		"interfaces":{"llm":{"kind":"http","service":"openai","required":true},"data":{"kind":"http"}},"provides":{"api":{"kind":"http","service":"x"}}}`})
	w.rescan()
	w.bindSet(t, "apps/x", "data", "apps/pg")
	if err := w.b.cron.add(cronJob{Name: "nightly", Schedule: "@every 1h", Component: "apps/x", Path: "/tick"}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(w.root, ".xbin", "log", util.CompKey("apps/x")+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("starting\npanic: assignment to entry in nil map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = w.b.Users.SetUserTile("erin", "apps/pg", "") // erin reads apps/pg as fin's admin
	seen := map[string]int{}
	for _, level := range []string{"full", "read", "off"} {
		if level != "full" {
			w.write(map[string]string{"apps/x/xbin.json": strings.Replace(string(readFile(t, filepath.Join(w.root, "apps/x/xbin.json"))), `{"runtime":"go"`, `{"runtime":"go","agent":"`+level+`"`, 1)})
			w.rescan()
		}
		for _, optIn := range []bool{false, true} {
			for _, user := range []string{"alice", "bob", "carol", "erin"} {
				if _, err := w.store.SetState(user, func(st *delegation.State) { st.AdminChanges = optIn }); err != nil {
					t.Fatal(err)
				}
				for _, tile := range []string{"apps/x", "apps/pg"} {
					cred, handle := w.freshAgent(t, user, nil)
					var st standing
					code := w.getJSON(t, "/delegate/tile?tile="+tile, cred, &st)
					where := fmt.Sprintf("%s on %s, agent level %s, opt-in %v", user, tile, level, optIn)
					if pcode := w.getJSON(t, "/components/"+tile, cred, nil); code != pcode {
						t.Fatalf("%s: the read %d, GET /components/<path> %d", where, code, pcode)
					}
					if code != 200 {
						continue
					}
					seen[fmt.Sprintf("wiring=%v", st.Wiring)]++
					// the conversation holds the orgs of what it was shown: the tile's, and a bound provider's it can see
					tainted := w.taintOf(t, user, handle)
					for _, o := range st.Orgs {
						if !slices.Contains(tainted, o) {
							t.Errorf("%s: the tile's org %s isn't in the conversation's taint %v", where, o, tainted)
						}
					}
					for _, sl := range st.Slots {
						if slices.Contains(sl.Bound, "apps/pg") && !slices.Contains(tainted, "fin") {
							t.Errorf("%s: apps/pg was named and the conversation isn't tainted with fin: %v", where, tainted)
						}
					}
					if tile == "apps/x" && !st.Wiring && slices.Contains(tainted, "fin") {
						t.Errorf("%s: tainted with fin by a provider it wasn't shown: %v", where, tainted)
					}
					w.unionCheck(t, where, user, tile, cred, st) // (its reads taint the conversation on their own)
				}
			}
		}
	}
	if seen["wiring=true"] == 0 || seen["wiring=false"] == 0 {
		t.Fatalf("the corpus doesn't cover both: %v", seen)
	}
	// a provider the conversation can't see is "another tile", and taints nothing: outside its scope, or its org's rule
	scoped, handle := w.freshAgent(t, "bob", &delegation.SessionScope{Tiles: []string{"apps/x"}, Orgs: []string{}})
	var st standing
	if code := w.getJSON(t, "/delegate/tile?tile=apps/x", scoped, &st); code != 200 || !st.Wiring || boundOf(st, "data") != "another tile" || slices.Contains(w.taintOf(t, "bob", handle), "fin") {
		t.Errorf("a provider outside the conversation's scope: %d %+v %v", code, st.Slots, w.taintOf(t, "bob", handle))
	}
	w.srv.AgentPolicy = delegation.OpenPolicy{Deny: map[string]error{"apps/pg": errors.New("fin keeps agents out")}}
	cred, _ := w.freshAgent(t, "bob", nil)
	if code := w.getJSON(t, "/delegate/tile?tile=apps/x", cred, &st); code != 200 || boundOf(st, "data") != "another tile" {
		t.Errorf("a provider its org's rule keeps the agent from: %d %+v", code, st.Slots)
	}
	w.srv.AgentPolicy = delegation.OpenPolicy{}
	// what the gate refuses: a tile alice's conversation is scoped away from; a tile carol can't read
	if code, body := w.do("GET", "/delegate/tile?tile=apps/x", "", w.scoped(t, "alice", "apps/pu")); code != 403 || !strings.Contains(body, `"hidden"`) {
		t.Errorf("a tile outside the conversation's scope: %d %s", code, body)
	}
	if code, body := w.do("GET", "/delegate/tile?tile=apps/pu", "", agentOf("carol")); code != 403 || !strings.Contains(body, `"tile"`) {
		t.Errorf("a tile carol can't read: %d %s", code, body)
	}
	if code, _ := w.do("GET", "/delegate/tile?tile=apps/nope", "", agentOf("carol")); code != 403 {
		t.Errorf("a tile that isn't there answers otherwise: %d", code)
	}
	w.srv.AgentPolicy = delegation.OpenPolicy{Deny: map[string]error{"apps/x": errors.New("the org keeps agents out")}}
	if code, body := w.do("GET", "/delegate/tile?tile=apps/x", "", agentOf("bob")); code != 403 || !strings.Contains(body, "keeps agents out") {
		t.Errorf("a tile the org's rules block: %d %s", code, body)
	}
	w.srv.AgentPolicy = delegation.OpenPolicy{}
}

func boundOf(st standing, slot string) string {
	for _, sl := range st.Slots {
		if sl.Slot == slot {
			return strings.Join(sl.Bound, ",")
		}
	}
	return ""
}

// unionCheck holds one answer of GET /delegate/tile to the reads it
// replaces, each made with the same agent credential (cred).
func (w *csWorld) unionCheck(t *testing.T, where, user, tile string, cred func(*http.Request), st standing) {
	t.Helper()
	// the map's row
	var m struct{ Tiles []map[string]any }
	w.getJSON(t, "/delegate/map", cred, &m)
	i := slices.IndexFunc(m.Tiles, func(r map[string]any) bool { return r["tile"] == tile })
	if i < 0 {
		t.Fatalf("%s: not on the map", where)
	}
	if row := m.Tiles[i]; row["title"] != st.Title || row["kind"] != st.Kind || row["agent"] != st.Agent || len(st.Orgs) != len(row["orgs"].([]any)) {
		t.Errorf("%s: the map says %v, the read %+v", where, row, st)
	}
	// the owner: GET /components' row; a person's name as the lookup gives it, an org's for the person's own
	var comps []struct{ Path, Owner string }
	w.getJSON(t, "/components", cred, &comps)
	ci := slices.IndexFunc(comps, func(c struct{ Path, Owner string }) bool { return c.Path == tile })
	if ci < 0 || st.Owner == nil || strings.TrimPrefix(st.Owner.Kind+":"+st.Owner.ID, "workspace:") != comps[ci].Owner {
		t.Errorf("%s: owner %+v, GET /components %+v", where, st.Owner, comps)
	}
	if st.Owner != nil && st.Owner.Name != "" {
		switch st.Owner.Kind {
		case users.OwnerKindUser:
			_, body := w.do("POST", "/delegate/accounts/lookup", `{"name":"`+st.Owner.ID+`"}`, cred)
			if st.Owner.ID != user && !strings.Contains(body, `"displayName":"`+st.Owner.Name+`"`) {
				t.Errorf("%s: the owner's name %q, the lookup %s", where, st.Owner.Name, body)
			}
		case users.OwnerKindOrg:
			var mo struct{ Orgs []struct{ ID, Name string } }
			w.getJSON(t, "/delegate/map", cred, &mo)
			if !slices.ContainsFunc(mo.Orgs, func(o struct{ ID, Name string }) bool { return o.ID == st.Owner.ID && o.Name == st.Owner.Name }) {
				t.Errorf("%s: the owning org's name %q isn't on the map's orgs %v", where, st.Owner.Name, mo.Orgs)
			}
		}
	}
	// what it is and asks for: GET /components/<path> and its manifest, GET /code/file
	var comp struct {
		Component struct {
			Runtime string
			Uses    []struct{ Target, Role string }
		}
	}
	w.getJSON(t, "/components/"+tile, cred, &comp)
	if comp.Component.Runtime != st.Runtime {
		t.Errorf("%s: runtime %q, the component's %q", where, st.Runtime, comp.Component.Runtime)
	}
	for _, u := range st.Uses {
		if !slices.Contains(comp.Component.Uses, struct{ Target, Role string }{u.Target, u.Role}) {
			t.Errorf("%s: use %+v isn't in GET /components/<path>", where, u)
		}
	}
	if len(st.Slots)+len(st.Provides)+len(st.Exposes) > 0 {
		var file struct{ Content string }
		fcode := w.getJSON(t, "/code/file?component="+tile+"&file=xbin.json", cred, &file)
		manifest := file.Content
		if fcode != 200 {
			t.Errorf("%s: slots said, and the agent can't read the manifest: %d", where, fcode)
		}
		for _, sl := range st.Slots {
			if !strings.Contains(manifest, `"`+sl.Slot+`"`) {
				t.Errorf("%s: slot %s isn't in the manifest the agent reads", where, sl.Slot)
			}
		}
		for _, pr := range st.Provides {
			if !strings.Contains(manifest, `"`+pr.Slot+`"`) {
				t.Errorf("%s: provide %s isn't in the manifest the agent reads", where, pr.Slot)
			}
		}
	}
	// wiring: this agent's own admin reads
	var binds struct {
		Bindings map[string]map[string]registry.Binding
	}
	bcode := w.getJSON(t, "/bindings", cred, &binds)
	var grants struct {
		Grants  []registry.Grant
		Pending []registry.Grant
	}
	gcode := w.getJSON(t, "/grants", cred, &grants)
	_, inView := binds.Bindings[tile]
	if st.Wiring && (bcode != 200 || gcode != 200) {
		t.Errorf("%s: wiring said, and the agent's GET /bindings %d, GET /grants %d", where, bcode, gcode)
	}
	if !st.Wiring && bcode == 200 && inView {
		t.Errorf("%s: the agent's GET /bindings has the tile and the read says no wiring", where)
	}
	if st.Unmet && st.Wiring {
		t.Errorf("%s: unmet said beside the wiring", where)
	}
	for _, sl := range st.Slots {
		if sl.Bound == nil {
			continue
		}
		got := binds.Bindings[tile][sl.Slot].Refs()
		if !st.Wiring || len(got) != len(sl.Bound) {
			t.Errorf("%s: slot %s bound to %v, the agent's view %v", where, sl.Slot, sl.Bound, got)
			continue
		}
		for k := range got {
			if sl.Bound[k] != got[k] && sl.Bound[k] != "another tile" {
				t.Errorf("%s: slot %s bound to %v, the agent's view %v", where, sl.Slot, sl.Bound, got)
			}
		}
	}
	for _, u := range st.Uses {
		if (u.Granted || u.Pending) && !st.Wiring {
			t.Errorf("%s: a use's grant said without wiring", where)
		}
		if u.Granted && !slices.ContainsFunc(grants.Grants, func(g registry.Grant) bool { return g.From == tile && g.Target == u.Target }) {
			t.Errorf("%s: %s is granted, and the agent's grants view doesn't say so", where, u.Target)
		}
		if u.Pending && !slices.ContainsFunc(grants.Pending, func(g registry.Grant) bool { return g.From == tile && g.Target == u.Target }) {
			t.Errorf("%s: %s is pending, and the agent's grants view doesn't say so", where, u.Target)
		}
	}
	// the log: GET /logs as the agent; the jobs at the same gate, as the person's terminal on the tile lists them
	lcode, ltext := w.do("GET", "/logs?component="+tile, "", cred)
	if st.Log.Shown != (lcode == 200) {
		t.Errorf("%s: log shown %v, GET /logs %d", where, st.Log.Shown, lcode)
	}
	for _, l := range st.Log.Lines {
		if !strings.Contains(ltext, l) {
			t.Errorf("%s: log line %q isn't in GET /logs", where, l)
		}
	}
	if len(st.Cron) > 0 {
		if lcode != 200 {
			t.Errorf("%s: scheduled jobs where the agent's GET /logs answers %d", where, lcode)
		}
		p := personP(t, w.partWS, user)
		term := auth.Principal{UserID: user, User: p.User, Access: p.Access, Component: tile, Via: "terminal"}
		r := httptest.NewRequest("GET", "/api/xbin/cron/jobs", nil).WithContext(auth.WithPrincipal(context.Background(), term))
		rec := httptest.NewRecorder()
		w.b.apiCronList(rec, r)
		for _, j := range st.Cron {
			if !strings.Contains(rec.Body.String(), `"name":"`+j.Name+`"`) {
				t.Errorf("%s: job %s isn't in the terminal's listing %s", where, j.Name, rec.Body.String())
			}
		}
	}
}
