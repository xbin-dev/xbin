package main

// fake_backend_test.go — the `fake` Backend: every sandbox a directory on
// the host, every command a host process, every terminal a host
// pseudo-terminal. TEST ONLY — nothing here isolates anything; it lives in
// _test.go files so no build of the tile ever contains it. It speaks the
// runtime's semantics (docs/protocol.md §Tile sandboxes) the way
// hack/fakesandbox speaks the contract's: names, definitions (a fixed
// layout: <dir>/work and <dir>/home, reported in Defaults), egress classes
// with egressNext, autoStart, exec rings read by offset, clientIds per
// sandbox, snapshots and clones as directory copies.
//
// The rest of it: fake_exec_test.go (commands), fake_tty_test.go
// (terminals), fake_files_test.go (files, trees, snapshots). Every file is
// self-contained (no testing import), so a harness may copy the four in
// under names without _test to run a manager on host directories.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	registerBackend("fake", func(env BackendEnv) (Backend, error) {
		root, _ := env.Config["root"].(string)
		if root == "" {
			return nil, fmt.Errorf("the fake backend needs backendConfig.root (a host directory)")
		}
		return &fakeBackend{Root: root}, nil
	})
}

// fakeBackend is the fake substrate.
type fakeBackend struct {
	Root    string            // sandboxes live in <Root>/<name>/, snapshots in <Root>/.snaps/
	Caps    []string          // what it offers (nil: everything it can)
	Grace   time.Duration     // TERM → KILL on a timeout (0 = 5 s)
	Ring    int               // an exec's output ring (0 = 1 MiB)
	FileMax int64             // limits.fileMax (0 = 64 MiB)
	Classes map[string]string // sandbox-net slot → the reach it is bound to ("" unbound); nil: internet and open bound
	Users   string            // "any" (default) or "root"
	Limits  xbin.SandboxLimits

	mu     sync.Mutex
	boxes  map[string]*fkBox
	closed bool
	fail   map[string]error // op → the next call's error (tests)
	calls  map[string]int   // op → how many calls (tests)
	wmu    sync.Mutex       // serialises content writes (a conditional write's check and its rename)
}

type fkBox struct {
	info        xbin.SandboxInfo
	hash        string // its create request's, for a repeated clientId
	pendingSize bool
	dir         string // symlink-free, so "inside" paths and host paths agree
	execs       map[string]*fkExec
	eseq        int
	eidem       map[string]fkIdem
	snaps       map[string]*fkSnap
	sseq        int
	sidem       map[string]fkIdem
}

type fkIdem struct{ id, hash string }

var fkName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func fkErr(status int, refusal, format string, a ...any) *xbin.SandboxError {
	return &xbin.SandboxError{Status: status, Refusal: refusal, Message: fmt.Sprintf(format, a...)}
}

func fkState(state, format string, a ...any) *xbin.SandboxError {
	return &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: state, Message: fmt.Sprintf(format, a...)}
}

func fkNow() int64 { return time.Now().UnixMilli() }

func fkHash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// FailNext makes the next call of op fail with err (tests).
func (f *fakeBackend) FailNext(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[op] = err
}

// Calls is how many calls of op there were (tests).
func (f *fakeBackend) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// enter counts a call of op and answers an injected failure (f.mu held).
func (f *fakeBackend) enter(op string) error {
	if f.boxes == nil {
		f.boxes = map[string]*fkBox{}
	}
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[op]++
	if err := f.fail[op]; err != nil {
		delete(f.fail, op)
		return err
	}
	return nil
}

func (f *fakeBackend) caps() []string {
	if f.Caps != nil {
		return f.Caps
	}
	if fkHasPTY() {
		return []string{"exec", "files", "tar", "tty", "snapshots", "clone"}
	}
	return []string{"exec", "files", "tar", "snapshots", "clone"}
}

func (f *fakeBackend) hasCap(c string) bool {
	for _, x := range f.caps() {
		if x == c {
			return true
		}
	}
	return false
}

func (f *fakeBackend) grace() time.Duration {
	if f.Grace > 0 {
		return f.Grace
	}
	return 5 * time.Second
}

func (f *fakeBackend) ringSize() int {
	if f.Ring > 0 {
		return f.Ring
	}
	return 1 << 20
}

func (f *fakeBackend) fileMax() int64 {
	if f.FileMax > 0 {
		return f.FileMax
	}
	return 64 << 20
}

func (f *fakeBackend) classes() map[string]string {
	if f.Classes != nil {
		return f.Classes
	}
	return map[string]string{"internet": "internet", "open": "open"}
}

// reach is what a class reaches now.
func (f *fakeBackend) reach(class string) string {
	if class == "" || class == "none" {
		return "none"
	}
	if r := f.classes()[class[len("class:"):]]; r != "" {
		return r
	}
	return "none"
}

