package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// build produces a runnable entry from c's work tree. For go it compiles;
// for node/python it just validates the entry file exists (the interpreter
// is the "binary"). A checkpoint builds through buildCode.
func (r *Runner) build(c *registry.Component) (string, error) {
	switch c.Manifest.Runtime {
	case "go":
		entry := goEntry(c.Manifest)
		out := filepath.Join(r.Root, ".xbin", "build", util.CompKey(c.Path), "bin")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		if r.Isolate {
			// in a sandbox, never as xbind (D78, build.go); fully static
			// (CGO_ENABLED=0) so the backend runs on any sandbox rootfs,
			// independent of the base image's glibc (plans/isolation-impl.md)
			if err := r.buildConfined(c, entry, out); err != nil {
				return "", err
			}
			return out, nil
		}
		cmd := exec.Command("go", "build", "-o", out, entry) // exec-ok: isolation off — no sandbox exists; backends run as xbind too
		cmd.Dir = c.Dir
		cmd.Env = append(os.Environ(),
			"GOCACHE="+filepath.Join(r.Root, ".xbin", "cache", "go-build"),
		)
		if outp, err := cmd.CombinedOutput(); err != nil {
			return "", &BuildError{Output: string(outp)}
		}
		return out, nil
	case "node", "python":
		entry := interpEntry(c.Manifest)
		p := filepath.Join(c.Dir, filepath.FromSlash(entry))
		if _, err := os.Stat(p); err != nil {
			return "", &BuildError{Output: fmt.Sprintf("entry %s not found (set \"entry\" in xbin.json)", entry)}
		}
		return p, nil
	default:
		return "", fmt.Errorf("unknown runtime %q", c.Manifest.Runtime)
	}
}

func goEntry(m registry.Manifest) string {
	if m.Entry != "" {
		return m.Entry
	}
	return "./backend"
}

func interpEntry(m registry.Manifest) string {
	switch {
	case m.Entry != "":
		return m.Entry
	case m.Runtime == "node":
		return "backend/server.js"
	}
	return "backend/server.py"
}

// errPinnedNeedsIsolation refuses pinned backend code without isolation:
// only a sandbox shows a checkpoint at the tile's canonical path, and a
// direct build or start would run the work tree instead (P18, D78).
var errPinnedNeedsIsolation = errors.New("pinning a backend to a checkpoint needs isolation (--isolate)")

// buildCode produces the runnable entry of code, what one generation of a
// deployment of c runs; c is that deployment's view (Runner.View), whose
// manifest carries the deployment-level fields of code and whose CodeRoot is
// its materialized checkpoint. The work tree builds as ever (build). A
// checkpoint builds only under isolation, from its tree shown at the tile's
// canonical path (07-runtime §3): Go into an artifact per (tile, tree),
// reused while its build.json records the same tile (P9); node and python
// check their entry beneath the tree and answer its canonical path, where
// the backend's sandbox binds the tree.
func (r *Runner) buildCode(c *registry.Component, code Code) (string, error) {
	if code.WorkTree {
		return r.build(c)
	}
	if !fullTree(code.Tree) {
		return "", fmt.Errorf("%s: %q is not a checkpoint tree", c.Path, code.Tree)
	}
	if !r.Isolate {
		return "", errPinnedNeedsIsolation
	}
	root := c.CodeRoot
	if root == "" {
		var err error
		if root, err = r.materialize(c.Path, code.Tree); err != nil {
			return "", err
		}
	}
	switch c.Manifest.Runtime {
	case "go":
		return r.buildCheckpointGo(c, code.Tree, root)
	case "node", "python":
		entry := interpEntry(c.Manifest)
		f, err := fsutil.OpenBeneath(root, entry)
		if err == nil {
			fi, serr := f.Stat()
			f.Close()
			if serr == nil && fi.Mode().IsRegular() {
				return filepath.Join(c.Dir, filepath.FromSlash(entry)), nil
			}
		}
		return "", &BuildError{Output: fmt.Sprintf("entry %s not found in the checkpoint (set \"entry\" in xbin.json)", entry)}
	default:
		return "", fmt.Errorf("unknown runtime %q", c.Manifest.Runtime)
	}
}

