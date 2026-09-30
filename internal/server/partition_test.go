package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// partPolicy partitions apps/a (user + global) as the broker would: people
// and the tile's frames reach their person's partition, the root token and
// the owner's frames global, view-as nothing; its instances their
// registration. BusAllows passes tile principals only, as busFilter does.
type partPolicy struct{ NoopPolicy }

func (partPolicy) AddressedPartition(p auth.Principal, tile string) (util.Partition, error) {
	switch {
	case tile != "apps/a":
		return "", nil
	case p.Impersonator != "":
		return "", errors.New("apps/a keeps " + p.UserID + "'s data private: view-as can't open it")
	case p.Via == "instance" && p.Partition != "":
		return p.Partition, nil
	case p.Component != "" && p.Component != tile:
		return "", errors.New("another tile")
	case p.UserID != "":
		return util.UserPartition(p.UserID), nil
	}
	return util.PartitionGlobal, nil
}

func (partPolicy) BusAllows(p auth.Principal, _ events.Event) bool { return p.Component != "" }

// partServer is newAssetWS with apps/a recorded partitioned (user +
// global) and partPolicy installed.
func partServer(t *testing.T) *assetWS {
	t.Helper()
	w := newAssetWS(t, TileAssetsLegacy)
	w.s.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == "apps/a" {
			return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true, Global: true}}
		}
		return registry.PartitionMode{}
	}
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	w.s.InstallPolicy(partPolicy{})
	return w
}

// covers G2 PD-09 S2 S12 — /ws/events on a partitioned tile (02 §9): a
// partition's status (or runner, partitions) event reaches its person's own
// sockets and the tile's principals acting in it, never an admin's shell,
// another person, another tile's frame or view-as; a partition's bus event
// gets no admin pass; a partitioned tile's term and session events reach
// only the session's person (and their terminal on the tile), admins
// included in "no"; and every event of an unpartitioned tile, or without a
// partition, filters as before.
func TestPartitionEvents(t *testing.T) {
	w := partServer(t)
	s := w.s
	ana := auth.Principal{UserID: "ana", Via: "session"}
	bob := auth.Principal{UserID: "bob", Via: "session"}
	owner := auth.Principal{Owner: true, Via: "cookie"}
	admin := auth.Principal{UserID: "root", User: &users.User{ID: "root", Role: users.RoleAdmin}, Via: "session"}
	anaFrame := auth.Principal{Component: "apps/a", UserID: "ana", Via: "frame"}
	anaOther := auth.Principal{Component: "apps/b", UserID: "ana", Via: "frame"}
	anaTerm := auth.Principal{Component: "apps/a", UserID: "ana", Via: "terminal"}
	viewAna := auth.Principal{UserID: "ana", Via: "session", Impersonator: "owner"}
	anaInstance := auth.Principal{Component: "apps/a", Via: "instance", Partition: "user:ana"}
	status := events.Event{Type: "status", Component: "apps/a", Partition: "user:ana", Data: map[string]any{"level": "warn"}}
	bus := events.Event{Type: "bus", Topic: "res:apps/a/feed/x", Partition: "user:ana"}
	term := events.Event{Type: "term", Component: "apps/a", Data: termChange{Op: "open", ID: "t1", User: "ana"}}
	session := events.Event{Type: "session", Component: "apps/a", Data: termChange{User: "ana"}}
	partSession := events.Event{Type: "session", Component: "apps/a", Partition: "user:ana", Data: termChange{User: "ana"}}
	for _, c := range []struct {
		name string
		p    auth.Principal
		e    events.Event
		want bool
	}{
		{"ana's shell, her status", ana, status, true},
		{"ana's frame, her status", anaFrame, status, true},
		{"ana's partition's instance, its status", anaInstance, status, true},
		{"bob's shell", bob, status, false},
		{"the owner's shell", owner, status, false},
		{"an admin's shell", admin, status, false},
		{"ana's frame of another tile", anaOther, status, false},
		{"view-as ana", viewAna, status, false},
		{"the owner, a partition's bus event", owner, bus, false},
		{"ana's frame, her bus event", anaFrame, bus, true},
		{"the owner, a bus event of no partition", owner, events.Event{Type: "bus", Topic: "res:apps/b/feed/x"}, true},
		{"the owner, a term event on apps/a", owner, term, false},
		{"ana, her term event on apps/a", ana, term, true},
		{"bob, ana's term event", bob, term, false},
		{"ana's terminal on apps/a", anaTerm, term, true},
		{"view-as ana, her term event", viewAna, term, false},
		{"the owner, a session event on apps/a", owner, session, false},
		{"ana, her session event", ana, session, true},
		// a partition's stamp narrows the type's own rules, never replaces them
		{"ana, her partition's session event", ana, partSession, true},
		{"ana's frame, her partition's session event", anaFrame, partSession, false},
		{"ana's partition's instance, its session event", anaInstance, partSession, false},
		{"ana's terminal on apps/a, her partition's session event", anaTerm, partSession, true},
		{"the owner, a partition's session event", owner, partSession, false},
		{"the owner, a term event on apps/b", owner, events.Event{Type: "term", Component: "apps/b", Data: termChange{User: "ana"}}, true},
		{"bob, a status of no partition", bob, events.Event{Type: "status", Component: "apps/a", Data: map[string]any{}}, true},
	} {
		if got := s.eventFilter(c.p)(c.e); got != c.want {
			t.Errorf("%s: delivered %v, want %v", c.name, got, c.want)
		}
	}
}

