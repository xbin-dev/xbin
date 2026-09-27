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
// No `tty` capability: terminals come with the real manager.

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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// fsbManager serves the contract under /sbx/.
type fsbManager struct {
	Root        string        // sandboxes live in <Root>/<id>/, snapshots in <Root>/.snaps/
	DefaultFrom string        // the consumer when X-XBin-From is missing (a test calling directly)
	Caps        []string      // the capabilities hello offers (nil = all but tty)
	Grace       time.Duration // TERM → KILL on a timeout or a stop (0 = 5 s)
	Ring        int           // an exec's output ring (0 = 1 MiB)

	mu    sync.Mutex
	boxes map[string]*fsbBox
	seq   int
	idem  map[string]fsbIdem
	calls []fsbCall
	fault map[string]fsbFault
	gate  chan struct{} // non-nil: exec starts wait for it to close
	f412  int           // the next N conditional writes fail
	once  sync.Once
	mux   *http.ServeMux
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
	dir   string
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

	ring   *fsbRing
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	killed bool
}

var fsbImages = []map[string]any{{"id": "base", "title": "the host's tools (a test fixture)", "default": true, "tools": []string{"git"}}}
var fsbSizes = []fsbSize{{ID: "small", MemMiB: 2048, VCPUs: 2, DiskGiB: 20}}

const (
	fsbFileMax   = 64 << 20
	fsbRunOutMax = 1 << 20
	fsbRunMaxMs  = 600000
	fsbWaitMax   = 120
)

func fsbNow() int64 { return time.Now().UnixMilli() }

// --- test hooks --------------------------------------------------------------

// FailNext makes the next request of op (hello, list, create, get, patch,
// delete, action, run, exec, output, stdin, signal, stat, read, write,
// list-dir, mkdir, remove, move, tar-get, tar-put, snapshot) answer this
// refusal.
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

// Close kills every running command (tests' cleanup).
func (m *fsbManager) Close() {
	m.mu.Lock()
	var all []*fsbExec
	for _, b := range m.boxes {
		for _, e := range b.execs {
			all = append(all, e)
		}
	}
	m.mu.Unlock()
	for _, e := range all {
		fsbKillGroup(e.cmd, syscall.SIGKILL)
	}
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
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
	return []string{"exec", "files", "tar", "snapshots", "clone", "archive"}
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
	x.HandleFunc("POST /sbx/sandboxes/{id}/execs/{eid}/resize", m.unsupported)
	x.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/tty", m.unsupported)
	x.HandleFunc("GET /sbx/sandboxes/{id}/tty", m.unsupported)
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
	m.mux = x
}

func (m *fsbManager) unsupported(w http.ResponseWriter, _ *http.Request) {
	fsbFail(w, http.StatusNotImplemented, "unsupported", "this manager has no terminals (tty)")
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
		b.State = "running"
		b.Version++
	default:
		return fmt.Errorf("the sandbox is %s", b.State)
	}
	b.LastActive = fsbNow()
	return nil
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
			"execsRunning": 16, "outputRing": m.ringSize(), "stdinMax": 1 << 20, "fileMax": fsbFileMax,
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
	dir := filepath.Join(m.Root, id)
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
	restart := false
	if q.Name != nil {
		if n := strings.TrimSpace(*q.Name); n == "" || len(n) > 64 {
			fsbFail(w, http.StatusBadRequest, "invalid", "name is 1–64 characters")
			return
		}
		b.Name = strings.TrimSpace(*q.Name)
	}
	if q.Visibility != nil {
		if *q.Visibility != "private" && *q.Visibility != "team" {
			fsbFail(w, http.StatusBadRequest, "invalid", "visibility is private or team")
			return
		}
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
		if *q.Egress != "none" && *q.Egress != "internet" {
			fsbFail(w, http.StatusBadRequest, "invalid", "egress is none or internet here")
			return
		}
		restart = restart || *q.Egress != b.Egress
		b.Egress = *q.Egress
	}
	if q.Size != nil && *q.Size != "small" {
		fsbFail(w, http.StatusBadRequest, "invalid", "no size "+*q.Size)
		return
	}
	if q.AutoStopMin != nil {
		b.AutoStopMin = *q.AutoStopMin
	}
	b.Version++
	v := b.view(c)
	fsbJSON(w, http.StatusOK, struct {
		fsbSandbox
		RestartNeeded bool `json:"restartNeeded,omitempty"`
	}{v, restart && b.State == "running"})
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
	execs := b.execList()
	m.mu.Unlock()
	for _, e := range execs {
		fsbKillGroup(e.cmd, syscall.SIGKILL)
	}
	_ = os.RemoveAll(b.dir)
	w.WriteHeader(http.StatusNoContent)
}

