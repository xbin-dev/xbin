// manager.go — the contract layer's core: the substrate's offer, the
// errors, hello, and the sandbox resource as each caller sees it
// (docs/sandbox-manager.md); who is asking and what they may see are in
// access.go. The routes are in sandboxes.go (list, get, PATCH, DELETE),
// create.go, lifecycle.go (start, stop, ready), commands.go (run, execs,
// terminals) and files.go (files, trees, snapshots); the operators' own
// routes in operator.go.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// managerVersion is this template's version in hello.
const managerVersion = "1.0.0"

// Manager serves the contract over a Backend.
type Manager struct {
	st *store

	mu       sync.Mutex
	be       Backend
	beErr    error // why the configured backend is down ("" when it runs)
	cfg      Config
	recs     map[string]*record     // by contract id
	imgs     map[string]*builtImage // by image id
	execIdem map[string]execIdem    // consumer, sandbox, clientId → exec
	live     map[string]time.Time   // sandbox id → when it was last known running (quota and prepare checks)
	starting map[string]bool        // sandbox ids a start of ours is bringing up (they count as running)
	locks    map[string]*opLock     // sandbox id → its lifecycle lock
	creates  map[string]*createJob  // sandbox id → its creation under way
	builds   map[string]*buildJob   // image id → the build under way
	rt       *xbin.SandboxRuntime
	rtErr    error
	rtAt     time.Time
	// self is this tile's path (XBIN_COMPONENT): its own page's calls come
	// from it (pageReader). Tests set it.
	self string

	ctx    context.Context // background work (creations, image builds)
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// RuntimeTTL is how long the substrate's Runtime answer is reused; LiveTTL
	// how long a sandbox seen running is taken to be (0: the defaults).
	RuntimeTTL time.Duration
	LiveTTL    time.Duration
	// KeepAlive is how often an open terminal or stdio socket is activity
	// on its sandbox (keepAlive; 0: every 30 s).
	KeepAlive time.Duration
	// Logf logs (nil: the standard logger).
	Logf func(format string, args ...any)
}

type execIdem struct{ id, hash string }

// opLock serialises one sandbox's definition changes and lifecycle.
type opLock struct {
	sync.Mutex
	n int // holders and waiters (the entry goes when it drops to 0)
}

