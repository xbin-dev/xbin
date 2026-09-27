package xbin

// sandbox.go — tile sandboxes, for manager tiles (docs/sdk.md §Sandboxes,
// docs/protocol.md §Tile sandboxes). A manager tile's backend, holding
// cap:sandboxes, defines sandboxes xbind runs for it and drives them over
// /api/xbin/sandboxes/… through the gateway. The routes mirror the
// sandbox-manager contract, so most of a manager's contract routes are a
// typed call here or a Forward (sandbox_tty.go).
//
// Request structs set omitempty everywhere, and answers are decoded
// leniently, so a newer SDK talks to an older xbind and the reverse.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sandboxesURL is the runtime's root, reached through the gateway.
const sandboxesURL = "http://xbin/api/xbin/sandboxes"

// sandboxAnswerMax bounds a JSON answer (a run's two streams are at most
// 1 MiB each, escaped).
const sandboxAnswerMax = 32 << 20

// Sandboxes is this tile's sandboxes: the runtime xbind runs for a manager
// tile. Every call goes through Client(), so it carries the instance
// credential; xbind answers 403 not-allowed unless the tile holds
// cap:sandboxes.
type Sandboxes struct {
	c *http.Client
}

// SandboxAPI returns this tile's sandboxes.
//
//	sbx := xbin.SandboxAPI()
//	info, err := sbx.Create(ctx, xbin.SandboxSpec{Name: "sb-7f3a", Mode: "vm"})
//	res, err := sbx.Sandbox("sb-7f3a").Run(ctx, xbin.RunRequest{Cmd: "go test ./..."})
func SandboxAPI() *Sandboxes { return &Sandboxes{c: Client()} }

// Sandbox is one sandbox of this tile, by name. It makes no call.
func (s *Sandboxes) Sandbox(name string) *Sandbox { return &Sandbox{s: s, name: name} }

// Sandbox is one of this tile's sandboxes: its commands (sandbox_exec.go),
// files (sandbox_files.go), terminals and forwarding (sandbox_tty.go) and
// snapshots.
type Sandbox struct {
	s    *Sandboxes
	name string
}

// Name is the sandbox's name.
func (b *Sandbox) Name() string { return b.name }

// --- errors ------------------------------------------------------------------

// SandboxError is a refusal from the runtime: the contract's error body,
// {error, refusal, state?, etag?, retryAfterMs?}, with the HTTP status.
// Refusal is empty when the answer wasn't one of the runtime's refusals (an
// xbind without these routes, a gateway error); Message then carries what
// the body said.
type SandboxError struct {
	Status     int
	Refusal    string // invalid | not-allowed | not-found | state | exists | lost | precondition | too-large | limit | unsupported | unavailable
	Message    string
	State      string        // refusal state: where the sandbox stands
	ETag       string        // refusal precondition: the file's current etag
	RetryAfter time.Duration // refusal unavailable: when to try again
}

// Refusals errors.Is matches on a *SandboxError.
var (
	ErrSandboxNotFound = errors.New("sandbox: not found")          // refusal not-found: no such sandbox, exec, snapshot or path
	ErrSandboxLost     = errors.New("sandbox: exec lost")          // refusal lost: the exec is gone (another xbind run, or its sandbox stopped)
	ErrSandboxState    = errors.New("sandbox: in the wrong state") // refusal state: SandboxError.State says which
)