func (b *fsbBox) execList() []*fsbExec {
	out := make([]*fsbExec, 0, len(b.execs))
	for _, e := range b.execs {
		out = append(out, e)
	}
	return out
}

// stopExecs kills a sandbox's running commands (m.mu held by the caller —
// the kill itself doesn't take it).
func (b *fsbBox) stopExecs() {
	for _, e := range b.execs {
		if e.State == "running" {
			e.killed = true
			fsbKillGroup(e.cmd, syscall.SIGKILL)
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
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	defer m.mu.Unlock()
	switch act {
	case "start":
		if b.State != "stopped" && b.State != "running" {
			fsbStateErr(w, fmt.Errorf("a %s sandbox can't start", b.State), b.State)
			return
		}
		b.State = "running"
	case "stop":
		if b.State != "running" && b.State != "stopped" {
			fsbStateErr(w, fmt.Errorf("a %s sandbox can't stop", b.State), b.State)
			return
		}
		b.stopExecs()
		b.State = "stopped"
	case "archive":
		if !m.hasCap("archive") {
			fsbFail(w, http.StatusNotImplemented, "unsupported", "no archives here")
			return
		}
		if b.State == "archived" {
			break
		}
		b.stopExecs()
		b.State = "archived"
	case "thaw":
		if !m.hasCap("archive") {
			fsbFail(w, http.StatusNotImplemented, "unsupported", "no archives here")
			return
		}
		if b.State != "archived" {
			fsbStateErr(w, fmt.Errorf("a %s sandbox isn't archived", b.State), b.State)
			return
		}
		b.State = "stopped"
		if q.Start {
			b.State = "running"
		}
	default:
		fsbFail(w, http.StatusNotFound, "not-found", "no action "+act)
		return
	}
	b.Version++
	b.LastActive = fsbNow()
	fsbJSON(w, http.StatusOK, b.view(c))
}

// --- commands ---------------------------------------------------------------------

// path resolves an absolute in-sandbox path (m.mu held; b.dir is fixed).
func (b *fsbBox) path(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	c := filepath.Clean(p)
	if rel, err := filepath.Rel(b.dir, c); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is outside the sandbox", p)
	}
	return c, nil
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
		p, err := b.path(q.Cwd)
		if err != nil {
			return nil, err
		}
		cwd = p
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("cwd %s doesn't exist", q.Cwd)
	}
	var c *exec.Cmd
	if len(q.Argv) > 0 {
		c = exec.Command(q.Argv[0], q.Argv[1:]...) // exec-ok: a test fixture; the "sandbox" is a host directory
	} else {
		c = exec.Command("sh", "-c", q.Cmd) // exec-ok: a test fixture (see above)
	}
	c.Dir = cwd
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + b.Home, "LANG=C.UTF-8",
		"IN_SANDBOX=1", "SANDBOX_ID=" + b.ID, "SANDBOX_NAME=" + b.Name}
	keys := make([]string, 0, len(q.Env))
	for k := range q.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Env = append(c.Env, k+"="+q.Env[k])
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c, nil
}

func fsbKillGroup(c *exec.Cmd, sig syscall.Signal) {
	if c == nil || c.Process == nil {
		return
	}
	_ = syscall.Kill(-c.Process.Pid, sig)
}