// newManager loads the store and opens the configured backend (be, when not
// nil, is used instead — tests).
func newManager(st *store, be Backend) (*Manager, error) {
	cfg, err := st.config()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	recs, err := st.records()
	if err != nil {
		return nil, fmt.Errorf("sandboxes: %w", err)
	}
	imgs, err := st.images()
	if err != nil {
		return nil, fmt.Errorf("images: %w", err)
	}
	// A manager that made sandboxes before "vm" became the default, and never
	// saved a config, keeps the automatic mode it ran with.
	if saved, _ := st.setting("config"); saved == "" && (len(recs) > 0 || len(imgs) > 0) {
		cfg.Mode = "auto"
		if err := st.putConfig(cfg); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	m := &Manager{st: st, cfg: cfg, recs: map[string]*record{}, imgs: map[string]*builtImage{},
		execIdem: map[string]execIdem{}, live: map[string]time.Time{}, starting: map[string]bool{},
		locks: map[string]*opLock{}, creates: map[string]*createJob{}, builds: map[string]*buildJob{}, self: xbin.Self()}
	for _, r := range recs {
		m.recs[r.ID] = r
	}
	for _, im := range imgs {
		m.imgs[im.ID] = im
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if be != nil {
		m.be = be
	} else {
		m.be, m.beErr = openBackend(cfg.backendName(), BackendEnv{Config: cfg.BackendConfig})
	}
	return m, nil
}

// Close stops the background work (creations and builds resume at the next
// start) and waits for it.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (m *Manager) backend() Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.be
}

func (m *Manager) config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// go runs background work that Close waits for.
func (m *Manager) goBG(f func(ctx context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		f(m.ctx)
	}()
}

// --- the substrate's offer ------------------------------------------------------------

// runtime is the backend's Runtime, reused for RuntimeTTL.
func (m *Manager) runtime(ctx context.Context) (*xbin.SandboxRuntime, error) {
	ttl := m.RuntimeTTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	m.mu.Lock()
	if (m.rt != nil || m.rtErr != nil) && time.Since(m.rtAt) < ttl && (m.rtErr == nil || time.Since(m.rtAt) < time.Second) {
		rt, err := m.rt, m.rtErr
		m.mu.Unlock()
		return rt, err
	}
	be := m.be
	m.mu.Unlock()
	rt, err := be.Runtime(ctx)
	if err == nil && (!rt.Enabled || !rt.Isolation) {
		why := "the tile-sandbox runtime is off"
		if !rt.Isolation {
			why = "xbind runs without isolation, and tile sandboxes need it"
		}
		rt, err = nil, &xbin.SandboxError{Status: http.StatusServiceUnavailable, Refusal: "unavailable", Message: why, RetryAfter: time.Minute}
	}
	if ctx.Err() == nil {
		m.mu.Lock()
		m.rt, m.rtErr, m.rtAt = rt, err, time.Now()
		m.mu.Unlock()
	}
	return rt, err
}

// forgetRuntime drops the cached Runtime (config and backend changes).
func (m *Manager) forgetRuntime() {
	m.mu.Lock()
	m.rt, m.rtErr = nil, nil
	m.mu.Unlock()
}

// contractCaps are the contract's capabilities the backend offers
// (archive: not in this manager yet).
func contractCaps(rt *xbin.SandboxRuntime) []string {
	out := []string{}
	for _, c := range []string{"exec", "files", "tar", "tty", "snapshots", "clone"} {
		if rt != nil && slices.Contains(rt.Caps, c) {
			out = append(out, c)
		}
	}
	return out
}

// egress classes: the contract's words and the sandbox-net slots behind them.
var egressOrder = map[string]int{"none": 0, "internet": 1, "open": 2}

func egressClass(word string) string {
	if word == "" || word == "none" {
		return "none"
	}
	return "class:" + word
}

// egressWord is the contract's word for a runtime class and its reach: the
// class's own word, or its reach when that is wider — a sandbox never
// claims less than it can reach.
func egressWord(class, reach string) string {
	w := "none"
	switch class {
	case "", "none":
	case "class:internet":
		w = "internet"
	default:
		w = "open"
	}
	if reach == "" {
		return w
	}
	r, known := egressOrder[reach]
	switch {
	case !known:
		return "open" // an unknown reach counts as open
	case r > egressOrder[w]:
		return reach
	}
	return w
}

// offeredEgress is hello.egress: none, then internet while the internet
// class is bound to the public internet, then open while the open class is
// bound to anything.
func offeredEgress(rt *xbin.SandboxRuntime) []string {
	out := []string{"none"}
	for _, word := range []string{"internet", "open"} {
		for _, e := range rt.Egress {
			if e.Class != "class:"+word && e.Slot != word {
				continue
			}
			if e.Ref == "" || e.Reach == "" || e.Reach == "none" {
				continue
			}
			if word == "internet" && e.Reach != "internet" {
				continue // bound to more than the internet: it isn't "internet"
			}
			out = append(out, word)
		}
	}
	return out
}

// --- errors ------------------------------------------------------------------------------

// refusals by status, for an answer that came without one.
var statusRefusal = map[int]string{400: "invalid", 403: "not-allowed", 404: "not-found", 409: "state", 410: "lost",
	412: "precondition", 413: "too-large", 429: "limit", 501: "unsupported", 503: "unavailable"}

func fail(w http.ResponseWriter, status int, refusal, msg string) {
	xbin.WriteSandboxError(w, &xbin.SandboxError{Status: status, Refusal: refusal, Message: msg})
}

func failState(w http.ResponseWriter, msg, state string) {
	xbin.WriteSandboxError(w, &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", Message: msg, State: state})
}

func errf(status int, refusal, format string, a ...any) *xbin.SandboxError {
	return &xbin.SandboxError{Status: status, Refusal: refusal, Message: fmt.Sprintf(format, a...)}
}

// hideName is err with a runtime name in its message replaced by with (a
// refusal keeps its status and refusal word).
func hideName(err error, name, with string) error {
	if err == nil || name == "" || !strings.Contains(err.Error(), name) {
		return err
	}
	if se, ok := err.(*xbin.SandboxError); ok {
		e := *se
		e.Message = strings.ReplaceAll(e.Message, name, with)
		return &e
	}
	return errors.New(strings.ReplaceAll(err.Error(), name, with))
}

// writeErr answers err in the contract's shape: a backend's refusal as it
// came (its runtime name replaced by the contract id), anything else 503
// unavailable.
func writeErr(w http.ResponseWriter, err error, rec *record) {
	var se *xbin.SandboxError
	if !errors.As(err, &se) {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			fail(w, http.StatusRequestEntityTooLarge, "too-large", "the body is over this manager's limit")
			return
		}
		se = &xbin.SandboxError{Status: http.StatusServiceUnavailable, Refusal: "unavailable",
			Message: "the sandbox substrate: " + err.Error(), RetryAfter: 5 * time.Second}
	}
	e := *se
	if e.Refusal == "" {
		e.Refusal = statusRefusal[e.Status]
		if e.Refusal == "" {
			e.Status, e.Refusal = http.StatusServiceUnavailable, "unavailable"
		}
	}
	if rec != nil && rec.Runtime != "" {
		e.Message = strings.ReplaceAll(e.Message, rec.Runtime, rec.ID)
	}
	xbin.WriteSandboxError(w, &e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON body (at most max bytes; empty is fine).
func decode(r *http.Request, max int64, v any) error {
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > max {
		return errors.New("the body is too large")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func hashOf(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() int64 { return time.Now().UnixMilli() }

// --- hello --------------------------------------------------------------------------------

type imageEntry struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Default   bool      `json:"default,omitempty"`
	Tools     []string  `json:"tools"`
	Harnesses []Harness `json:"harnesses,omitempty"` // additive: an image's coding agents
}

type sizeEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	MemMiB  int    `json:"memMiB"`
	VCPUs   int    `json:"vcpus"`
	DiskGiB int    `json:"diskGiB"`
	Default bool   `json:"default,omitempty"`
}

// offer is what a caller may pick from now: the images the backend can make,
// the sizes within its caps, the egress bound, the capabilities.
type offer struct {
	rt     *xbin.SandboxRuntime
	caps   []string
	egress []string
	images []Image
	sizes  []Size
	notes  []string
}

func (m *Manager) offer(ctx context.Context) (*offer, error) {
	rt, err := m.runtime(ctx)
	if err != nil {
		return nil, err
	}
	cfg := m.config()
	o := &offer{rt: rt, caps: contractCaps(rt), egress: offeredEgress(rt)}
	if m.portsOffered(rt) { // D135 (ports.go)
		o.caps = append(o.caps, "ports")
	}
	if slices.Contains(rt.Caps, "stdio") && m.stdioBackend() {
		o.caps = append(o.caps, "stdio")
	}
	clones := slices.Contains(o.caps, "clone") && slices.Contains(o.caps, "snapshots")
	hidden := 0
	for _, im := range cfg.Images {
		if im.Setup != "" && !clones {
			hidden++
			continue
		}
		o.images = append(o.images, im)
	}
	if hidden > 0 {
		o.notes = append(o.notes, fmt.Sprintf("%d image(s) with a setup script are hidden: building one needs the substrate's snapshots and clones, which it doesn't offer yet — only plain base images are offered", hidden))
	}
	if len(o.images) == 0 {
		o.images = []Image{{ID: "base", Title: "the substrate's base image", Default: true}}
	}
	if mode, err := m.chooseMode(rt); err != nil {
		o.notes = append(o.notes, "no sandbox can be made now: "+errText(err))
	} else if !sudoWorks(mode) {
		var sudo []string
		for _, im := range o.images {
			if im.Sudo {
				sudo = append(sudo, im.ID)
			}
		}
		if len(sudo) > 0 {
			o.notes = append(o.notes, fmt.Sprintf("the image(s) %s give their user sudo, which only VM sandboxes can: this manager makes %s sandboxes now, which run with no new privileges, so theirs get none", strings.Join(sudo, ", "), mode))
		}
	}
	if missing := missingCaps(o.caps, "exec", "files"); len(missing) > 0 {
		o.notes = append(o.notes, "the substrate doesn't serve "+strings.Join(missing, " or ")+" yet: sandboxes can be made, started and stopped, but nothing runs in them")
	}
	o.images = oneDefault(o.images, func(im *Image) *bool { return &im.Default })
	ps := rt.Limits.PerSandbox
	for _, s := range cfg.Sizes {
		if ps.MaxMemMiB > 0 && s.MemMiB > ps.MaxMemMiB || ps.MaxVCPUs > 0 && s.VCPUs > ps.MaxVCPUs || ps.MaxDiskGiB > 0 && s.DiskGiB > ps.MaxDiskGiB {
			continue // over what the substrate gives one sandbox
		}
		o.sizes = append(o.sizes, s)
	}
	if len(o.sizes) == 0 { // the smallest, clamped by the substrate
		s := cfg.Sizes[0]
		for _, x := range cfg.Sizes {
			if x.MemMiB < s.MemMiB {
				s = x
			}
		}
		o.sizes = []Size{s}
		o.notes = append(o.notes, "every configured size is over the substrate's per-sandbox caps; the smallest is offered and clamped")
	}
	o.sizes = oneDefault(o.sizes, func(s *Size) *bool { return &s.Default })
	return o, nil
}

// stdioBackend reports that the backend's sandboxes serve stdio sockets
// (StdioBox): a backend written before them builds without it, and the
// manager doesn't offer `stdio` over it.
func (m *Manager) stdioBackend() bool {
	_, ok := m.backend().Sandbox("").(StdioBox)
	return ok
}

// missingCaps are those of want that caps lacks.
func missingCaps(caps []string, want ...string) []string {
	var out []string
	for _, c := range want {
		if !slices.Contains(caps, c) {
			out = append(out, c)
		}
	}
	return out
}

// oneDefault keeps exactly one default: the first marked one, else the first.
func oneDefault[T any](xs []T, def func(*T) *bool) []T {
	out := append([]T(nil), xs...)
	found := false
	for i := range out {
		d := def(&out[i])
		if *d && !found {
			found = true
		} else {
			*d = false
		}
	}
	if !found && len(out) > 0 {
		*def(&out[0]) = true
	}
	return out
}

func (o *offer) image(id string) (Image, bool) {
	for _, im := range o.images {
		if im.ID == id || id == "" && im.Default {
			return im, true
		}
	}
	return Image{}, false
}

func (o *offer) size(id string) (Size, bool) {
	for _, s := range o.sizes {
		if s.ID == id || id == "" && s.Default {
			return s, true
		}
	}
	return Size{}, false
}

func (m *Manager) hello(w http.ResponseWriter, r *http.Request) {
	if p := r.URL.Query().Get("protocol"); p != "" && p != "1" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "protocol " + p + " is not spoken here",
			"refusal": "protocol", "protocols": []int{1}})
		return
	}
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return
	}
	c := callerOf(r)
	images := []imageEntry{}
	for _, im := range o.images {
		tools := im.Tools
		if tools == nil {
			tools = []string{}
		}
		images = append(images, imageEntry{ID: im.ID, Title: im.Title, Default: im.Default, Tools: tools, Harnesses: im.Harnesses})
	}
	sizes := []sizeEntry{}
	for _, s := range o.sizes {
		sizes = append(sizes, sizeEntry{ID: s.ID, Title: s.Title, MemMiB: s.MemMiB, VCPUs: s.VCPUs, DiskGiB: s.DiskGiB, Default: s.Default})
	}
	lim := o.rt.Limits
	q := m.effectiveQuota(c, o.rt)
	h := map[string]any{
		"protocol": 1, "protocols": []int{1},
		"manager": map[string]string{"name": "coding-sandbox", "title": "Coding sandboxes", "version": managerVersion},
		// partitions: this manager keys consumers on their user partitions
		// (the contract's §Partitioned consumers) — the manager's, never a
		// sandbox's capability
		"caps": append(slices.Clone(o.caps), "partitions"), "egress": o.egress, "images": images, "sizes": sizes,
		"limits": map[string]int64{
			"sandboxes":       int64(q.Sandboxes),
			"runTimeoutMaxMs": or64(lim.RunTimeoutMaxMs, 600000),
			"runOutputMax":    or64(lim.RunOutputMax, 1<<20),
			"execsRunning":    or64(int64(lim.ExecsRunning), 16),
			"outputRing":      or64(lim.OutputRing, 1<<20),
			"stdinMax":        or64(lim.StdinMax, 1<<20),
			"fileMax":         or64(lim.FileMax, 64<<20),
			"tarMax":          or64(lim.TarMax, 1<<30),
			"waitMaxSec":      or64(int64(lim.WaitMaxSec), 120),
			// this caller's other quotas (additive; 0 = no fixed limit)
			"running": int64(q.Running), "memMiB": int64(q.MemMiB), "vcpus": int64(q.VCPUs), "diskGiB": int64(q.DiskGiB),
		},
	}
	if len(o.notes) > 0 {
		h["notes"] = o.notes
	}
	writeJSON(w, http.StatusOK, h)
}