// covers PD-08 PD-10 PD-29 — the partition gate beyond the table (02 §8):
// a view-as frame of a partitioned tile is refused on everything but
// neutral routes; a PersonOnly route refuses every tile principal —
// instance, frame and terminal, of any tile, partitioned or not — the root
// token, --no-auth, view-as and an anonymous subrequest, and passes a
// person; the principal a handler sees carries its user partition.
func TestPartitionGate(t *testing.T) {
	w := partServer(t)
	partitionClasses["POST /zz-person-only"] = PersonOnly
	t.Cleanup(func() { delete(partitionClasses, "POST /zz-person-only") })
	w.s.RegisterAPI("POST /zz-person-only", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	w.s.RegisterAPI("GET /zz-probe-neutral", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	partitionClasses["GET /zz-probe-neutral"] = PartitionNeutral
	t.Cleanup(func() { delete(partitionClasses, "GET /zz-probe-neutral") })
	w.s.RegisterAPI("GET /zz-probe-scoped", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	partitionClasses["GET /zz-probe-scoped"] = PartitionScoped
	t.Cleanup(func() { delete(partitionClasses, "GET /zz-probe-scoped") })
	// the real rows of routes the data plane converted (kv, blob, the bus
	// publish), the registrations (F5: a subscription, a dormant interface
	// instance), one still unconverted, and a mode decision
	for _, pat := range []string{"PUT /kv/{rest...}", "GET /blob/{rest...}", "POST /bus/publish", "PUT /bus/subscriptions",
		"PUT /iface-instances", "GET /logs", "POST /partitions/mode"} {
		w.s.RegisterAPI(pat, func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	}
	w.s.Handler() // mounts the mux
	gate := func(m, path string, p auth.Principal) (int, string, util.Partition) {
		r := httptest.NewRequest(m, path, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		r2, deny := w.s.partitionGate(r)
		if deny != nil {
			rec := httptest.NewRecorder()
			deny(rec, r2)
			return rec.Code, rec.Body.String(), ""
		}
		return 200, "", auth.PrincipalOf(r2).Partition
	}
	view := auth.Principal{Component: "apps/a", UserID: "ana", Via: "frame", Impersonator: "owner"}
	if code, body, _ := gate("GET", "/zz-probe-scoped", view); code != 403 || !strings.Contains(body, "view-as can't open it") {
		t.Errorf("a view-as frame on a scoped route: %d %s", code, body)
	}
	if code, _, _ := gate("GET", "/zz-probe-neutral", view); code != 200 {
		t.Errorf("a view-as frame on a neutral route: %d", code)
	}
	for _, p := range []auth.Principal{
		{Component: "apps/a", Via: "instance", Partition: "user:ana"},
		{Component: "apps/a", UserID: "ana", Via: "frame"},
		{Component: "apps/a", UserID: "ana", Via: "terminal"},
		{Component: "apps/b", Via: "instance"},
		{Component: "apps/b", UserID: "ana", Via: "frame"},
	} {
		if code, body, _ := gate("POST", "/zz-person-only", p); code != 403 || !strings.Contains(body, "a person's own act") {
			t.Errorf("PersonOnly for %s via %s: %d %s", p.Component, p.Via, code, body)
		}
	}
	for name, p := range map[string]auth.Principal{
		"the root token":          {Owner: true, Via: "cookie"},
		"--no-auth":               {Owner: true, Via: "dev"},
		"view-as ana":             {UserID: "ana", Via: "session", Impersonator: "owner"},
		"an anonymous subrequest": {},
	} {
		if code, body, _ := gate("POST", "/zz-person-only", p); code != 403 || !strings.Contains(body, "a person's own act") {
			t.Errorf("PersonOnly for %s: %d %s", name, code, body)
		}
	}
	if code, _, _ := gate("POST", "/zz-person-only", auth.Principal{UserID: "ana", Via: "session"}); code != 200 {
		t.Errorf("PersonOnly for ana herself: %d", code)
	}
	if _, _, part := gate("GET", "/zz-probe-scoped", auth.Principal{Component: "apps/a", UserID: "ana", Via: "frame"}); part != "user:ana" {
		t.Errorf("the handler's principal carries %q, want user:ana", part)
	}
	if _, _, part := gate("GET", "/zz-probe-scoped", auth.Principal{Component: "apps/a/editor", UserID: "ana", Via: "frame"}); part != "user:ana" {
		t.Errorf("an xbin.window document's principal carries %q, want its tile's user:ana", part)
	}
	for _, p := range []auth.Principal{{Component: "apps/a", Via: "frame"}, {Component: "apps/b", UserID: "ana", Via: "frame"}} {
		if _, _, part := gate("GET", "/zz-probe-scoped", p); part != "" {
			t.Errorf("%s via %s (%s): the handler's principal carries %q", p.Component, p.Via, p.UserID, part)
		}
	}
	// a user partition's kv, blob and bus publish reach their handlers, which
	// act on the caller's partition (the data plane), and so do its
	// registrations — a dormant one (an interface instance) too, stamped,
	// which its handler stores apart and never routes; what isn't converted
	// yet is refused; a mode decision is never a partition's
	for _, p := range []auth.Principal{{Component: "apps/a", UserID: "ana", Via: "frame"}, {Component: "apps/a", Via: "instance", Partition: "user:ana"}} {
		for _, c := range [][2]string{{"PUT", "/kv/res:apps/a/db/k"}, {"GET", "/blob/res:apps/a/box/f"}, {"POST", "/bus/publish"},
			{"PUT", "/bus/subscriptions"}, {"PUT", "/iface-instances"}} {
			if code, body, part := gate(c[0], c[1], p); code != 200 || part != "user:ana" {
				t.Errorf("%s via %s: %s %s: %d %s, partition %q", p.Component, p.Via, c[0], c[1], code, body, part)
			}
		}
		if code, body, _ := gate("GET", "/logs", p); code != 403 || !strings.Contains(body, "isn't available to a partition's credentials yet") {
			t.Errorf("%s via %s: an unconverted route: %d %s", p.Component, p.Via, code, body)
		}
		if code, body, _ := gate("POST", "/partitions/mode", p); code != 403 || !strings.Contains(body, "the global instance's alone") {
			t.Errorf("%s via %s: a mode decision: %d %s", p.Component, p.Via, code, body)
		}
	}
}

// covers PD-10 — the xbin-partition meta (02 §10): a partitioned tile's
// document names the viewer's partition — the person's, or global for the
// root token — beside the frame token; an unpartitioned tile's has none
// (today's bytes); a view-as viewer gets the document without a frame
// token.
func TestPartitionMeta(t *testing.T) {
	w := partServer(t)
	meta := `<meta name="xbin-partition" content="`
	for _, c := range []struct {
		name, url string
		opts      []reqOpt
		want      string // the meta's content; "" = no meta
	}{
		{"ana", "/c/apps/a/", []reqOpt{w.session("ana")}, "user:ana"},
		{"the root token", "/c/apps/a/", []reqOpt{cookie(auth.CookieName, w.a.OwnerTokenValue())}, "global"},
		{"ana on an unpartitioned tile", "/c/apps/b/", []reqOpt{w.session("ana")}, ""},
	} {
		rec := w.do(c.url, c.opts...)
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, body)
		}
		switch {
		case c.want == "" && strings.Contains(body, meta):
			t.Errorf("%s: the document carries the partition meta", c.name)
		case c.want != "" && !strings.Contains(body, meta+c.want+`">`):
			t.Errorf("%s: no %q partition meta in\n%s", c.name, c.want, body)
		}
		if !strings.Contains(body, `<meta name="xbin-frame-token" content="ey`) && !strings.Contains(body, `name="xbin-frame-token" content="`) {
			t.Errorf("%s: no frame-token meta", c.name)
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
	body := w.do("/c/apps/a/", cookie(auth.CookieName, sid)).Body.String()
	if strings.Contains(body, meta) || !strings.Contains(body, `<meta name="xbin-frame-token" content="">`) {
		t.Errorf("view-as: a token or partition meta in\n%s", body)
	}
}

// partURLPolicy is urlPolicy with apps/a partitioned as partPolicy says.
type partURLPolicy struct{ *urlPolicy }

func (partURLPolicy) AddressedPartition(p auth.Principal, tile string) (util.Partition, error) {
	return partPolicy{}.AddressedPartition(p, tile)
}

// covers PD-17 — a non-primary deployment's document of a partitioned tile
// (/c/<tile>+<dep>/) names global, not the viewer's own partition: its
// frame token is bound to that deployment, whose one instance every writer
// shares; the primary's document names the viewer's partition.
func TestPartitionMetaNonPrimary(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	w.s.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == "apps/a" {
			return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true, Global: true}}
		}
		return registry.PartitionMode{}
	}
	w.rescan()
	w.s.Pol = partURLPolicy{w.pol}
	meta := `<meta name="xbin-partition" content="`
	for url, want := range map[string]string{"/c/apps/a/": "user:wes", "/c/apps/a+dev/": "global"} {
		rec := w.do(url, w.session("wes"))
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, meta+want+`">`) || strings.Count(body, meta) != 1 {
			t.Errorf("%s: %d, want one %q partition meta in\n%s", url, rec.Code, want, body)
		}
		if frameTokenIn(t, body) == "" {
			t.Errorf("%s: no frame token", url)
		}
	}
}
