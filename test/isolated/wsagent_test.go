//go:build linux && integration

package isolated

// wsagent_test.go — the workspace agent (xbin/wsagent; D231, D227, D228,
// D232) end to end, on an xbind as an install runs one: this tree's
// binary with its release table and prebuilt backend, booted without
// --dev under --isolate with owner auth and the vault; hack/fakeopenai as
// the LLM connector's upstream (no real provider, no key that is one).

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const wsAgent = "xbin/wsagent"

// wsEnv is a production xbind with people, and what the tests ask of it.
type wsEnv struct {
	*sealedEnv
	t *testing.T
}

func startWsAgent(t *testing.T) *wsEnv {
	t.Helper()
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		xbindtest.Skip(t, "no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	a = a.WithSealedBuilds(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Production: true,
		Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	return &wsEnv{sealedEnv: &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}, t: t}
}

func (e *wsEnv) person(id, role string, tiles map[string]string) {
	e.t.Helper()
	e.d.AddUser(e.t, id, "pw-"+id+"-5c2e81", role, tiles)
	e.sess[id] = e.d.Login(e.t, id, "pw-"+id+"-5c2e81")
}

// state is person's GET /delegate/state.
func (e *wsEnv) state(person string) map[string]any {
	e.t.Helper()
	var out map[string]any
	e.d.Must(e.t, "GET", "/api/xbin/delegate/state", nil, 200, e.as(person)...).Decode(e.t, &out)
	return out
}

// backends lists the running backends of the workspace agent.
func (e *wsEnv) backends() []string {
	e.t.Helper()
	var rt struct {
		Backends []struct{ Path, State, Partition string }
	}
	e.d.Must(e.t, "GET", "/api/xbin/runtime", nil, 200).Decode(e.t, &rt)
	var out []string
	for _, b := range rt.Backends {
		if b.Path == wsAgent {
			out = append(out, b.State+":"+b.Partition)
		}
	}
	return out
}

func TestWorkspaceAgentExplore(t *testing.T) {
	e := startWsAgent(t)
	d := e.d
	e.person("root2", "admin", nil)
	e.person("alice", "user", map[string]string{"*": "write"})
	e.person("bob", "user", nil)

	t.Logf("sealed: %s", d.Must(t, "GET", "/api/xbin/sealed", nil, 200))
	t.Logf("alice state: %v", e.state("alice"))
	t.Logf("admin state: %v", e.state("root2"))
	t.Logf("backends: %v", e.backends())
	t.Logf("alice components: %.300s", d.Call(t, "GET", "/api/xbin/components", nil, e.as("alice")...))
	t.Logf("alice alerts: %s", d.Call(t, "GET", "/api/xbin/alerts", nil, e.as("alice")...))
	r := d.Call(t, "GET", "/api/xbin/frame-token?component="+wsAgent, nil, e.as("alice")...)
	t.Logf("alice frame token before access: %d %s", r.Status, r)

	fake := xbindtest.FakeOpenAI(t, d.A)
	d.Must(t, "PUT", "/api/xbin/llm/upstreams/fake", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-WSAGENT-0000"}, 200)
	d.Must(t, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "fake/fake-chat"}}, 200)
	t.Logf("workspace on: %s", d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "me"}, 200, e.as("root2")...))
	t.Logf("everyone: %s", d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"who": "everyone"}, 200, e.as("root2")...))
	t.Logf("alice state: %v", e.state("alice"))
	t.Logf("sealed: %s", d.Must(t, "GET", "/api/xbin/sealed", nil, 200))

	const tile = "apps/wsnote"
	if err := d.WriteFiles(tile, map[string]string{
		"go.mod":          "module ws/note\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": sdNote,
		"index.html":      "<!doctype html><html><head></head><body>note</body></html>\n",
		"API.md":          "# Note\n\nGET /note reads the note. PUT /note replaces it with the body.\n",
	}); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"runtime":"go","agent":"full","expose":{"roles":{"reader":"read","writer":"write"}}}` + "\n"})
	d.WaitComponent(t, tile)

	fr := e.frame(t, wsAgent, "alice")
	api := "/api/" + wsAgent
	xbindtest.Eventually(t, 3*time.Minute, "alice's agent answers /ws/state", func() (bool, string) {
		r := d.Call(t, "GET", api+"/ws/state", nil, fr)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	t.Logf("runtime: %.1500s", d.Must(t, "GET", "/api/xbin/runtime", nil, 200))
	var made struct {
		Session struct {
			ID     int64
			Handle string
		}
	}
	d.Must(t, "POST", api+"/ws/sessions", map[string]any{"main": true}, 200, fr).Decode(t, &made)
	run := fmt.Sprint(made.Session.ID)
	say := func(text string) {
		t.Helper()
		r := d.Call(t, "POST", api+"/runs/"+run+"/message", map[string]any{"text": text}, fr)
		t.Logf("message %q: %d %.300s", text, r.Status, r)
		xbindtest.Eventually(t, 2*time.Minute, "the turn ends", func() (bool, string) {
			var ss struct {
				Sessions []struct {
					ID     int64
					Status string
				}
			}
			d.Must(t, "GET", api+"/ws/sessions", nil, 200, fr).Decode(t, &ss)
			return len(ss.Sessions) > 0 && ss.Sessions[0].Status == "idle", fmt.Sprint(ss)
		})
		time.Sleep(4 * time.Second)
		_ = os.WriteFile(os.Getenv("WS_DUMP")+"/view.json", d.Call(t, "GET", api+"/runs/"+run+"/view", nil, fr).Body, 0o644)
		_ = os.WriteFile(os.Getenv("WS_DUMP")+"/xbind.log", []byte(d.LogTail(4000)), 0o644)
		v := d.Call(t, "GET", api+"/runs/"+run+"/view", nil, fr).String()
		if i := strings.Index(v, `"events"`); i > 0 {
			v = v[i:]
		}
		if len(v) > 6000 {
			v = v[len(v)-6000:]
		}
		t.Logf("view tail: %s", v)
		t.Logf("run: %.1500s", d.Call(t, "GET", api+"/runs/"+run, nil, fr))
	}
	say(`ws: workspace_map ;; tile_call {"tile":"apps/wsnote","method":"GET","path":"/note"} ;; tile_call {"tile":"apps/wsnote","method":"PUT","path":"/note","body":"\"by the agent\"","why":"set the note"}`)
	t.Logf("cards: %.2000s", d.Call(t, "GET", api+"/ws/sessions/"+run+"/cards", nil, fr))
	t.Logf("requests: %.2000s", d.Call(t, "GET", "/api/xbin/delegate/requests", nil, e.as("alice")...))
	t.Logf("needs: %.1000s", d.Call(t, "GET", api+"/ws/needs", nil, fr))
	t.Logf("fake requests: %.3000s", httpGet(t, fake+"/debug/requests"))
	t.Logf("xbind log tail:\n%s", d.LogTail(80))
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
