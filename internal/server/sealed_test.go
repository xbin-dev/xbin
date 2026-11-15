package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
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
