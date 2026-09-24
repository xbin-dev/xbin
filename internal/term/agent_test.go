package term

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// The agent session end to end, non-isolated: the real `bx __agent-host`
// as a plain child, the scripted ACP agent (hack/fakeacp) as its child —
// both built once here.
var (
	fakeBin, bxBin string
	buildErr       error
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "xbin-agent-test-*")
	if err != nil {
		panic(err)
	}
	repo, _ := filepath.Abs("../..")
	fakeBin, bxBin = filepath.Join(dir, "fakeacp"), filepath.Join(dir, "bx")
	for _, b := range [][2]string{{fakeBin, "./hack/fakeacp"}, {bxBin, "./cmd/bx"}} {
		build := exec.Command("go", "build", "-o", b[0], b[1])
		build.Dir = repo
		if out, err := build.CombinedOutput(); err != nil {
			buildErr = errors.New("build " + b[1] + ": " + string(out))
			break
		}
	}
	os.Setenv(agent.FakeEnv, fakeBin)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type agentRig struct {
	m      *Manager
	root   string
	events chan SessionEvent
	change chan string
}

func newAgentRig(t *testing.T) *agentRig {
	t.Helper()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &agentRig{m: NewManager(root, nil), root: root, events: make(chan SessionEvent, 4096), change: make(chan string, 64)}
	r.m.BxPath = bxBin
	r.m.OnEvent = func(cwd string, ev SessionEvent) { r.events <- ev }
	r.m.OnChange = func(op, homeKey, id, cwd string) { r.change <- op + ":" + id }
	return r
}

// until drains live events until one satisfies pred (5 s).
func (r *agentRig) until(t *testing.T, pred func(e SessionEvent) bool) SessionEvent {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case e := <-r.events:
			if pred(e) {
				return e
			}
		case <-deadline:
			t.Fatal("timed out waiting for an event")
		}
	}
}

func ofType(typ string) func(e SessionEvent) bool {
	return func(e SessionEvent) bool { return e.Type == typ }
}

func edata(e agent.Event) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(e.Data, &m)
	return m
}