func or64(v, def int64) int64 {
	if v > 0 {
		return v
	}
	return def
}

// waitMax is limits.waitMaxSec.
func (m *Manager) waitMax(ctx context.Context) time.Duration {
	rt, err := m.runtime(ctx)
	if err != nil || rt.Limits.WaitMaxSec <= 0 {
		return 120 * time.Second
	}
	return time.Duration(rt.Limits.WaitMaxSec) * time.Second
}

// --- the sandbox as a caller sees it ------------------------------------------------------

type imageRef struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type sizeView struct {
	ID      string `json:"id"`
	MemMiB  int    `json:"memMiB"`
	VCPUs   int    `json:"vcpus"`
	DiskGiB int    `json:"diskGiB"`
}

// sandboxView is the contract's sandbox resource.
type sandboxView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	State         string            `json:"state"`
	StateDetail   string            `json:"stateDetail"`
	Image         imageRef          `json:"image"`
	Size          sizeView          `json:"size"`
	Isolation     string            `json:"isolation"`
	Egress        string            `json:"egress"`
	EgressNext    string            `json:"egressNext,omitempty"`
	EgressDetail  string            `json:"egressDetail"`
	Owner         owner             `json:"owner"`
	Visibility    string            `json:"visibility"`
	Members       []string          `json:"members"`
	Shares        []share           `json:"shares"`
	Shared        bool              `json:"shared"`
	Labels        map[string]string `json:"labels"`
	Workdir       string            `json:"workdir"`
	Home          string            `json:"home"`
	User          string            `json:"user"`
	Shell         string            `json:"shell"`
	Caps          []string          `json:"caps"`
	Created       int64             `json:"created"`
	LastActive    int64             `json:"lastActive"`
	AutoStopMin   int               `json:"autoStopMin"`
	Version       int               `json:"version"`
	RestartNeeded bool              `json:"restartNeeded,omitempty"`
}

