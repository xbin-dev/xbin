package server

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// ifacePartPolicy is partPolicy with one multi http slot on every tile and
// a person's view that adds their personal bind (PartitionInterfaces).
type ifacePartPolicy struct{ partPolicy }

func (ifacePartPolicy) Interfaces(comp string) map[string]any {
	return map[string]any{"mcp": map[string]any{"service": "mcp", "multi": true,
		"endpoints": []map[string]any{{"provider": "apps/shared", "url": "/api/apps/shared"}}}}
}

func (p ifacePartPolicy) PartitionInterfaces(comp string, part util.Partition) map[string]any {
	out := p.Interfaces(comp)
	id, _ := part.User()
	m := out["mcp"].(map[string]any)
	m["endpoints"] = append(m["endpoints"].([]map[string]any),
		map[string]any{"provider": "users/" + id + "/mcp", "url": "/api/users/" + id + "/mcp", "personal": true})
	return out
}

// covers PD-54 — the xbin-interfaces meta of a partitioned tile's document
// (plans/partitions/05 §3): a person's own view lists their personal bind;
// the root token's (global), view-as and a tile that isn't partitioned get
// Interfaces exactly.
func TestPartitionInterfacesMeta(t *testing.T) {
	w := partServer(t)
	w.s.InstallPolicy(ifacePartPolicy{})
	personal := `users/ana/mcp`
	for _, c := range []struct {
		name, url string
		opts      []reqOpt
		personal  bool
	}{
		{"ana", "/c/apps/a/", []reqOpt{w.session("ana")}, true},
		{"the root token", "/c/apps/a/", []reqOpt{cookie(auth.CookieName, w.a.OwnerTokenValue())}, false},
		{"ana on an unpartitioned tile", "/c/apps/b/", []reqOpt{w.session("ana")}, false},
	} {
		rec := w.do(c.url, c.opts...)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `<meta name="xbin-interfaces"`) || !strings.Contains(body, "apps/shared") {
			t.Fatalf("%s: %d, no interfaces meta with the global bind in\n%s", c.name, rec.Code, body)
		}
		if got := strings.Contains(body, personal); got != c.personal {
			t.Errorf("%s: personal bind in the meta %v, want %v\n%s", c.name, got, c.personal, body)
		}
	}
	tk, err := w.a.NewImpersonationTicket(auth.Principal{Owner: true, Via: "cookie"}, "ana")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w.a.RedeemImpersonation(tk, auth.Principal{Owner: true, Via: "cookie"}, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if body := w.do("/c/apps/a/", cookie(auth.CookieName, sid)).Body.String(); strings.Contains(body, personal) {
		t.Errorf("view-as ana: her personal bind in\n%s", body)
	}
}

// ifaceURLPolicy is partURLPolicy (deployments, apps/a partitioned) with
// ifacePartPolicy's interfaces.
type ifaceURLPolicy struct{ partURLPolicy }

func (ifaceURLPolicy) Interfaces(comp string) map[string]any {
	return ifacePartPolicy{}.Interfaces(comp)
}

func (ifaceURLPolicy) PartitionInterfaces(comp string, part util.Partition) map[string]any {
	return ifacePartPolicy{}.PartitionInterfaces(comp, part)
}

// covers PD-17 PD-54 — a non-primary deployment's document of a partitioned
// tile (/c/<tile>+<dep>/, whose one instance is global) lists the global
// binds only: personal binds reach a person's partition on the primary
// alone; the primary's document lists the viewer's.
func TestPartitionInterfacesMetaNonPrimary(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	w.s.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == "apps/a" {
			return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true, Global: true}}
		}
		return registry.PartitionMode{}
	}
	w.rescan()
	w.s.Pol = ifaceURLPolicy{partURLPolicy{w.pol}}
	for url, want := range map[string]bool{"/c/apps/a/": true, "/c/apps/a+dev/": false} {
		rec := w.do(url, w.session("wes"))
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `<meta name="xbin-interfaces"`) || !strings.Contains(body, "apps/shared") {
			t.Fatalf("%s: %d, no interfaces meta with the global bind in\n%s", url, rec.Code, body)
		}
		if got := strings.Contains(body, "users/wes/mcp"); got != want {
			t.Errorf("%s: wes's personal bind in the meta %v, want %v\n%s", url, got, want, body)
		}
	}
}

// covers PD-54 G2 — a personal bind's change publishes grants for the
// requester stamped with its person's partition (personalbind.go's
// publishPersonalBinds): it reaches that person's own sockets and the
// tile's frames acting in their partition, never another person, an
// admin's shell, the root token or view-as.
func TestPersonalBindGrantsEvent(t *testing.T) {
	w := partServer(t)
	s := w.s
	ev := events.Event{Type: "grants", Component: "apps/a", Partition: "user:ana"}
	for _, c := range []struct {
		name string
		p    auth.Principal
		want bool
	}{
		{"ana's shell", auth.Principal{UserID: "ana", Via: "session"}, true},
		{"ana's frame of apps/a", auth.Principal{Component: "apps/a", UserID: "ana", Via: "frame"}, true},
		{"bob's shell", auth.Principal{UserID: "bob", Via: "session"}, false},
		{"bob's frame of apps/a", auth.Principal{Component: "apps/a", UserID: "bob", Via: "frame"}, false},
		{"an admin's shell", auth.Principal{UserID: "root", User: &users.User{ID: "root", Role: users.RoleAdmin}, Via: "session"}, false},
		{"the root token", auth.Principal{Owner: true, Via: "cookie"}, false},
		{"view-as ana", auth.Principal{UserID: "ana", Via: "session", Impersonator: "owner"}, false},
	} {
		if got := s.eventFilter(c.p)(ev); got != c.want {
			t.Errorf("%s: delivered %v, want %v", c.name, got, c.want)
		}
	}
}
