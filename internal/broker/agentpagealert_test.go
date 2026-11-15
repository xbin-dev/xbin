package broker

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

type fakeAgentPage struct {
	seen map[string]bool
	fail bool
}

func (f *fakeAgentPage) AgentPageNotice(user string) (string, bool) {
	return "your agent is at /xbin/agent", !f.seen[user]
}

func (f *fakeAgentPage) AgentPageSeen(user string) error {
	if f.fail {
		return errors.New("disk")
	}
	f.seen[user] = true
	return nil
}

// covers D232 — the one notice that names the workspace agent's page: GET
// /alerts carries kind agent-page, with its dismiss route, for a person —
// their own session, or the shell they drive (a trusted chrome frame minted
// under their own login) — and for nobody else: not the root token, a
// view-as session, an ordinary tile's frame, a terminal, an instance or the
// agent itself. POST /delegate/page/dismiss ends it for that person alone,
// from the same two kinds of caller.
func TestAgentPageAlert(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	page := &fakeAgentPage{seen: map[string]bool{}}
	fx.b.agentPage = page
	ps["carolAgent"] = auth.Principal{UserID: "carol", User: ps["carol"].User, Access: ps["carol"].Access, Agent: "xbin/wsagent", Via: "delegate"}
	ps["ownerShell"] = auth.Principal{Component: "shell", Via: "frame", Gen: "o.1"} // the root token's login: nobody's agent's person
	has := func(p auth.Principal) bool {
		w := call(t, fx.b.apiAlerts, p, "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("alerts: %d %s", w.Code, w.Body)
		}
		for _, a := range out.Alerts {
			if a.Kind == server.AgentPageAlertKind {
				if a.Dismiss != server.AgentPageDismissRoute || a.Level != "warn" || a.System || a.Tile != "" {
					t.Fatalf("the alert: %+v", a)
				}
				return true
			}
		}
		return false
	}
	want := map[string]bool{
		"carol": true, "bob": true, "root2": true, "shellC": true, "shellA": true,
		"root": false, "ownerShell": false, "viewAs": false, "frame": false, "instance": false, "terminal": false, "agent": false,
		"wsTerm": false, "carolAgent": false,
		"consoleB": false, // a tile's frame whose chrome no admin approved here: an ordinary tile's
	}
	for who, w := range want {
		if got := has(ps[who]); got != w {
			t.Errorf("%s: alert %v, want %v", who, got, w)
		}
	}
	// only a person, or the shell they drive, dismisses — and only their own
	for _, who := range []string{"root", "viewAs", "frame", "instance", "terminal", "carolAgent", "ownerShell"} {
		if w := call(t, fx.b.apiAgentPageDismiss, ps[who], "POST", server.AgentPageDismissRoute, "", nil); w.Code != 403 {
			t.Errorf("%s dismisses: %d", who, w.Code)
		}
	}
	if len(page.seen) != 0 {
		t.Fatalf("dismissed by a refused caller: %v", page.seen)
	}
	if w := call(t, fx.b.apiAgentPageDismiss, ps["shellC"], "POST", server.AgentPageDismissRoute, "", nil); w.Code != 200 {
		t.Fatalf("carol's shell dismisses: %d %s", w.Code, w.Body)
	}
	if has(ps["carol"]) || has(ps["shellC"]) || !has(ps["bob"]) || !has(ps["shellA"]) {
		t.Errorf("after carol's shell dismissed: carol %v, her shell %v, bob %v, an admin's shell %v", has(ps["carol"]), has(ps["shellC"]), has(ps["bob"]), has(ps["shellA"]))
	}
	if w := call(t, fx.b.apiAgentPageDismiss, ps["bob"], "POST", server.AgentPageDismissRoute, "", nil); w.Code != 200 || has(ps["bob"]) {
		t.Errorf("bob dismisses in his own session: %d", w.Code)
	}
	page.fail = true
	if w := call(t, fx.b.apiAgentPageDismiss, ps["root2"], "POST", server.AgentPageDismissRoute, "", nil); w.Code != 500 || !has(ps["root2"]) {
		t.Errorf("a failed write: %d", w.Code)
	}
	// no source (a Server without delegation): no alert, and nothing to dismiss
	fx.b.agentPage = nil
	if has(ps["root2"]) {
		t.Error("an alert without a source")
	}
	if w := call(t, fx.b.apiAgentPageDismiss, ps["root2"], "POST", server.AgentPageDismissRoute, "", nil); w.Code != 403 {
		t.Errorf("dismiss without a source: %d", w.Code)
	}
}
