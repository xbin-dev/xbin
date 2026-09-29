package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// PD-02 — the "partition" values: absent is today's; ["user"] and
// ["user","global"] in any order; anything else is invalid, a later word
// ("org") included, and a value of another JSON type parses (the rest of
// the manifest still applies) and is invalid. partitionMail and
// partitionNote are judged beside a request only.
func TestValidatePartition(t *testing.T) {
	user, both := &PartitionSpec{User: true}, &PartitionSpec{User: true, Global: true}
	cases := []struct {
		manifest string
		want     *PartitionSpec
		err      string
	}{
		{`{}`, nil, ""},
		{`{"partition": null}`, nil, ""},
		{`{"partition": ["user"]}`, user, ""},
		{`{"partition": ["user", "global"]}`, both, ""},
		{`{"partition": ["global", "user"]}`, both, ""},
		{`{"partition": []}`, nil, "asks for nothing"},
		{`{"partition": ["global"]}`, nil, "partitions nothing"},
		{`{"partition": ["user", "user"]}`, nil, "listed twice"},
		{`{"partition": ["user", "org"]}`, nil, `unknown word "org"`},
		{`{"partition": ["User"]}`, nil, `unknown word "User"`},
		{`{"partition": "user"}`, nil, "must be a list"},
		{`{"partition": true}`, nil, "must be a list"},
		{`{"partition": {"user": true}}`, nil, "must be a list"},
		{`{"partition": [1]}`, nil, "must be a list"},
		{`{"partition": ["user"], "partitionMail": "/mailbox"}`, user, ""},
		{`{"partition": ["user"], "partitionMail": "mailbox"}`, nil, "absolute path"},
		{`{"partition": ["user"], "partitionMail": "/mailbox?x=1"}`, nil, "without a query"},
		{`{"partition": ["user"], "partitionMail": "//host/x"}`, nil, "absolute path"},
		{`{"partitionMail": "not-judged-without-a-request"}`, nil, ""},
		{`{"partition": ["user"], "partitionNote": "` + strings.Repeat("é", 280) + `"}`, user, ""},
		{`{"partition": ["user"], "partitionNote": "` + strings.Repeat("x", 281) + `"}`, nil, "over 280"},
	}
	for _, c := range cases {
		var m Manifest
		if err := json.Unmarshal([]byte(c.manifest), &m); err != nil {
			t.Fatalf("%s: the manifest doesn't parse: %v", c.manifest, err)
		}
		got, err := ValidatePartition(m)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v, want valid", c.manifest, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: error %v, want one saying %q", c.manifest, err, c.err)
		case !reflect.DeepEqual(got, c.want):
			t.Errorf("%s: spec %+v, want %+v", c.manifest, got, c.want)
		}
	}
	// A non-list value leaves the other keys parsed.
	var m Manifest
	if err := json.Unmarshal([]byte(`{"runtime": "go", "partition": 7}`), &m); err != nil || m.Runtime != "go" {
		t.Errorf("a non-list partition broke the manifest: %v %+v", err, m)
	}
}

