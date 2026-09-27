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
}

// openTerm opens a new session on tile (host networking, so $XBIN_URL is
// the daemon's own listener) and quiets its shell: no echo, no prompt. The
// session ends with the test.
func openTerm(t *testing.T, a dlAPI, tile string) *termSession {
	t.Helper()
	hdr := http.Header{}
	if a.token != "" {
		hdr.Set("Authorization", "Bearer "+a.token)
	}
	u := "ws" + strings.TrimPrefix(a.url, "http") + "/ws/term?net=host&cwd=" + url.QueryEscape(tile)
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
					Op string `json:"op"`
					ID string `json:"id"`
				}
				if json.Unmarshal(msg, &c) == nil && c.Op == "session" {
					once.Do(func() { s.id = c.ID; close(ready) })
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