func (e *SandboxError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sandboxes: %d", e.Status)
	if e.Refusal != "" {
		b.WriteString(" " + e.Refusal)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.State != "" {
		b.WriteString(" (state " + e.State + ")")
	}
	return b.String()
}

// Is matches ErrSandboxNotFound, ErrSandboxLost and ErrSandboxState by the
// refusal.
func (e *SandboxError) Is(target error) bool {
	switch target {
	case ErrSandboxNotFound:
		return e.Refusal == "not-found"
	case ErrSandboxLost:
		return e.Refusal == "lost"
	case ErrSandboxState:
		return e.Refusal == "state"
	}
	return false
}

// sandboxError reads a refused answer (and closes its body).
func sandboxError(resp *http.Response) *SandboxError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error        string `json:"error"`
		Detail       string `json:"detail"`
		Refusal      string `json:"refusal"`
		State        string `json:"state"`
		ETag         string `json:"etag"`
		RetryAfterMs int64  `json:"retryAfterMs"`
	}
	e := &SandboxError{Status: resp.StatusCode}
	if json.Unmarshal(raw, &body) == nil {
		e.Refusal, e.Message, e.State = body.Refusal, body.Error, body.State
		e.ETag = strings.Trim(body.ETag, `"`)
		e.RetryAfter = time.Duration(body.RetryAfterMs) * time.Millisecond
		if e.Message == "" {
			e.Message = body.Detail
		}
	} else {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		e.Message = msg
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	if e.RetryAfter == 0 {
		if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 {
			e.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return e
}

// invalidf is the SDK's own refusal of a call it won't send.
func invalidf(format string, a ...any) *SandboxError {
	return &SandboxError{Status: http.StatusBadRequest, Refusal: "invalid", Message: fmt.Sprintf(format, a...)}
}

// --- plumbing ----------------------------------------------------------------

// segment escapes one path segment of a route: a sandbox name. One that
// would change the route (empty, ".", "..", or with a "/") is refused
// before anything is sent.
func segment(what, s string) (string, error) {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "/") {
		return "", invalidf("%s %s isn't one", what, quoteID(s))
	}
	return url.PathEscape(s), nil
}

// The runtime's id grammars (docs/protocol.md §Tile sandboxes). An id that
// doesn't fit names nothing there, and it holds no character a path would
// read (no "/", ".", "%", "?" or "#"), so a typed call or a SandboxRoute
// refuses it (400 invalid) before anything is sent: a consumer's id in a
// manager's route can only fail its grammar, never reach another route.
var (
	execIDRE     = regexp.MustCompile(`^[0-9a-f]{6}-[0-9]{1,12}$`)
	snapshotIDRE = regexp.MustCompile(`^s-[0-9]{1,12}$`)
)

// IsExecID reports whether id fits the runtime's exec id grammar,
// ^[0-9a-f]{6}-[0-9]{1,12}$ (like "ab12cd-7"). A manager that uses the
// runtime's exec ids as its own answers one that doesn't fit with the
// contract's not-found: it names no exec.
func IsExecID(id string) bool { return execIDRE.MatchString(id) }

// IsSnapshotID reports whether id fits the runtime's snapshot id grammar,
// ^s-[0-9]{1,12}$ (like "s-3").
func IsSnapshotID(id string) bool { return snapshotIDRE.MatchString(id) }

// execIDSeg is id as a route segment (the grammar leaves nothing to escape).
func execIDSeg(id string) (string, error) {
	if !IsExecID(id) {
		return "", invalidf("exec id %s isn't one (the runtime's are like ab12cd-7)", quoteID(id))
	}
	return id, nil
}

// snapshotIDSeg is id as a route segment.
func snapshotIDSeg(id string) (string, error) {
	if !IsSnapshotID(id) {
		return "", invalidf("snapshot id %s isn't one (the runtime's are like s-3)", quoteID(id))
	}
	return id, nil
}

// quoteID quotes an id for a refusal, cut short: it may be a consumer's.
func quoteID(s string) string {
	if len(s) > 64 {
		return strconv.Quote(s[:64]) + "…"
	}
	return strconv.Quote(s)
}

// route is the path below sandboxesURL of this sandbox's sub-route; sub
// is already escaped.
func (b *Sandbox) route(sub string) (string, error) {
	switch b.name {
	case "runtime", "policy", "copy": // the fixed route segments
		return "", invalidf("%q is reserved, never a sandbox's name", b.name)
	}
	n, err := segment("sandbox name", b.name)
	if err != nil {
		return "", err
	}
	if sub == "" {
		return "/" + n, nil
	}
	return "/" + n + "/" + sub, nil
}

// execRoute is the route of one of this sandbox's execs, plus tail.
func (b *Sandbox) execRoute(id, tail string) (string, error) {
	rt := execSub(id, tail)
	if rt.err != nil {
		return "", rt.err
	}
	return b.route(rt.sub)
}

// open sends one call and hands back a 2xx answer, whose body the caller
// closes. body is nil, an io.Reader (sent as is, streamed) or a value sent
// as JSON. Anything but 2xx is a *SandboxError.
func (s *Sandboxes) open(ctx context.Context, method, path string, q url.Values, body any, ctype string) (*http.Response, error) {
	u := sandboxesURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case io.Reader:
		rd = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		rd, ctype = bytes.NewReader(b), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := s.c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, sandboxError(resp)
	}
	return resp, nil
}

