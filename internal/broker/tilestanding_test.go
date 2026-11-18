package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// standing is GET /delegate/tile's answer, as the test reads it.
type standing struct {
	Tile, Title, Kind, Runtime, Level, Agent string
	Orgs                                     []string
	MCP                                      bool
	Owner                                    *server.StandingOwner
	Wiring                                   bool
	Slots                                    []server.StandingSlot
	Uses                                     []server.StandingUse
	Provides                                 []server.StandingProvide
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

// covers D246 — GET /delegate/tile returns nothing the reads it replaces
// wouldn't have given this agent: for a tile at each agent level and a
// person who owns it, administers the workspace or only reads it, every
// field is in the union of the map's row, GET /components/<path>, GET
// /logs (as the agent), and the person's own GET /owner, /bindings,
// /grants and the tile terminal's GET /cron/jobs — and the gate refuses
// what it refuses for those reads (a tile the person can't see, one
// outside the conversation's scope) and taints the conversation with the
// tile's orgs as a read does.
func TestTileStandingIsUnionOfReads(t *testing.T) {
	w := accessWorld(t)
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"writer"}],
		"interfaces":{"llm":{"kind":"http","service":"openai","required":true},"data":{"kind":"http"}},"provides":{"api":{"kind":"http","service":"x"}}}`})
	w.rescan()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		ws.Bindings["apps/x"] = map[string]registry.Binding{"data": {{Ref: "apps/pg"}}}
	}); err != nil {
		t.Fatal(err)
	}
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
	for _, level := range []string{"full", "read", "off"} {
		if level != "full" {
			w.write(map[string]string{"apps/x/xbin.json": strings.Replace(string(readFile(t, filepath.Join(w.root, "apps/x/xbin.json"))), `{"runtime":"go"`, `{"runtime":"go","agent":"`+level+`"`, 1)})
			w.rescan()
		}
		for _, user := range []string{"alice", "bob", "carol"} {
			_, person := w.session(user)
			var st standing
			if code := w.getJSON(t, "/delegate/tile?tile=apps/x", agentOf(user), &st); code != 200 {
				t.Fatalf("%s at %s: %d", user, level, code)
			}
			where := user + " at agent level " + level
			// the map's row
			var m struct{ Tiles []map[string]any }
			w.getJSON(t, "/delegate/map", agentOf(user), &m)
			i := slices.IndexFunc(m.Tiles, func(r map[string]any) bool { return r["tile"] == "apps/x" })
			if i < 0 {
				t.Fatalf("%s: not on the map", where)
			}
			row := m.Tiles[i]
			if row["title"] != st.Title || row["kind"] != st.Kind || row["agent"] != st.Agent || len(st.Orgs) != len(row["orgs"].([]any)) {
				t.Errorf("%s: the map says %v, the read %+v", where, row, st)
			}
			var comp struct{ Component struct{ Runtime string } }
			w.getJSON(t, "/components/apps/x", agentOf(user), &comp)
			if comp.Component.Runtime != st.Runtime {
				t.Errorf("%s: runtime %q, the component's %q", where, st.Runtime, comp.Component.Runtime)
			}
			// the owner, as the person reads it — a name only where they see accounts, or it is themselves
			var own struct{ Owner string }
			w.getJSON(t, "/owner?tile=apps/x", person, &own)
			if st.Owner == nil || st.Owner.Kind+":"+st.Owner.ID != own.Owner || st.Owner.Name != "" && user == "carol" {
				t.Errorf("%s: owner %+v, GET /owner %q", where, st.Owner, own.Owner)
			}
			// bindings and grants: the person's own views
			var binds struct {
				Bindings map[string]map[string]registry.Binding
			}
			bcode := w.getJSON(t, "/bindings", person, &binds)
			_, inView := binds.Bindings["apps/x"]
			if st.Wiring != (bcode == 200 && (inView || user != "carol")) {
				t.Errorf("%s: wiring %v, the person's bindings view %d (apps/x in it: %v)", where, st.Wiring, bcode, inView)
			}
			for _, sl := range st.Slots {
				if sl.Bound == nil {
					continue
				}
				got := binds.Bindings["apps/x"][sl.Slot].Refs()
				if strings.Join(got, ",") != strings.Join(sl.Bound, ",") || !st.Wiring {
					t.Errorf("%s: slot %s bound to %v, the person's view %v", where, sl.Slot, sl.Bound, got)
				}
			}
			var grants struct {
				Grants  []registry.Grant
				Pending []registry.Grant
			}
			w.getJSON(t, "/grants", person, &grants)
			for _, u := range st.Uses {
				if u.Granted && !slices.ContainsFunc(grants.Grants, func(g registry.Grant) bool { return g.From == "apps/x" && g.Target == u.Target }) {
					t.Errorf("%s: %s is granted, and the person's grants view doesn't say so", where, u.Target)
				}
				if u.Pending && !slices.ContainsFunc(grants.Pending, func(g registry.Grant) bool { return g.From == "apps/x" && g.Target == u.Target }) {
					t.Errorf("%s: %s is pending, and the person's grants view doesn't say so", where, u.Target)
				}
				if (u.Granted || u.Pending) && !st.Wiring {
					t.Errorf("%s: a use's grant said without wiring", where)
				}
			}
			// scheduled jobs: as the tile's own terminal of this person lists them, or an admin
			if st.Cron != nil {
				p := personP(t, w.partWS, user)
				term := auth.Principal{UserID: user, User: p.User, Access: p.Access, Component: "apps/x", Via: "terminal"}
				if !p.CanTerminalTile("apps/x") && !p.IsAdmin() {
					t.Errorf("%s: scheduled jobs for a person who can't open a terminal on the tile", where)
				}
				r := httptest.NewRequest("GET", "/api/xbin/cron/jobs", nil).WithContext(auth.WithPrincipal(context.Background(), term))
				rec := httptest.NewRecorder()
				w.b.apiCronList(rec, r)
				for _, j := range st.Cron {
					if !strings.Contains(rec.Body.String(), `"name":"`+j.Name+`"`) {
						t.Errorf("%s: job %s isn't in the terminal's listing %s", where, j.Name, rec.Body.String())
					}
				}
			}
			// the log: GET /logs as the agent
			lcode, ltext := w.do("GET", "/logs?component=apps/x", "", agentOf(user))
			if st.Log.Shown != (lcode == 200) {
				t.Errorf("%s: log shown %v, GET /logs %d", where, st.Log.Shown, lcode)
			}
			for _, l := range st.Log.Lines {
				if !strings.Contains(ltext, l) {
					t.Errorf("%s: log line %q isn't in GET /logs", where, l)
				}
			}
			if user == "carol" && (st.Wiring || st.Cron != nil || st.Log.Shown || len(st.Log.Lines) > 0) {
				t.Errorf("%s: a reader reads wiring %v, jobs %v, the log %v", where, st.Wiring, st.Cron, st.Log)
			}
		}
	}
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
	// the conversation now holds the orgs of what it read
	if code, _ := w.do("GET", "/delegate/tile?tile=apps/pg", "", agentOf("bob")); code != 200 {
		t.Fatalf("bob reads apps/pg: %d", code)
	}
	if h, ok := csHandles.Load("bob"); ok {
		s, _ := w.store.AgentSession("bob", "apps/q", h.(string))
		if !slices.Contains(s.Tainted, "fin") {
			t.Errorf("reading fin's tile left the conversation untainted: %v", s.Tainted)
		}
	}
}
