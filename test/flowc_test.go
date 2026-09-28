//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// An agent's flow with bx only (SC-AGENT-BX), in a real terminal session:
// the M1 variant. The session is an isolated one, so .xbin is masked as in
// any real session and bx reaches xbind with the session's own tile-scoped
// token.

// covers SC-AGENT-BX — the M1 variant, inside an isolated terminal session
// over /ws/term, with the session's token and no terminal on bx's stdin:
// read where saves go; pause live reload (bx says saves stop); an edit;
// reload now without --yes exits 4 and changes nothing, with --yes ships it;
// bx status; bx logs through GET /logs (.xbin is masked in the session);
// roll back; resume (bx says saves reach main again and the tile is back to
// plain live reload); --json prints one JSON object.
func TestAgentBxLiveReload(t *testing.T) {
	t.Parallel()
	bxDir := buildBx(t)
	d := startIsolatedDaemon(t, isoOpts{Env: []string{"XBIN_BIN=" + bxDir}})
	a := d.dl()
	const tile = "apps/flow-m1"
	writeProbe(t, d.WS, tile, "m1")
	waitProbe(t, d, tile, "m1")

	s := openTerm(t, a, tile)
	bx := s.installBx(t, bxDir)
	if out, _ := s.run(t, `printf '%s|%s' "$XBIN_COMPONENT" "${XBIN_TOKEN:+set}"`, 10*time.Second); out != tile+"|set" {
		t.Fatalf("the session's env: %q, want its tile and a token", out)
	}
	if _, rc := s.run(t, `test -e "$XBIN_WORKSPACE/.xbin/log"`, 10*time.Second); rc == 0 {
		t.Fatal(".xbin/log is visible in the session: bx logs would read the file, not GET /logs")
	}
	step := func(what, args string, want int) string {
		t.Helper()
		out, rc := s.run(t, bx+" "+args+" </dev/null 2>&1", 5*time.Minute)
		if rc != want {
			t.Errorf("%s: bx %s exited %d, want %d:\n%s", what, args, rc, want, out)
		}
		return out
	}

	if out := step("the state", "live-reload", 0); !strings.Contains(out, tile) || !strings.Contains(out, "every save reaches everyone") {
		t.Errorf("bx live-reload before pausing:\n%s", out)
	}
	if out := step("pause", "live-reload pause", 0); !strings.Contains(out, "Live reload paused") || !strings.Contains(out, "Saves stop reaching main") {
		t.Errorf("bx live-reload pause didn't say where saves go:\n%s", out)
	}
	if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") == "" {
		t.Fatalf("after bx live-reload pause: live reload %q, main %q", st.LiveReload, st.pinned("main"))
	}

	writeProbe(t, d.WS, tile, "m2")
	entries := len(a.log(t, tile))
	if out := step("reload now, unconfirmed", "live-reload now", 4); !strings.Contains(out, "--yes") {
		t.Errorf("bx live-reload now without --yes and without a terminal didn't ask for --yes:\n%s", out)
	}
	if n := len(a.log(t, tile)); n != entries {
		t.Errorf("an unconfirmed reload now changed the deploy log: %d entries, was %d", n, entries)
	}
	a.waitServed(t, tile, "go", "m1", 10*time.Second)
	step("reload now", "live-reload now --yes", 0)
	a.waitServed(t, tile, "go", "m2", time.Minute)

	if out := step("status", "status", 0); !strings.Contains(out, tile) || !strings.Contains(out, "backend") {
		t.Errorf("bx status:\n%s", out)
	}
	step("logs", "logs "+tile, 0)

	step("rollback", "rollback --to main --yes", 0)
	a.waitServed(t, tile, "go", "m1", time.Minute)

	out := step("resume", "live-reload resume --yes", 0)
	if !strings.Contains(out, "saves reach main again") || !strings.Contains(out, "back to plain live reload") {
		t.Errorf("bx live-reload resume didn't say where saves go:\n%s", out)
	}
	a.waitServed(t, tile, "go", "m2", time.Minute)
	if st := a.state(t, tile); st.Record {
		t.Errorf("after bx live-reload resume the tile kept its record: %+v", st)
	}
	if out := step("--json", "live-reload --json", 0); !json.Valid([]byte(out)) {
		t.Errorf("bx live-reload --json printed more than one JSON object:\n%s", out)
	}
}

