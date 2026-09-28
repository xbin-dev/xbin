package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// The launch-spec goldens pin what a zero-state tile's backend generation
// launches with: binds, entry, argv, env, overlay lowers and the network and
// capability wiring — master's, NoFollow's walk included (WP-2b, D120). They are hand-maintained; no switch regenerates them.
// Only values that differ between runs are masked: the temp workspace
// ({{ROOT}}), the temp rootfs ({{ROOTFS}}), the host uid and gid, and the env
// (the test's own input, which must pass through unchanged). {{RUN}} is a
// constant stand-in for the tmpfs run dir.

const (
	lsRun = "/run/xbin/xbin-0badc0de/run"
	lsDir = lsRun + "/apps~counter-0badc0de" // the generation's run dir (an input)
)

type lsFixture struct {
	root, rootfs string
}

func newLaunchFixture(t *testing.T) lsFixture {
	t.Helper()
	f := lsFixture{root: t.TempDir(), rootfs: t.TempDir()}
	for _, d := range []string{"usr/local/node/bin", "usr/bin"} {
		if err := os.MkdirAll(filepath.Join(f.rootfs, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []string{"usr/local/node/bin/node", "usr/bin/python3"} {
		if err := os.WriteFile(filepath.Join(f.rootfs, b), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.root, "data/resources/apps~counter/files"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// runner is an isolated Runner with no hooks wired, built without New so no
// reaper, run dir or log dir is created.
func (f lsFixture) runner() *Runner {
	return &Runner{Root: f.root, RunDir: lsRun, Isolate: true, Rootfs: f.rootfs, states: map[string]*state{}}
}

func (f lsFixture) comp(t *testing.T, manifest string) *registry.Component {
	t.Helper()
	c := &registry.Component{Path: "apps/counter", Dir: filepath.Join(f.root, "apps/counter")}
	if err := json.Unmarshal([]byte(manifest), &c.Manifest); err != nil {
		t.Fatal(err)
	}
	return c
}

// bin is what build hands start for c's runtime.
func (f lsFixture) bin(c *registry.Component) string {
	switch c.Manifest.Runtime {
	case "node":
		return filepath.Join(c.Dir, "backend/server.js")
	case "python":
		return filepath.Join(c.Dir, "backend/server.py")
	}
	return filepath.Join(f.root, ".xbin/build/apps~counter-0badc0de/bin")
}

// lsEnv is the env start hands sandboxCmd, plus the case's extra entries.
func lsEnv(extra ...string) []string {
	return append([]string{
		"PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp",
		"XBIN_SOCKET=" + lsDir + "/g1.sock",
		"XBIN_COMPONENT=apps/counter",
		"XBIN_GATEWAY=" + lsRun + "/gateway.sock",
		"XBIN_TOKEN=0123456789abcdef",
		"XBIN_RES_KV=res:apps/counter/kv",
	}, extra...)
}

func egress(t *testing.T, targets ...string) sandbox.EgressPolicy {
	t.Helper()
	var p sandbox.EgressPolicy
	for _, tg := range targets {
		r, err := sandbox.ParseRule(tg)
		if err != nil {
			t.Fatal(err)
		}
		p.Rules = append(p.Rules, r)
	}
	return p
}

// The binds every backend generation starts with: its code read-only at its
// own path, its run dir and the gateway socket read-write.
const lsBaseBinds = `
	{"src":"{{ROOT}}/apps/counter","dst":"{{ROOT}}/apps/counter","ro":true},
	{"src":"{{RUN}}/apps~counter-0badc0de","dst":"{{RUN}}/apps~counter-0badc0de","ro":false},
	{"src":"{{RUN}}/gateway.sock","dst":"{{RUN}}/gateway.sock","ro":false}`

const lsGoBin = `{"src":"{{ROOT}}/.xbin/build/apps~counter-0badc0de/bin","dst":"/run/backend","ro":true}`

// lsGoGolden is a go backend with nothing granted or bound.
const lsGoGolden = `{
	"lower":["{{ROOTFS}}"],
	"binds":[` + lsBaseBinds + `, ` + lsGoBin + `],
	"entry":"/run/backend",
	"argv":["/run/backend"],
	"env":{{ENV}},
	"cwd":"{{ROOT}}/apps/counter",
	"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
	"unprivileged":true
}`

// lsGoWith is lsGoGolden plus the given network and capability fields (the
// comparison ignores key order).
func lsGoWith(fields string) string {
	return `{
	"lower":["{{ROOTFS}}"],
	"binds":[` + lsBaseBinds + `, ` + lsGoBin + `],
	"entry":"/run/backend",
	"argv":["/run/backend"],
	"env":{{ENV}},
	"cwd":"{{ROOT}}/apps/counter",
	"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
	"unprivileged":true,
	` + fields + `
}`
}

type lsCase struct {
	name     string
	manifest string
	wire     func(r *Runner)
	extraEnv []string
	pol      []string
	envLower string
	golden   string
}

// covers P5 PO-11 Z8 SC-ZERO — a zero-state tile's backend launch spec
// (binds, argv, env, overlay lowers, egress and capability wiring) is exactly
// today's, for every runtime and every wiring the hooks can answer.
func TestZeroStateLaunchSpec(t *testing.T) {
	cases := []lsCase{
		{name: "go", manifest: `{"runtime":"go"}`, golden: lsGoGolden},
		{name: "node", manifest: `{"runtime":"node"}`, golden: `{
			"lower":["{{ROOTFS}}"],
			"binds":[` + lsBaseBinds + `],
			"entry":"/usr/local/node/bin/node",
			"argv":["node","{{ROOT}}/apps/counter/backend/server.js"],
			"env":{{ENV}},
			"cwd":"{{ROOT}}/apps/counter",
			"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
			"unprivileged":true
		}`},
		{name: "python", manifest: `{"runtime":"python"}`, golden: `{
			"lower":["{{ROOTFS}}"],
			"binds":[` + lsBaseBinds + `],
			"entry":"/usr/bin/python3",
			"argv":["python3","{{ROOT}}/apps/counter/backend/server.py"],
			"env":{{ENV}},
			"cwd":"{{ROOT}}/apps/counter",
			"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
			"unprivileged":true
		}`},
		{name: "env layer", manifest: `{"runtime":"go","setup":"apk add jq"}`,
			envLower: "{{ROOT}}/.xbin/env/apps~counter-0badc0de/0123456789abcdef/upper",
			golden: `{
			"lower":["{{ROOT}}/.xbin/env/apps~counter-0badc0de/0123456789abcdef/upper","{{ROOTFS}}"],
			"binds":[` + lsBaseBinds + `, ` + lsGoBin + `],
			"entry":"/run/backend",
			"argv":["/run/backend"],
			"env":{{ENV}},
			"cwd":"{{ROOT}}/apps/counter",
			"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
			"unprivileged":true
		}`},
		{name: "file resources", manifest: `{"runtime":"go"}`,
			extraEnv: []string{
				"XBIN_RES_DB={{ROOT}}/data/resources/apps~counter/db.sqlite",   // sqlite: its dir
				"XBIN_RES_FILES={{ROOT}}/data/resources/apps~counter/files",    // filesystem: itself
				"XBIN_RES_LOG={{ROOT}}/data/resources/apps~counter/log.sqlite", // same dir: once
				"XBIN_RES_GONE={{ROOT}}/data/resources/apps~gone/gone.sqlite",  // no dir: skipped
				"XBIN_RES_OUT=/var/lib/elsewhere/x.sqlite",                     // outside the workspace
			},
			golden: `{
			"lower":["{{ROOTFS}}"],
			"binds":[` + lsBaseBinds + `,
				{"src":"{{ROOT}}/data/resources/apps~counter","dst":"{{ROOT}}/data/resources/apps~counter","ro":false},
				{"src":"{{ROOT}}/data/resources/apps~counter/files","dst":"{{ROOT}}/data/resources/apps~counter/files","ro":false},
				` + lsGoBin + `],
			"entry":"/run/backend",
			"argv":["/run/backend"],
			"env":{{ENV}},
			"cwd":"{{ROOT}}/apps/counter",
			"noFollow":true,"followBase":true,"rootHint":"the tile's environment layer holds it: its setup script made it",
	"hostUid":{{UID}},"hostGid":{{GID}},
			"unprivileged":true
		}`},
		{name: "egress granted", manifest: `{"runtime":"go"}`, pol: []string{"net:internet:443"},
			golden: lsGoWith(`"net":"relay"`)},
		{name: "host network wins over splice and relay", manifest: `{"runtime":"go"}`, pol: []string{"net:internet"},
			wire: func(r *Runner) {
				r.NetHost = func(*registry.Component) bool { return true }
				r.NetTarget = func(*registry.Component) (string, string, string, bool) {
					return "apps/router", "10.42.0.2/30", "10.42.0.1", true
				}
			},
			golden: lsGoWith(`"hostNet":true`)},
		{name: "splice wins over relay", manifest: `{"runtime":"go"}`, pol: []string{"net:internet"},
			wire: func(r *Runner) {
				r.NetTarget = func(*registry.Component) (string, string, string, bool) {
					return "apps/router", "10.42.0.2/30", "10.42.0.1", true
				}
			},
			golden: lsGoWith(`"net":"splice","netAddr":"10.42.0.2/30","netGw":"10.42.0.1"`)},
		{name: "ingress plumbing without egress", manifest: `{"runtime":"go"}`,
			wire: func(r *Runner) {
				r.IngressNet = func(*registry.Component) bool { return true }
			},
			golden: lsGoWith(`"net":"relay"`)},
		{name: "lan-ingress legs", manifest: `{"runtime":"go"}`,
			wire: func(r *Runner) {
				r.NetLinks = func(*registry.Component) []sandbox.NetLink {
					return []sandbox.NetLink{{Provider: "apps/router", Slot: "lan", Addr: "10.43.0.2/30"}}
				}
			},
			golden: lsGoWith(`"net":"relay","netLinks":[{"provider":"apps/router","slot":"lan","addr":"10.43.0.2/30"}]`)},
		{name: "net provider", manifest: `{"runtime":"go"}`, pol: []string{"net:internet"},
			wire: func(r *Runner) {
				r.NetRoster = func(*registry.Component) []sandbox.NetClient {
					return []sandbox.NetClient{{Name: "apps/client", Addr: "10.42.0.1/30"}}
				}
				r.NetCaps = func(*registry.Component) bool { return true }
			},
			golden: lsGoWith(`"net":"relay","netClients":[{"name":"apps/client","addr":"10.42.0.1/30"}],"netAdmin":true`)},
		{name: "container host", manifest: `{"runtime":"go"}`,
			wire: func(r *Runner) {
				r.ContainerCaps = func(*registry.Component) bool { return true }
			},
			golden: lsGoWith(`"containers":true`)},
		{name: "hooks answering no", manifest: `{"runtime":"go"}`,
			wire: func(r *Runner) {
				r.GPU = func(*registry.Component) []gpu.Device { return nil }
				r.NetRoster = func(*registry.Component) []sandbox.NetClient { return nil }
				r.NetLinks = func(*registry.Component) []sandbox.NetLink { return nil }
				r.NetCaps = func(*registry.Component) bool { return false }
				r.ContainerCaps = func(*registry.Component) bool { return false }
				r.NetHost = func(*registry.Component) bool { return false }
				r.NetTarget = func(*registry.Component) (string, string, string, bool) { return "", "", "", false }
				r.IngressNet = func(*registry.Component) bool { return false }
			},
			golden: lsGoGolden},
		// A VM tile's spec is the namespace spec; vmApply turns it into a VM
		// in sandboxCmd, after launchSpec.
		{name: "vm manifest", manifest: `{"runtime":"go","vm":true}`, golden: lsGoGolden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			r, c := f.runner(), f.comp(t, tc.manifest)
			if tc.wire != nil {
				tc.wire(r)
			}
			env := lsEnv(f.expand(tc.extraEnv)...)
			spec := r.launchSpec(c, f.bin(c), lsDir, env, egress(t, tc.pol...), f.expand1(tc.envLower))
			assertSpecGolden(t, spec, f.golden(t, tc.golden, env))
		})
	}
}

// covers P5 PO-11 — granted GPUs add gpu.Binds' binds after the code, run
// dir, gateway, resource and binary binds, and its env after the input env;
// with nothing to bind, neither changes.
func TestZeroStateLaunchSpecGPU(t *testing.T) {
	f := newLaunchFixture(t)
	node := filepath.Join(t.TempDir(), "nvidia0") // exists, so gpu.Binds binds it on any host
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	devs := []gpu.Device{{Index: 0, UUID: "GPU-0", Name: "test", Node: node}}
	r, c := f.runner(), f.comp(t, `{"runtime":"go"}`)
	r.GPU = func(*registry.Component) []gpu.Device { return devs }
	env := lsEnv()
	spec := r.launchSpec(c, f.bin(c), lsDir, env, sandbox.EgressPolicy{}, "")

	gb, genv := gpu.Binds(devs)
	want := &sandbox.Spec{}
	if err := json.Unmarshal([]byte(f.golden(t, lsGoGolden, env)), want); err != nil {
		t.Fatal(err)
	}
	want.Binds = append(want.Binds, gb...)
	want.Env = append(append([]string(nil), env...), genv...)
	if !reflect.DeepEqual(spec, want) {
		t.Errorf("GPU spec:\n got %s\nwant %s", specJSON(t, spec), specJSON(t, want))
	}
	if !containsBind(spec.Binds, sandbox.Bind{Src: node, Dst: node}) {
		t.Errorf("device node %s not bound: %+v", node, spec.Binds)
	}
}

// covers P5 — launchSpec is pure: it creates nothing, leaves its env input's
// backing array alone, and gives the same spec twice.
func TestLaunchSpecIsPure(t *testing.T) {
	f := newLaunchFixture(t)
	node := filepath.Join(t.TempDir(), "nvidia0")
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r, c := f.runner(), f.comp(t, `{"runtime":"go","setup":"true"}`)
	r.GPU = func(*registry.Component) []gpu.Device { return []gpu.Device{{Node: node}} }
	r.IngressNet = func(*registry.Component) bool { return true }
	before := treeOf(t, f.root)

	base := lsEnv("XBIN_RES_DB=" + f.root + "/data/resources/apps~counter/db.sqlite")
	env := make([]string, len(base), len(base)+8) // spare capacity an append could write into
	copy(env, base)
	spare := env[:cap(env)]
	one := r.launchSpec(c, f.bin(c), lsDir, env, sandbox.EgressPolicy{}, "/lower")
	two := r.launchSpec(c, f.bin(c), lsDir, env, sandbox.EgressPolicy{}, "/lower")

	for i := len(env); i < cap(env); i++ {
		if spare[i] != "" {
			t.Errorf("launchSpec wrote %q into the caller's env backing array", spare[i])
		}
	}
	if !reflect.DeepEqual(env, base) {
		t.Errorf("env input changed: %q", env)
	}
	if !reflect.DeepEqual(one, two) {
		t.Errorf("two calls differ:\n%s\n%s", specJSON(t, one), specJSON(t, two))
	}
	if after := treeOf(t, f.root); after != before {
		t.Errorf("launchSpec touched the workspace:\nbefore %s\nafter  %s", before, after)
	}
}

// covers PO-11 — sandboxCmd keeps today's VM gate: a VM tile on a runner
// with no VM manager is refused (a registry refusal), before anything
// launches.
func TestSandboxCmdRefusesVMWithoutManager(t *testing.T) {
	f := newLaunchFixture(t)
	r, c := f.runner(), f.comp(t, `{"runtime":"go","vm":true}`)
	cmd, h, err := r.sandboxCmd(c, f.bin(c), lsDir, lsDir+"/g1.sock", lsEnv(), sandbox.EgressPolicy{}, "")
	if !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM sandboxes need isolation and KVM") {
		t.Fatalf("err = %v, want the no-VM-manager refusal", err)
	}
	if cmd != nil || h != nil {
		t.Errorf("a refused VM tile returned a command (%v) or handle (%v)", cmd, h)
	}
}

func (f lsFixture) expand1(s string) string {
	return strings.NewReplacer("{{ROOT}}", f.root, "{{ROOTFS}}", f.rootfs, "{{RUN}}", lsRun).Replace(s)
}

func (f lsFixture) expand(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = f.expand1(s)
	}
	return out
}

func (f lsFixture) golden(t *testing.T, g string, env []string) string {
	t.Helper()
	ej, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer(
		"{{ENV}}", string(ej),
		"{{UID}}", strconv.Itoa(os.Getuid()),
		"{{GID}}", strconv.Itoa(os.Getgid()),
	).Replace(f.expand1(g))
}

// assertSpecGolden compares the spec's JSON (what the sandbox init decodes)
// with the golden, as decoded values: every key, value and absent key counts;
// key order doesn't.
func assertSpecGolden(t *testing.T, spec *sandbox.Spec, golden string) {
	t.Helper()
	var want, got any
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatalf("golden: %v\n%s", err, golden)
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		var g, w bytes.Buffer
		_ = json.Indent(&g, b, "", "  ")
		_ = json.Indent(&w, []byte(golden), "", "  ")
		t.Errorf("launch spec differs from today's:\n got %s\nwant %s", g.String(), w.String())
	}
}

func specJSON(t *testing.T, s *sandbox.Spec) string {
	t.Helper()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsBind(bs []sandbox.Bind, b sandbox.Bind) bool {
	for _, x := range bs {
		if x == b {
			return true
		}
	}
	return false
}

// treeOf lists every path under root with its mode, lstat only.
func treeOf(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		sb.WriteString(strings.TrimPrefix(p, root) + " " + d.Type().String() + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}
