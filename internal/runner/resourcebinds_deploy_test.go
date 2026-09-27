package runner

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// nsLayout is a workspace whose tile apps/counter has a filesystem resource
// (files) and a sqlite one (db) in main's data namespace, and the dev
// deployment's volumes for both, laid out as 08-data §3.1 has them.
type nsLayout struct {
	lsFixture
	mainFiles, mainDB string // main's canonical dirs: the XBIN_RES_* values (db's is its dir)
	devFiles, devDB   string // dev's volumes behind them
}

func newNSLayout(t *testing.T) nsLayout {
	t.Helper()
	f := newLaunchFixture(t)
	l := nsLayout{
		lsFixture: f,
		mainFiles: filepath.Join(f.root, ".xbin/resenc/apps~counter/files"),
		mainDB:    filepath.Join(f.root, ".xbin/resenc/apps~counter/db"),
		devFiles:  filepath.Join(f.root, ".xbin/resenc/.deployments/apps~counter/dev/fs/files"),
		devDB:     filepath.Join(f.root, ".xbin/resenc/.deployments/apps~counter/dev/fs/db"),
	}
	for _, d := range []string{l.mainFiles, l.mainDB, l.devFiles, l.devDB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// env is what the broker's EnvFor emits for either deployment: main's
// canonical values (08-data §5 item 1), plus kv and a blocked resource.
func (l nsLayout) env(extra ...string) []string {
	return lsEnv(append([]string{
		"XBIN_RES_FILES=" + l.mainFiles,
		"XBIN_RES_DB=" + filepath.Join(l.mainDB, "db.sqlite"),
	}, extra...)...)
}

// remap is dev's resource remap, keyed by the canonical directories.
func (l nsLayout) remap() map[string]ResBind {
	return map[string]ResBind{l.mainFiles: {Src: l.devFiles}, l.mainDB: {Src: l.devDB}}
}

// covers P6 P17 PO-2 T18 — TestResourceBinds' remap rows (07-runtime
// §10.4): main unchanged; dev remapped at the canonical paths, an Omit entry
// unbound; a missing entry, an entry in main's data namespace or
// overlapping the canonical path, a relative one and one not mounted each
// refused; the read-only flag carried; another deployment binds only
// XBIN_RES_* values, never a run dir the env names under the workspace.
func TestResourceBindsRemap(t *testing.T) {
	l := newNSLayout(t)
	env := l.env()

	t.Run("main: a nil remap is today's binds", func(t *testing.T) {
		got, err := resourceBindsFor(env, l.root, "main", nil)
		if err != nil || !reflect.DeepEqual(got, resourceBinds(env, l.root)) {
			t.Fatalf("got %+v %v, want resourceBinds' %+v", got, err, resourceBinds(env, l.root))
		}
		for _, b := range got {
			if b.Src != b.Dst || b.RO {
				t.Errorf("main's bind %+v isn't read-write at itself", b)
			}
		}
	})

	t.Run("dev: bound at the canonical paths from its own volumes", func(t *testing.T) {
		got, err := resourceBindsFor(env, l.root, "dev", l.remap())
		want := []sandbox.Bind{{Src: l.devFiles, Dst: l.mainFiles}, {Src: l.devDB, Dst: l.mainDB}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v %v, want %+v", got, err, want)
		}
	})

	t.Run("dev: an Omit entry binds nothing", func(t *testing.T) {
		m := l.remap()
		m[l.mainDB] = ResBind{Omit: true}
		got, err := resourceBindsFor(env, l.root, "dev", m)
		if want := []sandbox.Bind{{Src: l.devFiles, Dst: l.mainFiles}}; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v %v, want %+v", got, err, want)
		}
	})

	t.Run("dev: the read-only flag is carried", func(t *testing.T) {
		m := l.remap()
		m[l.mainFiles] = ResBind{Src: l.devFiles, RO: true}
		got, err := resourceBindsFor(env, l.root, "dev", m)
		if err != nil || len(got) == 0 || !got[0].RO {
			t.Fatalf("got %+v %v, want the files bind read-only", got, err)
		}
	})

	t.Run("dev: the run dir under the workspace is never bound from the env", func(t *testing.T) {
		run := filepath.Join(l.root, ".xbin/run/apps~counter-0badc0de")
		if err := os.MkdirAll(run, 0o755); err != nil {
			t.Fatal(err)
		}
		e := append(l.env(), "XBIN_SOCKET="+run+"/g1.sock", "XBIN_GATEWAY="+filepath.Dir(run)+"/gateway.sock")
		got, err := resourceBindsFor(e, l.root, "dev", l.remap())
		if err != nil || len(got) != 2 {
			t.Fatalf("got %+v %v, want the two resource binds alone", got, err)
		}
	})

	refused := []struct {
		name  string
		remap func(map[string]ResBind)
		want  string
	}{
		{"a missing entry", func(m map[string]ResBind) { delete(m, l.mainDB) }, "no data namespace for dev's resource XBIN_RES_DB"},
		{"main's mount", func(m map[string]ResBind) { m[l.mainDB] = ResBind{Src: l.mainFiles} }, "would bind main's data"},
		{"main's ciphertext", func(m map[string]ResBind) {
			m[l.mainDB] = ResBind{Src: filepath.Join(l.root, "data/resources-enc/apps~counter/db")}
		}, "would bind main's data"},
		{"main's plaintext", func(m map[string]ResBind) {
			m[l.mainDB] = ResBind{Src: filepath.Join(l.root, "data/resources/apps~counter")}
		}, "would bind main's data"},
		{"the canonical dir's parent", func(m map[string]ResBind) {
			m[l.mainDB] = ResBind{Src: filepath.Join(l.root, ".xbin/resenc")}
		}, "would bind main's data"},
		{"a relative path", func(m map[string]ResBind) { m[l.mainDB] = ResBind{Src: "dev/fs/db"} }, "isn't an absolute path"},
		{"a namespace not mounted", func(m map[string]ResBind) {
			m[l.mainDB] = ResBind{Src: filepath.Join(l.root, ".xbin/resenc/.deployments/apps~counter/dev/fs/none")}
		}, "isn't mounted"},
	}
	for _, tc := range refused {
		t.Run("dev refused: "+tc.name, func(t *testing.T) {
			m := l.remap()
			tc.remap(m)
			got, err := resourceBindsFor(env, l.root, "dev", m)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
				t.Fatalf("got %+v %v, want an error with %q", got, err, tc.want)
			}
		})
	}

	t.Run("dev refused: a volume that is a symlink", func(t *testing.T) {
		link := filepath.Join(l.root, ".xbin/resenc/.deployments/apps~counter/dev/fs/link")
		if err := os.Symlink(l.devDB, link); err != nil {
			t.Fatal(err)
		}
		m := l.remap()
		m[l.mainDB] = ResBind{Src: link}
		if _, err := resourceBindsFor(env, l.root, "dev", m); err == nil || !strings.Contains(err.Error(), "isn't mounted") {
			t.Fatalf("got %v, want the symlinked volume refused", err)
		}
	})

	t.Run("dev refused: an entry overlapping its canonical dir", func(t *testing.T) {
		canon := filepath.Join(l.root, "res/files")
		if err := os.MkdirAll(canon, 0o755); err != nil {
			t.Fatal(err)
		}
		e := lsEnv("XBIN_RES_FILES=" + canon)
		for _, src := range []string{canon, filepath.Dir(canon)} {
			if _, err := resourceBindsFor(e, l.root, "dev", map[string]ResBind{canon: {Src: src}}); err == nil {
				t.Errorf("Src %s over the canonical %s was bound", src, canon)
			}
		}
	})
}