// view is rec (a copy) with the substrate's info (nil: it has none) as the
// consumer c sees it.
func (m *Manager) view(rec record, info *xbin.SandboxInfo, c caller, caps []string) sandboxView {
	cfg := m.config()
	v := sandboxView{ID: rec.ID, Name: rec.Name, Image: imageRef{ID: rec.Image}, Owner: rec.Owner, Visibility: rec.Visibility,
		Members: rec.Members, Shares: rec.Shares, Shared: !rec.home(c), Labels: rec.Labels,
		Workdir: rec.Workdir, Home: rec.Home, User: rec.User, Shell: rec.Shell, Caps: caps,
		Created: rec.Created, LastActive: rec.Created, Version: rec.Version, Egress: orStr(rec.Egress, "none"), Isolation: "other"}
	if im, ok := cfg.image(rec.Image); ok {
		v.Image.Title = im.Title
	}
	if isolationWord[rec.Mode] { // the mode it was made in (the substrate's info says it too)
		v.Isolation = rec.Mode
	}
	v.Size.ID = rec.Size
	if s, ok := cfg.size(rec.Size); ok {
		v.Size.MemMiB, v.Size.VCPUs, v.Size.DiskGiB = s.MemMiB, s.VCPUs, s.DiskGiB
	}
	if v.Members == nil {
		v.Members = []string{}
	}
	if v.Shares == nil {
		v.Shares = []share{}
	}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	if v.Caps == nil {
		v.Caps = []string{}
	}
	switch {
	case rec.Overlay != "":
		v.State, v.StateDetail = rec.Overlay, rec.Detail
	case info == nil:
		v.State, v.StateDetail = "error", orStr(rec.Detail, "the substrate has no such sandbox any more")
	default:
		v.State, v.StateDetail = info.State, strings.ReplaceAll(info.StateDetail, rec.Runtime, rec.ID)
	}
	if info != nil {
		if isolationWord[info.Mode] {
			v.Isolation = info.Mode
		}
		if info.MemMiB > 0 { // what applied (clamped by the substrate)
			v.Size.MemMiB, v.Size.VCPUs, v.Size.DiskGiB = info.MemMiB, info.VCPUs, info.DiskGiB
		}
		v.Egress = egressWord(info.Net.Egress, info.Net.Reach)
		if info.Net.EgressNext != "" && info.Net.EgressNext != info.Net.Egress {
			v.EgressNext = egressWord(info.Net.EgressNext, "")
		}
		v.EgressDetail = info.Net.Note
		v.AutoStopMin = info.IdleStopMin
		if info.LastActive > v.LastActive {
			v.LastActive = info.LastActive
		}
		v.RestartNeeded = info.RestartNeeded || v.EgressNext != ""
		if agentPredatesPorts(rec.ID, info) { // its agent refused a port: a restart brings today's (ports.go)
			v.RestartNeeded = true
			v.StateDetail = strings.TrimPrefix(v.StateDetail+"; its agent predates ports (live previews): restart it", "; ")
		}
	}
	return v
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
