package main

// fsb.go — a sandbox manager for tests and the UI harness: the
// sandbox-manager contract, protocol 1 (docs/sandbox-manager.md), with every
// sandbox a directory on the host and every command a host process.
//
// TEST ONLY: nothing here isolates anything. A "sandbox" is a directory whose
// absolute host path is also its path "inside" (workdir and home are
// subdirectories), so commands and file operations agree without a mount
// namespace. It never ships: hack/fakesandbox runs it for the UI harness, and
// the agent template's tests carry a byte-identical copy
// (_backend/fsb_fake_test.go; mirror_test.go keeps them equal) — every
// identifier here starts with fsb so it can live in their package main.
//
// Terminals (`tty`) are host pseudo-terminals opened with the standard
// library (Linux: /dev/ptmx); their WebSocket is the SDK's sdk/ws, speaking
// the /ws/term framing. Where the host has none, hello leaves `tty` out.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	fsbws "github.com/xbin-dev/xbin/sdk/ws"
)

// fsbManager serves the contract under /sbx/.
type fsbManager struct {
	Root        string        // sandboxes live in <Root>/<id>/, snapshots in <Root>/.snaps/
	DefaultFrom string        // the consumer when X-XBin-From is missing (a test calling directly)
	Caps        []string      // the capabilities hello offers (nil = all; tty where the host has terminals)
	Grace       time.Duration // TERM → KILL on a timeout (0 = 5 s)
	Ring        int           // an exec's output ring (0 = 1 MiB); it keeps between Ring and 2×Ring bytes
	FileMax     int64         // limits.fileMax (0 = 64 MiB)

	mu     sync.Mutex
	boxes  map[string]*fsbBox
	seq    int
	idem   map[string]fsbIdem
	calls  []fsbCall
	fault  map[string]fsbFault
	gate   chan struct{} // non-nil: exec starts wait for it to close
	f412   int           // the next N conditional writes fail
	closed bool          // Close ran: nothing new starts
	wmu    sync.Mutex    // serialises content writes (a conditional write's check and its rename)
	once   sync.Once
	mux    *http.ServeMux
}

// fsbCall is one request the manager saw (tests assert on them).
type fsbCall struct {
	Method, Path, Query, From, User, SbxUser, Body string
}

type fsbFault struct {
	status          int
	refusal, errMsg string
}

type fsbIdem struct {
	id, hash string
}

type fsbRef struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type fsbSize struct {
	ID      string `json:"id"`
	MemMiB  int    `json:"memMiB"`
	VCPUs   int    `json:"vcpus"`
	DiskGiB int    `json:"diskGiB"`
}

type fsbOwner struct {
	User     string `json:"user"`
	Via      string `json:"via"`
	Asserted bool   `json:"asserted"`
}

// fsbUsers is a share's people: "*" (everyone the consumer serves) or a list.
type fsbUsers struct {
	All  bool
	List []string
}

func (u fsbUsers) MarshalJSON() ([]byte, error) {
	if u.All {
		return []byte(`"*"`), nil
	}
	if u.List == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(u.List)
}

func (u *fsbUsers) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if s != "*" {
			return errors.New(`users is "*" or a list`)
		}
		*u = fsbUsers{All: true}
		return nil
	}
	*u = fsbUsers{}
	return json.Unmarshal(b, &u.List)
}

func (u fsbUsers) has(user string) bool {
	if u.All {
		return true
	}
	for _, x := range u.List {
		if x == user {
			return true
		}
	}
	return false
}

type fsbShare struct {
	Consumer string   `json:"consumer"`
	Users    fsbUsers `json:"users"`
}

// fsbSandbox is the contract's sandbox resource.
type fsbSandbox struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	State        string            `json:"state"`
	StateDetail  string            `json:"stateDetail"`
	Image        fsbRef            `json:"image"`
	Size         fsbSize           `json:"size"`
	Isolation    string            `json:"isolation"`
	Egress       string            `json:"egress"`
	EgressNext   string            `json:"egressNext,omitempty"` // a PATCHed egress that applies at the next start
	EgressDetail string            `json:"egressDetail"`
	Owner        fsbOwner          `json:"owner"`
	Visibility   string            `json:"visibility"`
	Members      []string          `json:"members"`
	Shares       []fsbShare        `json:"shares"`
	Shared       bool              `json:"shared"`
	Labels       map[string]string `json:"labels"`
	Workdir      string            `json:"workdir"`
	Home         string            `json:"home"`
	User         string            `json:"user"`
	Shell        string            `json:"shell"`
	Caps         []string          `json:"caps"`
	Created      int64             `json:"created"`
	LastActive   int64             `json:"lastActive"`
	AutoStopMin  int               `json:"autoStopMin"`
	Version      int               `json:"version"`
}

type fsbBox struct {
	fsbSandbox
	dir   string // the sandbox's root: symlink-free, so "inside" paths and host paths agree
	execs map[string]*fsbExec
	eseq  int
	snaps map[string]*fsbSnap
	sseq  int
}

type fsbSnap struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"`
	Bytes   int64  `json:"bytes"`
	dir     string
	seq     int
}

// fsbExec is a background command.
type fsbExec struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Cmd      string   `json:"cmd"`
	Argv     []string `json:"argv"`
	Cwd      string   `json:"cwd"`
	TTY      bool     `json:"tty"`
	State    string   `json:"state"`
	ExitCode *int     `json:"exitCode"`
	Signal   string   `json:"signal"`
	Started  int64    `json:"started"`
	Ended    int64    `json:"ended"`
	Total    int64    `json:"total"`
	ClientID string   `json:"clientId,omitempty"`

	ring    *fsbRing
	cmd     *exec.Cmd
	pid     int // the process group, once started (m.mu); 0 before
	seq     int
	stdin   io.WriteCloser
	eof     bool          // stdin was closed (m.mu)
	done    chan struct{} // closed once the exec has ended (or never started)
	killed  bool
	pty     *os.File      // a tty exec's terminal (master), once started (m.mu)
	ptyDone chan struct{} // closed when its output has all reached the ring
}

var fsbImages = []map[string]any{{"id": "base", "title": "the host's tools (a test fixture)", "default": true, "tools": []string{"git"}}}
var fsbSizes = []fsbSize{{ID: "small", MemMiB: 2048, VCPUs: 2, DiskGiB: 20}}

const (
	fsbFileMax   = 64 << 20
	fsbStdinMax  = 1 << 20
	fsbRunOutMax = 1 << 20
	fsbRunMaxMs  = 600000
	fsbWaitMax   = 120
)

func fsbNow() int64 { return time.Now().UnixMilli() }

// --- test hooks --------------------------------------------------------------

// FailNext makes the next request of op (hello, list, create, get, patch,
// delete, action, run, exec, execs, exec-get, exec-delete, output, stdin,
// signal, resize, tty, stat, read, write, list-dir, mkdir, remove, move,
// tar-get, tar-put, snapshot, snapshots, restore, snapshot-delete) answer
// this refusal.
func (m *fsbManager) FailNext(op string, status int, refusal, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fault == nil {
		m.fault = map[string]fsbFault{}
	}
	m.fault[op] = fsbFault{status, refusal, msg}
}

// GateExecs holds every exec start until the returned release is called.
func (m *fsbManager) GateExecs() (release func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := make(chan struct{})
	m.gate = g
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.gate == g {
				m.gate = nil
			}
			m.mu.Unlock()
			close(g)
		})
	}
}

// Fail412 makes the next n conditional writes fail as if the file changed.
func (m *fsbManager) Fail412(n int) {
	m.mu.Lock()
	m.f412 = n
	m.mu.Unlock()
}

// Calls returns the requests seen so far.
func (m *fsbManager) Calls() []fsbCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fsbCall(nil), m.calls...)
}

// Box returns a sandbox's current resource (tests).
func (m *fsbManager) Box(id string) (fsbSandbox, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.boxes[id]
	if b == nil {
		return fsbSandbox{}, false
	}
	return b.fsbSandbox, true
}

// Close kills every running command and waits (a few seconds at most) for
// them to end; nothing starts after it (tests' cleanup).
func (m *fsbManager) Close() {
	m.mu.Lock()
	m.closed = true
	var all []*fsbExec
	for _, b := range m.boxes {
		all = append(all, b.stopExecs()...)
	}
	m.mu.Unlock()
	fsbAwait(all, 5*time.Second)
}

// --- plumbing ----------------------------------------------------------------

type fsbCaller struct {
	from, user string
	verified   bool
}

func (m *fsbManager) caller(r *http.Request) fsbCaller {
	from := r.Header.Get("X-XBin-From")
	if from == "" {
		from = m.DefaultFrom
	}
	if u := r.Header.Get("X-XBin-User"); u != "" {
		return fsbCaller{from: from, user: u, verified: true}
	}
	return fsbCaller{from: from, user: r.Header.Get("Sbx-User")}
}

type fsbError struct {
	Error        string `json:"error"`
	Refusal      string `json:"refusal"`
	State        string `json:"state,omitempty"`
	ETag         string `json:"etag,omitempty"`
	RetryAfterMs int    `json:"retryAfterMs,omitempty"`
	Protocols    []int  `json:"protocols,omitempty"`
}

func fsbFail(w http.ResponseWriter, status int, refusal, msg string) {
	fsbJSON(w, status, fsbError{Error: msg, Refusal: refusal})
}

func fsbJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fsbDecode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // room for a run's stdin (limits.stdinMax) as JSON
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// faulted answers a fault injected for op, if any.
func (m *fsbManager) faulted(w http.ResponseWriter, op string) bool {
	m.mu.Lock()
	f, ok := m.fault[op]
	if ok {
		delete(m.fault, op)
	}
	m.mu.Unlock()
	if ok {
		fsbFail(w, f.status, f.refusal, f.errMsg)
	}
	return ok
}

func (m *fsbManager) caps() []string {
	if m.Caps != nil {
		return m.Caps
	}
	if fsbHasPTY() {
		return []string{"exec", "files", "tar", "tty", "snapshots", "clone", "archive", "ports"}
	}
	return []string{"exec", "files", "tar", "snapshots", "clone", "archive", "ports"}
}

func (m *fsbManager) hasCap(c string) bool {
	for _, x := range m.caps() {
		if x == c {
			return true
		}
	}
	return false
}

func (m *fsbManager) grace() time.Duration {
	if m.Grace > 0 {
		return m.Grace
	}
	return 5 * time.Second
}

func (m *fsbManager) ringSize() int {
	if m.Ring > 0 {
		return m.Ring
	}
	return 1 << 20
}

func (m *fsbManager) fileMax() int64 {
	if m.FileMax > 0 {
		return m.FileMax
	}
	return fsbFileMax
}

// ServeHTTP records the call and routes it.
func (m *fsbManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.once.Do(m.routes)
	body := ""
	if r.Body != nil && r.Method != http.MethodPut && r.ContentLength > 0 && r.ContentLength < 64<<10 {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		r.Body = io.NopCloser(strings.NewReader(body))
	}
	m.mu.Lock()
	m.calls = append(m.calls, fsbCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		From: r.Header.Get("X-XBin-From"), User: r.Header.Get("X-XBin-User"), SbxUser: r.Header.Get("Sbx-User"), Body: body})
	m.mu.Unlock()
	m.mux.ServeHTTP(w, r)
}

func (m *fsbManager) routes() {
	m.mu.Lock()
	if m.boxes == nil {
		m.boxes = map[string]*fsbBox{}
	}
	if m.idem == nil {
		m.idem = map[string]fsbIdem{}
	}
	m.mu.Unlock()
	x := http.NewServeMux()
	x.HandleFunc("GET /sbx/hello", m.hello)
	x.HandleFunc("GET /sbx/sandboxes", m.list)
	x.HandleFunc("POST /sbx/sandboxes", m.create)
	x.HandleFunc("GET /sbx/sandboxes/{id}", m.get)
	x.HandleFunc("PATCH /sbx/sandboxes/{id}", m.patch)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}", m.del)
	x.HandleFunc("POST /sbx/sandboxes/{id}/{action}", m.action)
	x.HandleFunc("POST /sbx/sandboxes/{id}/run", m.run)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs", m.execList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs", m.execStart)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}", m.execGet)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}/execs/{eid}", m.execDelete)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/output", m.execOutput)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/stdin", m.execStdin)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/signal", m.execSignal)
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/resize", m.execResize)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/tty", m.ttyAttach)
	x.HandleFunc("GET /sbx/sandboxes/{id}/tty", m.ttyStart)
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/stat", m.fileStat)
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/content", m.fileRead)
	x.HandleFunc("PUT /sbx/sandboxes/{id}/files/content", m.fileWrite)
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/list", m.fileList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/mkdir", m.fileMkdir)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/remove", m.fileRemove)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/move", m.fileMove)
	x.HandleFunc("GET /sbx/sandboxes/{id}/tar", m.tarGet)
	x.HandleFunc("PUT /sbx/sandboxes/{id}/tar", m.tarPut)
	x.HandleFunc("GET /sbx/sandboxes/{id}/snapshots", m.snapList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/snapshots", m.snapCreate)
	x.HandleFunc("POST /sbx/sandboxes/{id}/snapshots/{sid}/restore", m.snapRestore)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}/snapshots/{sid}", m.snapDelete)
	x.HandleFunc("/sbx/sandboxes/{id}/ports/{port}/{path...}", m.port)
	x.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // errors are JSON, even for a route that isn't here
		fsbFail(w, http.StatusNotFound, "not-found", "no route "+r.Method+" "+r.URL.Path)
	})
	m.mux = x
}

// noTTY answers a terminal route of a manager without the tty capability.
func (m *fsbManager) noTTY(w http.ResponseWriter) bool {
	if m.hasCap("tty") {
		return false
	}
	fsbFail(w, http.StatusNotImplemented, "unsupported", "this manager has no terminals (tty)")
	return true
}

// --- access ------------------------------------------------------------------

// visible: the caller's consumer is the sandbox's home or it was shared with it.
func (b *fsbBox) share(consumer string) (fsbShare, bool) {
	for _, s := range b.Shares {
		if s.Consumer == consumer {
			return s, true
		}
	}
	return fsbShare{}, false
}

func (b *fsbBox) visible(c fsbCaller) bool {
	if b.Owner.Via == c.from {
		return true
	}
	_, ok := b.share(c.from)
	return ok
}

// personOK: on a verified call the person must be allowed; a backend call is
// the consumer's to police.
func (b *fsbBox) personOK(c fsbCaller) bool {
	if !c.verified {
		return true
	}
	if b.Owner.Via != c.from {
		if s, _ := b.share(c.from); !s.Users.has(c.user) {
			return false
		}
	}
	if b.Owner.User == c.user || b.Visibility == "team" {
		return true
	}
	for _, x := range b.Members {
		if x == c.user {
			return true
		}
	}
	return false
}

// canAdmin: who may change visibility, members and shares.
func (b *fsbBox) canAdmin(c fsbCaller) bool {
	return b.Owner.Via == c.from && (!c.verified || c.user == b.Owner.User)
}

// view is the resource as c sees it.
func (b *fsbBox) view(c fsbCaller) fsbSandbox {
	v := b.fsbSandbox
	v.Shared = b.Owner.Via != c.from
	if v.Members == nil {
		v.Members = []string{}
	}
	if v.Shares == nil {
		v.Shares = []fsbShare{}
	}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	return v
}

// box finds {id} for the caller, answering the refusal itself. The lock is
// held on return when ok.
func (m *fsbManager) box(w http.ResponseWriter, r *http.Request) (*fsbBox, fsbCaller, bool) {
	c := m.caller(r)
	m.mu.Lock()
	b := m.boxes[r.PathValue("id")]
	if b == nil || !b.visible(c) {
		m.mu.Unlock()
		fsbFail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return nil, c, false
	}
	if !b.personOK(c) {
		m.mu.Unlock()
		fsbFail(w, http.StatusForbidden, "not-allowed", c.user+" may not use this sandbox")
		return nil, c, false
	}
	return b, c, true
}

// usable brings a stopped sandbox up for an exec or a file operation (m.mu
// held); archived and the rest are refused.
func (b *fsbBox) usable() error {
	switch b.State {
	case "running":
	case "stopped":
		b.started()
		b.Version++
	default:
		return fmt.Errorf("the sandbox is %s", b.State)
	}
	b.LastActive = fsbNow()
	return nil
}

// started brings the sandbox up (m.mu held): a pending egress applies now.
func (b *fsbBox) started() {
	b.State = "running"
	if b.EgressNext != "" {
		b.Egress, b.EgressNext = b.EgressNext, ""
	}
}

func fsbStateErr(w http.ResponseWriter, err error, state string) {
	fsbJSON(w, http.StatusConflict, fsbError{Error: err.Error(), Refusal: "state", State: state})
}

// --- hello, sandboxes -----------------------------------------------------------

func (m *fsbManager) hello(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "hello") {
		return
	}
	if p := r.URL.Query().Get("protocol"); p != "" && p != "1" {
		fsbJSON(w, http.StatusBadRequest, fsbError{Error: "protocol " + p + " is not spoken here", Refusal: "protocol", Protocols: []int{1}})
		return
	}
	fsbJSON(w, http.StatusOK, map[string]any{
		"protocol": 1, "protocols": []int{1},
		"manager": map[string]string{"name": "fakesandbox", "title": "Fake sandboxes (test fixture)", "version": "1.0.0"},
		"caps":    m.caps(), "egress": []string{"none", "internet"},
		"images": fsbImages, "sizes": []map[string]any{{"id": "small", "memMiB": 2048, "vcpus": 2, "diskGiB": 20, "default": true}},
		"limits": map[string]int{"sandboxes": 0, "runTimeoutMaxMs": fsbRunMaxMs, "runOutputMax": fsbRunOutMax,
			"execsRunning": 16, "outputRing": m.ringSize(), "stdinMax": fsbStdinMax, "fileMax": int(m.fileMax()),
			"tarMax": 1 << 30, "waitMaxSec": fsbWaitMax},
	})
}

func (m *fsbManager) list(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "list") {
		return
	}
	c := m.caller(r)
	m.mu.Lock()
	out := []fsbSandbox{}
	for _, b := range m.boxes {
		if b.visible(c) && b.personOK(c) {
			out = append(out, b.view(c))
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created < out[j].Created || out[i].Created == out[j].Created && out[i].ID < out[j].ID
	})
	fsbJSON(w, http.StatusOK, map[string]any{"sandboxes": out})
}

type fsbCreateReq struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Size       string            `json:"size"`
	Egress     string            `json:"egress"`
	Visibility string            `json:"visibility"`
	Members    []string          `json:"members"`
	Labels     map[string]string `json:"labels"`
	ClientID   string            `json:"clientId"`
	Start      *bool             `json:"start"`
	From       *struct {
		Sandbox  string `json:"sandbox"`
		Snapshot string `json:"snapshot"`
	} `json:"from"`
}