func TestAgentSessionEndToEnd(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	// the user's home carries the CLI's settings; the agent must see them
	home := HomeDir(r.root, "owner")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"permissions":{"defaultMode":"plan"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{Owner: true}
	info, code, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "my agent", "", map[string]string{"model": "fake-fast"})
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	if info.Kind != KindAgent || info.Provider != "fake" || info.Mode != "ask" || info.Status != agent.StatusStarting || info.Name != "my agent" {
		t.Fatalf("info: %+v", info)
	}
	id := info.ID
	if op := <-r.change; op != "open:"+id {
		t.Fatalf("directory told %q", op)
	}
	// the handshake lands as status events with increasing seq, owned by the creator
	e := r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if e.User != "owner" || e.ID != id || e.Seq < 2 {
		t.Fatalf("idle event: %+v", e)
	}
	rows := m.ListFor("owner", "apps/x", nil)
	if len(rows) != 1 || rows[0].Kind != KindAgent || rows[0].Status != agent.StatusIdle || rows[0].Model != "fake-fast" {
		t.Fatalf("directory row (the requested model shows): %+v", rows)
	}

	// env: the key from the vault is in the agent's environ, HOME is the
	// per-user home, the settings file is readable through fs/read_text_file
	ctx := context.Background()
	turn, err := m.AgentPrompt(ctx, id, "env")
	if err != nil || turn != 1 {
		t.Fatalf("prompt: %d %v", turn, err)
	}
	if _, err := m.AgentPrompt(ctx, id, "again"); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("a second prompt mid-turn: %v", err)
	}
	e = r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent" })
	text, _ := edata(e.Event)["text"].(string)
	// the agent runs with the per-user home, so its login/settings carry over;
	// there is no injected API key (key=no) — auth is the home's, like a shell's
	if !strings.Contains(text, "HOME="+home) || !strings.Contains(text, "key=no") || !strings.Contains(text, `"defaultMode":"plan"`) || !strings.Contains(text, "model=fake-fast") {
		t.Fatalf("the agent's view of its home and the requested model: %q", text)
	}
	e = r.until(t, ofType(agent.EvTurnEnd))
	if d := edata(e.Event); d["stopReason"] != "end_turn" || d["turn"] != float64(1) || d["usage"] == nil {
		t.Fatalf("turn.end: %v", d)
	}

	// the agent titled the session after the first turn: the unnamed tab took it — but ours is named
	if info, _ := m.Info(id); info.Name != "my agent" {
		t.Fatalf("a user-named tab keeps its name over the agent's title: %q", info.Name)
	}

	// a setting changed mid-session applies to the next turn and shows in the row
	if err := m.AgentSetOption(ctx, id, "model", "fake-default"); err != nil {
		t.Fatal(err)
	}
	if err := m.AgentSetOption(ctx, id, "model", "bogus"); err == nil {
		t.Fatal("a value the agent refuses was accepted")
	}
	if _, err := m.AgentPrompt(ctx, id, "env again"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent" })
	if text, _ := edata(e.Event)["text"].(string); !strings.Contains(text, "model=fake-default") {
		t.Fatalf("the next turn ran on the new model: %q", text)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	if info, _ := m.Info(id); info.Model != "fake-default" {
		t.Fatalf("row model: %+v", info)
	}

	// a permission: the first answer wins, the turn completes
	if _, err := m.AgentPrompt(ctx, id, "perm"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, ofType(agent.EvPermissionRequest))
	pid, _ := edata(e.Event)["pid"].(string)
	// the row's status follows the status event that comes right AFTER the
	// request (the client emits the request, then setStatus(waiting)) — wait
	// for it rather than read the row the instant the request lands (a CI flake)
	var row SessionInfo
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if row, _ = m.Info(id); (row.Status == agent.StatusWaiting && row.Pending == 1) || time.Now().After(deadline) {
			break
		}
	}
	if row.Status != agent.StatusWaiting || row.Pending != 1 {
		t.Fatalf("waiting: %+v", row)
	}
	if pend, _ := m.AgentPending(id); len(pend) != 1 || pend[0].PID != pid || pend[0].ToolCall.Title != "run ls" {
		t.Fatalf("pending: %+v", pend)
	}
	if err := m.AgentPermit(id, pid, "", agent.AllowOnce, "user:dev1"); err != nil {
		t.Fatal(err)
	}
	if err := m.AgentPermit(id, pid, "", agent.AllowOnce, "user:dev2"); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("second answer: %v", err)
	}
	e = r.until(t, ofType(agent.EvPermissionResolved))
	if d := edata(e.Event); d["by"] != "user:dev1" || d["optionId"] != "once" {
		t.Fatalf("resolved: %v", d)
	}
	e = r.until(t, ofType(agent.EvToolUpdate))
	if edata(e.Event)["status"] != "completed" {
		t.Fatalf("tool: %v", edata(e.Event))
	}
	e = r.until(t, ofType(agent.EvTurnEnd))
	if edata(e.Event)["stopReason"] != "end_turn" {
		t.Fatalf("turn: %v", edata(e.Event))
	}

	// "allow for session": the next request of the same kind is auto-answered
	if _, err := m.AgentPrompt(ctx, id, "perm"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, ofType(agent.EvPermissionRequest))
	pid2, _ := edata(e.Event)["pid"].(string)
	if err := m.AgentPermit(id, pid2, "", agent.AllowAlways, "user:dev1"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	if _, err := m.AgentPrompt(ctx, id, "perm"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, ofType(agent.EvPermissionResolved))
	if edata(e.Event)["by"] != "auto" {
		t.Fatalf("the session rule did not apply: %v", edata(e.Event))
	}
	r.until(t, ofType(agent.EvTurnEnd))

	// terminal/*: served in the host; the key is not in the terminal's env
	if _, err := m.AgentPrompt(ctx, id, "term"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent" })
	if text, _ := edata(e.Event)["text"].(string); text != "term: hi 0" {
		t.Fatalf("terminal output: %q (the key must not leak into a terminal)", text)
	}
	r.until(t, ofType(agent.EvTurnEnd))

	// fs/write_text_file: under the tile
	if _, err := m.AgentPrompt(ctx, id, "write"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	if b, err := os.ReadFile(filepath.Join(r.root, "apps", "x", "fake-wrote.txt")); err != nil || string(b) != "written by fakeacp\n" {
		t.Fatalf("write: %q %v", b, err)
	}

	// a burst of deltas is coalesced; the text is intact
	if _, err := m.AgentPrompt(ctx, id, "burst"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	evs, next, truncated, err := m.AgentEvents(id, 0)
	if err != nil || truncated || next != evs[len(evs)-1].Seq {
		t.Fatalf("replay: %v %d %v", err, next, truncated)
	}
	xs, n := 0, 0
	seenTurn := false
	for _, e := range evs {
		if e.Type == agent.EvTurnEnd && edata(e)["turn"] == float64(7) {
			seenTurn = true
		}
		if e.Type == agent.EvMessageDelta && edata(e)["role"] == "agent" {
			if txt, _ := edata(e)["text"].(string); strings.Trim(txt, "x") == "" && txt != "" {
				xs += len(txt)
				n++
			}
		}
	}
	if !seenTurn || xs != 50 || n >= 50 {
		t.Fatalf("burst: %d x's in %d events (want 50 in fewer)", xs, n)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq != evs[i-1].Seq+1 {
			t.Fatalf("seq gap at %d", i)
		}
	}
	// replay from a cursor: exactly what followed it (a late event — the
	// fake's slash-command status — may have landed after evs was read)
	tail, _, _, _ := m.AgentEvents(id, evs[len(evs)-3].Seq)
	if len(tail) < 2 || tail[0].Seq != evs[len(evs)-2].Seq || tail[1].Seq != evs[len(evs)-1].Seq {
		t.Fatalf("since: %d events from seq %d", len(tail), evs[len(evs)-3].Seq)
	}

	// cancel mid-turn: the turn ends cancelled
	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent" })
	if err := m.AgentCancel(id); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, ofType(agent.EvTurnEnd))
	if edata(e.Event)["stopReason"] != "cancelled" {
		t.Fatalf("cancelled turn: %v", edata(e.Event))
	}
	if log, _ := m.AgentLog(id); !strings.Contains(log, "fakeacp: up") {
		t.Fatalf("the agent's stderr is the text log: %q", log)
	}

	// lastActive moved with the events (the reaper counts from it)
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	s.mu.Lock()
	moved := s.lastActive.After(s.born)
	s.mu.Unlock()
	if !moved {
		t.Fatal("lastActive did not move")
	}

	// kill: the session, the host and the agent are gone
	hostPid := s.cmd.Process.Pid
	if !m.Kill(id) {
		t.Fatal("kill")
	}
	waitClose(t, r.change, "close:"+id)
	if _, ok := m.Info(id); ok {
		t.Fatal("still listed")
	}
	if err := syscall.Kill(hostPid, 0); err == nil {
		t.Fatal("the host outlived the session")
	}
	if pids := procsRunning(fakeBin); len(pids) != 0 {
		t.Fatalf("the agent outlived the session: %v", pids)
	}
	if _, err := m.AgentPrompt(ctx, id, "x"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after the end: %v", err)
	}
}

func waitClose(t *testing.T, ch chan string, want string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case op := <-ch:
			if op == want {
				return
			}
		case <-deadline:
			t.Fatalf("no %s", want)
		}
	}
}