func (f *fakeBackend) Runtime(ctx context.Context) (*xbin.SandboxRuntime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("runtime"); err != nil {
		return nil, err
	}
	rt := &xbin.SandboxRuntime{Enabled: true, Isolation: true, Modes: []xbin.SandboxMode{{Mode: "namespace"}, {Mode: "vm", Accel: "fake"}},
		Users: orStr(f.Users, "any"), Caps: f.caps(), Egress: []xbin.SandboxEgress{{Class: "none", Reach: "none"}}}
	var slots []string
	for s := range f.classes() {
		slots = append(slots, s)
	}
	sort.Strings(slots)
	for _, s := range slots {
		reach, ref := f.classes()[s], f.classes()[s]
		if reach == "" {
			reach = "none"
		}
		rt.Egress = append(rt.Egress, xbin.SandboxEgress{Class: "class:" + s, Slot: s, Ref: ref, Reach: reach})
	}
	l := f.Limits
	l.RunTimeoutMaxMs = or64(l.RunTimeoutMaxMs, 600000)
	l.RunOutputMax = or64(l.RunOutputMax, 1<<20)
	l.ExecsRunning = int(or64(int64(l.ExecsRunning), 16))
	l.OutputRing = int64(f.ringSize())
	l.StdinMax = or64(l.StdinMax, 1<<20)
	l.FileMax = f.fileMax()
	l.TarMax = or64(l.TarMax, 1<<30)
	l.WaitMaxSec = int(or64(int64(l.WaitMaxSec), 120))
	if l.PerSandbox.MaxMemMiB == 0 {
		l.PerSandbox = xbin.SandboxSizeLimits{MemMiB: 2048, VCPUs: 2, DiskGiB: 20, MaxMemMiB: 8192, MaxVCPUs: 8, MaxDiskGiB: 200}
	}
	rt.Limits = l
	for _, b := range f.boxes {
		rt.Used.Sandboxes++
		if b.info.State == "running" {
			rt.Used.Running++
		}
	}
	return rt, nil
}

// view is b's info now (f.mu held).
func (f *fakeBackend) view(b *fkBox) xbin.SandboxInfo {
	in := b.info
	in.Net.Reach = f.reach(in.Net.Egress)
	in.RestartNeeded = in.Net.EgressNext != "" || b.pendingSize
	in.Labels = map[string]string{}
	for k, v := range b.info.Labels {
		in.Labels[k] = v
	}
	in.Defaults.Env = map[string]string{}
	for k, v := range b.info.Defaults.Env {
		in.Defaults.Env[k] = v
	}
	in.Snapshots = len(b.snaps)
	for _, e := range b.execs {
		if e.State == "running" {
			in.ExecsRunning++
		}
	}
	return in
}