// call sends one call and decodes a JSON answer into out (nil: the body is
// dropped).
func (s *Sandboxes) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := s.open(ctx, method, path, q, body, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	return decodeAnswer(resp, out)
}

// decodeAnswer decodes a 2xx JSON answer into out.
func decodeAnswer(resp *http.Response, out any) error {
	if err := json.NewDecoder(io.LimitReader(resp.Body, sandboxAnswerMax)).Decode(out); err != nil {
		return fmt.Errorf("sandboxes: %s %s: the answer: %w", resp.Request.Method, resp.Request.URL.Path, err)
	}
	return nil
}

// waitQuery is ?wait=<seconds>, rounded up (none for 0).
func waitQuery(wait time.Duration) url.Values {
	if wait <= 0 {
		return nil
	}
	return url.Values{"wait": {strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10)}}
}

// --- the runtime -------------------------------------------------------------

// SandboxRuntime is what this tile may use now (GET /sandboxes/runtime).
type SandboxRuntime struct {
	Enabled     bool            `json:"enabled"`
	Isolation   bool            `json:"isolation"` // false: xbind runs without --isolate, and no sandbox runs
	Modes       []SandboxMode   `json:"modes"`
	Unavailable []SandboxMode   `json:"unavailable"`
	Users       string          `json:"users"` // "any", or "root" on a namespace host mapping one uid
	Egress      []SandboxEgress `json:"egress"`
	Caps        []string        `json:"caps"` // the contract capabilities served: exec, files, tar, tty, snapshots, clone
	Limits      SandboxLimits   `json:"limits"`
	Used        SandboxUsage    `json:"used"`
}

