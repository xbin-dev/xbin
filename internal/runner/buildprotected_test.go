package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// plantArtifact lays out a checkpoint artifact of tree beneath base/arts, as
// a build leaves it: bin and a build.json recording tile.
func plantArtifact(t *testing.T, base, arts, tree, tile string) string {
	t.Helper()
	dir := filepath.Join(base, arts, tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(buildRecord{Tile: tile, Tree: tree})
	if err := os.WriteFile(filepath.Join(dir, "build.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bin")
}

// writable is the host paths a confined run may write: its read-write
// binds, and the directories it runs from and its caches besides.
func writable(cmd confine.Cmd) []string {
	var out []string
	for _, b := range cmd.Binds {
		if !b.RO && !b.Mask {
			out = append(out, b.Src)
		}
	}
	return out
}

// covers D127m T16 T17 NP-06-6 NP-07-14 — a protected primary's build
// products live apart (07-runtime §3.4): its artifacts, Go caches and env
// layers sit under .xbin/deploy/<TileKey>/protected/, disjoint from the
// tile's shared ones; its Go build writes only there, and no other build
// binds anything of it (every build masks .xbin); an artifact of the same
// tree in the shared namespace is never reused for it, nor a protected one
// for anyone else; old protected artifacts are pruned but the current, the
// previous and one a generation runs. The lost-products case: a restart
// path whose artifact is gone rebuilds, into the protected namespace, only
// when every input the saved build.json recorded matches, and otherwise
// holds the start with the checkpoint a manager must redeploy.
func TestProtectedBuildProductsSeparated(t *testing.T) {
	w := newCkWorkspace(t, map[string]string{"apps/x": `{"runtime":"go"}`}, map[string]string{"apps/x": ckTree('a')})
	r := w.runner()
	tree := ckTree('a')
	v := w.view("apps/x", "go")
	pr, sh := r.protectedProducts("apps/x"), r.sharedProducts("apps/x")
	pdir := filepath.Join(w.root, ".xbin/deploy", util.TileKey("apps/x"), "protected")

	t.Run("the namespaces are apart", func(t *testing.T) {
		for _, p := range []string{pr.base, pr.gocache, pr.modcache, pr.env} {
			if !within(p, pdir) {
				t.Errorf("protected product %s outside %s", p, pdir)
			}
			for _, s := range []string{sh.base, sh.gocache, sh.modcache, sh.env} {
				if within(p, s) || within(s, p) {
					t.Errorf("protected %s overlaps shared %s", p, s)
				}
			}
		}
	})

	t.Run("the protected build writes only its namespace", func(t *testing.T) {
		if _, err := hostToolchain(); err != nil {
			t.Skip("no go toolchain:", err)
		}
		out := filepath.Join(pr.base, pr.arts, tree+".tmp-x")
		cmd, _, err := r.goBuildCmd(v, "./backend", goBuild{dirFrom: v.CodeRoot, out: filepath.Join(out, "bin"), outDir: out, gocache: pr.gocache, modcache: pr.modcache})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range writable(cmd) {
			if !within(p, pdir) {
				t.Errorf("the protected build may write %s", p)
			}
		}
		if got := strings.Join(cmd.Env, " "); !strings.Contains(got, "GOCACHE="+pr.gocache) || !strings.Contains(got, "GOMODCACHE="+pr.modcache) {
			t.Errorf("the protected build's caches: %s", got)
		}

		// the tile's other builds: the work tree's and a shared checkpoint build
		for name, g := range map[string]goBuild{
			"work tree":  {out: filepath.Join(sh.base, "bin"), outDir: sh.base},
			"checkpoint": {dirFrom: v.CodeRoot, out: filepath.Join(sh.base, "c", tree+".tmp-y", "bin"), outDir: filepath.Join(sh.base, "c", tree+".tmp-y")},
		} {
			cmd, _, err := r.goBuildCmd(v, "./backend", g)
			if err != nil {
				t.Fatal(err)
			}
			masked := false
			for _, b := range cmd.Binds {
				if b.Mask && b.Dst == filepath.Join(w.root, ".xbin") {
					masked = true
				}
				if !b.Mask && (within(b.Src, pdir) || within(b.Dst, pdir)) {
					t.Errorf("the %s build binds %+v of the protected namespace", name, b)
				}
			}
			if !masked {
				t.Errorf("the %s build doesn't mask .xbin: %+v", name, cmd.Binds)
			}
		}
	})

	t.Run("artifacts are reused only within their namespace", func(t *testing.T) {
		shared := plantArtifact(t, sh.base, sh.arts, tree, "apps/x")
		if _, ok := r.protectedArtifact(v, tree); ok {
			t.Error("the shared artifact was taken for the protected primary")
		}
		prot := plantArtifact(t, pr.base, pr.arts, tree, "apps/x")
		if got, ok := r.protectedArtifact(v, tree); !ok || got != prot {
			t.Errorf("protected artifact %q %v, want %q", got, ok, prot)
		}
		if got, ok := r.Artifact(v, tree); !ok || got != shared {
			t.Errorf("the shared lookup answered %q %v, want %q", got, ok, shared)
		}
		plantArtifact(t, pr.base, pr.arts, ckTree('b'), "apps/other")
		if _, ok := r.protectedArtifact(v, ckTree('b')); ok {
			t.Error("a protected artifact recorded for another tile was taken")
		}
		if _, ok := r.protectedArtifact(v, "../../x"); ok {
			t.Error("a tree that isn't a full id was looked up")
		}
	})

	t.Run("pruning keeps the current, the previous and what runs", func(t *testing.T) {
		cur, prev, stale, running := ckTree('c'), ckTree('d'), ckTree('e'), ckTree('f')
		for _, tr := range []string{cur, prev, stale, running} {
			plantArtifact(t, pr.base, pr.arts, tr, "apps/x")
		}
		oldTmp, newTmp := filepath.Join(pr.base, pr.arts, stale+".tmp-old"), filepath.Join(pr.base, pr.arts, stale+".tmp-new")
		for _, d := range []string{oldTmp, newTmp} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		long := time.Now().Add(-2 * artifactTmpAge)
		if err := os.Chtimes(oldTmp, long, long); err != nil {
			t.Fatal(err)
		}
		release := r.inUse.hold(&r.inUse.arts, filepath.Join(pr.base, pr.arts, running))
		defer release()
		r.pruneProtected(pr, cur, prev)
		for p, want := range map[string]bool{cur: true, prev: true, running: true, stale: false, filepath.Base(oldTmp): false, filepath.Base(newTmp): true} {
			if _, err := os.Lstat(filepath.Join(pr.base, pr.arts, p)); (err == nil) != want {
				t.Errorf("%s kept=%v, want %v", p, err == nil, want)
			}
		}
	})

	t.Run("a lost artifact rebuilds only when its inputs match", func(t *testing.T) {
		if _, err := hostToolchain(); err != nil {
			t.Skip("no go toolchain:", err)
		}
		lost := ckTree('9')
		v := &registry.Component{Path: "apps/x", Dir: w.dir("apps/x"), CodeRoot: w.mat("apps/x", lost), Manifest: registry.Manifest{Runtime: "go"}}
		if err := os.MkdirAll(v.CodeRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		plan, err := r.checkpointPlan(v, lost, v.CodeRoot)
		if err != nil {
			t.Fatal(err)
		}
		rec := func(edit func(*buildRecord)) []byte {
			b := r.newBuildRecord(v, lost, plan, time.Now())
			if edit != nil {
				edit(b)
			}
			j, _ := json.Marshal(b)
			return j
		}
		for name, saved := range map[string][]byte{
			"no saved record":   nil,
			"garbage":           []byte("{"),
			"another tile's":    rec(func(b *buildRecord) { b.Tile = "apps/y" }),
			"another tree's":    rec(func(b *buildRecord) { b.Tree = ckTree('8') }),
			"another toolchain": rec(func(b *buildRecord) { b.Toolchain = "go0.1" }),
			"another module":    rec(func(b *buildRecord) { b.Modules = append(b.Modules, buildModule{Use: "./apps/y", Code: "worktree"}) }),
		} {
			_, err := r.rebuildLostProtected(v, lost, saved)
			if !errors.Is(err, ErrProtectedLost) || !strings.Contains(err.Error(), "a tile manager must redeploy c:"+lost[:7]) {
				t.Errorf("%s: %v, want the start held", name, err)
			}
		}
		if _, err := r.rebuildLostProtected(v, lost, rec(func(b *buildRecord) { b.Toolchain = "go0.1" })); err == nil || !strings.Contains(err.Error(), "toolchain go0.1 →") {
			t.Errorf("the hold doesn't name what moved: %v", err)
		}
		// every input matches: it builds, into the protected namespace alone
		// (here refused by confine, which has no sandbox in a unit test)
		_, err = r.rebuildLostProtected(v, lost, rec(nil))
		if !errors.Is(err, confine.ErrNeedsIsolation) {
			t.Fatalf("a matching record: %v, want the protected build tried", err)
		}
		if ents, _ := os.ReadDir(filepath.Join(sh.base, sh.arts)); len(ents) != 1 { // the shared artifact planted above alone
			t.Errorf("the rebuild wrote the shared artifacts: %v", ents)
		}
		if !realDir(filepath.Join(pr.base, pr.arts)) {
			t.Error("the rebuild didn't go to the protected namespace")
		}
		for _, d := range []string{sh.gocache, sh.modcache} {
			if _, err := os.Lstat(d); err == nil {
				t.Errorf("the protected rebuild made the shared cache %s", d)
			}
		}
	})
}

// covers T17 D127m NP-06-6 — a protected primary never shares an env layer
// (07-runtime §3.3, §3.4): the same setup resolves to a layer of its own in
// the protected namespace, while the live reload target and non-primary
// deployments keep sharing theirs by hash; the shared layers' GC never
// touches it, and the protected namespace's keeps only the new layer and
// the previous one; a restart path takes only the protected layer, never
// the shared one of the same hash, and holds the start when it is gone,
// since setup reaches the network.
func TestProtectedPrimaryLayerNotShared(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Root: root, Isolate: true, Rootfs: filepath.Join(root, "rootfs"), states: map[string]*state{}}
	tree := ckTree('a')
	view := func(dep, codeRoot string) *registry.Component {
		return &registry.Component{Path: "apps/x", Dir: filepath.Join(root, "apps/x"), Deployment: dep, CodeRoot: codeRoot,
			Manifest: registry.Manifest{Runtime: "go", Setup: "apk add jq"}}
	}
	primary := view("", "/ckpt/a")
	pr := r.protectedProducts("apps/x")

	prot, shared := r.envLayerDirIn(primary, pr.env), r.envLayerDir(primary)
	if prot == shared || !within(prot, pr.env) || filepath.Base(prot) != filepath.Base(shared) {
		t.Fatalf("protected layer %s, shared %s: want the same hash in its own namespace", prot, shared)
	}
	for _, v := range []*registry.Component{view("", ""), view("dev", "/ckpt/b"), view("dev", "")} {
		if got := r.envLayerDir(v); got != shared {
			t.Errorf("%q/%q: layer %s, want the shared %s", v.Deployment, v.CodeRoot, got, shared)
		}
	}

	// a shared layer of the same hash, ready: the restart path doesn't take it
	mkLayer := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "upper"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".ok"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkLayer(shared)
	if _, err := r.protectedLayer(primary, tree); !errors.Is(err, ErrProtectedLost) || !strings.Contains(err.Error(), "redeploy c:"+tree[:7]) {
		t.Fatalf("a missing protected layer: %v, want the start held", err)
	}
	mkLayer(prot)
	if got, err := r.protectedLayer(primary, tree); err != nil || got != filepath.Join(prot, "upper") {
		t.Fatalf("protected layer %q %v, want %q", got, err, filepath.Join(prot, "upper"))
	}
	if got, err := r.protectedLayer(&registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}, tree); got != "" || err != nil {
		t.Errorf("no setup: %q %v, want no layer", got, err)
	}

	// the shared GC never reaches the protected namespace
	r.gcEnvLayers(primary, map[string]bool{})
	if _, err := os.Lstat(shared); err == nil {
		t.Error("the shared GC kept an unreferenced shared layer")
	}
	if _, err := os.Lstat(prot); err != nil {
		t.Error("the shared GC removed the protected layer")
	}
	// the protected namespace keeps the new layer and the previous one
	prev, stale := filepath.Join(pr.env, "prev0000"), filepath.Join(pr.env, "stale000")
	mkLayer(prev)
	mkLayer(stale)
	gcLayers(pr.env, map[string]bool{filepath.Base(prot): true, filepath.Base(prev): true})
	for p, want := range map[string]bool{prot: true, prev: true, stale: false} {
		if _, err := os.Lstat(p); (err == nil) != want {
			t.Errorf("%s kept=%v, want %v", p, err == nil, want)
		}
	}
	gcLayers(pr.env, nil)
	if _, err := os.Lstat(prot); err != nil {
		t.Error("a nil keep removed a layer")
	}
}

// covers T17 SC-ROLLBACK — env-layer GC keeps the layers of the tile's
// retained checkpoints (each deployment's current one and its roll-back
// targets), read through their views, besides the work tree's and the one
// just built; a retained checkpoint whose setup can't be read keeps every
// layer; a layer another deployment is building (its lock held) is left to
// it. (WP-67's A4.)
func TestEnvLayerGCKeepsRetained(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apps/x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(`{"runtime":"go","setup":"apk add jq"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Root: root}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	setups := map[string]string{ckTree('a'): "apk add curl", ckTree('b'): ""}
	r := &Runner{Root: root, Isolate: true, Rootfs: filepath.Join(root, "rootfs"), Reg: reg, states: map[string]*state{}}
	r.View = func(c *registry.Component, code Code) (*registry.Component, error) {
		s, ok := setups[code.Tree]
		if !ok {
			return nil, errors.New("checkpoint unreadable")
		}
		return &registry.Component{Path: c.Path, Dir: c.Dir, Manifest: registry.Manifest{Runtime: "go", Setup: s}}, nil
	}
	c, _ := reg.Component("apps/x")
	workTree, retained, built, stale := r.setupHash("apk add jq"), r.setupHash("apk add curl"), r.setupHash("apk add make"), r.setupHash("apk add gcc")
	base := r.envLayers("apps/x")
	for _, h := range []string{workTree, retained, built, stale} {
		if err := os.MkdirAll(filepath.Join(base, h, "upper"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if keep := r.envKeep(c, built, ckTree('a'), ckTree('c')); keep != nil {
		t.Fatalf("an unreadable retained checkpoint: keep %v, want nil", keep)
	}
	r.gcEnvLayers(c, r.envKeep(c, built, ckTree('a'), ckTree('c')))
	if ents, _ := os.ReadDir(base); len(ents) != 4 {
		t.Fatalf("a keep that can't be known collected: %v", ents)
	}
	mu := layerLock(filepath.Join(base, stale))
	mu.Lock()
	r.gcEnvLayers(c, r.envKeep(c, built, ckTree('a'), ckTree('b')))
	if _, err := os.Lstat(filepath.Join(base, stale)); err != nil {
		t.Error("GC took a layer being built")
	}
	mu.Unlock()
	r.gcEnvLayers(c, r.envKeep(c, built, ckTree('a'), ckTree('b')))
	for h, want := range map[string]bool{workTree: true, retained: true, built: true, stale: false} {
		if _, err := os.Lstat(filepath.Join(base, h)); (err == nil) != want {
			t.Errorf("layer %s kept=%v, want %v", h, err == nil, want)
		}
	}
}

// covers D127c D127j — a setup run writes to its deployment's backend log (07-runtime
// §9): main's is today's .xbin/log/<CompKey>.log; another deployment's is
// under the tile's deploy state, made for it; the primary's view names the
// primary, whatever its name.
func TestEnvSetupLogPerDeployment(t *testing.T) {
	root := t.TempDir()
	primary := "main"
	r := &Runner{Root: root}
	r.Primary = func(string) string { return primary }
	v := func(dep string) *registry.Component { return &registry.Component{Path: "apps/x", Deployment: dep} }
	if got, want := r.envSetupLog(v("")), filepath.Join(root, ".xbin/log", util.CompKey("apps/x")+".log"); got != want {
		t.Errorf("main's setup log %s, want %s", got, want)
	}
	if _, err := os.Lstat(filepath.Join(root, ".xbin/deploy")); err == nil {
		t.Error("main's setup log made deploy state")
	}
	devLog := filepath.Join(root, ".xbin/deploy", util.TileKey("apps/x"), "d/dev/backend.log")
	if got := r.envSetupLog(v("dev")); got != devLog || !realDir(filepath.Dir(devLog)) {
		t.Errorf("dev's setup log %s (dir made: %v), want %s", got, realDir(filepath.Dir(devLog)), devLog)
	}
	primary = "dev"
	if got := r.envSetupLog(v("")); got != devLog {
		t.Errorf("the primary dev's setup log %s, want %s", got, devLog)
	}
}

// covers D127q T10 SC-PRIMARY-FIRST — one build limiter for every tile
// (07-runtime §10.3): at most its capacity of non-primary builds hold a
// turn at once, a further one waits for a release, and a primary's build
// never waits; a release is idempotent.
func TestNonPrimaryBuildLimiter(t *testing.T) {
	dev, primary := &registry.Component{Path: "apps/x", Deployment: "dev"}, &registry.Component{Path: "apps/x"}
	var held []func()
	for range cap(nonPrimaryBuilds) {
		held = append(held, buildTurn(dev))
	}
	defer func() {
		for _, rel := range held {
			rel()
		}
	}()
	done := make(chan struct{})
	go func() { buildTurn(primary)(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a primary build waited for the non-primary limiter")
	}
	got := make(chan func(), 1)
	go func() { got <- buildTurn(dev) }()
	select {
	case <-got:
		t.Fatal("a non-primary build took a turn past the limit")
	case <-time.After(50 * time.Millisecond):
	}
	held[0]()
	held[0]() // idempotent: frees one turn, not two
	select {
	case rel := <-got:
		held[0] = rel
	case <-time.After(5 * time.Second):
		t.Fatal("a released turn wasn't handed on")
	}
	if n := len(nonPrimaryBuilds); n != cap(nonPrimaryBuilds) {
		t.Errorf("%d turns held, want %d", n, cap(nonPrimaryBuilds))
	}
}

// covers D119e D127j SC-ROLLBACK — the collection after a layer is built reads the
// deployments plane's retained checkpoints (the Retained hook): a list it
// can't read collects nothing; the layers of the listed checkpoints stay,
// and so does every deployment's running generation's, not only main's.
func TestEnvLayerGCReadsRetainedHook(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apps/x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(`{"runtime":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Root: root}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Isolate: true, Rootfs: filepath.Join(root, "rootfs"), Reg: reg, states: map[string]*state{}}
	r.View = func(c *registry.Component, code Code) (*registry.Component, error) {
		return &registry.Component{Path: c.Path, Dir: c.Dir, Manifest: registry.Manifest{Runtime: "go", Setup: "apk add curl"}}, nil
	}
	c, _ := reg.Component("apps/x")
	retained, built, devRun, stale := r.setupHash("apk add curl"), r.setupHash("apk add make"), r.setupHash("apk add git"), r.setupHash("apk add gcc")
	base := r.envLayers("apps/x")
	for _, h := range []string{retained, built, devRun, stale} {
		if err := os.MkdirAll(filepath.Join(base, h, "upper"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.states[stateKey("apps/x", "dev")] = &state{comp: "apps/x", dep: "dev", cur: &instance{dep: "dev", envHash: devRun}}
	r.Retained = func(string) ([]string, bool) { return nil, false }
	r.collectEnvLayers(c, built)
	if ents, _ := os.ReadDir(base); len(ents) != 4 {
		t.Fatalf("an unreadable retained list collected: %v", ents)
	}
	r.Retained = func(tile string) ([]string, bool) {
		if tile != "apps/x" {
			t.Errorf("Retained asked for %q", tile)
		}
		return []string{ckTree('a')}, true
	}
	r.collectEnvLayers(c, built)
	for h, want := range map[string]bool{retained: true, built: true, devRun: true, stale: false} {
		if _, err := os.Lstat(filepath.Join(base, h)); (err == nil) != want {
			t.Errorf("layer %s kept=%v, want %v", h, err == nil, want)
		}
	}
}