func (f *fakeBackend) List(ctx context.Context) ([]xbin.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("list"); err != nil {
		return nil, err
	}
	out := []xbin.SandboxInfo{}
	for _, b := range f.boxes {
		out = append(out, f.view(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// box finds name (f.mu held).
func (f *fakeBackend) box(name string) (*fkBox, error) {
	if f.boxes == nil {
		f.boxes = map[string]*fkBox{}
	}
	b := f.boxes[name]
	if b == nil {
		return nil, fkErr(http.StatusNotFound, "not-found", "no sandbox %s", name)
	}
	return b, nil
}

func (f *fakeBackend) checkNet(n *xbin.SandboxNet) (string, error) {
	if n == nil || n.Egress == "" || n.Egress == "none" {
		return "none", nil
	}
	if len(n.Egress) > len("class:") && n.Egress[:len("class:")] == "class:" {
		if _, ok := f.classes()[n.Egress[len("class:"):]]; ok {
			return n.Egress, nil
		}
	}
	return "", fkErr(http.StatusBadRequest, "invalid", "egress %q: none or class:<a sandbox-net slot>", n.Egress)
}

func (f *fakeBackend) Create(ctx context.Context, spec xbin.SandboxSpec) (*xbin.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("create"); err != nil {
		return nil, err
	}
	switch {
	case f.closed:
		return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "closed")
	case !fkName.MatchString(spec.Name) || spec.Name == "runtime" || spec.Name == "policy" || spec.Name == "copy":
		return nil, fkErr(http.StatusBadRequest, "invalid", "name %q", spec.Name)
	case spec.Mode != "vm" && spec.Mode != "namespace":
		return nil, fkErr(http.StatusBadRequest, "invalid", "mode is vm or namespace")
	}
	egress, err := f.checkNet(spec.Net)
	if err != nil {
		return nil, err
	}
	h := fkHash(spec)
	if spec.ClientID != "" {
		for _, b := range f.boxes {
			if b.info.ClientID == spec.ClientID {
				if b.hash != h {
					return nil, fkErr(http.StatusConflict, "exists", "clientId %s was used for a different sandbox", spec.ClientID)
				}
				in := f.view(b)
				return &in, nil
			}
		}
	}
	if f.boxes[spec.Name] != nil {
		return nil, fkErr(http.StatusConflict, "exists", "a sandbox %s exists", spec.Name)
	}
	var src string
	if spec.From != nil {
		if !f.hasCap("clone") {
			return nil, fkErr(http.StatusNotImplemented, "unsupported", "no clones here")
		}
		sb, err := f.box(spec.From.Sandbox)
		if err != nil {
			return nil, err
		}
		src = sb.dir
		if spec.From.Snapshot != "" {
			sn := sb.snaps[spec.From.Snapshot]
			if sn == nil {
				return nil, fkErr(http.StatusNotFound, "not-found", "no snapshot %s", spec.From.Snapshot)
			}
			src = sn.dir
		}
	}
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // a sandbox's paths are its host paths: keep them symlink-free (pwd agrees)
	}
	dir := filepath.Join(root, spec.Name)
	_ = os.RemoveAll(dir) // a previous run's leftovers: a new sandbox starts empty
	if src != "" {
		if err := fkCopyTree(src, dir); err != nil {
			return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "clone: %v", err)
		}
	}
	for _, d := range []string{"work", "home"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "%v", err)
		}
	}
	now := fkNow()
	in := xbin.SandboxInfo{Name: spec.Name, State: "stopped", Mode: "fake", MemMiB: spec.MemMiB, VCPUs: spec.VCPUs, DiskGiB: spec.DiskGiB,
		Net: xbin.SandboxNetInfo{Egress: egress}, Labels: spec.Labels, For: spec.For, ForUser: spec.ForUser,
		IdleStopMin: spec.IdleStopMin, AutoStart: spec.AutoStart == nil || *spec.AutoStart, Base: xbin.SandboxBase{Version: "fake-1"},
		Users: orStr(f.Users, "any"), Created: now, LastActive: now, Version: 1, ClientID: spec.ClientID}
	if in.MemMiB == 0 {
		in.MemMiB, in.VCPUs, in.DiskGiB = 2048, 2, 20
	}
	if in.IdleStopMin == 0 {
		in.IdleStopMin = 30
	}
	in.Defaults = fkDefaults(dir, spec.Defaults)
	b := &fkBox{info: in, hash: h, dir: dir, execs: map[string]*fkExec{}, eidem: map[string]fkIdem{},
		snaps: map[string]*fkSnap{}, sidem: map[string]fkIdem{}}
	f.boxes[spec.Name] = b
	if spec.Start {
		b.started()
	}
	out := f.view(b)
	return &out, nil
}

// fkDefaults is the fixed layout — work and home in the sandbox's
// directory, /bin/sh — with the rest of what was asked.
func fkDefaults(dir string, d *xbin.SandboxDefaults) xbin.SandboxDefaults {
	out := xbin.SandboxDefaults{Cwd: filepath.Join(dir, "work"), Shell: "/bin/sh", Env: map[string]string{}}
	if d != nil {
		out.UID, out.GID = d.UID, d.GID
		for k, v := range d.Env {
			out.Env[k] = v
		}
	}
	out.Env["HOME"] = filepath.Join(dir, "home")
	return out
}

// started brings b up (f.mu held): a pending egress applies now.
func (b *fkBox) started() {
	b.info.State = "running"
	b.info.Started, b.info.LastActive = fkNow(), fkNow()
	if b.info.Net.EgressNext != "" {
		b.info.Net.Egress, b.info.Net.EgressNext = b.info.Net.EgressNext, ""
	}
	b.pendingSize = false
}

func (f *fakeBackend) Get(ctx context.Context, name string) (*xbin.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("get"); err != nil {
		return nil, err
	}
	b, err := f.box(name)
	if err != nil {
		return nil, err
	}
	in := f.view(b)
	return &in, nil
}