// SandboxMode is a mode a sandbox can run in ("namespace" | "vm"), with its
// accelerator, or why it's unavailable.
type SandboxMode struct {
	Mode   string `json:"mode"`
	Accel  string `json:"accel,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// SandboxEgress is one egress a sandbox may be given: "none", or one of
// this tile's sandbox-net slots ("class:<slot>") with what it reaches.
type SandboxEgress struct {
	Class string   `json:"class"`
	Slot  string   `json:"slot,omitempty"`
	Ref   string   `json:"ref,omitempty"` // what the slot is bound to ("" = unbound)
	Reach string   `json:"reach"`         // none | internet | open
	Rules []string `json:"rules,omitempty"`
	Note  string   `json:"note,omitempty"`
}

// SandboxLimits are the runtime's limits for this tile.
type SandboxLimits struct {
	Sandboxes       int               `json:"sandboxes"`
	Running         int               `json:"running"`
	MemMiB          int               `json:"memMiB"`
	VCPUs           int               `json:"vcpus"`
	DiskGiB         int               `json:"diskGiB"`
	PerSandbox      SandboxSizeLimits `json:"perSandbox"`
	IdleStopMin     int               `json:"idleStopMin"`
	RunTimeoutMaxMs int64             `json:"runTimeoutMaxMs"`
	RunOutputMax    int64             `json:"runOutputMax"`
	ExecsRunning    int               `json:"execsRunning"`
	OutputRing      int64             `json:"outputRing"`
	StdinMax        int64             `json:"stdinMax"`
	FileMax         int64             `json:"fileMax"`
	TarMax          int64             `json:"tarMax"`
	WaitMaxSec      int               `json:"waitMaxSec"`
}

// SandboxSizeLimits are one sandbox's default sizes and caps.
type SandboxSizeLimits struct {
	MemMiB     int `json:"memMiB"`
	VCPUs      int `json:"vcpus"`
	DiskGiB    int `json:"diskGiB"`
	MaxMemMiB  int `json:"maxMemMiB"`
	MaxVCPUs   int `json:"maxVCPUs"`
	MaxDiskGiB int `json:"maxDiskGiB"`
	Pids       int `json:"pids,omitempty"`
}

// SandboxUsage is what this tile's sandboxes use now.
type SandboxUsage struct {
	Sandboxes int   `json:"sandboxes"`
	Running   int   `json:"running"`
	MemMiB    int   `json:"memMiB"`
	VCPUs     int   `json:"vcpus"`
	DiskBytes int64 `json:"diskBytes"`
}

// Runtime is what this tile may use now: modes, egress classes, limits.
func (s *Sandboxes) Runtime(ctx context.Context) (*SandboxRuntime, error) {
	var rt SandboxRuntime
	if err := s.call(ctx, http.MethodGet, "/runtime", nil, nil, &rt); err != nil {
		return nil, err
	}
	return &rt, nil
}

// --- definitions -------------------------------------------------------------

// SandboxMount is a filesystem a sandbox gets at At: a filesystem resource
// this tile holds (Res, optionally a clean relative sub-path Path of it),
// or the tile's own code (Source, always read-only).
type SandboxMount struct {
	Res    string `json:"res,omitempty"`
	Path   string `json:"path,omitempty"`
	Source bool   `json:"source,omitempty"`
	At     string `json:"at"`
	RO     bool   `json:"ro,omitempty"`
}

// SandboxNet is a sandbox's egress: "none" (the default) or
// "class:<sandbox-net slot>".
type SandboxNet struct {
	Egress string `json:"egress,omitempty"`
}

// SandboxDefaults are what every command in the sandbox gets unless it
// says otherwise. Env keys XBIN_* are refused.
type SandboxDefaults struct {
	Cwd   string            `json:"cwd,omitempty"`
	UID   *int              `json:"uid,omitempty"`
	GID   *int              `json:"gid,omitempty"`
	Shell string            `json:"shell,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
}

// SandboxFrom names a clone's source: a sandbox of this tile, at a
// snapshot.
type SandboxFrom struct {
	Sandbox  string `json:"sandbox"`
	Snapshot string `json:"snapshot,omitempty"`
}

// SandboxSpec defines a sandbox (POST /sandboxes). Mode is required and
// never chosen for you. Sizes of 0 are the policy's defaults; a size over a
// cap is clamped, and the answer says what applied.
type SandboxSpec struct {
	Name        string            `json:"name"`
	Mode        string            `json:"mode"` // "namespace" | "vm"
	MemMiB      int               `json:"memMiB,omitempty"`
	VCPUs       int               `json:"vcpus,omitempty"`
	DiskGiB     int               `json:"diskGiB,omitempty"`
	Net         *SandboxNet       `json:"net,omitempty"`
	Mounts      []SandboxMount    `json:"mounts,omitempty"`
	Defaults    *SandboxDefaults  `json:"defaults,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`  // opaque, ≤ 1 KiB in all
	For         string            `json:"for,omitempty"`     // a claim: the consumer tile
	ForUser     string            `json:"forUser,omitempty"` // a claim: the person
	IdleStopMin int               `json:"idleStopMin,omitempty"`
	AutoStart   *bool             `json:"autoStart,omitempty"` // nil = true
	ClientID    string            `json:"clientId,omitempty"`  // makes the create repeat-safe
	Start       bool              `json:"start,omitempty"`
	From        *SandboxFrom      `json:"from,omitempty"` // a clone (the clone capability)
}

