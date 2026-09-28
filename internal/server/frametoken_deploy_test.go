package server

// frametoken_deploy_test.go — GET /frame-token on a tile with deployments
// (11-contract §7.2, §8).

import (
	"cmp"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127g D127d D127l D119c T3 — renewing a frame token on a tile with
// deployments. A person gets the primary's token, or with ?deployment= the
// named deployment's, echoed: read for the primary, write at their current
// level for any other (a reader is refused alike whether or not the name
// exists), 404 for an unknown one of a writer, 400 for a malformed name.
// The tile's own credential renews only its bound deployment's token: a
// frame keeps its claim and may name only its own deployment; one bound
// beyond the primary renews only while its user writes the tile; a terminal
// that follows the primary gets the current primary's (the reassigned
// primary's claim) and nothing while that primary is protected. main's
// tokens keep today's five fields, and a zero-state tile answers as today.
func TestFrameTokenRenewalPerDeployment(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	wes, ana := w.session("wes"), w.session("ana")
	// bound is the deployment a minted token binds its frame principal to,
	// and whether the token has today's five fields.
	bound := func(tok string) (string, bool) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "192.0.2.1:1"
		r.Header.Set(auth.FrameTokenHeader, tok)
		p, ok := w.a.FromRequest(r)
		if !ok || p.Via != "frame" {
			t.Fatalf("minted token %q doesn't verify", tok)
		}
		return cmp.Or(p.Deployment, util.MainDeployment), strings.Count(tok, "|") == 4
	}
	type want struct {
		status int
		dep    string // the token's deployment; the refusal's text
		echo   bool
	}
	check := func(label, query string, who reqOpt, c want) {
		t.Helper()
		rec := w.do("/api/xbin/frame-token?"+query, who)
		var m map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if c.status != 200 {
			if rec.Code != c.status || !strings.HasPrefix(m["error"], c.dep) || m["docs"] == "" {
				t.Errorf("%s ?%s: %d %s, want %d %q", label, query, rec.Code, rec.Body, c.status, c.dep)
			}
			return
		}
		if rec.Code != 200 || m["token"] == "" {
			t.Errorf("%s ?%s: %d %s", label, query, rec.Code, rec.Body)
			return
		}
		dep, five := bound(m["token"])
		_, echoed := m["deployment"]
		switch {
		case dep != c.dep:
			t.Errorf("%s ?%s: a token of %s, want %s", label, query, dep, c.dep)
		case five != (dep == util.MainDeployment):
			t.Errorf("%s ?%s: %s's token %q has the wrong field count", label, query, dep, m["token"])
		case echoed != c.echo, c.echo && m["deployment"] != c.dep:
			t.Errorf("%s ?%s: answer %v, echo wanted %v", label, query, m, c.echo)
		case !c.echo && len(m) != 1:
			t.Errorf("%s ?%s: answer %v, want {token} alone", label, query, m)
		}
	}
	const needsWrite = "deployment URLs need write access on apps/a"
	instance := util.RandomToken(24)
	w.a.RegisterInstanceDeployment(instance, "apps/a", "dev")

	// People.
	check("a writer", "component=apps/a", wes, want{200, "main", false})
	check("a writer", "component=apps/a&deployment=dev", wes, want{200, "dev", true})
	check("a writer", "component=apps/a&deployment=main", wes, want{200, "main", true})
	check("a writer", "component=apps/a&deployment=nope", wes, want{404, `apps/a has no deployment "nope"`, false})
	check("a writer", "component=apps/a&deployment=Dev", wes, want{400, "deployment names are lowercase", false})
	check("the owner", "component=apps/a&deployment=dev", w.zsOwner(), want{200, "dev", true})
	check("a reader", "component=apps/a", ana, want{200, "main", false})
	check("a reader", "component=apps/a&deployment=main", ana, want{200, "main", true})
	check("a reader", "component=apps/a&deployment=dev", ana, want{403, needsWrite, false})
	check("a reader", "component=apps/a&deployment=nope", ana, want{403, needsWrite, false})
	check("a reader, a zero-state tile", "component=apps/b", ana, want{200, "main", false})
	check("a reader, a zero-state tile", "component=apps/b&deployment=dev", ana, want{403, "deployment URLs need write access on apps/b", false})
	check("the owner, a zero-state tile", "component=apps/b&deployment=dev", w.zsOwner(), want{404, `apps/b has no deployment "dev"`, false})
	check("the owner, a zero-state tile", "component=apps/b&deployment=main", w.zsOwner(), want{200, "main", true})
	// D127j: a query names the deployment beside the tile, never as tile+name
	// (escaped, or unescaped and read as a space).
	const qualified = "a deployment is named with deployment=, not tile+name"
	check("a writer, a qualified component", "component=apps/a%2Bdev", wes, want{400, qualified, false})
	check("a writer, an unescaped qualified component", "component=apps/a+dev", wes, want{400, qualified, false})
	check("the owner, a qualified component", "component=apps/a%2Bdev&deployment=dev", w.zsOwner(), want{400, qualified, false})

	// The tile's own credentials.
	check("dev's frame", "component=apps/a", w.devFrame("wes"), want{200, "dev", false})
	check("dev's frame", "component=apps/a&deployment=dev", w.devFrame("wes"), want{200, "dev", true})
	check("dev's frame", "component=apps/a&deployment=main", w.devFrame("wes"), want{403, "a tile's own credentials act only on their own deployment (dev)", false})
	check("dev's frame of a reader", "component=apps/a", w.devFrame("ana"), want{403, needsWrite, false})
	check("main's frame of a reader", "component=apps/a", w.frame("apps/a", "ana"), want{200, "main", false})
	check("main's frame of a reader", "component=apps/a&deployment=dev", w.frame("apps/a", "ana"), want{403, "a tile's own credentials act only on their own deployment (main)", false})
	check("a dev terminal", "component=apps/a", w.terminal("wes", "dev"), want{200, "dev", false})
	check("a following terminal", "component=apps/a", w.terminal("wes", ""), want{200, "main", false})
	check("dev's instance", "component=apps/a", hdr("Authorization", "Bearer "+instance), want{403, "cannot mint frame token for this tile", false})

	// dev becomes the primary: the bare tile's token is dev's.
	w.pol.recs["apps/a"].primary = "dev"
	check("a following terminal, dev primary", "component=apps/a", w.terminal("wes", ""), want{200, "dev", false})
	check("a writer, dev primary", "component=apps/a", wes, want{200, "dev", false})
	check("a reader, dev primary", "component=apps/a", ana, want{200, "dev", false})
	check("a reader, dev primary", "component=apps/a&deployment=main", ana, want{403, needsWrite, false})
	check("main's frame of a reader, dev primary", "component=apps/a", w.frame("apps/a", "ana"), want{403, needsWrite, false})
	check("main's frame of a writer, dev primary", "component=apps/a", w.frame("apps/a", "wes"), want{200, "main", false})
	check("dev's frame of a reader, dev primary", "component=apps/a", w.devFrame("ana"), want{200, "dev", false})

	// A protected primary: a session that follows it is bound to nothing.
	w.pol.recs["apps/a"].primary, w.pol.recs["apps/a"].protected = util.MainDeployment, true
	check("a following terminal, protected primary", "component=apps/a", w.terminal("wes", ""),
		want{403, "the primary of apps/a is protected: terminal and agent sessions can't target it", false})
	check("a writer, protected primary", "component=apps/a", wes, want{200, "main", false})
}