// procsRunning lists pids whose command line names bin.
func procsRunning(bin string) []int {
	var out []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid := 0
		for _, c := range e.Name() {
			if c < '0' || c > '9' {
				pid = -1
				break
			}
			pid = pid*10 + int(c-'0')
		}
		if pid <= 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(string(b), bin) {
			out = append(out, pid)
		}
	}
	return out
}

func TestAgentSessionGatesAndFailures(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	bob := auth.Principal{UserID: "bob", Via: "session",
		User: &users.User{ID: "bob", Role: "user", Tiles: map[string]string{"apps/x": users.LevelWrite}}}
	if _, code, err := m.OpenAgent(bob, "apps/x", "", "fake", "", "", "", nil); code != 403 || err == nil {
		t.Fatalf("write-level user: %d %v", code, err)
	}
	if _, code, _ := m.OpenAgent(auth.Principal{Owner: true}, "", "", "fake", "", "", "", nil); code != 403 {
		t.Fatalf("no cwd: %d", code)
	}
	if _, code, _ := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "nope", "", "", "", nil); code != 400 {
		t.Fatalf("unknown provider: %d", code)
	}
	if _, code, err := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "ludicrous", "", "", nil); code != 400 || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("unknown mode: %d %v", code, err)
	}
	m.BxPath = ""
	if _, code, _ := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil); code != 503 {
		t.Fatalf("no bx: %d", code)
	}
	m.BxPath = bxBin

	// a terminal token drives its OWN tile's sessions; another tile's, no
	termTok := auth.Principal{Component: "apps/x", UserID: "alice", Via: "terminal",
		User: &users.User{ID: "alice", Role: "user", Tiles: map[string]string{"apps/x": users.LevelTerminal}}}
	info, code, err := m.OpenAgent(termTok, "apps/x", "", "fake", "yolo", "", "", nil)
	if err != nil || code != 200 || info.Mode != "yolo" {
		t.Fatalf("from a terminal: %d %v %+v", code, err, info)
	}
	if err := m.MayDrive(info.ID, termTok); err != nil {
		t.Fatalf("drive own: %v", err)
	}
	other := termTok
	other.Component = "apps/y"
	if err := m.MayDrive(info.ID, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("drive from another tile's terminal: %v", err)
	}
	if err := m.MayDrive("nope", termTok); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown: %v", err)
	}
	// the browser session of the same user sees and drives it (same home)
	alice := auth.Principal{UserID: "alice", Via: "session", User: termTok.User}
	if rows := m.ListFor("alice", "", alice.CanTerminalTileVia); len(rows) != 1 || rows[0].ID != info.ID {
		t.Fatalf("alice's directory: %+v", rows)
	}
	if err := m.MayDrive(info.ID, alice); err != nil {
		t.Fatalf("alice drives: %v", err)
	}
	// yolo: no permission request
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := m.AgentPrompt(context.Background(), info.ID, "perm"); err != nil {
		t.Fatal(err)
	}
	e := r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvTurnEnd || e.Type == agent.EvPermissionRequest })
	if e.Type != agent.EvTurnEnd {
		t.Fatal("yolo asked for permission")
	}
	// the agent's title named this unnamed tab, and the directory was told
	waitClose(t, r.change, "rename:"+info.ID)
	if row, _ := m.Info(info.ID); row.Name != "fake: perm" {
		t.Fatalf("the agent's title names an unnamed tab: %q", row.Name)
	}
	// /ws/term refuses to attach to an agent session
	m.Kill(info.ID)
	waitClose(t, r.change, "close:"+info.ID)

	// the agent fails a turn: status error names the vault command; a crash ends the session
	info, _, err = m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := m.AgentPrompt(context.Background(), info.ID, "fail"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusError
	})
	if d, _ := edata(e.Event)["detail"].(string); !strings.Contains(d, "apps/x") || !strings.Contains(d, "terminal") || strings.Contains(d, "vault") {
		t.Fatalf("the auth error points at the home login, not a vault key: %q", d)
	}
	if _, err := m.AgentPrompt(context.Background(), info.ID, "crash"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusExited
	})
	waitClose(t, r.change, "close:"+info.ID)
}