// buildBx builds this tree's bx for the test, static (CGO_ENABLED=0) so it
// runs on a terminal sandbox's rootfs too, and returns its directory.
func buildBx(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "bx"), "./cmd/bx")
	cmd.Dir, cmd.Env = repo, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/bx: %v\n%s", err, out)
	}
	return dir
}

// runBx runs the bx in bxDir on the host against d (no terminal on stdin,
// no tile of its own unless tile says so) and returns its exit code and
// its merged output.
func runBx(t *testing.T, d *isoDaemon, bxDir, tile string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(bxDir, "bx"), args...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "XBIN_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "XBIN_URL="+d.URL, "XBIN_WORKSPACE="+d.WS)
	if d.Token != "" {
		cmd.Env = append(cmd.Env, "XBIN_TOKEN="+d.Token)
	}
	if tile != "" {
		cmd.Env = append(cmd.Env, "XBIN_COMPONENT="+tile)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	} else if err != nil {
		t.Fatalf("bx %v: %v", args, err)
	}
	return 0, out.String()
}

// termSession is one terminal session over /ws/term, driven as a person
// drives a shell: a line typed, its output read back.
type termSession struct {
	conn  *websocket.Conn
	id    string
	mu    sync.Mutex
	out   []byte
	more  chan struct{}
	read  int // how much of out earlier commands consumed
	calls atomic.Int64

	// deployment is the target the session frame echoed ("" = none: the
	// session follows the primary, or this xbind can't target one).
	deployment string
}

// openTerm opens a new session on tile (host networking, so $XBIN_URL is
// the daemon's own listener) and quiets its shell: no echo, no prompt. The
// session ends with the test.
func openTerm(t *testing.T, a dlAPI, tile string) *termSession {
	t.Helper()
	return openTermAt(t, a, tile, "")
}

// openTermAt is openTerm for a session whose target is deployment dep of
// tile, asked for with ?deployment= as the terminal window's tile-API select
// asks (11-contract §7.4); "" asks for none. The echo is s.deployment.
func openTermAt(t *testing.T, a dlAPI, tile, dep string) *termSession {
	t.Helper()
	hdr := http.Header{}
	if a.token != "" {
		hdr.Set("Authorization", "Bearer "+a.token)
	}
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/ws/term?net=host&cwd=" + url.QueryEscape(tile)
	if dep != "" {
		u += "&deployment=" + url.QueryEscape(dep)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("/ws/term on %s: %v (%d)", tile, err, code)
	}
	s := &termSession{conn: conn, more: make(chan struct{}, 1)}
	ready := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			kind, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage {
				var c struct {
					Op         string `json:"op"`
					ID         string `json:"id"`
					Deployment string `json:"deployment"`
				}
				if json.Unmarshal(msg, &c) == nil && c.Op == "session" {
					once.Do(func() { s.id, s.deployment = c.ID, c.Deployment; close(ready) })
				}
				continue
			}
			s.mu.Lock()
			s.out = append(s.out, msg...)
			s.mu.Unlock()
			select {
			case s.more <- struct{}{}:
			default:
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(time.Minute):
		t.Fatal("/ws/term sent no session frame")
	}
	t.Cleanup(func() {
		conn.Close()
		a.do("DELETE", "/ws/term?session="+s.id, "")
	})
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"resize","cols":400,"rows":50}`))
	if _, rc := s.run(t, "stty -echo; PS1=''; PS2=''; unset PROMPT_COMMAND", time.Minute); rc != 0 {
		t.Fatalf("quieting the session's shell: exit %d", rc)
	}
	return s
}

// ansi matches the terminal's escape sequences, which run strips.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]|\x1b[=>]`)