func fsbHash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

func (m *fsbManager) create(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "create") {
		return
	}
	c := m.caller(r)
	var q fsbCreateReq
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	switch {
	case q.Name == "" || len(q.Name) > 64:
		fsbFail(w, http.StatusBadRequest, "invalid", "name is 1–64 characters")
		return
	case q.Image != "" && q.Image != "base":
		fsbFail(w, http.StatusBadRequest, "invalid", "no image "+q.Image)
		return
	case q.Size != "" && q.Size != "small":
		fsbFail(w, http.StatusBadRequest, "invalid", "no size "+q.Size)
		return
	case q.Egress != "" && q.Egress != "none" && q.Egress != "internet":
		fsbFail(w, http.StatusBadRequest, "invalid", "egress is none or internet here")
		return
	case q.Visibility != "" && q.Visibility != "private" && q.Visibility != "team":
		fsbFail(w, http.StatusBadRequest, "invalid", "visibility is private or team")
		return
	}
	lbytes, _ := json.Marshal(q.Labels)
	if len(lbytes) > 1024 {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "labels are 1 KiB at most")
		return
	}
	if q.From != nil && !m.hasCap("clone") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no clones here")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ikey := c.from + "\x00create\x00" + q.ClientID
	if q.ClientID != "" {
		if prev, ok := m.idem[ikey]; ok {
			if prev.hash != fsbHash(q) {
				fsbFail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different sandbox")
				return
			}
			if b := m.boxes[prev.id]; b != nil {
				fsbJSON(w, http.StatusOK, b.view(c))
				return
			}
		}
	}
	var src string // a tree to start from
	if q.From != nil {
		sb := m.boxes[q.From.Sandbox]
		if sb == nil || !sb.visible(c) || !sb.personOK(c) {
			fsbFail(w, http.StatusNotFound, "not-found", "no such sandbox to clone")
			return
		}
		src = sb.dir
		if q.From.Snapshot != "" {
			sn := sb.snaps[q.From.Snapshot]
			if sn == nil {
				fsbFail(w, http.StatusNotFound, "not-found", "no such snapshot")
				return
			}
			src = sn.dir
		}
	}
	m.seq++
	id := fmt.Sprintf("sb-%d", m.seq)
	root := m.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // a sandbox's paths are its host paths: keep them symlink-free (pwd agrees)
	}
	dir := filepath.Join(root, id)
	_ = os.RemoveAll(dir) // a previous run's leftovers: a new sandbox starts empty
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
		fsbFail(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	_ = os.MkdirAll(filepath.Join(dir, "home"), 0o755)
	if src != "" {
		if err := fsbCopyTree(src, dir); err != nil {
			fsbFail(w, http.StatusServiceUnavailable, "unavailable", "clone: "+err.Error())
			return
		}
	}
	now := fsbNow()
	b := &fsbBox{dir: dir, execs: map[string]*fsbExec{}, snaps: map[string]*fsbSnap{}}
	b.fsbSandbox = fsbSandbox{ID: id, Name: q.Name, State: "running",
		Image: fsbRef{ID: "base", Title: "the host's tools (a test fixture)"}, Size: fsbSizes[0],
		Isolation: "other", Egress: orFsb(q.Egress, "none"),
		Owner:      fsbOwner{User: c.user, Via: c.from, Asserted: !c.verified && c.user != ""},
		Visibility: orFsb(q.Visibility, "private"), Members: q.Members, Labels: q.Labels,
		Workdir: filepath.Join(dir, "work"), Home: filepath.Join(dir, "home"), User: "dev", Shell: "/bin/sh",
		Caps: m.caps(), Created: now, LastActive: now, AutoStopMin: 30, Version: 1}
	if q.Start != nil && !*q.Start {
		b.State = "stopped"
	}
	m.boxes[id] = b
	if q.ClientID != "" {
		m.idem[ikey] = fsbIdem{id: id, hash: fsbHash(q)}
	}
	fsbJSON(w, http.StatusCreated, b.view(c))
}

func orFsb(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m *fsbManager) get(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "get") {
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	v := b.view(c)
	m.mu.Unlock()
	fsbJSON(w, http.StatusOK, v)
}

func (m *fsbManager) patch(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "patch") {
		return
	}
	var q struct {
		Name        *string            `json:"name"`
		Visibility  *string            `json:"visibility"`
		Members     *[]string          `json:"members"`
		Shares      *[]fsbShare        `json:"shares"`
		Labels      *map[string]string `json:"labels"`
		Egress      *string            `json:"egress"`
		Size        *string            `json:"size"`
		AutoStopMin *int               `json:"autoStopMin"`
		Version     *int               `json:"version"`
	}
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	defer m.mu.Unlock()
	if q.Version != nil && *q.Version != b.Version {
		fsbJSON(w, http.StatusPreconditionFailed, fsbError{Error: "the sandbox changed", Refusal: "precondition"})
		return
	}
	if (q.Visibility != nil || q.Members != nil || q.Shares != nil) && !b.canAdmin(c) {
		fsbFail(w, http.StatusForbidden, "not-allowed", "only its home consumer or its owner changes who may use it")
		return
	}
	// Every field is checked before any applies: a refused PATCH changes nothing.
	bad := ""
	switch {
	case q.Name != nil && (strings.TrimSpace(*q.Name) == "" || len(strings.TrimSpace(*q.Name)) > 64):
		bad = "name is 1–64 characters"
	case q.Visibility != nil && *q.Visibility != "private" && *q.Visibility != "team":
		bad = "visibility is private or team"
	case q.Egress != nil && *q.Egress != "none" && *q.Egress != "internet":
		bad = "egress is none or internet here"
	case q.Size != nil && *q.Size != "small":
		bad = "no size " + *q.Size
	case q.AutoStopMin != nil && *q.AutoStopMin < 0:
		bad = "autoStopMin is 0 or more"
	}
	if q.Shares != nil {
		for _, s := range *q.Shares {
			if s.Consumer == "" {
				bad = "a share names its consumer"
			}
		}
	}
	if bad != "" {
		fsbFail(w, http.StatusBadRequest, "invalid", bad)
		return
	}
	if q.Labels != nil {
		if lb, _ := json.Marshal(*q.Labels); len(lb) > 1024 {
			fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "labels are 1 KiB at most")
			return
		}
	}
	if q.Name != nil {
		b.Name = strings.TrimSpace(*q.Name)
	}
	if q.Visibility != nil {
		b.Visibility = *q.Visibility
	}
	if q.Members != nil {
		b.Members = *q.Members
	}
	if q.Shares != nil {
		b.Shares = *q.Shares
	}
	if q.Labels != nil {
		b.Labels = *q.Labels
	}
	if q.Egress != nil {
		// a running sandbox keeps its network until it restarts (egressNext);
		// any other takes the new one at once
		switch {
		case b.State != "running":
			b.Egress, b.EgressNext = *q.Egress, ""
		case *q.Egress == b.Egress:
			b.EgressNext = ""
		default:
			b.EgressNext = *q.Egress
		}
	}
	if q.AutoStopMin != nil {
		b.AutoStopMin = *q.AutoStopMin
	}
	b.Version++
	v := b.view(c)
	fsbJSON(w, http.StatusOK, struct {
		fsbSandbox
		RestartNeeded bool `json:"restartNeeded,omitempty"`
	}{v, b.EgressNext != ""})
}

func (m *fsbManager) del(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "delete") {
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	if b.Owner.Via != c.from {
		m.mu.Unlock()
		fsbFail(w, http.StatusForbidden, "not-allowed", "only its home consumer deletes a sandbox")
		return
	}
	delete(m.boxes, b.ID)
	killed := b.stopExecs()
	m.mu.Unlock()
	fsbAwait(killed, 5*time.Second) // nothing writes into the tree while it goes
	_ = os.RemoveAll(b.dir)
	_ = os.RemoveAll(filepath.Join(m.Root, ".snaps", b.ID))
	w.WriteHeader(http.StatusNoContent)
}

// stopExecs kills a sandbox's running commands and returns those that had
// started (m.mu held; the kill doesn't wait — fsbAwait them after unlocking).
// One still held by GateExecs never starts.
func (b *fsbBox) stopExecs() []*fsbExec {
	var out []*fsbExec
	for _, e := range b.execs {
		if e.State == "running" {
			e.killed = true
			if e.pid > 0 {
				fsbKill(e.pid, syscall.SIGKILL)
				out = append(out, e)
			}
		}
	}
	return out
}

// fsbAwait waits for execs to end, up to d in all.
func fsbAwait(execs []*fsbExec, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for _, e := range execs {
		select {
		case <-e.done:
		case <-t.C:
			return
		}
	}
}

