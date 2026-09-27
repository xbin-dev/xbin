package broker

// The broker fixture of the tile-deployments tests (15-test-plan §3.7):
// one workspace with every edge kind, role shape and scope shape the
// deployment rules distinguish, opened as production opens it (a users
// store attached, so the policy ceilings run). Nothing here edits
// testWorkspace, which the whole broker suite shares; tests of later work
// packages reuse these helpers from files of their own.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// The fixture's tiles.
const (
	fxCalendar  = "apps/calendar"   // kv, bus and filesystem resources; expose reader, writer, admin
	fxEmail     = "apps/email"      // an explicit writer grant on apps/calendar
	fxAgent     = "apps/agent"      // provides inbox at the custom role channel, and declares reader: no implies between them
	fxWebhooks  = "apps/webhooks"   // binds apps/agent's inbox (channel)
	fxSbxA      = "apps/sbx-a"      // a sandbox manager: provides sandbox-manager at the custom role consumer
	fxSbxB      = "apps/sbx-b"      // another one
	fxCoder     = "apps/coder"      // a multi slot bound to both sandbox managers
	fxLLM       = "apps/llm-gw"     // provides openai at writer, which guards its completions; reader lists models
	fxChat      = "apps/chat"       // binds apps/llm-gw's openai (writer)
	fxPG        = "apps/pg"         // exposes a tcp stream
	fxPGClient  = "apps/pg-client"  // a stream slot bound to apps/pg's
	fxShop      = "apps/shop"       // roots a scope with a kv and a bus
	fxShopAdmin = "apps/shop/admin" // the scope's second tile
	fxChrome    = "tiles/bar"       // a chrome tile
	fxConsole   = "apps/console"    // holds xbin at admin
	fxPlain     = "apps/plain"      // no grants, no bindings, no resources
)

// The fixture's people (deployPerson): an admin, a terminal-level user, a
// writer, a reader, and a noTerminal account granted terminal (capped at
// write, D88), each on apps/calendar and apps/shop.
const (
	fxAdmin  = "ana"
	fxTerm   = "tom"
	fxWriter = "wes"
	fxReader = "rae"
	fxNoTerm = "nat"
)