// SandboxPatch changes the fields it sets (Defaults and Labels as a whole).
// Version, when set, refuses a lost update (412 precondition).
type SandboxPatch struct {
	MemMiB      *int               `json:"memMiB,omitempty"`
	VCPUs       *int               `json:"vcpus,omitempty"`
	DiskGiB     *int               `json:"diskGiB,omitempty"` // a VM's disk only grows
	Net         *SandboxNet        `json:"net,omitempty"`
	Mounts      *[]SandboxMount    `json:"mounts,omitempty"`
	Defaults    *SandboxDefaults   `json:"defaults,omitempty"`
	Labels      *map[string]string `json:"labels,omitempty"`
	For         *string            `json:"for,omitempty"`
	ForUser     *string            `json:"forUser,omitempty"`
	IdleStopMin *int               `json:"idleStopMin,omitempty"`
	AutoStart   *bool              `json:"autoStart,omitempty"`
	Version     int64              `json:"version,omitempty"`
}

// SandboxInfo is a sandbox as the runtime has it.
type SandboxInfo struct {
	Name          string            `json:"name"`
	State         string            `json:"state"` // stopped | starting | running | stopping | error
	StateDetail   string            `json:"stateDetail"`
	Mode          string            `json:"mode"`
	Accel         string            `json:"accel,omitempty"`
	MemMiB        int               `json:"memMiB"`
	VCPUs         int               `json:"vcpus"`
	DiskGiB       int               `json:"diskGiB"`
	Net           SandboxNetInfo    `json:"net"`
	Mounts        []SandboxMount    `json:"mounts"`
	Defaults      SandboxDefaults   `json:"defaults"`
	Labels        map[string]string `json:"labels"`
	For           string            `json:"for,omitempty"`
	ForUser       string            `json:"forUser,omitempty"`
	IdleStopMin   int               `json:"idleStopMin"`
	AutoStart     bool              `json:"autoStart"`
	Base          SandboxBase       `json:"base"`
	Users         string            `json:"users"`
	DiskBytes     int64             `json:"diskBytes"`
	Snapshots     int               `json:"snapshots"`
	ExecsRunning  int               `json:"execsRunning"`
	Created       int64             `json:"created"` // unix ms
	Started       int64             `json:"started,omitempty"`
	LastActive    int64             `json:"lastActive,omitempty"`
	Version       int64             `json:"version"`
	ClientID      string            `json:"clientId,omitempty"`
	RestartNeeded bool              `json:"restartNeeded"`
}

// SandboxNetInfo is a sandbox's egress and what it reaches (none |
// internet | open); EgressNext is an egress a PATCH set that waits for the
// next start.
type SandboxNetInfo struct {
	Egress     string `json:"egress"`
	Reach      string `json:"reach"`
	EgressNext string `json:"egressNext,omitempty"`
	Note       string `json:"note,omitempty"`
}

// SandboxBase is the base image version a sandbox's state is pinned to.
type SandboxBase struct {
	Version  string `json:"version"`
	Outdated bool   `json:"outdated"`
}

// List is this tile's sandboxes.
func (s *Sandboxes) List(ctx context.Context) ([]SandboxInfo, error) {
	var out struct {
		Sandboxes []SandboxInfo `json:"sandboxes"`
	}
	if err := s.call(ctx, http.MethodGet, "", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Sandboxes, nil
}

// Create defines a sandbox (and starts it with spec.Start). A repeat of
// the same spec with the same ClientID answers the existing sandbox.
func (s *Sandboxes) Create(ctx context.Context, spec SandboxSpec) (*SandboxInfo, error) {
	var in SandboxInfo
	if err := s.call(ctx, http.MethodPost, "", nil, spec, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// Get is one sandbox.
func (s *Sandboxes) Get(ctx context.Context, name string) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodGet, "", nil, nil)
}

// Patch changes a sandbox's definition; RestartNeeded in the answer says a
// change waits for the next start.
func (s *Sandboxes) Patch(ctx context.Context, name string, p SandboxPatch) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodPatch, "", nil, p)
}

// Delete stops a sandbox, removes its state and forgets it.
func (s *Sandboxes) Delete(ctx context.Context, name string) error {
	path, err := s.Sandbox(name).route("")
	if err != nil {
		return err
	}
	return s.call(ctx, http.MethodDelete, path, nil, nil, nil)
}