func (m *fsbManager) action(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "action") {
		return
	}
	act := r.PathValue("action")
	var q struct {
		Start bool `json:"start"`
	}
	_ = fsbDecode(r, &q)
	if (act == "archive" || act == "thaw") && !m.hasCap("archive") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no archives here")
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	var killed []*fsbExec
	switch act {
	case "start":
		if b.State != "stopped" && b.State != "running" {
			st := b.State
			m.mu.Unlock()
			fsbStateErr(w, fmt.Errorf("a %s sandbox can't start", st), st)
			return
		}
		if b.State == "stopped" {
			b.started()
		}
	case "stop":
		if b.State != "running" && b.State != "stopped" {
			st := b.State
			m.mu.Unlock()
			fsbStateErr(w, fmt.Errorf("a %s sandbox can't stop", st), st)
			return
		}
		killed = b.stopExecs()
		b.State = "stopped"
	case "archive":
		killed = b.stopExecs()
		b.State = "archived"
	case "thaw":
		if b.State != "archived" {
			st := b.State
			m.mu.Unlock()
			fsbStateErr(w, fmt.Errorf("a %s sandbox isn't archived", st), st)
			return
		}
		b.State = "stopped"
		if q.Start {
			b.started()
		}
	default:
		m.mu.Unlock()
		fsbFail(w, http.StatusNotFound, "not-found", "no action "+act)
		return
	}
	b.Version++
	b.LastActive = fsbNow()
	m.mu.Unlock()
	fsbAwait(killed, 5*time.Second) // its execs have ended when it answers
	m.mu.Lock()
	v := b.view(c)
	m.mu.Unlock()
	fsbJSON(w, http.StatusOK, v)
}

// --- commands ---------------------------------------------------------------------

// path resolves an absolute in-sandbox path for an operation on the entry
// itself (stat, remove, move, mkdir): lexically inside the sandbox, with the
// symlinks of its parent resolving inside it too. b.dir is fixed, so no lock.
func (b *fsbBox) path(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	c := filepath.Clean(p)
	if !fsbWithin(b.dir, c) || !fsbWithin(b.dir, fsbResolve(filepath.Dir(c))) {
		return "", fmt.Errorf("%s is outside the sandbox", p)
	}
	return c, nil
}

// pathFollow is path for an operation that follows a final symlink (read,
// write, list, a cwd, a tree): all of it must resolve inside the sandbox.
func (b *fsbBox) pathFollow(p string) (string, error) {
	c, err := b.path(p)
	if err == nil && !fsbWithin(b.dir, fsbResolve(c)) {
		err = fmt.Errorf("%s is outside the sandbox", p)
	}
	return c, err
}

// fsbWithin: p is dir or below it (both clean).
func fsbWithin(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// fsbResolve is p with the symlinks of its longest existing prefix resolved.
func fsbResolve(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

type fsbCmdReq struct {
	Cmd       string            `json:"cmd"`
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Stdin     json.RawMessage   `json:"stdin"`
	TimeoutMs int               `json:"timeoutMs"`
	MaxOutput int               `json:"maxOutput"`
	Merge     bool              `json:"merge"`
	TTY       bool              `json:"tty"`
	Rows      int               `json:"rows"`
	Cols      int               `json:"cols"`
	Label     string            `json:"label"`
	ClientID  string            `json:"clientId"`
}

// command builds a host process for a sandbox (m.mu held).
func (m *fsbManager) command(b *fsbBox, q fsbCmdReq) (*exec.Cmd, error) {
	if q.Cmd == "" && len(q.Argv) == 0 {
		return nil, errors.New("cmd or argv is required")
	}
	cwd := b.Workdir
	if q.Cwd != "" {
		p, err := b.pathFollow(q.Cwd)
		if err != nil {
			return nil, err
		}
		cwd = p
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("cwd %s isn't a directory in the sandbox", cwd)
	}
	var c *exec.Cmd
	if len(q.Argv) > 0 {
		c = exec.Command(q.Argv[0], q.Argv[1:]...) // exec-ok: a test fixture; the "sandbox" is a host directory
	} else {
		c = exec.Command("sh", "-c", q.Cmd) // exec-ok: a test fixture (see above)
	}
	c.Dir = cwd
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + b.Home, "PWD=" + cwd, "LANG=C.UTF-8",
		"IN_SANDBOX=1", "SANDBOX_ID=" + b.ID, "SANDBOX_NAME=" + b.Name}
	if q.TTY {
		c.Env = append(c.Env, "TERM=xterm-256color")
	}
	keys := make([]string, 0, len(q.Env))
	for k := range q.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Env = append(c.Env, k+"="+q.Env[k])
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if q.TTY { // a session of its own (its group too), the terminal its controlling one (Ctty 0: its stdin)
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	}
	return c, nil
}

// fsbKill signals a started command's process group. Only a group whose
// leader hasn't been waited for (a running exec, a run in flight) or that
// still has members is signalled — a finished one's number may be reused.
func fsbKill(pgid int, sig syscall.Signal) {
	if pgid > 0 { // never 0: kill(0) is our own group
		_ = syscall.Kill(-pgid, sig)
	}
}

// fsbEnd ends a process group the contract's way: TERM, then KILL once grace
// runs out while any of it is left (a member that ignores TERM, or one that
// outlived its leader).
func fsbEnd(pgid int, grace time.Duration) {
	if pgid <= 0 {
		return
	}
	fsbKill(pgid, syscall.SIGTERM)
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		if syscall.Kill(-pgid, 0) != nil {
			return // the group is gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	fsbKill(pgid, syscall.SIGKILL)
}

func fsbSigName(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		if n, ok := fsbSignalNames[ws.Signal()]; ok {
			return -1, n
		}
		return -1, strconv.Itoa(int(ws.Signal()))
	}
	return ps.ExitCode(), ""
}

var fsbSignalNames = map[syscall.Signal]string{syscall.SIGINT: "INT", syscall.SIGTERM: "TERM", syscall.SIGKILL: "KILL",
	syscall.SIGHUP: "HUP", syscall.SIGQUIT: "QUIT", syscall.SIGABRT: "ABRT", syscall.SIGSEGV: "SEGV", syscall.SIGBUS: "BUS",
	syscall.SIGFPE: "FPE", syscall.SIGILL: "ILL", syscall.SIGPIPE: "PIPE", syscall.SIGALRM: "ALRM", syscall.SIGUSR1: "USR1",
	syscall.SIGUSR2: "USR2", syscall.SIGTRAP: "TRAP", syscall.SIGXCPU: "XCPU", syscall.SIGXFSZ: "XFSZ"}

var fsbSignals = map[string]syscall.Signal{"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP}

// fsbHeadTail keeps an output's first quarter and last three quarters of max.
type fsbHeadTail struct {
	mu    sync.Mutex
	max   int
	head  []byte
	tail  []byte
	bytes int64
}

func (h *fsbHeadTail) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bytes += int64(len(p))
	q := h.max / 4
	rest := p
	if room := q - len(h.head); room > 0 {
		n := min(room, len(rest))
		h.head = append(h.head, rest[:n]...)
		rest = rest[n:]
	}
	h.tail = append(h.tail, rest...)
	if keep := h.max - q; len(h.tail) > 2*keep+4096 { // compact now and then, not on every write
		h.tail = append(h.tail[:0:0], h.tail[len(h.tail)-keep:]...)
	}
	return len(p), nil
}

func (h *fsbHeadTail) out() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	tail := h.tail
	if keep := h.max - h.max/4; len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	kept := int64(len(h.head) + len(tail))
	if kept >= h.bytes { // it all fit: one piece
		return map[string]any{"head": fsbText(append(append([]byte{}, h.head...), tail...)), "tail": "", "elided": 0, "bytes": h.bytes}
	}
	return map[string]any{"head": fsbText(h.head), "tail": fsbText(tail), "elided": h.bytes - kept, "bytes": h.bytes}
}

func fsbText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

func (m *fsbManager) run(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "run") {
		return
	}
	var q fsbCmdReq
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	q.TTY = false // a run has no terminal
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	if err := b.usable(); err != nil {
		st := b.State
		m.mu.Unlock()
		fsbStateErr(w, err, st)
		return
	}
	c, err := m.command(b, q)
	m.mu.Unlock()
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	limit := q.MaxOutput
	if limit <= 0 {
		limit = 65536
	}
	limit = min(limit, fsbRunOutMax)
	timeout := q.TimeoutMs
	if timeout <= 0 {
		timeout = 60000
	}
	timeout = min(timeout, fsbRunMaxMs)
	out, errs := &fsbHeadTail{max: limit}, &fsbHeadTail{max: limit}
	c.Stdout, c.Stderr = out, errs
	if q.Merge {
		c.Stderr = out
	}
	var stdin string
	if len(q.Stdin) > 0 {
		if err := json.Unmarshal(q.Stdin, &stdin); err != nil {
			fsbFail(w, http.StatusBadRequest, "invalid", "a run's stdin is a string")
			return
		}
	}
	if len(stdin) > fsbStdinMax {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "stdin is over limits.stdinMax")
		return
	}
	c.Stdin = strings.NewReader(stdin)
	start := time.Now()
	if err := c.Start(); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	pgid := c.Process.Pid
	done := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			timedOut.Store(true)
			fsbEnd(pgid, m.grace()) // on past the answer if a member outlived the leader
		case <-r.Context().Done(): // the caller hung up: the run is its request's
			select {
			case <-done: // the request ended because the run did
			default:
				fsbKill(pgid, syscall.SIGKILL)
			}
		}
	}()
	_ = c.Wait()
	close(done)
	code, sig := fsbSigName(c.ProcessState)
	var exit any = code
	if sig != "" { // a signal ended it: no exit code
		exit = nil
	}
	res := map[string]any{"exitCode": exit, "signal": sig, "timedOut": timedOut.Load(), "ms": time.Since(start).Milliseconds()}
	if q.Merge {
		res["output"] = out.out()
	} else {
		res["stdout"], res["stderr"] = out.out(), errs.out()
	}
	fsbJSON(w, http.StatusOK, res)
}