// nsRunner is an isolated runner over l whose primary is main and whose
// EnvFor hook answers dev's remap from remap (nil: no remap at all), counting
// its calls.
func (l nsLayout) nsRunner(remap func() map[string]ResBind, calls *int) *Runner {
	r := l.runner()
	r.Primary = func(string) string { return "main" }
	r.EnvFor = func(c *registry.Component, dep string) ([]string, map[string]ResBind) {
		*calls++
		if dep == "main" {
			return l.env(), nil
		}
		return l.env(), remap()
	}
	return r
}

func (l nsLayout) view(t *testing.T, dep, manifest string) *registry.Component {
	c := l.comp(t, manifest)
	c.Deployment = dep
	return c
}

// covers P6 P14 T18 SC-DATA — a non-primary deployment's data dirs are bound
// at the primary's paths (Src ≠ Dst): its XBIN_RES_* values are main's, and
// no bind of its launch spec has a Src in main's data namespace (08-data §5
// item 3); main's spec is today's, whatever hooks are wired, and main's
// start never asks the EnvFor hook for a remap. A dev start whose remap is
// missing (a hook answering none) or points into main's data fails before
// anything launches, and its spec binds none of it.
func TestNonPrimaryResourceBindsNeverPrimary(t *testing.T) {
	l := newNSLayout(t)
	env := l.env()
	calls := 0
	r := l.nsRunner(l.remap, &calls)

	mainView := l.comp(t, `{"runtime":"go"}`)
	mainSpec := r.launchSpec(mainView, l.bin(mainView), lsDir, env, sandbox.EgressPolicy{}, "")
	if zero := l.runner().launchSpec(mainView, l.bin(mainView), lsDir, env, sandbox.EgressPolicy{}, ""); !reflect.DeepEqual(mainSpec, zero) {
		t.Errorf("main's spec with the hooks wired differs from today's:\n%s\n%s", specJSON(t, mainSpec), specJSON(t, zero))
	}
	if calls != 0 {
		t.Errorf("main's launch asked EnvFor %d times", calls)
	}
	if !containsBind(mainSpec.Binds, sandbox.Bind{Src: l.mainFiles, Dst: l.mainFiles}) {
		t.Errorf("main's files aren't bound at themselves: %s", specJSON(t, mainSpec))
	}

	devView := l.view(t, "dev", `{"runtime":"go"}`)
	devSpec := r.launchSpec(devView, l.bin(devView), lsDir, env, sandbox.EgressPolicy{}, "")
	if !reflect.DeepEqual(resEnv(devSpec.Env), resEnv(mainSpec.Env)) || len(resEnv(devSpec.Env)) != 3 {
		t.Errorf("XBIN_RES_* differ: dev %q, main %q", resEnv(devSpec.Env), resEnv(mainSpec.Env))
	}
	for _, want := range []sandbox.Bind{{Src: l.devFiles, Dst: l.mainFiles}, {Src: l.devDB, Dst: l.mainDB}} {
		if !containsBind(devSpec.Binds, want) {
			t.Errorf("dev's spec lacks %+v: %s", want, specJSON(t, devSpec))
		}
	}
	for _, b := range devSpec.Binds {
		if !b.Mask && mainData(l.root, b.Src) {
			t.Errorf("dev's spec binds main's data: %+v", b)
		}
	}

	for name, remap := range map[string]func() map[string]ResBind{
		"no remap": func() map[string]ResBind { return nil },
		"into main's data": func() map[string]ResBind {
			m := l.remap()
			m[l.mainFiles] = ResBind{Src: l.mainFiles}
			return m
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := l.nsRunner(remap, new(int))
			cmd, h, err := r.sandboxCmd(devView, l.bin(devView), lsDir, lsDir+"/g1.sock", env, sandbox.EgressPolicy{}, "")
			if err == nil || cmd != nil || h != nil {
				t.Fatalf("dev started with %s: %v", name, err)
			}
			spec := r.launchSpec(devView, l.bin(devView), lsDir, env, sandbox.EgressPolicy{}, "")
			for _, b := range spec.Binds {
				if within(b.Dst, filepath.Join(l.root, ".xbin/resenc")) || mainData(l.root, b.Src) {
					t.Errorf("a refused spec binds %+v", b)
				}
			}
		})
	}
}