// Start starts a sandbox; wait > 0 returns once it runs (or the wait, at
// most limits.waitMaxSec, runs out) — the answer says where it stands.
func (s *Sandboxes) Start(ctx context.Context, name string, wait time.Duration) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodPost, "start", waitQuery(wait), nil)
}

// Stop syncs and stops a sandbox; its state is kept and its running execs
// end killed.
func (s *Sandboxes) Stop(ctx context.Context, name string, wait time.Duration) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodPost, "stop", waitQuery(wait), nil)
}

// Reset stops a sandbox, wipes its state and pins the current base image.
func (s *Sandboxes) Reset(ctx context.Context, name string, wait time.Duration) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodPost, "reset", waitQuery(wait), nil)
}

// Rebase stops a sandbox and pins the current base image under its kept
// state (which may break what a package manager installed).
func (s *Sandboxes) Rebase(ctx context.Context, name string, wait time.Duration) (*SandboxInfo, error) {
	return s.Sandbox(name).info(ctx, http.MethodPost, "rebase", waitQuery(wait), nil)
}

// SandboxPath is a path inside one of this tile's sandboxes.
type SandboxPath struct {
	Sandbox string `json:"sandbox"`
	Path    string `json:"path"`
}

// Copy copies a file or tree from one of this tile's sandboxes to another
// (a tar read spliced into a tar write, inside xbind).
func (s *Sandboxes) Copy(ctx context.Context, from, to SandboxPath, overwrite bool) error {
	body := struct {
		From      SandboxPath `json:"from"`
		To        SandboxPath `json:"to"`
		Overwrite bool        `json:"overwrite,omitempty"`
	}{from, to, overwrite}
	return s.call(ctx, http.MethodPost, "/copy", nil, body, nil)
}

// info sends a call on this sandbox that answers a SandboxInfo.
func (b *Sandbox) info(ctx context.Context, method, sub string, q url.Values, body any) (*SandboxInfo, error) {
	path, err := b.route(sub)
	if err != nil {
		return nil, err
	}
	var in SandboxInfo
	if err := b.s.call(ctx, method, path, q, body, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// --- snapshots ---------------------------------------------------------------

// Snapshot is a sandbox's saved state.
type Snapshot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"` // unix ms
	Bytes   int64  `json:"bytes,omitempty"`
}

// Snapshots lists the sandbox's snapshots (the snapshots capability).
func (b *Sandbox) Snapshots(ctx context.Context) ([]Snapshot, error) {
	path, err := b.route("snapshots")
	if err != nil {
		return nil, err
	}
	var out struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	if err := b.s.call(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Snapshots, nil
}

// Snapshot saves the sandbox's state as name (it stops briefly and starts
// again if it ran). A repeat with the same clientID answers the same one.
func (b *Sandbox) Snapshot(ctx context.Context, name, clientID string) (*Snapshot, error) {
	path, err := b.route("snapshots")
	if err != nil {
		return nil, err
	}
	body := struct {
		Name     string `json:"name,omitempty"`
		ClientID string `json:"clientId,omitempty"`
	}{name, clientID}
	var sn Snapshot
	if err := b.s.call(ctx, http.MethodPost, path, nil, body, &sn); err != nil {
		return nil, err
	}
	return &sn, nil
}

// RestoreSnapshot puts the sandbox's state back to snapshot id; its execs
// are killed.
func (b *Sandbox) RestoreSnapshot(ctx context.Context, id string) (*SandboxInfo, error) {
	sid, err := snapshotIDSeg(id)
	if err != nil {
		return nil, err
	}
	return b.info(ctx, http.MethodPost, "snapshots/"+sid+"/restore", nil, nil)
}

// DeleteSnapshot removes snapshot id.
func (b *Sandbox) DeleteSnapshot(ctx context.Context, id string) error {
	sid, err := snapshotIDSeg(id)
	if err != nil {
		return err
	}
	path, err := b.route("snapshots/" + sid)
	if err != nil {
		return err
	}
	return b.s.call(ctx, http.MethodDelete, path, nil, nil, nil)
}