// fsbRing is an exec's combined output: the newest bytes of a stream, at
// least max of them (up to 2×max between compactions).
type fsbRing struct {
	mu     sync.Mutex
	max    int
	buf    []byte
	total  int64
	closed bool
	ch     chan struct{} // closed (and replaced) on every change
}

func newFsbRing(size int) *fsbRing { return &fsbRing{max: size, ch: make(chan struct{})} }

func (r *fsbRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > 2*r.max {
		r.buf = append(r.buf[:0:0], r.buf[len(r.buf)-r.max:]...)
	}
	r.total += int64(len(p))
	close(r.ch)
	r.ch = make(chan struct{})
	r.mu.Unlock()
	return len(p), nil
}

func (r *fsbRing) close() {
	r.mu.Lock()
	r.closed = true
	close(r.ch)
	r.ch = make(chan struct{})
	r.mu.Unlock()
}

// read returns bytes from since (or the oldest kept), at most max, waiting up
// to wait for some when there are none and the stream is open.
func (r *fsbRing) read(ctx context.Context, since int64, limit int, wait time.Duration) (start, end, total, ringStart int64, data []byte) {
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		ringStart = r.total - int64(len(r.buf))
		if since < r.total || r.closed || time.Now().After(deadline) {
			start = max(since, ringStart)
			if start > r.total {
				start = r.total
			}
			end = min(r.total, start+int64(limit))
			data = append([]byte(nil), r.buf[start-ringStart:end-ringStart]...)
			total = r.total
			r.mu.Unlock()
			return
		}
		ch := r.ch
		r.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
		case <-ctx.Done():
			deadline = time.Now()
		}
	}
}

func (m *fsbManager) execView(e *fsbExec) fsbExec {
	v := *e
	v.Total = e.ring.totalNow()
	if v.Argv == nil {
		v.Argv = []string{}
	}
	return v
}

// ended: the stream is over and since has read all of it.
func (r *fsbRing) ended(since int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed && since >= r.total
}

func (r *fsbRing) totalNow() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

func (m *fsbManager) execStart(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "exec") {
		return
	}
	var q fsbCmdReq
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	e, status := m.launch(w, r, q)
	if e == nil {
		return
	}
	m.mu.Lock()
	v := m.execView(e)
	m.mu.Unlock()
	fsbJSON(w, status, v)
}

// launch starts a background exec of {id} — 201, or 200 for a repeated
// clientId — or answers the refusal itself (nil).
func (m *fsbManager) launch(w http.ResponseWriter, r *http.Request, q fsbCmdReq) (*fsbExec, int) {
	if q.TTY && m.noTTY(w) {
		return nil, 0
	}
	var wantStdin bool
	if len(q.Stdin) > 0 {
		_ = json.Unmarshal(q.Stdin, &wantStdin)
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return nil, 0
	}
	ikey := c.from + "\x00exec\x00" + b.ID + "\x00" + q.ClientID
	if q.ClientID != "" {
		if prev, ok := m.idem[ikey]; ok {
			e := b.execs[prev.id]
			if prev.hash != fsbHash(q) {
				m.mu.Unlock()
				fsbFail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different command")
				return nil, 0
			}
			if e != nil {
				m.mu.Unlock()
				return e, http.StatusOK
			}
		}
	}
	if err := b.usable(); err != nil {
		st := b.State
		m.mu.Unlock()
		fsbStateErr(w, err, st)
		return nil, 0
	}
	cmd, err := m.command(b, q)
	if err != nil {
		m.mu.Unlock()
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return nil, 0
	}
	gate := m.gate
	b.eseq++
	e := &fsbExec{ID: fmt.Sprintf("e%d", b.eseq), seq: b.eseq, Label: q.Label, Cmd: q.Cmd, Argv: q.Argv, Cwd: cmd.Dir, TTY: q.TTY,
		State: "running", Started: fsbNow(), ClientID: q.ClientID, ring: newFsbRing(m.ringSize()), cmd: cmd, done: make(chan struct{})}
	b.execs[e.ID] = e
	if q.ClientID != "" {
		m.idem[ikey] = fsbIdem{id: e.ID, hash: fsbHash(q)}
	}
	cmd.Stdout, cmd.Stderr = e.ring, e.ring
	m.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done(): // the caller gave up while it was held: it never starts
			m.mu.Lock()
			delete(b.execs, e.ID)
			m.mu.Unlock()
			m.unstarted(e, "killed")
			return nil, 0
		}
	}
	// Killed (a stop, a DELETE, Close) while it was held: it never starts.
	m.mu.Lock()
	gone := e.killed || m.closed || m.boxes[b.ID] != b || b.execs[e.ID] != e
	m.mu.Unlock()
	if gone {
		m.unstarted(e, "killed")
		return e, http.StatusCreated
	}
	var in io.WriteCloser
	var master, slave *os.File
	if q.TTY { // the terminal is its stdin, stdout and stderr; its output reaches the ring through us
		if master, slave, err = fsbOpenPTY(q.Rows, q.Cols); err != nil {
			m.mu.Lock()
			delete(b.execs, e.ID)
			m.mu.Unlock()
			m.unstarted(e, "exited")
			fsbFail(w, http.StatusServiceUnavailable, "unavailable", "a terminal: "+err.Error())
			return nil, 0
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		if wantStdin {
			in = fsbTTYIn{master}
		}
	} else if wantStdin {
		in, _ = cmd.StdinPipe()
	}
	err = cmd.Start() // (Start closes the pipe)
	if slave != nil {
		slave.Close() // the command's now: the terminal ends when the last of it lets go
	}
	if err != nil {
		if master != nil {
			master.Close()
		}
		m.mu.Lock()
		delete(b.execs, e.ID)
		m.mu.Unlock()
		m.unstarted(e, "exited")
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return nil, 0
	}
	m.mu.Lock()
	e.pid, e.stdin, e.pty = cmd.Process.Pid, in, master
	// A kill that came between the check above and now saw no pid: deliver it.
	late := e.killed || m.closed || m.boxes[b.ID] != b || b.execs[e.ID] != e
	m.mu.Unlock()
	if late {
		fsbKill(e.pid, syscall.SIGKILL)
	}
	if master != nil {
		e.ptyDone = make(chan struct{})
		go func() {
			_, _ = io.Copy(e.ring, master) // until the terminal hangs up (EIO) or reap closes it
			close(e.ptyDone)
		}()
	}
	go m.reap(e)
	if q.TimeoutMs > 0 {
		go func() {
			t := time.NewTimer(time.Duration(q.TimeoutMs) * time.Millisecond)
			defer t.Stop()
			select {
			case <-e.done:
			case <-t.C:
				m.mu.Lock()
				running := e.State == "running"
				e.killed = e.killed || running
				m.mu.Unlock()
				if running {
					fsbEnd(e.pid, m.grace())
				}
			}
		}()
	}
	return e, http.StatusCreated
}

// fsbTTYIn is a tty exec's stdin (with stdin: true): the terminal's input,
// where end-of-file is the terminal's ^D.
type fsbTTYIn struct{ f *os.File }

func (t fsbTTYIn) Write(p []byte) (int, error) { return t.f.Write(p) }
func (t fsbTTYIn) Close() error                { _, err := t.f.Write([]byte{4}); return err }

// unstarted ends an exec that never ran.
func (m *fsbManager) unstarted(e *fsbExec, state string) {
	m.mu.Lock()
	e.State, e.Ended = state, fsbNow()
	m.mu.Unlock()
	e.ring.close()
	close(e.done)
}

func (m *fsbManager) reap(e *fsbExec) {
	_ = e.cmd.Wait()
	if e.ptyDone != nil {
		// The terminal's last output drains — unless something the command
		// left behind still holds the terminal open: then it's cut.
		select {
		case <-e.ptyDone:
		case <-time.After(500 * time.Millisecond):
		}
		_ = e.pty.Close()
		<-e.ptyDone
	}
	code, sig := fsbSigName(e.cmd.ProcessState)
	m.mu.Lock()
	e.State = "exited"
	if e.killed || sig != "" {
		e.State = "killed"
	}
	e.ExitCode, e.Signal, e.Ended = &code, sig, fsbNow()
	if sig != "" {
		e.ExitCode = nil
	}
	m.mu.Unlock()
	e.ring.close()
	close(e.done)
}

// exec finds {eid} of a sandbox the caller may use (m.mu held on return).
func (m *fsbManager) exec(w http.ResponseWriter, r *http.Request) (*fsbExec, bool) {
	b, _, ok := m.box(w, r)
	if !ok {
		return nil, false
	}
	e := b.execs[r.PathValue("eid")]
	if e == nil {
		m.mu.Unlock()
		fsbFail(w, http.StatusNotFound, "not-found", "no such exec")
		return nil, false
	}
	return e, true
}

func (m *fsbManager) execList(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "execs") {
		return
	}
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	out := []fsbExec{}
	for _, e := range b.execs {
		out = append(out, m.execView(e))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	fsbJSON(w, http.StatusOK, map[string]any{"execs": out})
}

func (m *fsbManager) execGet(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "exec-get") {
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	v := m.execView(e)
	m.mu.Unlock()
	fsbJSON(w, http.StatusOK, v)
}