// partitionReg opens files as a workspace with no mode store: a valid
// request waits (pending), an invalid one is invalid.
func partitionReg(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func partErr(t *testing.T, r *Registry, rel string) string {
	t.Helper()
	c, ok := r.Component(rel)
	if !ok {
		t.Fatalf("%s isn't registered", rel)
	}
	return c.PartitionErr
}

// 01 §3 rule 1 — a tile that uses same-scope resources roots its scope.
func TestPartitionScopeRoot(t *testing.T) {
	r := partitionReg(t, map[string]string{
		"apps/suite/scope.json":      `{"resources":{"db":{"type":"kv"}}}`,
		"apps/suite/app/xbin.json":   `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/suite/db","role":"writer"}]}`,
		"apps/crm/scope.json":        `{"resources":{"db":{"type":"kv"}}}`,
		"apps/crm/xbin.json":         `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/crm/db","role":"writer"}]}`,
		"apps/solo/xbin.json":        `{"runtime":"go","partition":["user","global"]}`,
		"apps/other/xbin.json":       `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/crm/db","role":"reader"}]}`,
		"apps/templ/xbin.json":       `{"runtime":"go","partition":["bogus"],"template":{"title":"T","partition":["user"]}}`,
		"apps/suite/other/xbin.json": `{"runtime":"static"}`,
	})
	if e := partErr(t, r, "apps/suite/app"); !strings.Contains(e, "doesn't root that scope") {
		t.Errorf("a non-root user of the scope's resources: %q", e)
	}
	for _, rel := range []string{"apps/crm", "apps/solo", "apps/other", "apps/templ"} {
		if e := partErr(t, r, rel); e != "" {
			t.Errorf("%s: %q, want a valid request", rel, e)
		}
	}
	c, _ := r.Component("apps/suite/app")
	if st, _, _ := c.PartitionState(); st != PartitionInvalid || !strings.Contains(c.ManifestErr, c.PartitionErr) {
		t.Errorf("an invalid request: state %v, manifest error %q", st, c.ManifestErr)
	}
	// Without a mode store a valid request waits; a template asks nothing.
	crm, _ := r.Component("apps/crm")
	if st, rec, req := crm.PartitionState(); st != PartitionPending || !rec.IsZero() || req == nil || *req.Spec != (PartitionSpec{User: true}) {
		t.Errorf("no mode store: %v %v %+v, want pending unpartitioned → user", st, rec, req)
	}
	tpl, _ := r.Component("apps/templ")
	if tpl.PartitionShown() || tpl.PartitionRequested() != nil || tpl.Manifest.Template.Partition[0] != "user" {
		t.Errorf("a template's partition is its instances' and never judged: %+v", tpl.Manifest.Template)
	}
	if _, ok := crm.Partitioned(); ok {
		t.Error("a pending tile reads as partitioned")
	}
	if _, ok := r.PartitionedScope("apps/crm"); ok {
		t.Error("a pending scope reads as partitioned")
	}
	if !r.PartitionAsked() {
		t.Error("PartitionAsked is false with requests in the workspace")
	}
}

// 01 §3 rule 2 — every tile of a partitioned scope asks alike.
func TestPartitionScopeSiblings(t *testing.T) {
	r := partitionReg(t, map[string]string{
		"apps/a/scope.json":        `{"resources":{"db":{"type":"kv"}}}`,
		"apps/a/xbin.json":         `{"runtime":"go","partition":["user"]}`,
		"apps/a/sub/xbin.json":     `{"runtime":"go"}`,
		"apps/b/scope.json":        `{}`,
		"apps/b/xbin.json":         `{"runtime":"go","partition":["user","global"]}`,
		"apps/b/sub/xbin.json":     `{"runtime":"go","partition":["global","user"]}`,
		"apps/c/scope.json":        `{}`,
		"apps/c/xbin.json":         `{"runtime":"go","partition":["user","global"]}`,
		"apps/c/sub/xbin.json":     `{"runtime":"go","partition":["user"]}`,
		"apps/c/tpl/xbin.json":     `{"template":{"title":"x"}}`,
		"apps/d/scope.json":        `{}`,
		"apps/d/xbin.json":         `{"runtime":"go","partition":["user"]}`,
		"apps/d/nested/scope.json": `{}`,
		"apps/d/nested/xbin.json":  `{"runtime":"go"}`,
	})
	for rel, want := range map[string]string{
		"apps/a": "apps/a/sub, in the same scope apps/a, asks for unpartitioned",
		"apps/b": "", "apps/b/sub": "",
		"apps/c": "apps/c/sub, in the same scope apps/c, asks for user", "apps/c/sub": "apps/c, in the same scope apps/c, asks for user + global",
		"apps/d": "", // a nested scope is its own
	} {
		if e := partErr(t, r, rel); want == "" && e != "" || want != "" && !strings.Contains(e, want) {
			t.Errorf("%s: %q, want %q", rel, e, want)
		}
	}
}

// 01 §3 rules 3-6 — chrome, vm, governance and widening caps, a sqlite
// resource shared read-only, an unknown shared value.
func TestPartitionStructuralRefusals(t *testing.T) {
	r := partitionReg(t, map[string]string{
		"xbin.json":             `{"schema":1,"grants":[{"from":"apps/held","target":"cap:net-admin","role":"writer"}]}`,
		"apps/chrome/xbin.json": `{"partition":["user"],"chrome":true}`,
		"apps/vm/xbin.json":     `{"runtime":"go","partition":["user"],"vm":true}`,
		"apps/novm/xbin.json":   `{"runtime":"go","partition":["user"],"vm":false}`,
		"apps/gov/xbin.json":    `{"runtime":"go","partition":["user"],"uses":[{"target":"xbin:users","role":"admin"}]}`,
		"apps/sbx/xbin.json":    `{"runtime":"go","partition":["user"],"uses":[{"target":"cap:sandboxes","role":"writer"}]}`,
		"apps/ctr/xbin.json":    `{"runtime":"go","partition":["user"],"uses":[{"target":"cap:containers","role":"writer"}]}`,
		"apps/held/xbin.json":   `{"runtime":"go","partition":["user"]}`,
		"apps/sq/scope.json":    `{"resources":{"db":{"type":"sqlite","shared":"read"},"kv":{"type":"kv","shared":"read"}}}`,
		"apps/sq/xbin.json":     `{"runtime":"go","partition":["user"]}`,
		"apps/odd/scope.json":   `{"resources":{"kv":{"type":"kv","shared":"write"}}}`,
		"apps/odd/xbin.json":    `{"runtime":"go","partition":["user"]}`,
		"apps/ok/scope.json":    `{"resources":{"kv":{"type":"kv","shared":"read"},"db":{"type":"sqlite","shared":true},"t":{"type":"cron","shared":true}}}`,
		"apps/ok/xbin.json":     `{"runtime":"go","partition":["user","global"]}`,
		"apps/plain/scope.json": `{"resources":{"kv":{"type":"kv","shared":"write"}}}`,
		"apps/plain/xbin.json":  `{"runtime":"go"}`,
	})
	for rel, want := range map[string]string{
		"apps/chrome": "chrome", "apps/vm": "vm backend", "apps/novm": "",
		"apps/gov": "can't use xbin:users", "apps/sbx": "can't use cap:sandboxes", "apps/ctr": "can't use cap:containers",
		"apps/held": "can't hold cap:net-admin", "apps/sq": "sqlite resource can't be shared read-only",
		"apps/odd": `shared must be true or "read"`, "apps/ok": "",
		"apps/plain": "", // shared in a scope nobody partitions: ignored, even an unknown value
	} {
		if e := partErr(t, r, rel); want == "" && e != "" || want != "" && !strings.Contains(e, want) {
			t.Errorf("%s: %q, want %q", rel, e, want)
		}
	}
	plain, _ := r.Component("apps/plain")
	if plain.ManifestErr != "" || plain.PartitionShown() {
		t.Errorf("an unpartitioned tile carries partition state: %q", plain.ManifestErr)
	}
}

// PD-05 — scope.json's shared parses true, "read" and false; any other value
// is kept (invalid for a partitioned scope) and every value writes back as
// declared (a machine-managed workspace xbin.json keeps its resources).
func TestSharedMode(t *testing.T) {
	for in, want := range map[string]SharedMode{
		`{"type":"kv"}`: SharedNone, `{"type":"kv","shared":true}`: SharedAll, `{"type":"kv","shared":false}`: SharedNone,
		`{"type":"kv","shared":"read"}`: SharedRead, `{"type":"kv","shared":"write"}`: SharedMode(`?"write"`),
		`{"type":"kv","shared":2}`: SharedMode("?2"),
	} {
		var r Resource
		if err := json.Unmarshal([]byte(in), &r); err != nil || r.Shared != want {
			t.Errorf("%s: %q %v, want %q", in, r.Shared, err, want)
		}
		if r.Shared.Valid() != (want == SharedNone || want == SharedAll || want == SharedRead) {
			t.Errorf("%s: Valid %v", in, r.Shared.Valid())
		}
		out, _ := json.Marshal(r)
		var back Resource
		if err := json.Unmarshal(out, &back); err != nil || back != r {
			t.Errorf("%s: round trip %s → %+v", in, out, back)
		}
	}
	if out, _ := json.Marshal(Resource{Type: "kv"}); string(out) != `{"type":"kv"}` {
		t.Errorf("a resource without shared marshals as %s", out)
	}
}

// The zero state: a workspace without the keys carries nothing, with or
// without a mode store, and asks the store nothing it has to read.
func TestPartitionZeroState(t *testing.T) {
	files := map[string]string{
		"apps/a/scope.json": `{"resources":{"db":{"type":"kv"}}}`,
		"apps/a/xbin.json":  `{"runtime":"go","uses":[{"target":"res:apps/a/db","role":"writer"}]}`,
		"apps/b/xbin.json":  `{"runtime":"static","partitionNote":"ignored","partitionMail":"x"}`,
	}
	plain := partitionReg(t, files)
	var asks []PartitionAsk
	hooked := &Registry{Root: plain.Root, PartitionModes: func(a PartitionAsk) PartitionMode {
		asks = append(asks, a)
		return PartitionMode{}
	}}
	if err := hooked.Rescan(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"apps/a", "apps/b"} {
		got, _ := hooked.Component(rel)
		want, _ := plain.Component(rel)
		if !reflect.DeepEqual(got, want) || got.PartitionShown() || got.PartitionErr != "" {
			t.Errorf("%s: %+v, want the zero state %+v", rel, got, want)
		}
	}
	for _, a := range asks {
		if a.Requested != nil || a.Invalid != "" {
			t.Errorf("a zero-state tile asked for a mode: %+v", a)
		}
	}
	if plain.PartitionAsked() {
		t.Error("PartitionAsked on a zero-state workspace")
	}
}