// covers T18 P6 — a VM backend of a non-primary deployment gets its data the
// same way: the launch spec a VM sandbox is made from binds dev's volumes at
// the canonical paths, and VM exports hand the guest each bind by its
// destination (internal/vm), so the guest sees dev's data where main's
// would be, and none of main's. The data check comes before the VM gate: a
// VM start whose data can't be bound fails before any VM is reserved.
func TestVMBackendNamespaceBinds(t *testing.T) {
	l := newNSLayout(t)
	env := l.env()
	r := l.nsRunner(l.remap, new(int))
	v := l.view(t, "dev", `{"runtime":"go","vm":true}`)
	if !r.wantsVM(v) {
		t.Fatal("the view doesn't want a VM")
	}
	spec := r.launchSpec(v, l.bin(v), lsDir, env, sandbox.EgressPolicy{}, "")
	var data []sandbox.Bind
	for _, b := range spec.Binds {
		if mainData(l.root, b.Src) {
			t.Errorf("the VM spec binds main's data: %+v", b)
		}
		if within(b.Dst, filepath.Join(l.root, ".xbin/resenc")) {
			data = append(data, b)
		}
	}
	if want := []sandbox.Bind{{Src: l.devFiles, Dst: l.mainFiles}, {Src: l.devDB, Dst: l.mainDB}}; !reflect.DeepEqual(data, want) {
		t.Errorf("the VM spec's data binds %+v, want %+v", data, want)
	}

	// with its data bound, the start reaches the VM gate (no manager here)
	_, _, err := r.sandboxCmd(v, l.bin(v), lsDir, lsDir+"/g1.sock", env, sandbox.EgressPolicy{}, "")
	if !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("a VM start with its data mapped: %v, want the VM gate's refusal", err)
	}
	// without, it stops at the data check, before the VM gate
	bad := l.nsRunner(func() map[string]ResBind { return map[string]ResBind{} }, new(int))
	_, _, err = bad.sandboxCmd(v, l.bin(v), lsDir, lsDir+"/g1.sock", env, sandbox.EgressPolicy{}, "")
	if err == nil || errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "no data namespace for dev's") {
		t.Errorf("a VM start with unmapped data: %v, want the data refusal", err)
	}
}