func (m *fsbManager) execDelete(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "exec-delete") {
		return
	}
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	e := b.execs[r.PathValue("eid")]
	delete(b.execs, r.PathValue("eid"))
	if e != nil && e.State == "running" {
		e.killed = true
		fsbKill(e.pid, syscall.SIGKILL)
	}
	m.mu.Unlock()
	if e == nil {
		fsbFail(w, http.StatusNotFound, "not-found", "no such exec")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fsbManager) execOutput(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "output") {
		return
	}
	qs := r.URL.Query()
	enc := orFsb(qs.Get("encoding"), "text")
	if enc != "text" && enc != "base64" {
		fsbFail(w, http.StatusBadRequest, "invalid", "encoding is text or base64")
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	m.mu.Unlock()
	since, _ := strconv.ParseInt(qs.Get("since"), 10, 64)
	since = max(since, 0)
	limit, _ := strconv.Atoi(qs.Get("max"))
	if limit <= 0 {
		limit = 64 << 10
	}
	limit = min(limit, 1<<20)
	waitMs, _ := strconv.Atoi(qs.Get("waitMs"))
	waitMs = max(0, min(waitMs, 30000))
	start, end, total, ringStart, data := e.ring.read(r.Context(), since, limit, time.Duration(waitMs)*time.Millisecond)
	m.mu.Lock()
	st, code, sig := e.State, e.ExitCode, e.Signal
	m.mu.Unlock()
	out := map[string]any{"start": start, "end": end, "total": total, "ringStart": ringStart,
		"encoding": enc, "state": st, "exitCode": code, "signal": sig}
	if enc == "base64" {
		out["data"] = base64.StdEncoding.EncodeToString(data)
	} else {
		out["data"] = fsbText(data)
	}
	fsbJSON(w, http.StatusOK, out)
}

func (m *fsbManager) execStdin(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "stdin") {
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	in, st, eof := e.stdin, e.State, e.eof
	closing := r.URL.Query().Get("eof") == "1"
	m.mu.Unlock()
	switch {
	case in == nil:
		fsbFail(w, http.StatusBadRequest, "invalid", "this exec wasn't started with stdin")
		return
	case st != "running":
		fsbStateErr(w, errors.New("the exec has ended"), st)
		return
	case eof:
		fsbFail(w, http.StatusBadRequest, "invalid", "stdin is closed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, fsbStdinMax+1))
	if err == nil && len(body) > fsbStdinMax {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "over limits.stdinMax")
		return
	}
	if len(body) > 0 {
		if _, err := in.Write(body); err != nil {
			fsbStateErr(w, errors.New("the exec has stopped reading"), "exited")
			return
		}
	}
	if closing {
		_ = in.Close() // (closes once)
		m.mu.Lock()
		e.eof = true
		m.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fsbManager) execSignal(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "signal") {
		return
	}
	var q struct {
		Signal string `json:"signal"`
		Group  *bool  `json:"group"`
	}
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body")
		return
	}
	sig, known := fsbSignals[strings.TrimPrefix(strings.ToUpper(q.Signal), "SIG")]
	if !known {
		fsbFail(w, http.StatusBadRequest, "invalid", "signal is INT, TERM, KILL or HUP")
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	// A finished exec's group is gone (its number may be another's now): a
	// signal to it is a no-op, as a kill that raced the end is.
	// How it ends says whether it was killed: a TERM it handles and exits 7
	// on is "exited" with 7.
	if e.State == "running" && e.pid > 0 {
		if q.Group == nil || *q.Group {
			fsbKill(e.pid, sig)
		} else {
			_ = syscall.Kill(e.pid, sig)
		}
	}
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// --- terminals (tty) -----------------------------------------------------------------

func (m *fsbManager) execResize(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "resize") || m.noTTY(w) {
		return
	}
	var q struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if err := fsbDecode(r, &q); err != nil || q.Rows <= 0 || q.Cols <= 0 {
		fsbFail(w, http.StatusBadRequest, "invalid", "rows and cols are positive")
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	tty, st, pty := e.TTY, e.State, e.pty
	m.mu.Unlock()
	switch {
	case !tty:
		fsbFail(w, http.StatusBadRequest, "invalid", "this exec has no terminal (started without tty)")
	case st != "running" || pty == nil:
		fsbStateErr(w, errors.New("the exec has ended"), st)
	case fsbSetSize(pty, q.Rows, q.Cols) != nil:
		fsbStateErr(w, errors.New("the exec has ended"), "exited")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ttyAttach: GET /sbx/sandboxes/{id}/execs/{eid}/tty — a tty exec's
// terminal, over a WebSocket.
func (m *fsbManager) ttyAttach(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "tty") || m.noTTY(w) {
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	tty := e.TTY
	m.mu.Unlock()
	if !tty {
		fsbFail(w, http.StatusBadRequest, "invalid", "this exec has no terminal (started without tty)")
		return
	}
	m.ttyServe(w, r, r.PathValue("id"), e)
}

// ttyStart: GET /sbx/sandboxes/{id}/tty?cwd=&cmd=&rows=&cols= — a tty exec
// (the login shell unless cmd), attached.
func (m *fsbManager) ttyStart(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "tty") || m.noTTY(w) {
		return
	}
	if !fsbws.IsUpgrade(r) { // before anything starts
		fsbFail(w, http.StatusBadRequest, "invalid", "a terminal is a WebSocket upgrade")
		return
	}
	qs := r.URL.Query()
	q := fsbCmdReq{Cmd: qs.Get("cmd"), Cwd: qs.Get("cwd"), TTY: true, Label: "terminal"}
	q.Rows, _ = strconv.Atoi(qs.Get("rows"))
	q.Cols, _ = strconv.Atoi(qs.Get("cols"))
	if q.Cmd == "" {
		q.Argv = []string{"/bin/sh", "-l"} // the sandbox's shell (its `shell`), a login one
	}
	e, _ := m.launch(w, r, q)
	if e == nil {
		return
	}
	m.ttyServe(w, r, r.PathValue("id"), e)
}

// ttyServe speaks the /ws/term framing (docs/protocol.md §/ws/term) for a
// tty exec: the session frame, the ring from its oldest byte and then live
// output as binary frames, the exit frame when the command has ended and
// all its output is out. From the client: keystrokes (binary), resize and
// ping. A client that leaves doesn't end the command; another can attach.
func (m *fsbManager) ttyServe(w http.ResponseWriter, r *http.Request, sandbox string, e *fsbExec) {
	c, err := fsbws.Upgrade(w, r, &fsbws.UpgradeOptions{MaxMessageSize: 1 << 20,
		Error: func(w http.ResponseWriter, _ *http.Request, status int, reason string) {
			fsbFail(w, status, "invalid", reason)
		}})
	if err != nil {
		return
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// the terminal while the command runs (m.mu: a gated exec gets it late)
	term := func() *os.File {
		m.mu.Lock()
		defer m.mu.Unlock()
		if e.State != "running" {
			return nil
		}
		return e.pty
	}
	hello, _ := json.Marshal(map[string]any{"op": "session", "id": e.ID, "sandbox": sandbox, "echoAck": false})
	if c.WriteMessage(fsbws.TextMessage, hello) != nil {
		return
	}
	go func() {
		defer cancel()
		for {
			typ, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if typ == fsbws.BinaryMessage {
				if f := term(); f != nil {
					_, _ = f.Write(data)
				}
				continue
			}
			var ctl struct {
				Op   string          `json:"op"`
				Cols int             `json:"cols"`
				Rows int             `json:"rows"`
				T    json.RawMessage `json:"t"`
			}
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.Op { // anything else is ignored
			case "resize":
				if f := term(); f != nil && ctl.Cols > 0 && ctl.Rows > 0 {
					_ = fsbSetSize(f, ctl.Rows, ctl.Cols)
				}
			case "ping":
				pong, _ := json.Marshal(map[string]any{"op": "pong", "t": ctl.T})
				_ = c.WriteMessage(fsbws.TextMessage, pong)
			}
		}
	}()
	for since := int64(0); !e.ring.ended(since); {
		_, end, _, _, data := e.ring.read(ctx, since, 64<<10, 30*time.Second)
		if ctx.Err() != nil {
			return // the client left; the command runs on
		}
		if len(data) > 0 && c.WriteMessage(fsbws.BinaryMessage, data) != nil {
			return
		}
		since = end
	}
	m.mu.Lock()
	exit := map[string]any{"op": "exit", "code": e.ExitCode} // null when a signal ended it
	if e.Signal != "" {
		exit["signal"] = e.Signal
	}
	m.mu.Unlock()
	b, _ := json.Marshal(exit)
	_ = c.WriteMessage(fsbws.TextMessage, b)
}

// Linux's terminal ioctls (the generic numbers: amd64, arm64, 386, arm,
// riscv64, loong64) — literal, so the file still builds elsewhere.
const (
	fsbTIOCGPTN   = 0x80045430
	fsbTIOCSPTLCK = 0x40045431
	fsbTIOCSWINSZ = 0x5414
)

// fsbHasPTY: the host gives the fake terminals.
var fsbHasPTY = sync.OnceValue(func() bool {
	switch runtime.GOARCH {
	case "amd64", "arm64", "386", "arm", "riscv64", "loong64":
	default:
		return false
	}
	master, slave, err := fsbOpenPTY(24, 80)
	if err != nil {
		return false
	}
	master.Close()
	slave.Close()
	return true
})

// fsbOpenPTY opens a pseudo-terminal: its master (ours: pollable, so a Close
// ends a blocked read) and its slave (the command's), rows × cols.
func fsbOpenPTY(rows, cols int) (master, slave *os.File, err error) {
	if runtime.GOOS != "linux" {
		return nil, nil, errors.New("terminals need Linux")
	}
	if master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0); err != nil {
		return nil, nil, err
	}
	var unlock int32
	var n uint32
	if err = fsbIoctl(master, fsbTIOCSPTLCK, unsafe.Pointer(&unlock)); err == nil {
		err = fsbIoctl(master, fsbTIOCGPTN, unsafe.Pointer(&n))
	}
	if err == nil {
		slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err == nil {
		err = fsbSetSize(master, rows, cols)
	}
	if err != nil {
		master.Close()
		if slave != nil {
			slave.Close()
		}
		return nil, nil, err
	}
	return master, slave, nil
}

// fsbSetSize sets a terminal's window (0: 24 × 80); the command gets SIGWINCH.
func fsbSetSize(f *os.File, rows, cols int) error {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	ws := struct{ row, col, x, y uint16 }{uint16(min(rows, 0xffff)), uint16(min(cols, 0xffff)), 0, 0}
	return fsbIoctl(f, fsbTIOCSWINSZ, unsafe.Pointer(&ws))
}

// fsbIoctl without f.Fd(), which would make the file blocking.
func fsbIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// --- files ------------------------------------------------------------------------

func fsbETag(p string, fi fs.FileInfo) string {
	if fi.Mode().IsRegular() {
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				return hex.EncodeToString(h.Sum(nil))[:32]
			}
		}
	}
	return fmt.Sprintf("m%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

func fsbType(fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "symlink"
	case fi.IsDir():
		return "dir"
	case fi.Mode().IsRegular():
		return "file"
	}
	return "other"
}

func fsbStat(p, inside string) (map[string]any, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"path": inside, "type": fsbType(fi), "size": fi.Size(),
		"mode": fmt.Sprintf("%04o", fi.Mode().Perm()), "mtimeMs": fi.ModTime().UnixMilli(), "etag": fsbETag(p, fi)}
	if fi.Mode()&fs.ModeSymlink != 0 {
		out["target"], _ = os.Readlink(p)
	}
	return out, nil
}

// fileBox resolves {id} and a path for a file operation; follow: the
// operation follows a final symlink (see pathFollow).
func (m *fsbManager) fileBox(w http.ResponseWriter, r *http.Request, p string, follow bool) (*fsbBox, string, bool) {
	b, _, ok := m.box(w, r)
	if !ok {
		return nil, "", false
	}
	if err := b.usable(); err != nil {
		st := b.State
		m.mu.Unlock()
		fsbStateErr(w, err, st)
		return nil, "", false
	}
	m.mu.Unlock()
	hp, err := b.path(p)
	if follow && err == nil {
		hp, err = b.pathFollow(p)
	}
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return nil, "", false
	}
	return b, hp, true
}

func fsbNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		fsbFail(w, http.StatusNotFound, "not-found", "no such file or directory")
		return
	}
	fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
}