func fsbSigName(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, strings.ToUpper(strings.TrimPrefix(fsbSignalNames[ws.Signal()], "SIG"))
	}
	return ps.ExitCode(), ""
}

var fsbSignalNames = map[syscall.Signal]string{syscall.SIGINT: "INT", syscall.SIGTERM: "TERM", syscall.SIGKILL: "KILL", syscall.SIGHUP: "HUP"}

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
	if keep := h.max - q; len(h.tail) > keep {
		h.tail = append(h.tail[:0:0], h.tail[len(h.tail)-keep:]...)
	}
	return len(p), nil
}

func (h *fsbHeadTail) out() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := int64(len(h.head) + len(h.tail))
	if kept >= h.bytes { // it all fit: one piece
		return map[string]any{"head": fsbText(append(append([]byte{}, h.head...), h.tail...)), "tail": "", "elided": 0, "bytes": h.bytes}
	}
	return map[string]any{"head": fsbText(h.head), "tail": fsbText(h.tail), "elided": h.bytes - kept, "bytes": h.bytes}
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
		_ = json.Unmarshal(q.Stdin, &stdin)
	}
	c.Stdin = strings.NewReader(stdin)
	start := time.Now()
	if err := c.Start(); err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	done := make(chan struct{})
	timedOut := false
	go func() {
		select {
		case <-done:
		case <-time.After(time.Duration(timeout) * time.Millisecond):
			timedOut = true
			fsbKillGroup(c, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(m.grace()):
				fsbKillGroup(c, syscall.SIGKILL)
			}
		case <-r.Context().Done():
			fsbKillGroup(c, syscall.SIGKILL)
		}
	}()
	_ = c.Wait()
	close(done)
	code, sig := fsbSigName(c.ProcessState)
	res := map[string]any{"exitCode": code, "signal": sig, "timedOut": timedOut, "ms": time.Since(start).Milliseconds()}
	if q.Merge {
		res["output"] = out.out()
	} else {
		res["stdout"], res["stderr"] = out.out(), errs.out()
	}
	fsbJSON(w, http.StatusOK, res)
}

// fsbRing is an exec's combined output: the newest max bytes of a stream.
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
	if len(r.buf) > r.max {
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
	if q.TTY {
		fsbFail(w, http.StatusNotImplemented, "unsupported", "this manager has no terminals (tty)")
		return
	}
	var wantStdin bool
	if len(q.Stdin) > 0 {
		_ = json.Unmarshal(q.Stdin, &wantStdin)
	}
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	ikey := c.from + "\x00exec\x00" + b.ID + "\x00" + q.ClientID
	if q.ClientID != "" {
		if prev, ok := m.idem[ikey]; ok {
			e := b.execs[prev.id]
			if prev.hash != fsbHash(q) {
				m.mu.Unlock()
				fsbFail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different command")
				return
			}
			if e != nil {
				v := m.execView(e)
				m.mu.Unlock()
				fsbJSON(w, http.StatusOK, v)
				return
			}
		}
	}
	if err := b.usable(); err != nil {
		st := b.State
		m.mu.Unlock()
		fsbStateErr(w, err, st)
		return
	}
	cmd, err := m.command(b, q)
	if err != nil {
		m.mu.Unlock()
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	gate := m.gate
	b.eseq++
	e := &fsbExec{ID: fmt.Sprintf("e%d", b.eseq), Label: q.Label, Cmd: q.Cmd, Argv: q.Argv, Cwd: cmd.Dir,
		State: "running", Started: fsbNow(), ClientID: q.ClientID, ring: newFsbRing(m.ringSize()), cmd: cmd, done: make(chan struct{})}
	b.execs[e.ID] = e
	if q.ClientID != "" {
		m.idem[ikey] = fsbIdem{id: e.ID, hash: fsbHash(q)}
	}
	cmd.Stdout, cmd.Stderr = e.ring, e.ring
	if wantStdin {
		p, err := cmd.StdinPipe()
		if err == nil {
			e.stdin = p
		}
	}
	m.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		delete(b.execs, e.ID)
		m.mu.Unlock()
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	go m.reap(e)
	if q.TimeoutMs > 0 {
		go func() {
			select {
			case <-e.done:
			case <-time.After(time.Duration(q.TimeoutMs) * time.Millisecond):
				m.mu.Lock()
				e.killed = true
				m.mu.Unlock()
				fsbKillGroup(cmd, syscall.SIGTERM)
				select {
				case <-e.done:
				case <-time.After(m.grace()):
					fsbKillGroup(cmd, syscall.SIGKILL)
				}
			}
		}()
	}
	m.mu.Lock()
	v := m.execView(e)
	m.mu.Unlock()
	fsbJSON(w, http.StatusCreated, v)
}

func (m *fsbManager) reap(e *fsbExec) {
	_ = e.cmd.Wait()
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
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	out := []fsbExec{}
	for _, e := range b.execs {
		out = append(out, m.execView(e))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started || out[i].ID < out[j].ID })
	fsbJSON(w, http.StatusOK, map[string]any{"execs": out})
}