// run types cmd into the session's shell and returns its output (escape
// sequences and carriage returns stripped, trimmed) and exit status. The
// markers are spelled with an empty quote inside, so the typed line never
// matches them, echoed or not.
func (s *termSession) run(t *testing.T, cmd string, timeout time.Duration) (string, int) {
	t.Helper()
	n := s.calls.Add(1)
	begin, end := fmt.Sprintf("__XB%d__", n), fmt.Sprintf("__XE%d__", n)
	line := fmt.Sprintf("echo '__XB'%d__; %s; echo '__XE'%d__=$?\n", n, cmd, n)
	if err := s.conn.WriteMessage(websocket.BinaryMessage, []byte(line)); err != nil {
		t.Fatalf("typing into the session: %v", err)
	}
	done := regexp.MustCompile(regexp.QuoteMeta(end) + `=(\d+)`)
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		text := strings.ReplaceAll(ansi.ReplaceAllString(string(s.out[s.read:]), ""), "\r", "")
		consumed := len(s.out)
		s.mu.Unlock()
		if m := done.FindStringSubmatchIndex(text); m != nil {
			start := strings.Index(text, begin+"\n")
			if start < 0 || start > m[0] {
				start = 0
			} else {
				start += len(begin) + 1
			}
			rc, _ := strconv.Atoi(text[m[2]:m[3]])
			s.mu.Lock()
			s.read = consumed
			s.mu.Unlock()
			return strings.TrimSpace(text[start:m[0]]), rc
		}
		select {
		case <-s.more:
		case <-deadline:
			t.Fatalf("the session never finished %q; it printed:\n%s", cmd, text)
		}
	}
}