func (m *fsbManager) fileStat(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "stat") {
		return
	}
	p := r.URL.Query().Get("path")
	_, hp, ok := m.fileBox(w, r, p, false)
	if !ok {
		return
	}
	st, err := fsbStat(hp, p)
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	fsbJSON(w, http.StatusOK, st)
}

func (m *fsbManager) fileRead(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "read") {
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	_, hp, ok := m.fileBox(w, r, p, true)
	if !ok {
		return
	}
	fi, err := os.Stat(hp)
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	if fi.IsDir() {
		fsbFail(w, http.StatusBadRequest, "invalid", p+" is a directory")
		return
	}
	offset, err1 := strconv.ParseInt(orFsb(qs.Get("offset"), "0"), 10, 64)
	length, err2 := strconv.ParseInt(orFsb(qs.Get("length"), "-1"), 10, 64)
	if err1 != nil || err2 != nil || offset < 0 {
		fsbFail(w, http.StatusBadRequest, "invalid", "offset and length are byte counts")
		return
	}
	n := max(fi.Size()-offset, 0) // what the read returns
	if length >= 0 {
		n = min(n, length)
	}
	if n > m.fileMax() {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "over limits.fileMax; read a smaller range")
		return
	}
	f, err := os.Open(hp)
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	w.Header().Set("ETag", `"`+fsbETag(hp, fi)+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, io.LimitReader(f, n))
}

func (m *fsbManager) fileWrite(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "write") {
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	_, hp, ok := m.fileBox(w, r, p, false) // it replaces the entry (a symlink too), as a rename does
	if !ok {
		return
	}
	mode := fs.FileMode(0o644)
	ms := qs.Get("mode")
	if ms != "" {
		v, err := strconv.ParseUint(ms, 8, 32)
		if err != nil || v > 0o777 {
			fsbFail(w, http.StatusBadRequest, "invalid", "mode is octal permission bits")
			return
		}
		mode = fs.FileMode(v)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, m.fileMax()+1))
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if int64(len(body)) > m.fileMax() {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "over limits.fileMax")
		return
	}
	m.wmu.Lock() // the check and the rename are one step against another write
	defer m.wmu.Unlock()
	cur, statErr := os.Lstat(hp)
	if cur != nil && cur.IsDir() {
		fsbFail(w, http.StatusBadRequest, "invalid", p+" is a directory")
		return
	}
	if im, inm := strings.Trim(qs.Get("ifMatch"), `"`), qs.Get("ifNoneMatch"); im != "" || inm != "" {
		m.mu.Lock()
		forced := m.f412 > 0
		if forced {
			m.f412--
		}
		m.mu.Unlock()
		etag := ""
		if statErr == nil {
			etag = fsbETag(hp, cur)
		}
		if forced || (im != "" && im != etag) || (inm == "*" && statErr == nil) {
			if forced {
				etag = "changed-elsewhere"
			}
			fsbJSON(w, http.StatusPreconditionFailed, fsbError{Error: p + " changed", Refusal: "precondition", ETag: etag})
			return
		}
	}
	if qs.Get("mkdirs") == "1" {
		_ = os.MkdirAll(filepath.Dir(hp), 0o755)
	}
	if ms == "" && cur != nil && cur.Mode().IsRegular() {
		mode = cur.Mode().Perm() // a replaced file keeps its mode
	}
	tmp, err := os.CreateTemp(filepath.Dir(hp), ".fsb-tmp-*")
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	_, err = tmp.Write(body)
	if err2 := tmp.Close(); err == nil {
		err = err2
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), hp)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		fsbNotFound(w, err)
		return
	}
	st, _ := fsbStat(hp, p)
	fsbJSON(w, http.StatusOK, st)
}

func (m *fsbManager) fileList(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "list-dir") {
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	_, hp, ok := m.fileBox(w, r, p, true)
	if !ok {
		return
	}
	ents, err := os.ReadDir(hp)
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	limit, _ := strconv.Atoi(qs.Get("limit"))
	if limit <= 0 {
		limit = 1000
	}
	out := []map[string]any{}
	for _, d := range ents {
		if len(out) >= limit {
			break
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		e := map[string]any{"name": d.Name(), "type": fsbType(fi), "size": fi.Size(),
			"mtimeMs": fi.ModTime().UnixMilli(), "mode": fmt.Sprintf("%04o", fi.Mode().Perm())}
		if fi.Mode()&fs.ModeSymlink != 0 {
			e["target"], _ = os.Readlink(filepath.Join(hp, d.Name()))
		}
		out = append(out, e)
	}
	fsbJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out, "truncated": len(ents) > limit})
}