// fullTree reports whether s is a full git tree id: 40 (SHA-1) or 64
// (SHA-256) lowercase hex digits. It names a directory, so nothing else
// passes.
func fullTree(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// goBuild is what one confined Go build shows and writes beyond the
// standard binds of runGoBuild.
type goBuild struct {
	dirFrom string         // the code root shown at the tile's path; "" = the work tree
	out     string         // the binary (-o)
	outDir  string         // the one directory of .xbin/build the build may write
	extra   []sandbox.Bind // after the standard binds: other components' code, a go.work, masks
}

// buildConfined compiles c's work tree in a throwaway sandbox (runGoBuild).
// A tile with checkpoint artifacts beside out (c/) has them masked, so no
// work-tree build ever writes one (07-runtime §3.1); a zero-state tile has
// no c/ and gets today's binds.
func (r *Runner) buildConfined(c *registry.Component, entry, out string) error {
	g := goBuild{out: out, outDir: filepath.Dir(out)}
	if arts := filepath.Join(g.outDir, "c"); realDir(arts) {
		g.extra = append(g.extra, confine.Mask(arts))
	}
	return r.runGoBuild(c, entry, g)
}

// runGoBuild compiles a Go backend inside a throwaway sandbox (D78).
// `go build` reads the tile's content as configuration — VCS stamping runs
// git with the tile's .git/config (whose core.fsmonitor runs anything),
// go.mod steers downloads and replacements — so it never runs as xbind.
//
// What the build sees, chosen so a build behaves exactly as it did on the
// host: the host's own Go toolchain (read-only, same version as before), the
// workspace read-only with its secret dirs masked (.xbin, data, homes — so
// go.work and every `use`d module resolve unchanged), the SDK. What it may
// write is only the tile's own: its output dir, and its own build and module
// caches (a shared cache would let one tile's build plant code in another's).
// Modules the shared cache already holds are served from it read-only as a
// file:// GOPROXY — no network, no re-download; new ones come from the
// network: public addresses only (XBIN_BUILD_NET=host shares the host's, for
// a GOPROXY or private modules on the LAN). VCS stamping is off: nothing a
// tile's repo says runs even inside the sandbox. A checkpoint build shows its
// tree at the tile's path (g.dirFrom, confine's DirFrom), which only a
// sandbox can: a direct run refuses it (confine.ErrNeedsIsolation).
func (r *Runner) runGoBuild(c *registry.Component, entry string, g goBuild) error {
	tc, err := hostToolchain()
	if err != nil {
		return &BuildError{Output: "go toolchain: " + err.Error()}
	}
	gocache, modcache := goCaches(r.Root, c.Path)
	dirs := []string{gocache, modcache}
	if g.dirFrom == "" {
		dirs = append(dirs, g.outDir) // a checkpoint build's is made beneath its artifacts dir
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	binds := []sandbox.Bind{
		confine.RO(tc.goroot),
		confine.RO(r.Root),
		confine.MaskOpen(filepath.Join(r.Root, ".xbin")), // secrets; the tile's own dirs below nest back in
		confine.Mask(filepath.Join(r.Root, "data")),
		confine.Mask(filepath.Join(r.Root, "homes")),
		confine.RW(g.outDir),
		confine.RW(gocache),
		confine.RW(modcache),
	}
	proxy := tc.goproxy
	if tc.download != "" {
		binds = append(binds, confine.RO(tc.download))
		proxy = (&url.URL{Scheme: "file", Path: tc.download}).String() + "," + proxy
	}
	if sdk := deps.SDKPath(); sdk != "" && !within(sdk, r.Root) && pathExists(sdk) {
		binds = append(binds, confine.RO(sdk))
	}
	binds = append(binds, g.extra...)
	env := []string{
		"PATH=" + filepath.Join(tc.goroot, "bin") + ":/usr/local/bin:/usr/bin:/bin",
		"GOCACHE=" + gocache, "GOMODCACHE=" + modcache, "GOPATH=/tmp/go", "GOPROXY=" + proxy,
		"CGO_ENABLED=0", "GOTELEMETRY=off",
	}
	for _, k := range passGoEnv { // the operator's module settings keep applying
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// the tile's module cache stays deletable by xbind (Go makes it read-only)
	env = append(env, "GOFLAGS="+goFlags())
	net := confine.NetInternet
	if os.Getenv("XBIN_BUILD_NET") == "host" {
		net = confine.NetHost
	}
	res, err := confine.Run(context.Background(), confine.Cmd{
		Argv: []string{tc.gobin, "build", "-buildvcs=false", "-o", g.out, entry},
		Dir:  c.Dir, DirFrom: g.dirFrom, ReadOnlyDir: true, Binds: binds, Env: env, Net: net,
		Timeout: 20 * time.Minute, MaxOutput: 1 << 20,
	})
	if err != nil {
		if _, ok := confine.ExitCode(err); ok || strings.Contains(err.Error(), "timed out") {
			return &BuildError{Output: strings.TrimSpace(string(res.Stdout) + string(res.Stderr) + "\n" + timeoutNote(err))}
		}
		return fmt.Errorf("build sandbox: %w", err)
	}
	return nil
}

// goCaches are a tile's own Go build and module caches, shared by its
// deployments (07-runtime §3.1).
func goCaches(root, tile string) (gocache, modcache string) {
	cache := filepath.Join(root, ".xbin", "cache", "tile", util.CompKey(tile))
	return filepath.Join(cache, "go-build"), filepath.Join(cache, "mod")
}

func goFlags() string { return strings.TrimSpace(os.Getenv("GOFLAGS") + " -modcacherw") }

func timeoutNote(err error) string {
	if strings.Contains(err.Error(), "timed out") {
		return err.Error()
	}
	return ""
}

// passGoEnv are the operator's Go module settings, passed into the build
// sandbox when set in xbind's environment (GOFLAGS too, extended above).
var passGoEnv = []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB",
	"GOINSECURE", "GOVCS", "GOTOOLCHAIN", "GOAUTH", "GOAMD64", "GOARM64", "GOEXPERIMENT"}

// buildCheckpointGo compiles tile c's checkpoint tree, materialized at root,
// unless an earlier build left its artifact (P9). It is buildConfined's
// build with the tree shown at the tile's canonical path, so go.work's
// `use ./<tile>` and relative replace lines resolve unchanged; the components
// nested in it and every go.work module of a tile whose primary is pinned
// shown from their primaries' code (checkpointPlan, NP-07-4); and only a
// fresh .xbin/build/<CompKey>/c/<tree>.tmp-<rand>/ writable, renamed to
// c/<tree>/ with its build.json once the build succeeds, so a crash never
// leaves a partial artifact that looks valid.
func (r *Runner) buildCheckpointGo(c *registry.Component, tree, root string) (string, error) {
	base := filepath.Join(r.Root, ".xbin", "build", util.CompKey(c.Path))
	final := filepath.Join(base, "c", tree, "bin")
	old, usable := artifactRecord(base, tree)
	if old != nil && old.Tile != c.Path {
		old, usable = nil, false // another tile's under a colliding CompKey: rebuilt, nothing to compare
	}
	if usable {
		return final, nil
	}
	plan, err := r.checkpointPlan(c, tree, root)
	if err != nil {
		return "", err
	}
	cfd, err := artifactsDir(base)
	if err != nil {
		return "", fmt.Errorf("checkpoint artifacts: %w", err)
	}
	defer unix.Close(cfd)
	tmpName := tree + ".tmp-" + util.RandomToken(6)
	if err := unix.Mkdirat(cfd, tmpName, 0o755); err != nil {
		return "", fmt.Errorf("checkpoint artifacts: %w", err)
	}
	tmp := filepath.Join(base, "c", tmpName)
	placed := false
	defer func() {
		if !placed {
			_ = os.RemoveAll(tmp)
		}
	}()
	extra := plan.binds
	if plan.goWork != nil {
		name := tmpName + ".go.work"
		if err := writeAt(cfd, name, plan.goWork); err != nil {
			return "", fmt.Errorf("checkpoint build's go.work: %w", err)
		}
		defer unix.Unlinkat(cfd, name, 0)
		extra = append(extra, confine.At(filepath.Join(base, "c", name), filepath.Join(r.Root, "go.work"), true))
	}
	start := time.Now()
	err = r.runGoBuild(c, goEntry(c.Manifest), goBuild{dirFrom: root, out: filepath.Join(tmp, "bin"), outDir: tmp, extra: extra})
	if err != nil {
		return "", err
	}
	rec := r.newBuildRecord(c, tree, plan, start)
	if old != nil {
		rec.Rebuilt = rec.changedFrom(old)
	}
	if err := sealArtifact(cfd, tmpName, rec); err != nil {
		return "", err
	}
	if err := unix.Renameat(cfd, tmpName, cfd, tree); err != nil {
		// c/<tree> is taken: by a concurrent build of the same tree that
		// placed its artifact first (use it), or by a stale one (replace it)
		if again, ok := artifactRecord(base, tree); ok && again.Tile == c.Path {
			return final, nil
		}
		if err := os.RemoveAll(filepath.Join(base, "c", tree)); err != nil {
			return "", err
		}
		if err := unix.Renameat(cfd, tmpName, cfd, tree); err != nil {
			return "", fmt.Errorf("checkpoint artifacts: %w", err)
		}
	}
	placed = true
	return final, nil
}

// artifactsDir opens base/c, the tile's checkpoint artifacts, making it when
// missing, without following a symlink: base (.xbin/build/<CompKey>) is
// bound read-write into the tile's work-tree builds, so whatever else sits
// at c was left by one and is removed.
func artifactsDir(base string) (int, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return -1, err
	}
	bfd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(bfd)
	for try := 0; try < 3; try++ {
		fd, err := unix.Openat(bfd, "c", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case err == nil:
			return fd, nil
		case errors.Is(err, unix.ENOENT):
			if err := unix.Mkdirat(bfd, "c", 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return -1, err
			}
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
			if err := os.RemoveAll(filepath.Join(base, "c")); err != nil {
				return -1, err
			}
		default:
			return -1, err
		}
	}
	return -1, errors.New("c/ keeps changing under the build")
}

// writeAt creates name beneath dirfd with data, never through a symlink.
func writeAt(dirfd int, name string, data []byte) error {
	if err := unix.Unlinkat(dirfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(dirfd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// sealArtifact checks what the build sandbox left in tmp (its binary, a
// regular file: the sandbox could have planted a symlink there) and writes
// the build record beside it.
func sealArtifact(cfd int, tmp string, rec *buildRecord) error {
	tfd, err := unix.Openat(cfd, tmp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(tfd)
	var st unix.Stat_t
	if err := unix.Fstatat(tfd, "bin", &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return &BuildError{Output: "the build left no binary"}
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAt(tfd, "build.json", append(b, '\n'))
}

// buildRecordMax caps a build.json read back.
const buildRecordMax = 1 << 20

// artifactRecord reads the build.json of the checkpoint artifact for tree
// under base, nil when there is none; usable says its binary is in place.
// Nothing on the way is followed: c/ and c/<tree> must be directories.
func artifactRecord(base, tree string) (rec *buildRecord, usable bool) {
	f, err := fsutil.OpenIn(base, "c/"+tree, "build.json")
	if err != nil {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, buildRecordMax+1))
	f.Close()
	rec = &buildRecord{}
	if err != nil || len(b) > buildRecordMax || json.Unmarshal(b, rec) != nil || rec.Tree != tree {
		return nil, false
	}
	fi, err := os.Lstat(filepath.Join(base, "c", tree, "bin"))
	return rec, err == nil && fi.Mode().IsRegular()
}

// buildRecord is a checkpoint artifact's build.json: the inputs it was built
// from, since the artifact also embeds other tiles' Go code as their
// primaries stood, the modules go.sum pins and the host toolchain
// (07-runtime §3.1's reproducibility caveat).
type buildRecord struct {
	Tile      string            `json:"tile"`
	Tree      string            `json:"tree"`
	Toolchain string            `json:"toolchain"`
	GoFlags   string            `json:"goflags"`
	GoEnv     map[string]string `json:"goenv,omitempty"`   // the operator's settings passed in (passGoEnv)
	Modules   []buildModule     `json:"modules,omitempty"` // every go.work module
	Sum       []string          `json:"sum,omitempty"`     // module@version, as the tile's go.sum pins them
	Built     time.Time         `json:"built"`
	Millis    int64             `json:"durationMs"`
	Rebuilt   []string          `json:"rebuilt,omitempty"` // how the inputs differ from the record this build replaced
}

// buildModule is one go.work module and the code the build compiled it from.
type buildModule struct {
	Use  string `json:"use"`  // as go.work writes it
	Code string `json:"code"` // "worktree", or the checkpoint's tree
}

func (r *Runner) newBuildRecord(c *registry.Component, tree string, plan ckptPlan, start time.Time) *buildRecord {
	tc, _ := hostToolchain()
	env := map[string]string{}
	for _, k := range passGoEnv {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	return &buildRecord{
		Tile: c.Path, Tree: tree, Toolchain: tc.version, GoFlags: goFlags(), GoEnv: env,
		Modules: plan.modules, Sum: plan.sum,
		Built: start.UTC(), Millis: time.Since(start).Milliseconds(),
	}
}

// changedFrom names the inputs that differ from old's, for the status of a
// rebuild ("rebuilt with different inputs: …", 07-runtime §3.1).
func (b *buildRecord) changedFrom(old *buildRecord) []string {
	var d []string
	if old.Toolchain != b.Toolchain {
		d = append(d, fmt.Sprintf("toolchain %s → %s", old.Toolchain, b.Toolchain))
	}
	if old.GoFlags != b.GoFlags || !maps.Equal(old.GoEnv, b.GoEnv) {
		d = append(d, "the Go settings")
	}
	was := map[string]string{}
	for _, m := range old.Modules {
		was[m.Use] = m.Code
	}
	for _, m := range b.Modules {
		if was[m.Use] != m.Code {
			d = append(d, fmt.Sprintf("module %s: %s → %s", m.Use, was[m.Use], m.Code))
		}
	}
	if !slices.Equal(old.Sum, b.Sum) {
		d = append(d, "go.sum's module versions")
	}
	return d
}

// ckptPlan is what a checkpoint build shows beyond the tile's own tree.
type ckptPlan struct {
	binds   []sandbox.Bind // other components' code, each at its own path
	modules []buildModule  // every go.work module, with the code it builds against
	goWork  []byte         // a corrected go.work over the workspace's; nil = the workspace's as it is
	sum     []string       // the tile's go.sum pins
}

// checkpointPlan resolves the code a checkpoint build of c's tree (at root)
// sees. The components shown from elsewhere than the workspace are
// showCode's, with every go.work module's component asked about, so
// cross-tile references follow the other tile's primary (05-model §6). A
// module shown from a checkpoint that keeps its go.mod elsewhere than
// go.work says (the root versus backend/) gets that one use line corrected
// in a per-build go.work, bound read-only over the workspace's. Tile files
// are read beneath their trees only.
func (r *Runner) checkpointPlan(c *registry.Component, tree, root string) (ckptPlan, error) {
	var plan ckptPlan
	lines, uses, err := readGoWork(r.Root)
	if err != nil {
		return plan, err
	}
	comps := append(r.components(), c)
	owners := make([]*registry.Component, len(uses))
	modules := map[string]bool{}
	for i, u := range uses {
		owners[i] = moduleOwner(comps, u.dir)
		if n := owners[i]; n != nil && n.Path != c.Path {
			modules[n.Path] = true
		}
	}
	shown, err := r.showCode(c, modules)
	if err != nil {
		return plan, err
	}
	code := map[string]shownComp{c.Path: {comp: c, root: root, tree: tree}}
	for _, s := range shown {
		code[s.comp.Path] = s
		plan.binds = append(plan.binds, confine.At(s.root, s.comp.Dir, true))
	}
	rewrote := false
	for i, u := range uses {
		m := buildModule{Use: u.token, Code: "worktree"}
		if n := owners[i]; n != nil {
			if s := code[n.Path]; s.tree != "" {
				m.Code = s.tree
				if want := moduleDirIn(s.root, n.Dir); want != "" && want != u.dir {
					rel, _ := filepath.Rel(r.Root, want)
					lines[u.line] = strings.Replace(lines[u.line], u.token, "./"+filepath.ToSlash(rel), 1)
					rewrote = true
				}
			}
		}
		plan.modules = append(plan.modules, m)
	}
	if rewrote {
		plan.goWork = []byte(strings.Join(lines, "\n"))
	}
	if d := moduleDirIn(root, c.Dir); d != "" {
		sub, _ := filepath.Rel(c.Dir, d)
		plan.sum = goSumPins(root, filepath.Join(sub, "go.sum"))
	}
	return plan, nil
}

// moduleOwner is the component whose Go module dir is: at its root, or in
// its backend/.
func moduleOwner(comps []*registry.Component, dir string) *registry.Component {
	for _, n := range comps {
		if n.Dir == dir || filepath.Join(n.Dir, "backend") == dir {
			return n
		}
	}
	return nil
}

// moduleDirIn is where a tree materialized at root and shown at dir keeps
// its go.mod: dir itself, or dir/backend; "" when neither.
func moduleDirIn(root, dir string) string {
	for _, sub := range []string{"", "backend"} {
		f, err := fsutil.OpenBeneath(root, filepath.Join(sub, "go.mod"))
		if err != nil {
			continue
		}
		fi, err := f.Stat()
		f.Close()
		if err == nil && fi.Mode().IsRegular() {
			return filepath.Join(dir, sub)
		}
	}
	return ""
}

// goWorkMax and goSumMax cap what a build reads of go.work and go.sum.
const (
	goWorkMax = 1 << 20
	goSumMax  = 8 << 20
)

// workUse is one `use` directive of go.work.
type workUse struct {
	line  int    // its line
	token string // the path as written
	dir   string // the directory it names, absolute and clean
}

// readGoWork reads the workspace's go.work (beneath the workspace: a
// symlink out of it is refused) and its use directives; nothing when there
// is none.
func readGoWork(root string) ([]string, []workUse, error) {
	f, err := fsutil.OpenBeneath(root, "go.work")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("the workspace's go.work can't be read: %w", err)
	}
	b, err := io.ReadAll(io.LimitReader(f, goWorkMax+1))
	f.Close()
	if err != nil || len(b) > goWorkMax {
		return nil, nil, errors.New("the workspace's go.work can't be read")
	}
	lines := strings.Split(string(b), "\n")
	var uses []workUse
	add := func(i int, tok string) {
		p := tok
		if u, err := strconv.Unquote(tok); err == nil {
			p = u
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, filepath.FromSlash(p))
		}
		uses = append(uses, workUse{line: i, token: tok, dir: filepath.Clean(p)})
	}
	block := false
	for i, l := range lines {
		t := l
		if j := strings.Index(t, "//"); j >= 0 {
			t = t[:j]
		}
		t = strings.TrimSpace(t)
		if block {
			if t == ")" {
				block = false
			} else if t != "" {
				add(i, t)
			}
			continue
		}
		rest, ok := strings.CutPrefix(t, "use")
		if !ok || rest == "" || !strings.ContainsRune(" \t(", rune(rest[0])) {
			continue
		}
		rest = strings.TrimSpace(rest)
		if inner, ok := strings.CutPrefix(rest, "("); ok {
			inner, closed := strings.CutSuffix(inner, ")")
			for _, tok := range strings.Fields(inner) {
				add(i, tok)
			}
			block = !closed
			continue
		}
		add(i, rest)
	}
	return lines, uses, nil
}

// goSumPins lists the module@version pairs a go.sum beneath root pins,
// sorted and without duplicates; nil without one.
func goSumPins(root, rel string) []string {
	f, err := fsutil.OpenBeneath(root, rel)
	if err != nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, goSumMax))
	f.Close()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if fl := strings.Fields(l); len(fl) >= 2 {
			seen[fl[0]+"@"+strings.TrimSuffix(fl[1], "/go.mod")] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// realDir reports whether p is a directory, not a symlink to one.
func realDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

type toolchain struct {
	gobin, goroot string
	goproxy       string // the host's GOPROXY list
	download      string // the host module cache's download dir ("" = none)
	version       string // its GOVERSION, recorded in build.json
}

var (
	tcOnce sync.Once
	tcVal  toolchain
	tcErr  error
)

// hostToolchain finds the Go that xbind would have built with on the host:
// `go` on xbind's PATH, and its GOROOT, GOPROXY, module cache and version.
func hostToolchain() (toolchain, error) {
	tcOnce.Do(func() {
		gobin, err := exec.LookPath("go")
		if err != nil {
			tcErr = err
			return
		}
		gobin, _ = filepath.Abs(gobin)
		if p, err := filepath.EvalSymlinks(gobin); err == nil {
			gobin = p
		}
		cmd := exec.Command(gobin, "env", "GOROOT", "GOPROXY", "GOMODCACHE", "GOVERSION") // exec-ok: xbind's own toolchain, run in / — no workspace input
		cmd.Dir = "/"
		outb, err := cmd.Output()
		if err != nil {
			tcErr = fmt.Errorf("go env: %w", err)
			return
		}
		f := strings.Split(strings.TrimRight(string(outb), "\n"), "\n")
		if len(f) < 4 || f[0] == "" {
			tcErr = errors.New("go env: unexpected output")
			return
		}
		tcVal = toolchain{gobin: gobin, goroot: f[0], goproxy: f[1], version: f[3]}
		if tcVal.goproxy == "" {
			tcVal.goproxy = "https://proxy.golang.org,direct"
		}
		if dl := filepath.Join(f[2], "cache", "download"); f[2] != "" && pathExists(dl) {
			tcVal.download = dl
		}
		if !within(gobin, tcVal.goroot) {
			tcErr = fmt.Errorf("go binary %s is outside its GOROOT %s", gobin, tcVal.goroot)
		}
	})
	return tcVal, tcErr
}
