package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// The gate reads a body's tile names as the handlers' decoder does, or a
// request the handler acts on would pass it unseen.
func TestSealedBodyNamesReadAsTheHandlersDo(t *testing.T) {
	keys := []string{"target", "from", "providers"}
	cases := map[string]struct {
		body  string
		want  []string
		twice string
	}{
		"plain":                      {`{"from":"apps/a","target":"xbin/x","role":"r"}`, []string{"apps/a", "xbin/x"}, ""},
		"another case of the key":    {`{"Target":"xbin/x"}`, []string{"xbin/x"}, ""},
		"trailing data after it":     {`{"target":"xbin/x"} trailing`, []string{"xbin/x"}, ""},
		"a second value":             {`{"target":"xbin/x"}{"target":"apps/a"}`, []string{"xbin/x"}, ""},
		"a list":                     {`{"providers":["apps/a","xbin/x#i"]}`, []string{"apps/a", "xbin/x#i"}, ""},
		"an escaped key and value":   {`{"target":"xbin\/x"}`, []string{"xbin/x"}, ""},
		"a long field before it":     {`{"note":"` + strings.Repeat("a", 2<<20) + `","target":"xbin/x"}`, []string{"xbin/x"}, ""},
		"a number":                   {`{"target":7}`, nil, ""},
		"not an object":              {`["xbin/x"]`, nil, ""},
		"another field of that text": {`{"note":"xbin/x"}`, nil, ""},
		"cut short":                  {`{"from":"xbin/x","target":`, []string{"xbin/x"}, ""},
		// one key twice: a struct decode keeps the first value when the
		// second is null, a map decode the second — so it is refused
		"twice, the second null":   {`{"target":"xbin/x","target":null}`, nil, "target"},
		"twice, in two cases":      {`{"target":"apps/a","TARGET":"xbin/x"}`, nil, "target"},
		"twice, the same":          {`{"from":"apps/a","from":"apps/a"}`, nil, "from"},
		"another key twice is not": {`{"note":1,"note":2,"target":"xbin/x"}`, []string{"xbin/x"}, ""},
	}
	for name, c := range cases {
		got, twice := sealedBodyNames([]byte(c.body), keys)
		if twice != c.twice {
			t.Errorf("%s: twice = %q, want %q", name, twice, c.twice)
		}
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: names %q, want %q", name, got, want)
		}
	}
}

func TestSealedNameShapes(t *testing.T) {
	for v, want := range map[string]bool{
		"xbin/x": true, "/xbin/x/": true, " xbin/x ": true, "xbin/x/sub/file.js": true, "xbin/x#inst": true,
		"res:xbin/x/db": true, "res:xbin/x": true, "xbin/x.git/info/refs": true, "code:xbin/x": true, "code:/xbin/x": true,
		"code": false, "code:apps/a": false, "cap:xbin/x": false,
		"xbin": false, "xbin:admin": false, "xbin~x": false, "apps/xbin/x": false, "res:apps/x/db": false, "res:workspace/x": false, "": false,
	} {
		if _, got := sealedName(v); got != want {
			t.Errorf("sealedName(%q) = %v, want %v", v, got, want)
		}
	}
	if !sealedOwn("xbin/x", "res:xbin/x/db") || !sealedOwn("xbin/x", "xbin/x/k") || sealedOwn("xbin/x", "xbin/xy/k") || sealedOwn("", "xbin/x") || sealedOwn("apps/a", "xbin/x") {
		t.Error("sealedOwn: a component's own names are its path, below it, and its resources — nothing else's")
	}
}

