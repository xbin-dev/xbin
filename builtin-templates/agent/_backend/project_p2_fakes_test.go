package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// p2SCM is P1's in-memory provider (p1SCM) with its pull requests at K's
// fake provider through the real scm client: what the pr job opens, the
// refs job reads back.
type p2SCM struct{ *p1SCM }

// p2PullFails: the next this many pull-request creates fail as a
// connection would (not a refusal).
var p2PullFails atomic.Int32

func (f p2SCM) PullCreate(ctx context.Context, req scmPullReq) (*scmPull, error) {
	if p2PullFails.Add(-1) >= 0 {
		return nil, errors.New("connection reset by peer")
	}
	p2PullFails.Store(0)
	return f.creds.PullCreate(ctx, req)
}
func (f p2SCM) Pulls(ctx context.Context, q scmQuery) (*scmPage[scmPull], error) {
	return f.creds.Pulls(ctx, q)
}
func (f p2SCM) Pull(ctx context.Context, repo string, n int, as string) (*scmPull, error) {
	return f.creds.Pull(ctx, repo, n, as)
}

// --- a fake manager whose clones keep their paths -----------------------------------------

// keepPaths wraps the fake manager (a sandbox is a host directory, so a
// clone lives at another path) so that a task's fork looks as a real
// manager's clone does: at its source's paths. Every request to a fork has
// the source's directory turned into the fork's, every answer the fork's
// into the source's, and the git metadata the copy carried (gitdir,
// alternates, config) points into the fork, as it would in a clone that
// kept its paths. A fresh fork (labelled with a task, no from) is
// aliased to the fixture's primary sandbox.
type keepPaths struct {
	next    http.Handler
	mu      sync.Mutex
	primary string            // the fixture's sandbox id (a fresh fork's source)
	alias   map[string]string // fork id → source id
	root    string            // the manager's directory of sandboxes (resolved)
	// onSnap, when set, runs as a snapshot's create arrives, before the
	// manager takes it (what else reaches the sandbox meanwhile)
	onSnap func()
}

func (k *keepPaths) dir(id string) string { return filepath.Join(k.root, id) }

// forkOf is the fork a request path names ("": none).
func (k *keepPaths) forkOf(p string) string {
	rest, ok := strings.CutPrefix(p, "/sbx/sandboxes/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.alias[id]; ok {
		return id
	}
	return ""
}

func (k *keepPaths) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	k.mu.Lock()
	onSnap := k.onSnap
	k.mu.Unlock()
	if onSnap != nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/snapshots") {
		onSnap()
	}
	if fork := k.forkOf(r.URL.Path); fork != "" {
		k.mu.Lock()
		src, dst := k.dir(k.alias[fork])+"/", k.dir(fork)+"/"
		k.mu.Unlock()
		body = bytes.ReplaceAll(body, []byte(src), []byte(dst))
		q := r.URL.Query()
		for key, vs := range q {
			for i := range vs {
				vs[i] = strings.ReplaceAll(vs[i], src, dst)
			}
			q[key] = vs
		}
		r.URL.RawQuery = q.Encode()
	}
	var create struct {
		From *struct {
			Sandbox string `json:"sandbox"`
		} `json:"from"`
		Labels map[string]string `json:"labels"`
	}
	isCreate := r.Method == "POST" && r.URL.Path == "/sbx/sandboxes"
	if isCreate {
		_ = json.Unmarshal(body, &create)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	k.next.ServeHTTP(rec, r)
	out := rec.Body.Bytes()
	if isCreate && rec.Code < 300 && (create.From != nil || create.Labels[taskLabel] != "") {
		var box struct {
			ID      string `json:"id"`
			Workdir string `json:"workdir"`
		}
		_ = json.Unmarshal(out, &box)
		src := k.primary
		if create.From != nil {
			src = create.From.Sandbox
		}
		k.mu.Lock()
		if k.root == "" {
			k.root = filepath.Dir(filepath.Dir(box.Workdir))
		}
		_, known := k.alias[box.ID]
		k.alias[box.ID] = src
		k.mu.Unlock()
		if !known && create.From != nil {
			repointTree(k.dir(box.ID), k.dir(src)+"/", k.dir(box.ID)+"/")
		}
	}
	k.mu.Lock()
	for fork, src := range k.alias {
		out = bytes.ReplaceAll(out, []byte(k.dir(fork)+"/"), []byte(k.dir(src)+"/"))
	}
	k.mu.Unlock()
	for key, vs := range rec.Header() {
		if key != "Content-Length" {
			w.Header()[key] = vs
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(out)
}

// repointTree rewrites from → to in the git metadata under dir (a clone's
// copy of its source's absolute paths).
func repointTree(dir, from, to string) {
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return nil
		}
		switch info.Name() {
		case ".git", "gitdir", "alternates", "commondir", "config", ".task-env", "env":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && bytes.Contains(b, []byte(from)) {
			_ = os.WriteFile(p, bytes.ReplaceAll(b, []byte(from), []byte(to)), info.Mode())
		}
		return nil
	})
}