func TestServeWSRefusesAgentSessions(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	s := stub(m, "a1", "alice", "apps/mine", time.Now())
	s.kind = KindAgent
	r := httptest.NewRequest("GET", "/ws/term?session=a1", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	m.ServeWS(w, r)
	if w.Code != 409 {
		t.Fatalf("attach to an agent session: %d %s", w.Code, w.Body.String())
	}
}

// History + resume (history.go): an ended session's transcript is kept on
// disk and listed; reopening it (resume) carries the provider/name over and
// replays the earlier turns; once that continuation took a prompt and ended,
// it supersedes the entry. FlushAgents (shutdown) keeps a live one too.
func TestAgentHistoryAndResume(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	owner := auth.Principal{Owner: true}
	ctx := context.Background()
	info, code, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "first", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	id := info.ID
	<-r.change // open
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	// a session that never took a prompt is not history
	if m.FlushAgents(); len(m.ListHistory("owner", "apps/x", nil)) != 0 {
		t.Fatal("an unprompted session must not be kept")
	}
	if _, err := m.AgentPrompt(ctx, id, "remember this"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	// a live session flushes (shutdown): the entry exists while it still runs
	m.FlushAgents()
	hist := m.ListHistory("owner", "apps/x", nil)
	if len(hist) != 1 || hist[0].ID != id || hist[0].Turns != 1 || hist[0].Preview != "remember this" || hist[0].Provider != "fake" ||
		hist[0].Name != "first" || !hist[0].Loadable || hist[0].ACPSessionID != "fake-1" {
		t.Fatalf("history after flush: %+v", hist)
	}
	// ending it writes it again; the transcript reads back; it left the live directory
	m.Kill(id)
	for op := ""; op != "close:"+id; op = <-r.change {
	}
	meta, evs, err := m.ReadHistory("owner", id)
	if err != nil || meta.ID != id || len(evs) == 0 {
		t.Fatalf("read: %v %+v %d events", err, meta, len(evs))
	}
	if rows := m.ListFor("owner", "apps/x", nil); len(rows) != 0 {
		t.Fatalf("ended, yet still live: %+v", rows)
	}
	if len(m.ListHistory("bob", "", nil)) != 0 {
		t.Fatal("history is per user")
	}
	if _, _, err := m.ReadHistory("owner", "nope"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown id: %v", err)
	}

	// resume: provider/name carry over; the fake replays the earlier turns
	info2, code, err := m.OpenAgent(owner, "apps/x", "", "", "", "", id, nil)
	if err != nil || code != 200 {
		t.Fatalf("resume: %d %v", code, err)
	}
	if info2.Provider != "fake" || info2.Name != "first" {
		t.Fatalf("resumed row: %+v", info2)
	}
	<-r.change // open
	e := r.until(t, func(e SessionEvent) bool {
		return e.ID == info2.ID && e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "user"
	})
	if txt, _ := edata(e.Event)["text"].(string); !strings.Contains(txt, "resumed fake-1") {
		t.Fatalf("the earlier turns replay into the new session: %q", txt)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info2.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	// resuming on another tile, or an unknown entry, is refused
	if _, code, _ := m.OpenAgent(owner, "apps/y", "", "", "", "", id, nil); code != 400 && code != 403 {
		t.Fatalf("resume on another tile: %d", code)
	}
	if _, code, _ := m.OpenAgent(owner, "apps/x", "", "", "", "", "nope", nil); code != 404 {
		t.Fatalf("resume unknown: %d", code)
	}
	// the continuation, once prompted and ended, supersedes the original entry
	if _, err := m.AgentPrompt(ctx, info2.ID, "and this"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info2.ID && e.Type == agent.EvTurnEnd })
	m.Kill(info2.ID)
	for op := ""; op != "close:"+info2.ID; op = <-r.change {
	}
	hist = m.ListHistory("owner", "apps/x", nil)
	if len(hist) != 1 || hist[0].ID != info2.ID {
		t.Fatalf("the continuation supersedes the original: %+v", hist)
	}
	if err := m.DeleteHistory("owner", info2.ID); err != nil || len(m.ListHistory("owner", "", nil)) != 0 {
		t.Fatalf("delete: %v", err)
	}
}

func TestHistoryPrune(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	dir := m.historyDir("owner", "apps/x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < historyKeep+5; i++ {
		f := historyFile{Meta: HistoryMeta{ID: fmt.Sprintf("s%02d", i), Cwd: "apps/x", Ended: fmt.Sprintf("2026-01-01T00:00:%02dZ", i)}}
		b, _ := json.Marshal(f)
		if err := os.WriteFile(filepath.Join(dir, f.Meta.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.pruneHistory(dir)
	hist := m.ListHistory("owner", "apps/x", nil)
	if len(hist) != historyKeep || hist[0].ID != fmt.Sprintf("s%02d", historyKeep+4) || hist[len(hist)-1].ID != "s05" {
		t.Fatalf("prune keeps the newest %d: got %d, first %s, last %s", historyKeep, len(hist), hist[0].ID, hist[len(hist)-1].ID)
	}
}

// A question round trip through the real host and the fake: the fake's
// "ask" (Claude's AskUserQuestion shape) waits on an elicitation; the answer
// through AgentElicit reaches the agent; a second answer is ErrNoQuestion.
func TestAgentSessionQuestion(t *testing.T) {
	r := newAgentRig(t)
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	defer r.m.Kill(info.ID)
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "ask me"); err != nil {
		t.Fatal(err)
	}
	q := edata(r.until(t, ofType(agent.EvElicitRequest)).Event)
	eid, _ := q["eid"].(string)
	props, _ := q["schema"].(map[string]any)["properties"].(map[string]any)
	if eid == "" || q["toolCallId"] != "ask1" || props["question_0"] == nil || props["question_1_custom"] == nil {
		t.Fatalf("question: %v", q)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusWaiting
	})
	if err := r.m.AgentElicit(info.ID, eid, "accept", json.RawMessage(`{"question_0":"SQLite","question_1":["Metrics"]}`), "owner"); err != nil {
		t.Fatal(err)
	}
	if err := r.m.AgentElicit(info.ID, eid, "decline", nil, "owner"); !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("second answer: %v", err)
	}
	said := r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvMessageDelta && strings.HasPrefix(fmt.Sprint(edata(e.Event)["text"]), "answers:")
	})
	if txt := fmt.Sprint(edata(said.Event)["text"]); !strings.Contains(txt, `"question_0":"SQLite"`) || !strings.Contains(txt, `"question_1":["Metrics"]`) {
		t.Fatalf("the agent got: %s", txt)
	}
}