func (m *fsbManager) fileMkdir(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "mkdir") {
		return
	}
	var q struct {
		Path    string `json:"path"`
		Parents bool   `json:"parents"`
	}
	_ = fsbDecode(r, &q)
	_, hp, ok := m.fileBox(w, r, q.Path, false)
	if !ok {
		return
	}
	var err error
	if q.Parents {
		err = os.MkdirAll(hp, 0o755)
	} else {
		err = os.Mkdir(hp, 0o755)
	}
	if err != nil {
		fsbNotFound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fsbManager) fileRemove(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "remove") {
		return
	}
	var q struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	_ = fsbDecode(r, &q)
	b, hp, ok := m.fileBox(w, r, q.Path, false)
	if !ok {
		return
	}
	if hp == b.dir || hp == b.Workdir || hp == b.Home {
		fsbFail(w, http.StatusBadRequest, "invalid", "won't remove the sandbox's own directories")
		return
	}
	if _, err := os.Lstat(hp); err != nil {
		fsbNotFound(w, err)
		return
	}
	var err error
	if q.Recursive {
		err = os.RemoveAll(hp)
	} else {
		err = os.Remove(hp)
	}
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *fsbManager) fileMove(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "move") {
		return
	}
	var q struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	_ = fsbDecode(r, &q)
	b, from, ok := m.fileBox(w, r, q.From, false)
	if !ok {
		return
	}
	to, err := b.path(q.To)
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if _, err := os.Lstat(to); err == nil && !q.Overwrite {
		fsbJSON(w, http.StatusPreconditionFailed, fsbError{Error: q.To + " exists", Refusal: "precondition"})
		return
	}
	if err := os.Rename(from, to); err != nil {
		fsbNotFound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- trees ------------------------------------------------------------------------

func (m *fsbManager) tarGet(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "tar-get") {
		return
	}
	if !m.hasCap("tar") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no tar here")
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	_, hp, ok := m.fileBox(w, r, p, true)
	if !ok {
		return
	}
	if fi, err := os.Stat(hp); err != nil {
		fsbNotFound(w, err)
		return
	} else if !fi.IsDir() {
		fsbFail(w, http.StatusBadRequest, "invalid", p+" is not a directory (read a file with files/content)")
		return
	}
	excl := qs["exclude"]
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	_ = filepath.WalkDir(hp, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(hp, fp)
		if rel == "." {
			return nil
		}
		for _, x := range excl {
			if ok, _ := filepath.Match(x, rel); ok || x == d.Name() {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(fp)
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if tw.WriteHeader(hdr) != nil {
			return errors.New("stop")
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(fp)
			if err == nil {
				_, _ = io.Copy(tw, f)
				f.Close()
			}
		}
		return nil
	})
	_ = tw.Close()
}

func (m *fsbManager) tarPut(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "tar-put") {
		return
	}
	if !m.hasCap("tar") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no tar here")
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	b, hp, ok := m.fileBox(w, r, p, true)
	if !ok {
		return
	}
	if qs.Get("mkdirs") == "1" {
		_ = os.MkdirAll(hp, 0o755)
	}
	if fi, err := os.Stat(hp); err != nil {
		fsbNotFound(w, err)
		return
	} else if !fi.IsDir() {
		fsbFail(w, http.StatusBadRequest, "invalid", p+" is not a directory")
		return
	}
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, tar.ErrInsecurePath) && hdr != nil {
			continue // (GODEBUG=tarinsecurepath=0) skipped, as below
		}
		if err != nil {
			fsbFail(w, http.StatusBadRequest, "invalid", "bad tar: "+err.Error())
			return
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue // never outside path
		}
		dst := filepath.Join(hp, name)
		if !fsbWithin(b.dir, fsbResolve(filepath.Dir(dst))) {
			continue // nor through a symlink out of the sandbox
		}
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			_ = os.Remove(dst) // an entry replaces a symlink; it isn't written through it
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dst, 0o755)
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Remove(dst)
			err = os.Symlink(hdr.Linkname, dst)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			var f *os.File
			f, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode).Perm())
			if err == nil {
				_, err = io.Copy(f, tr)
				if err2 := f.Close(); err == nil {
					err = err2
				}
			}
			if err == nil {
				_ = os.Chmod(dst, fs.FileMode(hdr.Mode).Perm())
				_ = os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
			}
		}
		if err != nil {
			fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// fsbCopyTree copies a directory's contents into dst (dirs, files, symlinks).
func fsbCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			return os.MkdirAll(t, 0o755)
		case fi.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(t)
			return os.Symlink(l, t)
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(t, b, fi.Mode().Perm())
		}
		return nil
	})
}

func fsbTreeBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// --- snapshots --------------------------------------------------------------------

func (m *fsbManager) snapList(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "snapshots") {
		return
	}
	if !m.hasCap("snapshots") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no snapshots here")
		return
	}
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	out := []fsbSnap{}
	for _, s := range b.snaps {
		out = append(out, *s)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	fsbJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (m *fsbManager) snapCreate(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "snapshot") {
		return
	}
	if !m.hasCap("snapshots") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no snapshots here")
		return
	}
	var q struct {
		Name     string `json:"name"`
		ClientID string `json:"clientId"`
	}
	if err := fsbDecode(r, &q); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	ikey := c.from + "\x00snap\x00" + b.ID + "\x00" + q.ClientID
	// again answers a repeated clientId (m.mu held; false: not a repeat).
	again := func() bool {
		prev, ok := m.idem[ikey]
		if q.ClientID == "" || !ok || b.snaps[prev.id] == nil {
			return false
		}
		if prev.hash != fsbHash(q) {
			m.mu.Unlock()
			fsbFail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different snapshot")
			return true
		}
		s := *b.snaps[prev.id]
		m.mu.Unlock()
		fsbJSON(w, http.StatusOK, s)
		return true
	}
	if again() {
		return
	}
	b.sseq++
	s := &fsbSnap{ID: fmt.Sprintf("s%d", b.sseq), seq: b.sseq, Name: orFsb(q.Name, fmt.Sprintf("snapshot %d", b.sseq)), Created: fsbNow(),
		dir: filepath.Join(m.Root, ".snaps", b.ID, fmt.Sprintf("s%d", b.sseq))}
	src := b.dir
	m.mu.Unlock()
	// Copied first, published after: nobody sees (or clones) a half-made one.
	err := fsbCopyTree(src, s.dir)
	s.Bytes = fsbTreeBytes(s.dir)
	m.mu.Lock()
	if err == nil && m.boxes[b.ID] != b {
		err = errors.New("the sandbox was deleted")
	}
	if err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(s.dir)
		fsbFail(w, http.StatusServiceUnavailable, "unavailable", "snapshot: "+err.Error())
		return
	}
	if again() { // the same clientId won a race meanwhile
		_ = os.RemoveAll(s.dir)
		return
	}
	b.snaps[s.ID] = s
	if q.ClientID != "" {
		m.idem[ikey] = fsbIdem{id: s.ID, hash: fsbHash(q)}
	}
	v := *s
	m.mu.Unlock()
	fsbJSON(w, http.StatusCreated, v)
}

func (m *fsbManager) snapRestore(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "restore") {
		return
	}
	if !m.hasCap("snapshots") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no snapshots here")
		return
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	s := b.snaps[r.PathValue("sid")]
	if s == nil {
		m.mu.Unlock()
		fsbFail(w, http.StatusNotFound, "not-found", "no such snapshot")
		return
	}
	killed := b.stopExecs()
	dir := b.dir
	m.mu.Unlock()
	fsbAwait(killed, 5*time.Second) // nothing writes into the tree while it's replaced
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
	if err := fsbCopyTree(s.dir, dir); err != nil {
		fsbFail(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	m.mu.Lock()
	b.Version++
	v := b.view(c)
	m.mu.Unlock()
	fsbJSON(w, http.StatusOK, v)
}

func (m *fsbManager) snapDelete(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "snapshot-delete") {
		return
	}
	if !m.hasCap("snapshots") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no snapshots here")
		return
	}
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	s := b.snaps[r.PathValue("sid")]
	delete(b.snaps, r.PathValue("sid"))
	m.mu.Unlock()
	if s == nil {
		fsbFail(w, http.StatusNotFound, "not-found", "no such snapshot")
		return
	}
	_ = os.RemoveAll(s.dir)
	w.WriteHeader(http.StatusNoContent)
}

// --- ports (D135) -----------------------------------------------------------------

// port: ANY …/ports/{port}/{path…} — an HTTP proxy to a server on the
// sandbox's loopback. Here that is the HOST's loopback, 127.0.0.1 then
// [::1] (TEST ONLY: a fake sandbox is a directory, its commands host
// processes). A running sandbox only; nothing listening is 502
// not-listening; xbin's credentials and forwarding headers never pass in,
// Set-Cookie and X-XBin-* never come back.
func (m *fsbManager) port(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "port") {
		return
	}
	if !m.hasCap("ports") {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "no ports here")
		return
	}
	ps := r.PathValue("port")
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != ps {
		fsbFail(w, http.StatusBadRequest, "invalid", "port "+strconv.Quote(ps)+" must be 1-65535")
		return
	}
	segs := strings.SplitN(r.URL.EscapedPath(), "/", 7) // "", sbx, sandboxes, id, ports, port, tail
	tail := ""
	if len(segs) == 7 {
		tail = segs[6]
	}
	for _, s := range strings.Split(tail, "/") {
		if d, err := url.PathUnescape(s); err != nil || d == "." || d == ".." {
			fsbFail(w, http.StatusBadRequest, "invalid", "the path has a dot segment or a bad escape")
			return
		}
	}
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	st := b.State
	if st == "running" {
		b.LastActive = fsbNow()
	}
	m.mu.Unlock()
	if st != "running" {
		fsbStateErr(w, fmt.Errorf("the sandbox is %s: nothing listens in it", st), st)
		return
	}
	hp := strconv.Itoa(port)
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", hp), 5*time.Second)
	if err != nil {
		if c6, err6 := net.DialTimeout("tcp", net.JoinHostPort("::1", hp), 5*time.Second); err6 == nil {
			c, err = c6, nil
		}
	}
	if err != nil {
		fsbFail(w, http.StatusBadGateway, "not-listening", "nothing accepts connections on port "+hp+" in the sandbox")
		return
	}
	used := false
	host := "localhost:" + hp
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := &url.URL{Scheme: "http", Host: host, RawQuery: pr.In.URL.RawQuery}
			if p, err := url.PathUnescape("/" + tail); err == nil {
				u.Path = p
				if u.EscapedPath() != "/"+tail {
					u.RawPath = "/" + tail
				}
			}
			pr.Out.URL, pr.Out.Host = u, host
			h := pr.Out.Header
			if up := pr.In.Header.Get("Upgrade"); up != "" {
				h.Set("Connection", "Upgrade")
				h.Set("Upgrade", up)
			}
			for _, k := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Sbx-User", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				h.Del(k)
			}
			fsbDropXBin(h)
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			fsbDropXBin(res.Header)
			return nil
		},
		Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true,
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				if used {
					return nil, errors.New("one connection per request")
				}
				used = true
				return c, nil
			}},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				fsbFail(w, http.StatusBadGateway, "not-listening", "the server on port "+hp+" didn't answer: "+err.Error())
			}
		},
	}
	rp.ServeHTTP(w, r)
	if !used {
		c.Close()
	}
}

func fsbDropXBin(h http.Header) {
	for k := range h {
		if len(k) >= 7 && strings.EqualFold(k[:7], "X-XBin-") {
			delete(h, k)
		}
	}
}