// --- the fixture --------------------------------------------------------------------------

// p2Fix is P1's project fixture (projFix) over a fake manager whose forks
// keep their paths, its provider's pull requests at K's fake, and the fork
// loop looking at once.
type p2Fix struct {
	*projFix
	keep *keepPaths
	scm2 p2SCM
}

func newP2Fix(t *testing.T) *p2Fix {
	t.Helper()
	oldFirst, oldEvery := forkBaseFirst.Load(), forkBaseEvery.Load()
	forkBaseFirst.Store(int64(time.Hour)) // a test runs the loop's sweeps itself
	t.Cleanup(func() { forkBaseFirst.Store(oldFirst); forkBaseEvery.Store(oldEvery) })
	ag, mux := accessFixture(t)
	kp := &keepPaths{alias: map[string]string{}}
	m := bindSbxWith(t, func(h http.Handler) http.Handler { kp.next = h; return kp }, "apps/cs")["apps/cs"]
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Egress: "internet"})
	kp.primary = box.ID
	kp.root = filepath.Dir(filepath.Dir(box.Workdir))
	f := newP1SCM(t)
	u, dir := bareOrigin(t)
	f.addRepo("acme/web", u, "main", nil)
	f.prov.AddRepo("acme/web", true)
	s2 := p2SCM{f}
	oldFor := scmFor
	scmFor = func(provider string) (scmAPI, error) {
		if provider != f.name {
			return nil, errScmUnbound
		}
		return s2, nil
	}
	oldBot := scmBotAllowed
	scmBotAllowed = func(who, string) bool { return true }
	oldDelay := projStreamDelay.Load()
	projStreamDelay.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { ciBG.Wait(); scmFor, scmBotAllowed = oldFor, oldBot; projStreamDelay.Store(oldDelay) })
	return &p2Fix{projFix: &projFix{ag: ag, mux: mux, m: m, scm: f, box: box, origin: u, odir: dir}, keep: kp, scm2: s2}
}

// realDir is where the fake keeps a path of sandbox id (a fork's paths are
// its source's, as the agent sees them).
func (fx *p2Fix) realDir(id, p string) string {
	fx.keep.mu.Lock()
	defer fx.keep.mu.Unlock()
	if src, ok := fx.keep.alias[id]; ok {
		return strings.Replace(p, fx.keep.dir(src)+"/", fx.keep.dir(id)+"/", 1)
	}
	return p
}

// pushOrigin commits a file to the origin's main (a commit after the fork
// base was taken).
func (fx *p2Fix) pushOrigin(t *testing.T, name, text string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "w")
	gitRun(t, filepath.Dir(work), "clone", "-q", fx.odir, work)
	if err := os.WriteFile(filepath.Join(work, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "after the snapshot")
	gitRun(t, work, "push", "-q", "origin", "HEAD:main")
	return gitRun(t, work, "rev-parse", "HEAD")
}

// managerCalls are the fake manager's calls of method on a path ending in
// suffix.
func (fx *p2Fix) managerCalls(method, suffix string) []fsbCall {
	var out []fsbCall
	for _, c := range fx.m.Calls() {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// tokenPurposes are the purposes K's fake provider minted tokens for.
func (fx *p2Fix) tokenPurposes() []string {
	var out []string
	for _, rq := range fx.scm.prov.Requests("POST /token") {
		var b struct {
			Purpose string `json:"purpose"`
		}
		if json.Unmarshal([]byte(rq.Body), &b) == nil {
			out = append(out, b.Purpose)
		}
	}
	return out
}
