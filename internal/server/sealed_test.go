package server

import (
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