func (m *fsbManager) execGet(w http.ResponseWriter, r *http.Request) {
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	v := m.execView(e)
	m.mu.Unlock()
	fsbJSON(w, http.StatusOK, v)
}

func (m *fsbManager) execDelete(w http.ResponseWriter, r *http.Request) {
	b, _, ok := m.box(w, r)
	if !ok {
		return
	}
	e := b.execs[r.PathValue("eid")]
	delete(b.execs, r.PathValue("eid"))
	m.mu.Unlock()
	if e == nil {
		fsbFail(w, http.StatusNotFound, "not-found", "no such exec")
		return
	}
	fsbKillGroup(e.cmd, syscall.SIGKILL)
	w.WriteHeader(http.StatusNoContent)
}

func (m *fsbManager) execOutput(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "output") {
		return
	}
	e, ok := m.exec(w, r)
	if !ok {
		return
	}
	m.mu.Unlock()
	qs := r.URL.Query()
	since, _ := strconv.ParseInt(qs.Get("since"), 10, 64)
	limit, _ := strconv.Atoi(qs.Get("max"))
	if limit <= 0 {
		limit = 64 << 10
	}
	limit = min(limit, 1<<20)
	waitMs, _ := strconv.Atoi(qs.Get("waitMs"))
	waitMs = max(0, min(waitMs, 30000))
	enc := orFsb(qs.Get("encoding"), "text")
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
	in, running := e.stdin, e.State == "running"
	m.mu.Unlock()
	if in == nil {
		fsbFail(w, http.StatusBadRequest, "invalid", "this exec wasn't started with stdin")
		return
	}
	if !running {
		fsbStateErr(w, errors.New("the exec has ended"), "exited")
		return
	}
	if _, err := io.Copy(in, io.LimitReader(r.Body, 1<<20)); err != nil {
		fsbStateErr(w, err, "exited")
		return
	}
	if r.URL.Query().Get("eof") == "1" {
		_ = in.Close()
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
	if sig == syscall.SIGKILL || sig == syscall.SIGTERM {
		e.killed = true
	}
	c := e.cmd
	m.mu.Unlock()
	if q.Group == nil || *q.Group {
		fsbKillGroup(c, sig)
	} else if c.Process != nil {
		_ = c.Process.Signal(sig)
	}
	w.WriteHeader(http.StatusNoContent)
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

// fileBox resolves {id} and ?path for a file operation.
func (m *fsbManager) fileBox(w http.ResponseWriter, r *http.Request, p string) (*fsbBox, string, bool) {
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
	hp, err := b.path(p)
	m.mu.Unlock()
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
	_, hp, ok := m.fileBox(w, r, p)
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
	_, hp, ok := m.fileBox(w, r, p)
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
	offset, _ := strconv.ParseInt(qs.Get("offset"), 10, 64)
	length, _ := strconv.ParseInt(qs.Get("length"), 10, 64)
	if qs.Get("length") == "" && fi.Size()-offset > fsbFileMax {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "the file is over the file limit; read a range")
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
	var rd io.Reader = f
	if length > 0 {
		rd = io.LimitReader(f, length)
	}
	w.Header().Set("ETag", `"`+fsbETag(hp, fi)+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, rd)
}

func (m *fsbManager) fileWrite(w http.ResponseWriter, r *http.Request) {
	if m.faulted(w, "write") {
		return
	}
	qs := r.URL.Query()
	p := qs.Get("path")
	_, hp, ok := m.fileBox(w, r, p)
	if !ok {
		return
	}
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
	body, err := io.ReadAll(io.LimitReader(r.Body, fsbFileMax+1))
	if err != nil {
		fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if len(body) > fsbFileMax {
		fsbFail(w, http.StatusRequestEntityTooLarge, "too-large", "over the file limit")
		return
	}
	if qs.Get("mkdirs") == "1" {
		_ = os.MkdirAll(filepath.Dir(hp), 0o755)
	}
	mode := fs.FileMode(0o644)
	if cur != nil {
		mode = cur.Mode().Perm()
	}
	if ms := qs.Get("mode"); ms != "" {
		if v, err := strconv.ParseUint(ms, 8, 32); err == nil {
			mode = fs.FileMode(v)
		}
	}
	tmp := hp + ".fsb-tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		fsbNotFound(w, err)
		return
	}
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, hp); err != nil {
		_ = os.Remove(tmp)
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
	_, hp, ok := m.fileBox(w, r, p)
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
	_, hp, ok := m.fileBox(w, r, q.Path)
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
	b, hp, ok := m.fileBox(w, r, q.Path)
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
	b, from, ok := m.fileBox(w, r, q.From)
	if !ok {
		return
	}
	m.mu.Lock()
	to, err := b.path(q.To)
	m.mu.Unlock()
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
	_, hp, ok := m.fileBox(w, r, p)
	if !ok {
		return
	}
	if _, err := os.Stat(hp); err != nil {
		fsbNotFound(w, err)
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
	_, hp, ok := m.fileBox(w, r, p)
	if !ok {
		return
	}
	if qs.Get("mkdirs") == "1" {
		_ = os.MkdirAll(hp, 0o755)
	}
	if fi, err := os.Stat(hp); err != nil || !fi.IsDir() {
		fsbFail(w, http.StatusBadRequest, "invalid", p+" is not a directory")
		return
	}
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fsbFail(w, http.StatusBadRequest, "invalid", "bad tar: "+err.Error())
			return
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		dst := filepath.Join(hp, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(dst, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode).Perm())
			if err != nil {
				fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				fsbFail(w, http.StatusBadRequest, "invalid", err.Error())
				return
			}
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
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created || out[i].ID < out[j].ID })
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
	_ = fsbDecode(r, &q)
	b, c, ok := m.box(w, r)
	if !ok {
		return
	}
	ikey := c.from + "\x00snap\x00" + b.ID + "\x00" + q.ClientID
	if q.ClientID != "" {
		if prev, ok := m.idem[ikey]; ok && b.snaps[prev.id] != nil {
			s := *b.snaps[prev.id]
			m.mu.Unlock()
			fsbJSON(w, http.StatusOK, s)
			return
		}
	}
	b.sseq++
	s := &fsbSnap{ID: fmt.Sprintf("s%d", b.sseq), Name: orFsb(q.Name, fmt.Sprintf("snapshot %d", b.sseq)), Created: fsbNow(),
		dir: filepath.Join(m.Root, ".snaps", b.ID, fmt.Sprintf("s%d", b.sseq))}
	b.snaps[s.ID] = s
	if q.ClientID != "" {
		m.idem[ikey] = fsbIdem{id: s.ID}
	}
	src := b.dir
	m.mu.Unlock()
	if err := fsbCopyTree(src, s.dir); err != nil {
		fsbFail(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	s.Bytes = fsbTreeBytes(s.dir)
	fsbJSON(w, http.StatusCreated, *s)
}

func (m *fsbManager) snapRestore(w http.ResponseWriter, r *http.Request) {
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
	b.stopExecs()
	dir := b.dir
	m.mu.Unlock()
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