func (f *fakeBackend) Patch(ctx context.Context, name string, p xbin.SandboxPatch) (*xbin.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("patch"); err != nil {
		return nil, err
	}
	b, err := f.box(name)
	if err != nil {
		return nil, err
	}
	if p.Version != 0 && p.Version != b.info.Version {
		return nil, fkErr(http.StatusPreconditionFailed, "precondition", "version %d, not %d", b.info.Version, p.Version)
	}
	egress := ""
	if p.Net != nil {
		if egress, err = f.checkNet(p.Net); err != nil {
			return nil, err
		}
	}
	running := b.info.State == "running"
	if p.MemMiB != nil || p.VCPUs != nil || p.DiskGiB != nil {
		if p.MemMiB != nil {
			b.info.MemMiB = *p.MemMiB
		}
		if p.VCPUs != nil {
			b.info.VCPUs = *p.VCPUs
		}
		if p.DiskGiB != nil {
			b.info.DiskGiB = *p.DiskGiB
		}
		b.pendingSize = b.pendingSize || running
	}
	if p.Net != nil { // a running sandbox keeps its network until it restarts
		switch {
		case !running:
			b.info.Net.Egress, b.info.Net.EgressNext = egress, ""
		case egress == b.info.Net.Egress:
			b.info.Net.EgressNext = ""
		default:
			b.info.Net.EgressNext = egress
		}
	}
	if p.Defaults != nil {
		b.info.Defaults = fkDefaults(b.dir, p.Defaults)
	}
	if p.Labels != nil {
		b.info.Labels = *p.Labels
	}
	if p.For != nil {
		b.info.For = *p.For
	}
	if p.ForUser != nil {
		b.info.ForUser = *p.ForUser
	}
	if p.IdleStopMin != nil {
		b.info.IdleStopMin = *p.IdleStopMin
	}
	if p.AutoStart != nil {
		b.info.AutoStart = *p.AutoStart
	}
	b.info.Version++
	in := f.view(b)
	return &in, nil
}

func (f *fakeBackend) Delete(ctx context.Context, name string) error {
	f.mu.Lock()
	if err := f.enter("delete"); err != nil {
		f.mu.Unlock()
		return err
	}
	b, err := f.box(name)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	delete(f.boxes, name)
	killed := b.stopExecs()
	f.mu.Unlock()
	fkAwait(killed, 5*time.Second) // nothing writes into the tree while it goes
	_ = os.RemoveAll(b.dir)
	_ = os.RemoveAll(filepath.Join(f.Root, ".snaps", name))
	return nil
}

func (f *fakeBackend) Start(ctx context.Context, name string, wait time.Duration) (*xbin.SandboxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("start"); err != nil {
		return nil, err
	}
	b, err := f.box(name)
	if err != nil {
		return nil, err
	}
	switch b.info.State {
	case "running":
	case "stopped":
		b.started()
		b.info.Version++
	default:
		return nil, fkState(b.info.State, "a %s sandbox can't start", b.info.State)
	}
	in := f.view(b)
	return &in, nil
}

func (f *fakeBackend) Stop(ctx context.Context, name string, wait time.Duration) (*xbin.SandboxInfo, error) {
	f.mu.Lock()
	if err := f.enter("stop"); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	b, err := f.box(name)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	killed := b.stopExecs()
	if b.info.State == "running" {
		b.info.State = "stopped"
		b.info.Version++
	}
	f.mu.Unlock()
	fkAwait(killed, 5*time.Second) // its execs have ended when it answers
	f.mu.Lock()
	in := f.view(b)
	f.mu.Unlock()
	return &in, nil
}

// Close kills every running command and waits (a few seconds at most) for
// them to end; nothing starts after it (tests' cleanup).
func (f *fakeBackend) Close() {
	f.mu.Lock()
	f.closed = true
	var all []*fkExec
	for _, b := range f.boxes {
		all = append(all, b.stopExecs()...)
	}
	f.mu.Unlock()
	fkAwait(all, 5*time.Second)
}

// usable brings a stopped sandbox up for a command or a file operation, as
// the runtime's autoStart does (f.mu held).
func (b *fkBox) usable() error {
	switch b.info.State {
	case "running":
	case "stopped":
		if !b.info.AutoStart {
			return fkState("stopped", "the sandbox is stopped")
		}
		b.started()
		b.info.Version++
	default:
		return fkState(b.info.State, "the sandbox is %s", b.info.State)
	}
	b.info.LastActive = fkNow()
	return nil
}

// Sandbox is one of the fake's sandboxes.
func (f *fakeBackend) Sandbox(name string) Box { return &fkSandbox{f: f, name: name} }

type fkSandbox struct {
	f    *fakeBackend
	name string
}

// use finds the sandbox for a command or a file operation, started (f.mu
// held on return when ok).
func (s *fkSandbox) use(op string) (*fkBox, error) {
	s.f.mu.Lock()
	if err := s.f.enter(op); err != nil {
		s.f.mu.Unlock()
		return nil, err
	}
	b, err := s.f.box(s.name)
	if err == nil {
		err = b.usable()
	}
	if err != nil {
		s.f.mu.Unlock()
		return nil, err
	}
	return b, nil
}

// find finds the sandbox without starting it (f.mu held on return when ok).
func (s *fkSandbox) find(op string) (*fkBox, error) {
	s.f.mu.Lock()
	if err := s.f.enter(op); err != nil {
		s.f.mu.Unlock()
		return nil, err
	}
	b, err := s.f.box(s.name)
	if err != nil {
		s.f.mu.Unlock()
		return nil, err
	}
	return b, nil
}