// installBx puts this tree's bx where the session can run it and returns
// its path there. A namespace terminal's PATH is the rootfs's, whose bx is
// the image's own build, not this tree's; the session user's $HOME is
// mounted read-write at its host path, so the build goes there.
func (s *termSession) installBx(t *testing.T, bxDir string) string {
	t.Helper()
	home, rc := s.run(t, `printf '%s' "$HOME"`, 10*time.Second)
	if rc != 0 || !filepath.IsAbs(home) {
		t.Fatalf("the session's $HOME: %q (exit %d)", home, rc)
	}
	b, err := os.ReadFile(filepath.Join(bxDir, "bx"))
	must(t, err)
	dst := filepath.Join(home, ".bx-itest", "bx")
	must(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	must(t, os.WriteFile(dst, b, 0o755))
	if out, rc := s.run(t, dst+" help >/dev/null 2>&1; echo $?", 30*time.Second); rc != 0 || out == "126" || out == "127" {
		t.Fatalf("the session can't run %s: %q (exit %d)", dst, out, rc)
	}
	return dst
}

// covers P4 P21 P24 SC-AGENT-BX SC-PROTECT — flow C with the real bx, inside
// an isolated terminal session over /ws/term whose target is dev (the
// tile-API select's dev entry, echoed by the session frame), on an auth-on
// daemon: .xbin is masked, and bx reaches xbind with the session's own
// token. SC-AGENT-BX's seven steps: (1) bx deployment ls reads the
// deployments, the primary, main's pinned checkpoint, where live reload
// goes and this session's target; (2) bx status and bx logs show dev's
// build and log, the log through GET /logs; (3) curl reaches dev's API as
// $XBIN_URL/api/$XBIN_COMPONENT/, answered by dev; (4) bx deployment run-now
// delivers dev's cron job (nightly, active for dev) once, to dev; (5) bx deployment diff shows
// what a promotion would change against main's checkpoint; (6) bx promote
// dev → main ships dev's code at parity (without --yes and a terminal it
// exits 4 and changes nothing), and bx rollback puts main's back; (7) with
// the primary protected by a manager (the owner, in a person's own session),
// bx promote is refused: exit 3, a message naming the protection, and
// nothing changed.
func TestAgentFlowCWithBxOnly(t *testing.T) {
	t.Parallel()
	bxDir := buildBx(t)
	d := startIsolatedDaemon(t, isoOpts{Auth: true, Env: []string{"XBIN_BIN=" + bxDir}})
	a := d.dl()
	const tile = "apps/flow-c"
	writeFab(t, d.WS, tile, "m1", fabTile{})
	fabWait(t, a, tile, "m1")
	if _, e := a.op(t, "add", tile, "deployment", "dev", "attach", true); e.Result != "ok" {
		t.Fatalf("adding dev with live reload attached: %+v", e)
	}
	writeFab(t, d.WS, tile, "m2", fabTile{})
	fabWait(t, a, tile+"+dev", "m2")
	fabWait(t, a, tile, "m1")
	mainCP := a.state(t, tile).pinned("main")
	// dev's own code registers a job: active for dev (P13), nightly, so no
	// tick lands during the test.
	if r := fabHTTP(t, a, tile+"+dev", "PUT", "/api/xbin/cron/jobs", fabJSON(map[string]string{"name": "nightly",
		"resource": "res:" + tile + "/cron", "schedule": "0 3 * * *", "path": "/nightly", "role": "writer"})); r.Status != 200 ||
		strings.Contains(r.Body, `"dormant"`) {
		t.Fatalf("dev registering its job: %+v", r)
	}

	s := openTermAt(t, a, tile, "dev")
	if s.deployment != "dev" {
		t.Fatalf("the session frame echoed target %q, want dev", s.deployment)
	}
	bx := s.installBx(t, bxDir)
	if out, _ := s.run(t, `printf '%s|%s|%s' "$XBIN_COMPONENT" "$XBIN_DEPLOYMENT" "${XBIN_TOKEN:+set}"`, 10*time.Second); out != tile+"|dev|set" {
		t.Fatalf("the session's env: %q, want its tile, dev and a token", out)
	}
	if _, rc := s.run(t, `test -e "$XBIN_WORKSPACE/.xbin/log"`, 10*time.Second); rc == 0 {
		t.Fatal(".xbin/log is visible in the session: bx logs would read the file, not GET /logs")
	}
	step := func(what, args string, want int) string {
		t.Helper()
		out, rc := s.run(t, bx+" "+args+" </dev/null 2>&1", 5*time.Minute)
		if rc != want {
			t.Errorf("%s: bx %s exited %d, want %d:\n%s", what, args, rc, want, out)
		}
		return out
	}

	// 1. The state.
	out := step("the state", "deployment ls", 0)
	for _, want := range []string{"primary main", "live reload → dev", "pinned " + mainCP, "← this terminal"} {
		if !strings.Contains(out, want) {
			t.Errorf("bx deployment ls doesn't say %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "← this terminal") && !strings.HasPrefix(strings.TrimSpace(line), "dev ") {
			t.Errorf("bx deployment ls marks another deployment as this terminal's: %q", line)
		}
	}
	// 2. dev's build and logs.
	if out := step("status", "status", 0); !strings.Contains(out, tile+"+dev") || !strings.Contains(out, "healthy") ||
		!strings.Contains(out, "this terminal → dev") {
		t.Errorf("bx status for dev:\n%s", out)
	}
	if out := step("logs", "logs "+tile, 0); !strings.Contains(out, tile+"+dev") || !strings.Contains(out, "--- gen ") {
		t.Errorf("bx logs for dev (GET /logs?deployment=dev):\n%s", out)
	}
	// 3. dev's API with curl: the self-call routes to the session's target.
	curl := `curl -s -i -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/api/$XBIN_COMPONENT/v"`
	if out, rc := s.run(t, curl, time.Minute); rc != 0 || !strings.HasSuffix(out, "m2") ||
		!strings.Contains(strings.ToLower(out), "x-xbin-deployment: dev") {
		t.Errorf("curl of $XBIN_URL/api/$XBIN_COMPONENT/v: exit %d\n%s\nwant dev's m2, answered by dev", rc, out)
	}
	// 4. Run now: dev's job, delivered once, to dev.
	step("run now", "deployment run-now dev nightly", 0)
	hits := fabHits(t, a, tile+"+dev")
	if n := fabCount(hits, func(h fabHit) bool { return h.Path == "/nightly" && h.From == "xbin/cron" }); n != 1 {
		t.Errorf("run now reached dev %d times as xbin/cron, want once: %+v", n, hits)
	}
	if n := fabCount(fabHits(t, a, tile), fabPath("/nightly")); n != 0 {
		t.Errorf("run now of dev's job reached main %d times", n)
	}
	// 5. What a promotion would change, against main's checkpoint.
	if out := step("diff", "deployment diff", 0); !strings.Contains(out, `-const marker = "m1"`) || !strings.Contains(out, `+const marker = "m2"`) {
		t.Errorf("bx deployment diff (main's checkpoint → the work tree dev follows):\n%s", out)
	}
	// 6. Promote at parity: unconfirmed, nothing; confirmed, main runs dev's
	// code. Then roll main back.
	entries := len(a.log(t, tile))
	if out := step("promote, unconfirmed", "promote dev main", 4); !strings.Contains(out, "--yes") {
		t.Errorf("bx promote without --yes and without a terminal didn't ask for --yes:\n%s", out)
	}
	if n := len(a.log(t, tile)); n != entries {
		t.Errorf("an unconfirmed promote changed the deploy log: %d entries, was %d", n, entries)
	}
	step("promote", "promote dev main --yes", 0)
	fabWait(t, a, tile, "m2")
	if st := a.state(t, tile); st.Primary != "main" || st.pinned("main") == mainCP || st.LiveReload != "dev" {
		t.Errorf("after the promote: primary %q, main pinned %q (was %q), live reload %q", st.Primary, st.pinned("main"), mainCP, st.LiveReload)
	}
	step("rollback", "rollback --to main --yes", 0)
	fabWait(t, a, tile, "m1")
	if st := a.state(t, tile); st.pinned("main") != mainCP {
		t.Errorf("after the rollback main runs %q, want %q", st.pinned("main"), mainCP)
	}
	// 7. The primary protected by a manager: refused, exit 3, nothing moved.
	a.mustPost(t, "protect", dlBody(tile, "on", true))
	before, entries := a.state(t, tile), len(a.log(t, tile))
	if out := step("promote onto a protected primary", "promote dev main --yes", 3); !strings.Contains(out, "protected") {
		t.Errorf("bx promote onto the protected primary didn't name the protection:\n%s", out)
	}
	after := a.state(t, tile)
	if after.Seq != before.Seq || after.pinned("main") != before.pinned("main") || len(a.log(t, tile)) != entries {
		t.Errorf("a refused promote changed something: seq %d → %d, main %q → %q, log %d → %d entries",
			before.Seq, after.Seq, before.pinned("main"), after.pinned("main"), entries, len(a.log(t, tile)))
	}
	fabWait(t, a, tile, "m1")
}

// ---- primary first (P25) ----

// pfCgroupBase is the cgroup directory xbind delegates its tiles' leaves
// under (its own, which it left for an "init" leaf), "" while d's cgroup
// accounting is off (/sandboxes says so).
func pfCgroupBase(t *testing.T, d *isoDaemon) string {
	t.Helper()
	var sb struct {
		Cgroup bool `json:"cgroup"`
	}
	if c, b := d.do(t, "GET", "/api/xbin/sandboxes", ""); c != 200 || json.Unmarshal([]byte(b), &sb) != nil {
		t.Fatalf("/sandboxes: %d %.300s", c, b)
	}
	if !sb.Cgroup {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", d.cmd.Process.Pid))
	must(t, err)
	_, rel, ok := strings.Cut(strings.TrimSpace(string(b)), "::")
	if !ok || filepath.Base(rel) != "init" {
		t.Fatalf("xbind's cgroup %q isn't its delegated init leaf", b)
	}
	return filepath.Join("/sys/fs/cgroup", filepath.Dir(rel))
}

// pfDelegated restarts d inside a transient systemd scope that delegates
// its cgroup (Delegate=yes), where xbind is alone and can make its leaves,
// when the host's user manager can make one; the test binary's own cgroup,
// which xbind otherwise shares, never can. It reports why not otherwise.
func pfDelegated(t *testing.T, d *isoDaemon) string {
	t.Helper()
	run, err := exec.LookPath("systemd-run")
	if err != nil {
		return "no systemd-run to delegate a cgroup to xbind"
	}
	if out, err := exec.Command(run, "--user", "--scope", "-p", "Delegate=yes", "--quiet", "--", "true").CombinedOutput(); err != nil {
		return fmt.Sprintf("the user manager can't delegate a cgroup (systemd-run: %v %s)", err, bytes.TrimSpace(out))
	}
	wrap := filepath.Join(t.TempDir(), "xbind-delegated")
	script := "#!/bin/sh\nexec " + strconv.Quote(run) + " --user --scope -p Delegate=yes --quiet -- " + strconv.Quote(d.Bin) + " \"$@\"\n"
	must(t, os.WriteFile(wrap, []byte(script), 0o755))
	d.stop(t)
	d.Bin = wrap
	d.start(t)
	return ""
}

// pfLeaf is the cgroup directory of tile's backend generation of dep (""
// = main) under base, from its sandbox row, waiting (bounded) for one.
func pfLeaf(t *testing.T, a dlAPI, base, tile, dep string) string {
	t.Helper()
	var leaf string
	waitFor(func() bool {
		_, body := a.do("GET", "/api/xbin/sandboxes?tile="+tile, "")
		var out struct {
			Sandboxes []struct {
				Kind       string `json:"kind"`
				Deployment string `json:"deployment"`
				Leaf       string `json:"leaf"`
			} `json:"sandboxes"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		for _, s := range out.Sandboxes {
			if s.Kind == "backend" && s.Deployment == dep && s.Leaf != "" {
				leaf = s.Leaf
				return true
			}
		}
		return false
	}, time.Minute)
	switch {
	case leaf == "":
		t.Fatalf("no backend row of %s's %q with a cgroup leaf", tile, dep)
	case !strings.Contains(leaf, "/"):
		return filepath.Join(base, "comp-"+leaf) // the flat leaf
	}
	return filepath.Join(base, leaf)
}

// pfRead is a cgroup file's value, trimmed.
func pfRead(t *testing.T, dir, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, file))
	must(t, err)
	return strings.TrimSpace(string(b))
}

// pfEvent is one counter of a cgroup's memory.events.
func pfEvent(t *testing.T, dir, name string) int64 {
	t.Helper()
	for _, line := range strings.Split(pfRead(t, dir, "memory.events"), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == name {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}

// pfLimits is each deployment's effective limits, from the state.
func pfLimits(t *testing.T, a dlAPI, tile string) map[string]struct{ MemMiB, Pids int64 } {
	t.Helper()
	var st struct {
		Deployments []struct {
			Name   string `json:"name"`
			Limits struct {
				MemMiB int64 `json:"memMiB"`
				Pids   int64 `json:"pids"`
			} `json:"limits"`
		} `json:"deployments"`
	}
	if c, b := a.do("GET", "/api/xbin/deployments?tile="+tile, ""); c != 200 || json.Unmarshal([]byte(b), &st) != nil {
		t.Fatalf("the state of %s: %d %.300s", tile, c, b)
	}
	out := map[string]struct{ MemMiB, Pids int64 }{}
	for _, d := range st.Deployments {
		out[d.Name] = struct{ MemMiB, Pids int64 }{d.Limits.MemMiB, d.Limits.Pids}
	}
	return out
}

// covers P25 SC-PRIMARY-FIRST — a dev deployment never starves its primary.
// Memory: dev's limit lowered to 64 MiB by a tile manager, each deployment
// in a leaf of its own (the primary's node at the higher CPU weight), dev's
// backend allocates past its cap and hits it, while main answers every
// request and keeps the tile's caps. VMs: the tile's non-primary guests
// fill the budget up to the primary's headroom, the primary's guest
// crashes and restarts, and one more non-primary start is the one refused,
// recorded as refused in the sandbox failure ring. Each half skips, saying
// why, without delegated cgroups (xbind is restarted in a delegating systemd
// scope when the host can make one) or without VM support. Low disk's
// order (non-primary namespaces write-blocked first) isn't run end to end.
func TestPrimaryFirstUnderPressure(t *testing.T) {
	t.Parallel()
	bxDir := buildBx(t) // a VM guest's host side is bx (__vm-host)
	d := startIsolatedDaemon(t, isoOpts{Env: []string{"XBIN_BIN=" + bxDir}})
	a := d.dl()
	base, noCgroup := pfCgroupBase(t, d), ""
	if base == "" {
		if noCgroup = pfDelegated(t, d); noCgroup == "" {
			if base = pfCgroupBase(t, d); base == "" {
				noCgroup = "xbind found no delegated cgroup even in a Delegate=yes scope"
			}
		}
	}
	t.Run("memory", func(t *testing.T) {
		if base == "" {
			t.Skip("no delegated cgroups: " + noCgroup)
		}
		const tile = "apps/pf-mem"
		writeFab(t, d.WS, tile, "m1", fabTile{})
		fabWait(t, a, tile, "m1")
		if _, e := a.op(t, "add", tile, "deployment", "dev"); e.Result != "ok" {
			t.Fatalf("adding dev: %+v", e)
		}
		a.mustPost(t, "limits", dlBody(tile, "deployment", "dev", "limits", map[string]any{"memMiB": 64}))
		for _, dep := range []string{"dev", "main"} { // the next generation of each takes its own leaf
			a.op(t, "deploy", tile, "deployment", dep, "restart", true)
		}
		fabWait(t, a, tile+"+dev", "m1")
		fabWait(t, a, tile, "m1")
		lim := pfLimits(t, a, tile)
		if lim["dev"].MemMiB != 64 || lim["main"].MemMiB <= 64 {
			t.Fatalf("effective limits: %+v, want dev at 64 MiB below main's", lim)
		}
		mainLeaf, devLeaf := pfLeaf(t, a, base, tile, ""), pfLeaf(t, a, base, tile, "dev")
		if mainLeaf == devLeaf {
			t.Fatalf("main and dev share the leaf %s", mainLeaf)
		}
		if got := pfRead(t, devLeaf, "memory.max"); got != strconv.Itoa(64<<20) {
			t.Errorf("dev's memory.max: %s, want 64 MiB", got)
		}
		mainMax := strconv.FormatInt(lim["main"].MemMiB<<20, 10)
		if got := pfRead(t, mainLeaf, "memory.max"); got != mainMax {
			t.Errorf("main's memory.max: %s, want the tile's %s", got, mainMax)
		}
		mw, _ := strconv.Atoi(pfRead(t, filepath.Dir(mainLeaf), "cpu.weight"))
		if dw, _ := strconv.Atoi(pfRead(t, filepath.Dir(devLeaf), "cpu.weight")); mw <= dw {
			t.Errorf("cpu.weight: main's node %d, dev's %d, want the primary's higher", mw, dw)
		}

		// dev allocates twice its cap while main is asked every 100 ms.
		stop, failed := make(chan struct{}), make(chan string, 100)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(100 * time.Millisecond):
				}
				if c, b := a.do("GET", "/api/"+tile+"/v", ""); c != 200 || b != "m1" {
					select {
					case failed <- fmt.Sprintf("%d %q", c, b):
					default:
					}
				}
			}
		}()
		c, b := a.do("GET", "/api/"+tile+"+dev/act/alloc?mib=128", "")
		close(stop)
		wg.Wait()
		close(failed)
		for f := range failed {
			t.Errorf("main failed a request while dev was at its cap: %s", f)
		}
		// On a host with swap the leaf is throttled at its soft ceiling
		// (memory.high, 7/8 of the cap) and swapped rather than OOM-killed:
		// either way dev met its own cap, never more.
		if n := pfEvent(t, devLeaf, "high") + pfEvent(t, devLeaf, "max") + pfEvent(t, devLeaf, "oom_kill"); n == 0 {
			t.Errorf("dev never reached its cap (alloc answered %d %.200s): memory.events %s", c, b, pfRead(t, devLeaf, "memory.events"))
		}
		if got := pfRead(t, mainLeaf, "memory.max"); got != mainMax {
			t.Errorf("main's memory.max after dev's pressure: %s, want %s", got, mainMax)
		}
		if n := pfEvent(t, mainLeaf, "max") + pfEvent(t, mainLeaf, "oom_kill"); n != 0 {
			t.Errorf("main's leaf hit its cap %d times", n)
		}
		fabWait(t, a, tile, "m1")
	})
	t.Run("vm", func(t *testing.T) {
		pfVMHalf(t, d)
	})
	t.Log("not run end to end: low disk, which write-blocks non-primary namespaces first (the partition " +
		"holding the workspace must fall below 10% free, which a test can't arrange without privileges or " +
		"filling a shared filesystem; TestDiskQuotaCountsDeploymentData holds the order)")
}

// pfVMHalf is TestPrimaryFirstUnderPressure's VM half on d: a policy of
// three 256 MiB guests; tile pf-vm's primary runs one and dev1 a second,
// which fills the budget up to the headroom a non-primary deployment leaves
// for its primary's next guest. The primary's guest is killed and restarts
// (primary first: it is admitted), keeping its size, and dev2's start is the
// one refused, recorded as refused in the sandbox failure ring. It skips,
// saying why, where VMs can't run.
func pfVMHalf(t *testing.T, d *isoDaemon) {
	t.Helper()
	a := d.dl()
	var vmst struct {
		Status struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"status"`
	}
	if c, b := a.do("GET", "/api/xbin/vm", ""); c != 200 || json.Unmarshal([]byte(b), &vmst) != nil {
		t.Fatalf("GET /vm: %d %.300s", c, b)
	}
	if !vmst.Status.Available {
		t.Skip("no VM support: " + vmst.Status.Reason)
	}
	fabMust200(t, a, "PUT", "/api/xbin/vm/policy", `{"backends":true,"memMiB":256,"vcpus":1,"maxVMs":3,"budgetMiB":768}`)
	const tile = "apps/pf-vm"
	writeFab(t, d.WS, tile, "m1", fabTile{extra: `,"vm":true`})
	fabWait(t, a, tile, "m1")
	for _, dep := range []string{"dev1", "dev2"} {
		if _, e := a.op(t, "add", tile, "deployment", dep); e.Result != "ok" {
			t.Fatalf("adding %s: %+v", dep, e)
		}
	}
	fabWait(t, a, tile+"+dev1", "m1")
	type vmRow struct {
		Kind       string `json:"kind"`
		Deployment string `json:"deployment"`
		Mode       string `json:"mode"`
		MemMiB     int    `json:"memMiB"`
		PID        int    `json:"pid"`
		Gen        int    `json:"gen"`
	}
	rows := func() (out []vmRow) {
		_, body := a.do("GET", "/api/xbin/sandboxes?tile="+tile, "")
		var sb struct{ Sandboxes []vmRow }
		_ = json.Unmarshal([]byte(body), &sb)
		for _, r := range sb.Sandboxes {
			if r.Kind == "backend" {
				out = append(out, r)
			}
		}
		return out
	}
	primaryRow := func() (vmRow, bool) {
		for _, r := range rows() {
			if r.Deployment == "" {
				return r, true
			}
		}
		return vmRow{}, false
	}
	before, ok := primaryRow()
	if !ok || before.Mode != "vm" || before.MemMiB != 256 || before.PID == 0 {
		t.Fatalf("the primary's guest: %+v (listed %v), want a 256 MiB VM", before, ok)
	}

	// The primary's guest crashes; the next request restarts it, admitted.
	must(t, syscall.Kill(before.PID, syscall.SIGKILL))
	if !waitFor(func() bool { r, ok := primaryRow(); return !ok || r.Gen != before.Gen }, time.Minute) {
		t.Fatalf("the primary's guest (gen %d) never went after SIGKILL", before.Gen)
	}
	fabWait(t, a, tile, "m1")
	after, ok := primaryRow()
	if !ok || after.Gen == before.Gen || after.Mode != "vm" || after.MemMiB != before.MemMiB {
		t.Errorf("the primary after its crash: %+v, want a new generation of the same 256 MiB VM", after)
	}

	// One more non-primary start: refused, and recorded so.
	if c, b := a.do("GET", "/api/"+tile+"+dev2/v", ""); c == 200 {
		t.Errorf("dev2 started with the budget full up to the primary's headroom: %d %s", c, b)
	}
	var sb struct {
		Failures []struct {
			Tile, Deployment, Stage, Error string
		} `json:"failures"`
	}
	fb := fabMust200(t, a, "GET", "/api/xbin/sandboxes?tile="+tile, "")
	must(t, json.Unmarshal([]byte(fb), &sb))
	refused := false
	for _, f := range sb.Failures {
		if f.Tile == tile && f.Deployment == "dev2" && f.Stage == "refused" {
			refused = true
		}
		if f.Tile == tile && f.Deployment == "" && f.Stage == "refused" {
			t.Errorf("the primary was refused a guest: %+v", f)
		}
	}
	if !refused {
		t.Errorf("no refused failure recorded for dev2: %+v", sb.Failures)
	}
	fabWait(t, a, tile, "m1")
}