// A route with no row in the sealed table (the CI guard fails for one)
// takes no sealed path at runtime either; and an odd spelling of a sealed
// path is refused on every route, classified or not.
func TestSealedGateOnUnclassifiedRoutesAndSpellings(t *testing.T) {
	s := &Server{}
	s.RegisterAPI("POST /zz-no-row-yet", func(w http.ResponseWriter, r *http.Request) { WriteOK(w) })
	s.RegisterAPI("GET /zz-no-row-yet/{rest...}", func(w http.ResponseWriter, r *http.Request) { WriteOK(w) })
	for _, classified := range []string{"POST /lifecycle", "GET /logs", "GET /whoami", "GET /components/{path...}", "GET /kv/{rest...}"} {
		s.RegisterAPI(classified, func(w http.ResponseWriter, r *http.Request) { WriteOK(w) }) // (the broker's and the core's, in a daemon)
	}
	gate := func(method, target, body string) bool {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if body == "" {
			r = httptest.NewRequest(method, target, nil)
		}
		return s.sealedGate(r) != nil
	}
	for target, refused := range map[string]bool{
		"/zz-no-row-yet":                        false,
		"/zz-no-row-yet?tile=apps/a":            false,
		"/zz-no-row-yet?tile=xbin/x":            true,
		"/zz-no-row-yet?anything=res:xbin/x/db": true,
		"/zz-no-row-yet?x=code:xbin/x":          true,
		"/zz-no-row-yet?tile=%2Fxbin%2Fx":       true,
		"/zz-no-row-yet?tile=XBIN/x":            false, // other bytes: no sealed path
	} {
		if got := gate("POST", target, ""); got != refused {
			t.Errorf("POST %s: refused=%v, want %v", target, got, refused)
		}
	}
	for body, refused := range map[string]bool{
		`{"tile":"apps/a"}`: false, `{"tile":"xbin/x"}`: true, `{"deep":{"list":["res:xbin/x/db"]}}`: true, `{"tile":"xbin\/x"}`: true, `{"note":"xbin"}`: false,
	} {
		if got := gate("POST", "/zz-no-row-yet", body); got != refused {
			t.Errorf("POST /zz-no-row-yet %s: refused=%v, want %v", body, got, refused)
		}
	}
	if !gate("GET", "/zz-no-row-yet/xbin/x/file", "") || gate("GET", "/zz-no-row-yet/apps/a/file", "") {
		t.Error("an unclassified route's path tail")
	}
	// classified routes: the spelling, wherever a route names a tile
	for target, refused := range map[string]bool{
		"/logs?component=xbin/x":      false, // handled: the handler answers
		"/logs?component=/xbin/x":     true,
		"/logs?component=xbin//x":     true,
		"/logs?component=./xbin/x/":   true,
		"/whoami?next=apps/../xbin/x": true,
		"/components/xbin/x":          false,
		"/components//xbin/x":         true,
		"/kv/res:/xbin/x/db/k":        true,
		"/logs?component=/apps/a/":    false, // a tile's spelling is the handler's business, as before
	} {
		if got := gate("GET", target, ""); got != refused {
			t.Errorf("GET %s: refused=%v, want %v", target, got, refused)
		}
	}
	for body, refused := range map[string]bool{
		`{"component":"xbin/x","state":"disabled"}`:       false,
		`{"component":"/xbin/x","state":"disabled"}`:      true,
		`{"component":"xbin/x/","state":"disabled"}`:      true,
		`{"tiles":{"/xbin/x":"terminal"}}`:                true,
		`{"newUsers":{"tiles":{"xbin//x":"write"}}}`:      true,
		`{"tiles":{"xbin/x":"read","apps/a":"terminal"}}`: false,
		`{"component":"xbin/x","component":"apps/a"}`:     true, // one key twice
	} {
		if got := gate("POST", "/lifecycle", body); got != refused {
			t.Errorf("POST /lifecycle %s: refused=%v, want %v", body, got, refused)
		}
	}
}

// A view-as session opens nothing of a sealed component, and an admin's
// events socket carries no bus event of one.
func TestSealedViewAsAndAdminBus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "a", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := registry.SealedCode{Path: "xbin/probe", ID: "0123456789abcdef01234567", Dir: t.TempDir(), Manifest: []byte(`{}`),
		Files: map[string][]byte{"xbin.json": []byte(`{}`), "index.html": []byte("<html></html>")}}
	reg, err := registry.OpenSealed(root, func() []registry.SealedCode { return []registry.SealedCode{code} })
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg}
	viewAs := auth.Principal{UserID: "ann", Via: "session", Impersonator: "adm"}
	own := auth.Principal{UserID: "ann", Via: "session"}
	for target, refused := range map[string]bool{
		"/api/xbin/probe/hello":                      true,
		"/api/xbin/probe":                            true,
		"/c/xbin/probe/":                             true,
		"/c/xbin/probe/app.js":                       true,
		"/api/xbin/frame-token?component=xbin/probe": true,
		"/api/xbin/frame-token?component=apps/a":     false,
		"/api/apps/a/x":                              false,
		"/c/apps/a/":                                 false,
		"/api/xbin/components?sealed=1":              false, // that it exists is no secret
		"/api/xbin/sealed":                           false,
	} {
		r := httptest.NewRequest("GET", target, nil)
		if got := s.sealedViewAs(r, viewAs); got != refused {
			t.Errorf("view-as GET %s: refused=%v, want %v", target, got, refused)
		}
		if s.sealedViewAs(r, own) {
			t.Errorf("a person's own session is refused %s as if it were a view-as one", target)
		}
	}
	// a token minted inside such a session is refused everything
	if !s.sealedViewAs(httptest.NewRequest("GET", "/api/xbin/whoami", nil), auth.Principal{Component: "xbin/probe", Via: "frame", UserID: "ann", Impersonator: "adm"}) {
		t.Error("a view-as frame of the sealed component passes")
	}

	admin := auth.Principal{UserID: "adm", Via: "session", User: &users.User{ID: "adm", Role: users.RoleAdmin}}
	owner := auth.Principal{Owner: true, Via: "bearer"}
	for _, p := range []auth.Principal{admin, owner} {
		f := s.eventFilter(p)
		if f(events.Event{Type: "bus", Topic: "res:xbin/probe/events/t", Data: "its people's data"}) {
			t.Errorf("%s receives a sealed component's bus event", p.From())
		}
		if f(events.Event{Type: "bus", Topic: "res:/xbin/probe/events/t"}) {
			t.Errorf("%s receives it under another spelling", p.From())
		}
		if !f(events.Event{Type: "bus", Topic: "res:apps/a/events/t"}) {
			t.Errorf("%s no longer receives a tile's bus event", p.From())
		}
		if !f(events.Event{Type: "reload", Component: "xbin/probe"}) {
			t.Errorf("%s no longer receives its reload event", p.From())
		}
	}
	// the reload a stale page triggers reaches that person's sockets alone
	to := sealedReloadFor{user: "ann"}
	if !to.VisibleTo(own) || to.VisibleTo(admin) || to.VisibleTo(owner) || to.VisibleTo(auth.Principal{UserID: "bob", Via: "session"}) {
		t.Error("a person's reload event reaches someone else")
	}
	if rootTo := (sealedReloadFor{owner: true}); !rootTo.VisibleTo(owner) || rootTo.VisibleTo(own) {
		t.Error("the root token's reload event reaches a person")
	}
}
