package runner

// covers PD-04 PD-17 PD-19 PD-28 S5 — what a person's partition's
// generation spawns with (plans/partitions/03 §A.4): its own run dir, log,
// env and data, XBIN_PARTITION where it belongs (and nowhere on an
// unpartitioned tile), and the non-primary network rule whatever the hooks
// answer.

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

const pkA = "u-0123456789abcdef0123456789abcdef" // alice's pkey in these tests

// partSpawnRunner is spawnRunner with the partition hooks wired.
func partSpawnRunner(t *testing.T) (*Runner, *registry.Component, *[]string) {
	t.Helper()
	r, c := spawnRunner(t, true, "main")
	var registered []string
	r.PartitionEnv = func(v *registry.Component, dep, part string) ([]string, map[string]ResBind) {
		return []string{"XBIN_RES_DB=" + v.Dir + "/data/" + part}, map[string]ResBind{}
	}
	r.RegisterPartitionInstance = func(token, tile, dep, part, uid string) {
		registered = append(registered, tile+" "+dep+" "+part+" "+uid)
	}
	return r, c, &registered
}

// covers PD-04 — the env golden (deploy_test.go style): an unpartitioned
// tile's env is today's, byte for byte; the global instance of a
// partitioned tile gets XBIN_PARTITION=global right after XBIN_COMPONENT;
// a person's partition gets its key there and its PartitionEnv; a
// non-primary deployment whose own code asks for partitions gets global
// after XBIN_DEPLOYMENT.
func TestPartitionEnvGolden(t *testing.T) {
	r, c, registered := partSpawnRunner(t)
	gw := filepath.Join(r.RunDir, "gateway.sock")
	base := backendEnv(true)
	env := func(sp genSpawn, mid []string, tail ...string) []string {
		out := append(append([]string{}, base...), "XBIN_SOCKET="+sp.sock, "XBIN_COMPONENT=apps/x")
		out = append(out, mid...)
		out = append(out, "XBIN_GATEWAY="+gw, "XBIN_TOKEN="+sp.token)
		return append(out, tail...)
	}

	sp, err := r.spawnSetup(c, "main", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := env(sp, nil, "XBIN_RES_DB="+c.Dir+"/data/main"); !equalStrings(sp.env, want) {
		t.Errorf("unpartitioned:\n got %q\nwant %q", sp.env, want)
	}

	setMode(r, "apps/x", userGlobal)
	sp, err = r.spawnSetup(c, "main", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := env(sp, []string{"XBIN_PARTITION=global"}, "XBIN_RES_DB="+c.Dir+"/data/main"); !equalStrings(sp.env, want) {
		t.Errorf("the global instance:\n got %q\nwant %q", sp.env, want)
	}
	if sp.dir != filepath.Join(r.RunDir, ckX) || sp.log != filepath.Join(r.Root, ".xbin/log/"+ckX+".log") {
		t.Errorf("the global instance moved: %q %q (PD-04: today's keys)", sp.dir, sp.log)
	}

	v := partitionView(c, "user:alice", pkA)
	sp, err = r.spawnSetup(v, "main", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := env(sp, []string{"XBIN_PARTITION=user:alice"}, "XBIN_RES_DB="+c.Dir+"/data/user:alice"); !equalStrings(sp.env, want) {
		t.Errorf("alice's partition:\n got %q\nwant %q", sp.env, want)
	}
	if want := filepath.Join(r.RunDir, "p-2db3f2a97388013b"); sp.dir != want || sp.sock != filepath.Join(want, "g3.sock") {
		t.Errorf("alice's run dir %q, socket %q", sp.dir, sp.sock)
	}
	wantLog := filepath.Join(r.Root, ".xbin/partition/"+tkX+"/main/"+pkA+"/backend.log")
	if sp.log != wantLog {
		t.Errorf("alice's log %q, want %q", sp.log, wantLog)
	}
	if fi, err := os.Stat(filepath.Dir(wantLog)); err != nil || !fi.IsDir() {
		t.Errorf("alice's log directory: %v", err)
	}
	r.registerGen("tok", v, "main") // a spawn no state opened: no token
	if len(*registered) != 0 {
		t.Errorf("a spawn without a state registered %q", *registered)
	}
	s := &state{comp: "apps/x", dep: "main", pt: &partInfo{part: "user:alice", pkey: pkA, uid: "uid-a"}}
	r.parts.spawns = map[*registry.Component]*state{v: s}
	r.registerGen("tok", v, "main")
	if want := []string{"apps/x main user:alice uid-a"}; !equalStrings(*registered, want) || s.pt.token != "tok" {
		t.Errorf("registered %q (kept %q), want %q from its state", *registered, s.pt.token, want)
	}
	s.gone = true
	r.registerGen("tok2", v, "main")
	if len(*registered) != 1 {
		t.Errorf("a stopped state's spawn registered %q", *registered)
	}

	dev := nonPrimary(c, "dev")
	dev.Manifest.Partition = registry.PartitionList{"user", "global"}
	sp, err = r.spawnSetup(dev, "dev", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := env(sp, nil, "XBIN_DEPLOYMENT=dev", "XBIN_PARTITION=global", "XBIN_RES_DB="+c.Dir+"/data/dev"); !equalStrings(sp.env, want) {
		t.Errorf("a non-primary deployment whose code asks:\n got %q\nwant %q", sp.env, want)
	}
	setMode(r, "apps/x", registry.PartitionSpec{})
	dev.Manifest.Partition = nil
	sp, _ = r.spawnSetup(dev, "dev", 2)
	for _, e := range sp.env {
		if strings.HasPrefix(e, "XBIN_PARTITION=") {
			t.Errorf("an unpartitioned deployment got %s", e)
		}
	}

	t.Run("refusals", func(t *testing.T) {
		for name, tc := range map[string]struct {
			v    *registry.Component
			dep  string
			wire func(*Runner)
		}{
			"no isolation":    {partitionView(c, "user:alice", pkA), "main", func(r *Runner) { r.Isolate = false }},
			"not the primary": {partitionView(c, "user:alice", pkA), "dev", nil},
			"no env hook":     {partitionView(c, "user:alice", pkA), "main", func(r *Runner) { r.PartitionEnv = nil }},
			"no token hook":   {partitionView(c, "user:alice", pkA), "main", func(r *Runner) { r.RegisterPartitionInstance = nil }},
			"a bad id":        {partitionView(c, "user:alice", "../x"), "main", nil},
			"not a person":    {partitionView(c, "global", pkA), "main", nil},
		} {
			r, _, _ := partSpawnRunner(t)
			if tc.wire != nil {
				tc.wire(r)
			}
			if _, err := r.spawnSetup(tc.v, tc.dep, 1); err == nil {
				t.Errorf("%s: spawned", name)
			}
		}
	})
}

// covers PD-28 S5 — TestUserPartitionNetworkIsNonPrimary (03 §Tests): the
// spec of a person's partition of a host-net, spliced, net-provider or
// ingress fixture has no host network, splice, roster, links or ingress
// relay; its egress is withheld where the tile's net is host-shared or
// spliced (D127o's verdict), and is the relay under the tile's policy
// otherwise; its relay keeps only stream forwards, each dial refused.
func TestUserPartitionNetworkIsNonPrimary(t *testing.T) {
	f := newLaunchFixture(t)
	for _, tc := range []struct {
		name       string
		pol        []string
		wire       func(r *Runner)
		primary    func(s *sandbox.Spec) bool // the primary gets the wiring
		partNet    string                     // a partition's spec.Net
		partEgress bool                       // spawnEgress keeps the policy
	}{
		{"host network", []string{"net:internet"}, func(r *Runner) { r.NetHost = func(*registry.Component) bool { return true } },
			func(s *sandbox.Spec) bool { return s.HostNet }, "", false},
		{"splice", []string{"net:internet"}, func(r *Runner) {
			r.NetTarget = func(*registry.Component) (string, string, string, bool) {
				return "apps/router", "10.42.0.2/30", "10.42.0.1", true
			}
		}, func(s *sandbox.Spec) bool { return s.Net == "splice" }, "", false},
		{"net provider", nil, func(r *Runner) {
			r.NetRoster = func(*registry.Component) []sandbox.NetClient {
				return []sandbox.NetClient{{Name: "apps/c", Addr: "10.42.0.1/30"}}
			}
		}, func(s *sandbox.Spec) bool { return len(s.NetClients) == 1 }, "", true},
		{"lan-ingress legs", nil, func(r *Runner) {
			r.NetLinks = func(*registry.Component) []sandbox.NetLink {
				return []sandbox.NetLink{{Provider: "apps/router", Slot: "lan"}}
			}
		}, func(s *sandbox.Spec) bool { return len(s.NetLinks) == 1 }, "", true},
		{"ingress relay", nil, func(r *Runner) { r.IngressNet = func(*registry.Component) bool { return true } },
			func(s *sandbox.Spec) bool { return s.Net == "relay" }, "", true},
		{"granted egress", []string{"net:internet:443"}, nil,
			func(s *sandbox.Spec) bool { return s.Net == "relay" }, "relay", true},
	} {
		r := f.runner()
		if tc.wire != nil {
			tc.wire(r)
		}
		pol := egress(t, tc.pol...)
		r.Egress = func(*registry.Component) sandbox.EgressPolicy { return pol }
		c := f.comp(t, `{"runtime":"go"}`)
		v := partitionView(c, "user:alice", pkA)
		r.PartitionEnv = func(*registry.Component, string, string) ([]string, map[string]ResBind) {
			return nil, map[string]ResBind{}
		}
		if s := r.launchSpec(c, f.bin(c), lsDir, lsEnv(), pol, ""); !tc.primary(s) {
			t.Errorf("%s: the primary lacks the wiring (fixture): %+v", tc.name, s)
		}
		ppol, _ := r.spawnEgress(v) // what startDeployment hands the spec
		s := r.launchSpec(v, f.bin(c), lsDir, lsEnv(), ppol, "")
		if s.HostNet || s.Net == "splice" || len(s.NetClients) > 0 || len(s.NetLinks) > 0 || s.Net != tc.partNet {
			t.Errorf("%s: a person's partition got %+v", tc.name, s)
		}
		got, why := r.spawnEgress(v)
		if kept := reflect.DeepEqual(got.Strings(), pol.Strings()) && why == ""; kept != tc.partEgress || !kept && (!got.Empty() || why == "") {
			t.Errorf("%s: a partition's egress %v (%q), want the tile's kept: %v", tc.name, got.Strings(), why, tc.partEgress)
		}
		np := r.netPlanFor(v, "main", io.Discard)
		if len(np.clients) > 0 || np.spliced || len(np.links) > 0 {
			t.Errorf("%s: a partition's net plan %+v", tc.name, np)
		}
	}

	r := f.runner()
	r.IngressFwd = func(*registry.Component) map[int]string {
		return map[int]string{1: "unix:/door", 2: "stream:apps/db:5432"}
	}
	c := f.comp(t, `{"runtime":"go"}`)
	v := partitionView(c, "user:alice", pkA)
	if got, want := r.ingressFwd(v), map[int]string{2: "stream:apps/db:5432"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a partition's forwards %v, want %v", got, want)
	}
	for _, dst := range []string{"unix:/door", "stream:apps/db:5432"} {
		if _, err := r.hostDialFor("apps/counter", "main", true, io.Discard)(dst); err == nil {
			t.Errorf("a partition dialed %s", dst)
		}
	}
}

// covers T18 C7 — a person's partition binds only what its PartitionEnv
// remap names, on main as anywhere: without an entry a path-valued
// resource fails the start (never main's data at its own path).
func TestPartitionDataBinds(t *testing.T) {
	f := newLaunchFixture(t)
	r := f.runner()
	c := f.comp(t, `{"runtime":"go"}`)
	v := partitionView(c, "user:alice", pkA)
	canon := filepath.Join(f.root, "data/resources/apps~counter/files")
	env := lsEnv("XBIN_RES_FILES=" + canon)
	if _, err := r.dataBinds(v, env); err == nil {
		t.Error("a partition without the data hook bound its resource")
	}
	r.PartitionEnv = func(*registry.Component, string, string) ([]string, map[string]ResBind) {
		return nil, map[string]ResBind{}
	}
	if _, err := r.dataBinds(v, env); err == nil || !strings.Contains(err.Error(), "no data namespace") {
		t.Errorf("a partition without a remap entry: %v", err)
	}
	own := t.TempDir()
	r.PartitionEnv = func(*registry.Component, string, string) ([]string, map[string]ResBind) {
		return nil, map[string]ResBind{canon: {Src: own}}
	}
	binds, err := r.dataBinds(v, env)
	if err != nil || len(binds) != 1 || binds[0].Src != own || binds[0].Dst != canon {
		t.Errorf("alice's binds %+v (%v), want her own namespace at the canonical path", binds, err)
	}
	if binds := resourceBinds(env, f.root); len(binds) != 1 || binds[0].Src != canon {
		t.Errorf("the global instance's binds changed: %+v", binds)
	}
}