// deployWorkspace writes the fixture workspace and returns its root.
func deployWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"xbin.json": `{"schema":1,
			"grants":[
				{"from":"apps/email","target":"apps/calendar","role":"writer"},
				{"from":"apps/email","target":"res:apps/calendar/bus","role":"reader"},
				{"from":"apps/console","target":"xbin","role":"admin"}
			],
			"bindings":{
				"apps/webhooks":  {"agents":{"ref":"apps/agent"}},
				"apps/coder":     {"sandboxes":[{"ref":"apps/sbx-a"},{"ref":"apps/sbx-b"}]},
				"apps/chat":      {"llm":{"ref":"apps/llm-gw"}},
				"apps/pg-client": {"db":{"ref":"apps/pg#pg"}}
			}}`,
		"apps/calendar/scope.json": `{"resources":{
			"events":{"type":"kv"},"bus":{"type":"bus"},"files":{"type":"filesystem"}}}`,
		"apps/calendar/xbin.json": `{"runtime":"go",
			"expose":{"roles":{"reader":"read the calendar","writer":"edit events","admin":"configure"}},
			"uses":[{"target":"res:apps/calendar/events","role":"writer"},
			        {"target":"res:apps/calendar/bus","role":"writer"},
			        {"target":"res:apps/calendar/files","role":"writer"}]}`,
		"apps/calendar/index.html": `<html></html>`,
		"apps/email/xbin.json": `{"runtime":"go",
			"uses":[{"target":"apps/calendar","role":"writer"},{"target":"res:apps/calendar/bus","role":"reader"}]}`,
		"apps/agent/xbin.json": `{"runtime":"go",
			"expose":{"roles":{"channel":"deliver channel messages","reader":"read the inbox"}},
			"provides":{"inbox":{"kind":"http","service":"inbox","role":"channel"}}}`,
		"apps/webhooks/xbin.json": `{"runtime":"go","interfaces":{"agents":{"kind":"http","service":"inbox"}}}`,
		"apps/sbx-a/xbin.json": `{"runtime":"go","expose":{"roles":{"consumer":"use sandboxes"}},
			"provides":{"sandboxes":{"kind":"http","service":"sandbox-manager","role":"consumer"}}}`,
		"apps/sbx-b/xbin.json": `{"runtime":"go","expose":{"roles":{"consumer":"use sandboxes"}},
			"provides":{"sandboxes":{"kind":"http","service":"sandbox-manager","role":"consumer"}}}`,
		"apps/coder/xbin.json": `{"runtime":"go",
			"interfaces":{"sandboxes":{"kind":"http","service":"sandbox-manager","multi":true}}}`,
		"apps/llm-gw/xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"list models","writer":"run completions"}},
			"provides":{"openai":{"kind":"http","service":"openai","role":"writer"}}}`,
		"apps/chat/xbin.json":      `{"runtime":"go","interfaces":{"llm":{"kind":"http","service":"openai"}}}`,
		"apps/pg/xbin.json":        `{"runtime":"go","exposes":{"pg":{"kind":"stream","port":5432}}}`,
		"apps/pg-client/xbin.json": `{"runtime":"go","interfaces":{"db":{"kind":"stream"}}}`,
		"apps/shop/scope.json":     `{"resources":{"orders":{"type":"kv"},"events":{"type":"bus"}}}`,
		"apps/shop/xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"browse","writer":"order"}},
			"uses":[{"target":"res:apps/shop/orders","role":"writer"},{"target":"res:apps/shop/events","role":"writer"}]}`,
		"apps/shop/admin/xbin.json": `{"runtime":"go",
			"uses":[{"target":"res:apps/shop/orders","role":"writer"},{"target":"apps/shop","role":"writer"}]}`,
		"tiles/bar/xbin.json":    `{"chrome":true}`,
		"tiles/bar/index.html":   `<html></html>`,
		"apps/console/xbin.json": `{"runtime":"go"}`,
		"apps/plain/xbin.json":   `{"runtime":"go"}`,
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// deployBroker opens a broker on deployWorkspace, wired as production wires
// it: a users store attached, holding the fixture's people.
func deployBroker(t *testing.T) *Broker {
	t.Helper()
	reg, err := registry.Open(deployWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	upsert := func(u users.User) {
		t.Helper()
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	upsert(users.User{ID: fxAdmin, Role: users.RoleAdmin})
	for _, id := range []string{fxTerm, fxWriter, fxReader} {
		upsert(users.User{ID: id, Role: users.RoleUser})
	}
	upsert(users.User{ID: fxNoTerm, Role: users.RoleUser, NoTerminal: true})
	for _, tile := range []string{fxCalendar, fxShop} {
		for id, level := range map[string]string{fxTerm: users.LevelTerminal, fxWriter: users.LevelWrite,
			fxReader: users.LevelRead, fxNoTerm: users.LevelTerminal} {
			if err := st.GrantTile(id, tile, level); err != nil {
				t.Fatal(err)
			}
		}
	}
	return b
}

// deployPerson is the human principal of one of the fixture's people, as a
// session cookie authenticates them.
func deployPerson(t *testing.T, b *Broker, id string) auth.Principal {
	t.Helper()
	u, ok := b.Users.Get(id)
	if !ok {
		t.Fatalf("the fixture has no user %q", id)
	}
	a, _ := b.Users.Access(id)
	return auth.Principal{UserID: id, User: u, Access: a, Via: "session"}
}

// deployTilePrincipal is one of tile's own principals: via "instance",
// "frame" or "terminal", bound to deployment dep ("" is main, or for a
// terminal, following the primary).
func deployTilePrincipal(tile, via, dep string) auth.Principal {
	return auth.Principal{Component: tile, Via: via, Deployment: dep}
}

// covers 15-test-plan §3.7 — the fixture's shapes, which the tests built on it
// rely on: each tile registered in its scope, each binding and grant
// yielding its role, the people's levels.
func TestDeployFixture(t *testing.T) {
	b := deployBroker(t)
	for _, tile := range []string{fxCalendar, fxEmail, fxAgent, fxWebhooks, fxSbxA, fxSbxB, fxCoder, fxLLM,
		fxChat, fxPG, fxPGClient, fxShop, fxShopAdmin, fxChrome, fxConsole, fxPlain} {
		c, ok := b.Reg.Component(tile)
		switch {
		case !ok:
			t.Errorf("%s is not registered", tile)
		case c.ManifestErr != "":
			t.Errorf("%s: %s", tile, c.ManifestErr)
		}
	}
	if c, _ := b.Reg.Component(fxShopAdmin); c == nil || c.Scope != fxShop {
		t.Errorf("%s isn't in %s's scope", fxShopAdmin, fxShop)
	}
	if c, _ := b.Reg.Component(fxChrome); c == nil || !c.Manifest.Chrome {
		t.Errorf("%s isn't chrome", fxChrome)
	}
	for _, g := range []struct{ from, target, role string }{
		{fxEmail, fxCalendar, "writer"},
		{fxWebhooks, fxAgent, "channel"},
		{fxCoder, fxSbxA, "consumer"},
		{fxCoder, fxSbxB, "consumer"},
		{fxChat, fxLLM, "writer"},
		{fxShopAdmin, fxShop, "writer"},
		{fxShopAdmin, "res:apps/shop/orders", "writer"},
		{fxConsole, "xbin", "admin"},
	} {
		if role, ok := b.grantedRole(g.from, g.target); !ok || role != g.role {
			t.Errorf("%s → %s: role %q (granted %v), want %q", g.from, g.target, role, ok, g.role)
		}
	}
	if _, ok := b.grantedRole(fxPlain, fxCalendar); ok {
		t.Errorf("%s holds a role on %s", fxPlain, fxCalendar)
	}
	if !b.IsAdmin(deployTilePrincipal(fxConsole, "instance", "")) {
		t.Errorf("%s's instance isn't an admin element", fxConsole)
	}
	for id, want := range map[string]string{fxTerm: users.LevelTerminal, fxWriter: users.LevelWrite,
		fxReader: users.LevelRead, fxNoTerm: users.LevelWrite} {
		if got := deployPerson(t, b, id).Access.TileLevel(fxCalendar); got != want {
			t.Errorf("%s's level on %s: %q, want %q", id, fxCalendar, got, want)
		}
	}
	if !deployPerson(t, b, fxAdmin).IsAdmin() {
		t.Errorf("%s isn't an admin", fxAdmin)
	}
}
