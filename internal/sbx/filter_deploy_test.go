package sbx

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// covers D119c D127h — the registry's deployment dimension: a Filter narrows by
// deployment, reading an entry without one as main, while its zero value and
// a tile filter still list every deployment's entries under the tile; a
// non-main failure never coalesces with main's; and an entry or failure
// without a deployment carries no "deployment" key.
func TestFilterDeployment(t *testing.T) {
	r := New()
	t0 := time.Unix(1000, 0)
	r.Add(Entry{ID: "backend:apps~a-1:g3", Kind: Backend, Tile: "apps/a", Gen: 3, Started: t0})
	r.Add(Entry{ID: "backend+dev:apps~a-1:g7", Kind: Backend, Tile: "apps/a", Deployment: "dev", Gen: 7, Started: t0.Add(time.Second)})
	r.Add(Entry{ID: "backend+dev:apps~a-1:g8", Kind: Backend, Tile: "apps/a", Deployment: "dev", Gen: 8, Started: t0.Add(2 * time.Second)})
	r.Add(Entry{ID: "t1", Kind: Terminal, Tile: "apps/a", User: "alice", Started: t0.Add(3 * time.Second)})
	r.Add(Entry{ID: "backend:apps~b-2:g1", Kind: Backend, Tile: "apps/b", Gen: 1, Started: t0})

	ids := func(f Filter) string {
		var out []string
		for _, e := range r.List(f) {
			out = append(out, e.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		f    Filter
		want string
	}{
		{Filter{}, "backend:apps~a-1:g3,backend+dev:apps~a-1:g7,backend+dev:apps~a-1:g8,t1,backend:apps~b-2:g1"},
		{Filter{Tile: "apps/a"}, "backend:apps~a-1:g3,backend+dev:apps~a-1:g7,backend+dev:apps~a-1:g8,t1"},
		{Filter{Deployment: "main"}, "backend:apps~a-1:g3,t1,backend:apps~b-2:g1"},
		{Filter{Tile: "apps/a", Deployment: "main", Kind: Backend}, "backend:apps~a-1:g3"},
		{Filter{Tile: "apps/a", Deployment: "dev"}, "backend+dev:apps~a-1:g7,backend+dev:apps~a-1:g8"},
		{Filter{Deployment: "dev", Kind: Terminal}, ""},
		{Filter{Tile: "apps/b", Deployment: "dev"}, ""},
		{Filter{Deployment: "staging"}, ""},
	} {
		if got := ids(c.f); got != c.want {
			t.Errorf("List(%+v) = %q, want %q", c.f, got, c.want)
		}
	}

	// the same refusal for main and for dev: two rows, each counted apart
	refusal := "the workspace's VM memory budget (4096 MiB) is spent"
	r.Fail(Failure{Kind: Backend, Tile: "apps/a", Mode: VM, Stage: Refused, Error: refusal, Time: t0})
	r.Fail(Failure{Kind: Backend, Tile: "apps/a", Deployment: "dev", Mode: VM, Stage: Refused, Error: refusal, Time: t0.Add(time.Second)})
	r.Fail(Failure{Kind: Backend, Tile: "apps/a", Deployment: "dev", Mode: VM, Stage: Refused, Error: refusal, Time: t0.Add(2 * time.Second)})
	all := r.Failures(Filter{Tile: "apps/a"})
	if len(all) != 2 || all[0].Deployment != "dev" || all[0].Count != 2 || all[1].Deployment != "" || all[1].Count != 1 {
		t.Fatalf("a dev refusal merged with main's, or didn't coalesce with its own: %+v", all)
	}
	if f := r.Failures(Filter{Deployment: "main"}); len(f) != 1 || f[0].Deployment != "" {
		t.Fatalf("main's failures: %+v", f)
	}
	if f := r.Failures(Filter{Tile: "apps/a", Deployment: "dev"}); len(f) != 1 || f[0].Count != 2 {
		t.Fatalf("dev's failures: %+v", f)
	}
	if n := r.FailureCounts()[Refused]; n != 3 {
		t.Fatalf("refusals since boot: %d", n)
	}

	// main's rows gain no key; another deployment's carry its name
	for _, c := range []struct {
		v    any
		want bool
	}{
		{Entry{ID: "backend:apps~a-1:g3", Kind: Backend, Tile: "apps/a"}, false},
		{Failure{Kind: Backend, Tile: "apps/a", Stage: Refused}, false},
		{Entry{ID: "backend+dev:apps~a-1:g7", Kind: Backend, Tile: "apps/a", Deployment: "dev"}, true},
		{Failure{Kind: Backend, Tile: "apps/a", Deployment: "dev", Stage: Refused}, true},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if dep, has := m["deployment"]; has != c.want || (has && dep != "dev") {
			t.Errorf("%s: deployment key %v, want present=%v", b, dep, c.want)
		}
	}
}