// covers P23 T18 PO-11 — whatever the spawn-time hooks answer, a non-primary
// generation's launch spec never takes the primary-only wiring (07-runtime
// §10.4): no provider roster, no lan-ingress legs, no host network (the
// host-sharing tile), no splice and no ingress plumbing; the relay under
// its egress policy, capability grants and GPUs stay the hooks' answers.
// The primary's spec keeps all of today's wiring.
func TestNonPrimaryLaunchWiring(t *testing.T) {
	f := newLaunchFixture(t)
	yes := func(*registry.Component) bool { return true }
	wired := func(host bool) *Runner {
		r := f.runner()
		r.NetRoster = func(*registry.Component) []sandbox.NetClient {
			return []sandbox.NetClient{{Name: "apps/client", Addr: "10.42.0.1/30"}}
		}
		r.NetLinks = func(*registry.Component) []sandbox.NetLink {
			return []sandbox.NetLink{{Provider: "apps/lan", Slot: "lan", Addr: "10.43.0.2/30"}}
		}
		r.NetHost = func(*registry.Component) bool { return host }
		r.NetTarget = func(*registry.Component) (string, string, string, bool) {
			return "apps/prov", "10.44.0.2/30", "10.44.0.1", true
		}
		r.IngressNet, r.NetCaps, r.ContainerCaps = yes, yes, yes
		r.GPU = func(*registry.Component) []gpu.Device { return nil }
		r.Primary = func(string) string { return "main" }
		return r
	}
	relay := egress(t, "net:internet")
	primary, dev := f.comp(t, `{"runtime":"go"}`), f.comp(t, `{"runtime":"go"}`)
	dev.Deployment = "dev"

	cases := []struct {
		name string
		host bool
		v    *registry.Component
		pol  sandbox.EgressPolicy
		net  string // spec.Net
		hn   bool   // spec.HostNet
		legs bool   // roster and lan-ingress legs
	}{
		{"primary, host-sharing tile", true, primary, sandbox.EgressPolicy{}, "", true, true},
		{"primary, spliced to a provider", false, primary, sandbox.EgressPolicy{}, "splice", false, true},
		{"dev, host-sharing tile, no egress", true, dev, sandbox.EgressPolicy{}, "", false, false},
		{"dev, host-sharing tile, relay egress", true, dev, relay, "relay", false, false},
		{"dev, provider splice, no egress", false, dev, sandbox.EgressPolicy{}, "", false, false},
		{"dev, relay egress", false, dev, relay, "relay", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := wired(tc.host).launchSpec(tc.v, f.bin(tc.v), lsDir, lsEnv(), tc.pol, "")
			if spec.Net != tc.net || spec.HostNet != tc.hn {
				t.Errorf("net %q hostnet %v, want %q %v", spec.Net, spec.HostNet, tc.net, tc.hn)
			}
			if legs := len(spec.NetClients) > 0 || len(spec.NetLinks) > 0; legs != tc.legs {
				t.Errorf("roster %v legs %v, want present=%v", spec.NetClients, spec.NetLinks, tc.legs)
			}
			if tc.v.Deployment != "" && (spec.NetAddr != "" || spec.NetGw != "") {
				t.Errorf("dev spliced: %q %q", spec.NetAddr, spec.NetGw)
			}
			if !spec.NetAdmin || !spec.Containers {
				t.Errorf("the capability hooks' answers were dropped: net-admin %v containers %v", spec.NetAdmin, spec.Containers)
			}
		})
	}
}

// resEnv is env's XBIN_RES_* entries.
func resEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "XBIN_RES_") {
			out = append(out, e)
		}
	}
	return out
}
