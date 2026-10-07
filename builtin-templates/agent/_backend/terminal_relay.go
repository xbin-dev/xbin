// terminal_relay.go — terminals relayed to a person's page or app
// (D147 §4.2.8; API.md §Coding agents): a coding agent's run terminal
// (a shell in its sandbox at its cwd, or its sign-in command) and any
// terminal in a sandbox the caller may use. Both dial the manager's `tty`
// route as this tile with the person asserted (Sbx-User) and relay the
// /ws/term wire byte for byte (xbin.RelayManagerTTY); every refusal comes
// before the upgrade, as JSON. The manager doesn't police an asserted
// person, so the relay checks the caller's own sandboxAccess(caller).Use
// first, fresh from the manager; the runtime refuses a person with
// noTerminal (D88) at every attach.
//
// A relay that STARTED a terminal (no exec=) ends it when its client goes
// (DELETE …/execs/{id}): the app's terminal can neither end a shell nor
// come back to one, so what it leaves would run on for nobody. Not when
// the command exited by itself, and not when a client attached to it again
// through a relay here (exec=<id>) before, or within ttyEndGrace (5 s) after,
// the starter's client went — that client knows the terminal and keeps it
// (it runs on until it exits or someone ends it, as the contract's
// terminals do). The rule is this process's: a blue/green successor
// doesn't know the terminals its predecessor started.
//
// A terminal tab (tab=<name>, the native view's Terminals screen, D190): the
// app's terminal knows no exec id, and a tab not shown closes its socket —
// so the relay keeps a person's tab terminals by name: a client dialling a
// tab again attaches to its terminal while it runs (a new one when it
// exited), and one with no client is ended only after ttyTabIdle (30 min)
// with none, or by DELETE /terminals/{tab} (the tab's ✕).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// harnessRelayRoutes are registered with the route table's (routes.go).
// GET /sandboxes/{ref}/terminal lives inside GET /sandboxes/{ref...}
// (handleSandbox): a sandbox id never holds "/".
func harnessRelayRoutes() []routeDef {
	return []routeDef{
		{"GET /runs/{id}/harness/terminal", needParticipant, handleRunTerminal},
		{"GET /runs/{id}/harness/log", needViewer, handleHarnessLog},
		{"DELETE /terminals/{tab}", needUser, handleEndTab},
	}
}

// handleRunTerminal relays a terminal in a coding agent's sandbox, at its
// cwd: its sign-in command (login=1), or the login shell; exec=<id>
// attaches again to that tty exec instead (the others are then ignored —
// send a resize).
//
//	GET /runs/{id}/harness/terminal?login=1&rows=&cols=&exec= → WebSocket (/ws/term's wire)
func handleRunTerminal(w http.ResponseWriter, r *http.Request) {
	if notTerminalUpgrade(w, r) {
		return
	}
	if globalMode() && r.URL.Query().Get("login") == "1" { // no sign-in at the global instance (harness_partition.go)
		xbin.WriteError(w, http.StatusConflict, harnessNotAtGlobal)
		return
	}
	c := callerOf(r)
	if sbxUserOf(c) == "" {
		xbin.WriteError(w, http.StatusForbidden, "only a person can open a terminal")
		return
	}
	id := pathID(r)
	cfg, err := harnessRunOf(id)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	h := cfg.Harness
	by := h.By
	if b, ok := cfg.sandboxBinding(h.Ref); ok && b.By != "" {
		by = b.By
	}
	conn, sid, box, err := personSandbox(r.Context(), c, h.Ref, func(box *sbxSandbox) string {
		return fmt.Sprintf("you may not use %s — ask %s", sbxLabel(box), askWhom(by, box))
	})
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	q := r.URL.Query()
	o := xbin.ManagerTTYOptions{User: c.user, ExecID: q.Get("exec")}
	if o.ExecID == "" {
		if o.Rows, o.Cols, err = ttySize(q); err != nil {
			writeSbxErr(w, err)
			return
		}
		o.Cwd = h.Cwd
		if q.Get("login") == "1" {
			if o.Cmd = harnessLoginCmd(r.Context(), id, h, conn, box); o.Cmd == "" {
				p := harnessProvider(h.Provider, nil)
				xbin.WriteError(w, http.StatusConflict, p.Name+" has no sign-in command to run in a terminal")
				return
			}
		}
	}
	relayTTY(w, r, conn, sid, o)
}

// handleSandboxTerminal relays any terminal in sandbox ref (GET
// /sandboxes/{ref}/terminal?cwd=&cmd=&rows=&cols=&exec=): cmd as the
// contract's tty route (the login shell unless given), exec=<id> attaches
// again (the others are then ignored).
func handleSandboxTerminal(w http.ResponseWriter, r *http.Request, ref string) {
	if notTerminalUpgrade(w, r) {
		return
	}
	c := callerOf(r)
	if sbxUserOf(c) == "" {
		xbin.WriteError(w, http.StatusForbidden, "only a person can open a terminal")
		return
	}
	rs, err := loadRouteSandbox(r, ref) // fresh, as the caller; 404 for one they neither see nor have bound
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	if !rs.access.Use {
		writeSbxErr(w, refuse(http.StatusForbidden, "you may not use %s — ask %s", sbxLabel(rs.entry.Box), askWhom("", rs.entry.Box)))
		return
	}
	q := r.URL.Query()
	o := xbin.ManagerTTYOptions{User: c.user, ExecID: q.Get("exec")}
	if o.ExecID == "" {
		if o.Rows, o.Cols, err = ttySize(q); err != nil {
			writeSbxErr(w, err)
			return
		}
		o.Cwd, o.Cmd = q.Get("cwd"), q.Get("cmd")
	}
	relayTTY(w, r, rs.conn, rs.id, o)
}

// notTerminalUpgrade answers a request that isn't a WebSocket upgrade (400)
// — before anything is asked of a manager.
func notTerminalUpgrade(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet && ws.IsUpgrade(r) {
		return false
	}
	xbin.WriteError(w, http.StatusBadRequest, "a terminal is a WebSocket upgrade")
	return true
}

// ttySize reads rows and cols (0: the manager's default).
func ttySize(q url.Values) (rows, cols int, err error) {
	for _, f := range []struct {
		name string
		n    *int
	}{{"rows", &rows}, {"cols", &cols}} {
		if v := q.Get(f.name); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n < 0 || n > 10000 {
				return 0, 0, refuse(http.StatusBadRequest, "%s: a number of character cells", f.name)
			}
			*f.n = n
		}
	}
	return rows, cols, nil
}

// harnessRunOf is route run id's config when a coding agent answers it:
// 404 no such run, 409 on a built-in run.
func harnessRunOf(id int64) (Config, error) {
	run, err := agent.db.getRun(id)
	if err != nil {
		return Config{}, refuse(http.StatusNotFound, "no such run")
	}
	cfg, err := agent.db.runConfig(id)
	if err != nil {
		return Config{}, refuse(http.StatusNotFound, "no such run")
	}
	if run.Engine != engineHarness || cfg.Harness == nil || cfg.Harness.Ref == "" {
		return Config{}, refuse(http.StatusConflict, "not a coding-agent conversation")
	}
	return cfg, nil
}

// personSandbox is sandbox ref fresh from its manager, asked as person c,
// when c may use it themself (sandboxAccess(c).Use) — else a 403 in
// denied's words. The manager's refusals (not-found, …) pass through. In a
// person's partition it is a coding agent's sandbox as every use checks it
// (sandbox_partition.go): a manager that can't keep people apart is refused
// (409), and so is a sandbox that isn't homed there (403).
func personSandbox(ctx context.Context, c who, ref string, denied func(*sbxSandbox) string) (*sbxConn, string, *sbxSandbox, error) {
	conn, id, err := sbxDialRef(ref, sbxUserOf(c))
	if err != nil {
		return nil, "", nil, err
	}
	if userMode() {
		if _, err := managerHello(ctx, conn.M); err != nil {
			return nil, "", nil, err
		}
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return nil, "", nil, err
	}
	if why := partitionBoxRefusal(box); why != "" {
		return nil, "", nil, &sbxError{Provider: conn.M.Provider, Refusal: "not-allowed", Msg: why}
	}
	if !sandboxAccess(c, box).Use {
		return nil, "", nil, refuse(http.StatusForbidden, "%s", denied(box))
	}
	return conn, id, box, nil
}

// sbxLabel names a sandbox for people: its name, else its id.
func sbxLabel(b *sbxSandbox) string { return orStr(b.Name, b.ID) }

// askWhom is who can let someone use box: by (who bound it to the
// conversation) when that is a person, else its owner, else the tile's
// managers.
func askWhom(by string, box *sbxSandbox) string {
	switch {
	case by != "" && !strings.HasPrefix(by, "el:"):
		return byName(by)
	case box.Owner.User != "":
		return byName(box.Owner.User)
	}
	return "this agent's managers"
}

// harnessLoginCmd is the shell command that signs run's coding agent in at
// a terminal (harness.login.command, §4.3.2): the one the live adapter
// offered (the session's login, as the engine stored it), else the
// catalog's, else what the sandbox's manager advertises for its image.
func harnessLoginCmd(ctx context.Context, run int64, h *HarnessConfig, conn *sbxConn, box *sbxSandbox) string {
	if s, _ := agent.db.harnessSession(run); s != nil && s.Login != "" {
		var l struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(s.Login), &l) == nil && l.Command != "" {
			return l.Command
		}
	}
	var adv *sbxHarness
	if hello, err := managerHello(ctx, conn.M); err == nil {
		for _, e := range hello.imageHarnessEntries(box.Image.ID) {
			if e.ID == h.Provider {
				adv = &e
				break
			}
		}
	}
	if p := harnessProvider(h.Provider, adv); p.LoginCmd != "" {
		return p.LoginCmd
	}
	if adv != nil {
		return adv.Login
	}
	return ""
}

// --- relaying, and ending what a relay started ---------------------------------------

// ttyEndGrace is how long (ns) a terminal a relay started outlives its
// client before the relay ends it: time for a client to attach to it again
// (exec=<id>) after a drop. 5 s; tests shorten it.
var ttyEndGrace atomic.Int64

func init() { ttyEndGrace.Store(int64(5 * time.Second)) }

// relayTTYs are the terminals relays here started and may still end, by
// relayTTYKey: spared once a client attached to one again.
var relayTTYs = struct {
	sync.Mutex
	m map[string]*startedTTY
}{m: map[string]*startedTTY{}}

type startedTTY struct{ spared bool }

func relayTTYKey(conn *sbxConn, sandbox, exec string) string {
	return conn.M.Provider + "|" + sandbox + "|" + exec
}

// relayTTY relays the caller's terminal (o) on sandbox id at conn's
// manager, and ends one it started when its client has gone (the file's
// comment says when not).
func relayTTY(w http.ResponseWriter, r *http.Request, conn *sbxConn, id string, o xbin.ManagerTTYOptions) {
	if tab := r.URL.Query().Get("tab"); tab != "" && o.ExecID == "" {
		if !tabName.MatchString(tab) {
			xbin.WriteError(w, http.StatusBadRequest, "tab: 1-64 letters, digits, _ or -")
			return
		}
		relayTab(w, r, conn, id, o, tab)
		return
	}
	o.Client = sbxClient()
	started := o.ExecID == ""
	o.OnSession = func(eid string) {
		k := relayTTYKey(conn, id, eid)
		relayTTYs.Lock()
		defer relayTTYs.Unlock()
		if started {
			relayTTYs.m[k] = &startedTTY{}
		} else if t := relayTTYs.m[k]; t != nil {
			t.spared = true // a client came back to it: it keeps it
		}
	}
	res := xbin.RelayManagerTTY(w, r, conn.M.URL, id, o)
	if !started || res.Session == "" {
		return
	}
	k := relayTTYKey(conn, id, res.Session)
	end := func() {
		relayTTYs.Lock()
		t := relayTTYs.m[k]
		delete(relayTTYs.m, k)
		relayTTYs.Unlock()
		if t == nil || t.spared || res.Exited {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
		defer cancel()
		if err := conn.ExecDelete(ctx, id, res.Session); err != nil {
			if ref := sbxRefusal(err); ref != "not-found" && ref != "lost" {
				logf("ending terminal %s in sandbox %s (its client left): %v", res.Session, sandboxRef(conn.M.Provider, id), err)
			}
		}
	}
	if res.Exited {
		end()
		return
	}
	time.AfterFunc(time.Duration(ttyEndGrace.Load()), end)
}

// --- terminal tabs ---------------------------------------------------------------------

// ttyTabIdle is how long (ns) a tab's terminal runs with no client before
// the relay ends it. 30 min; tests shorten it.
var ttyTabIdle atomic.Int64

func init() { ttyTabIdle.Store(int64(30 * time.Minute)) }

var tabName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// tabTTYs are the people's tab terminals, by tabKey.
var tabTTYs = struct {
	sync.Mutex
	m map[string]*tabTTY
}{m: map[string]*tabTTY{}}

type tabTTY struct {
	conn          *sbxConn
	sandbox, exec string
	user, tab     string
	clients       int
	idle          *time.Timer
}

func tabKey(user string, conn *sbxConn, sandbox, tab string) string {
	return user + "\x00" + conn.M.Provider + "|" + sandbox + "\x00" + tab
}

// relayTab relays tab `tab` of the caller (o.User) on sandbox id: its
// terminal again while it runs, else a new one (o as given).
func relayTab(w http.ResponseWriter, r *http.Request, conn *sbxConn, id string, o xbin.ManagerTTYOptions, tab string) {
	k := tabKey(o.User, conn, id, tab)
	tabTTYs.Lock()
	t := tabTTYs.m[k]
	if t != nil {
		if t.idle != nil {
			t.idle.Stop()
			t.idle = nil
		}
		t.clients++
	}
	tabTTYs.Unlock()
	if t != nil {
		// still there? (a shell that exited, a manager that lost it: a new one)
		ctx, cancel := context.WithTimeout(r.Context(), sbxCallTimeout)
		ex, err := conn.ExecGet(ctx, id, t.exec)
		cancel()
		if err == nil && ex.State == "running" {
			o.ExecID = t.exec
		} else {
			tabTTYs.Lock()
			if tabTTYs.m[k] == t {
				delete(tabTTYs.m, k)
			}
			tabTTYs.Unlock()
			t = nil
		}
	}
	o.Client = sbxClient()
	o.OnSession = func(eid string) {
		if t != nil {
			return
		}
		tabTTYs.Lock()
		defer tabTTYs.Unlock()
		t = &tabTTY{conn: conn, sandbox: id, exec: eid, user: o.User, tab: tab, clients: 1}
		tabTTYs.m[k] = t
	}
	res := xbin.RelayManagerTTY(w, r, conn.M.URL, id, o)
	if t == nil {
		return
	}
	tabTTYs.Lock()
	defer tabTTYs.Unlock()
	if tabTTYs.m[k] != t {
		return
	}
	if res.Exited {
		delete(tabTTYs.m, k) // the shell ended: the tab's next dial starts another
		return
	}
	if t.clients--; t.clients > 0 {
		return
	}
	t.idle = time.AfterFunc(time.Duration(ttyTabIdle.Load()), func() {
		tabTTYs.Lock()
		gone := tabTTYs.m[k] == t && t.clients <= 0
		if gone {
			delete(tabTTYs.m, k)
		}
		tabTTYs.Unlock()
		if gone {
			endTab(t)
		}
	})
}

func endTab(t *tabTTY) {
	ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
	defer cancel()
	if err := t.conn.ExecDelete(ctx, t.sandbox, t.exec); err != nil {
		if ref := sbxRefusal(err); ref != "not-found" && ref != "lost" {
			logf("ending terminal tab %s in sandbox %s: %v", t.tab, sandboxRef(t.conn.M.Provider, t.sandbox), err)
		}
	}
}

// handleEndTab ends the caller's terminal of tab {tab} (in whatever sandbox:
// the tab's ✕) — {"ended": n}.
//
//	DELETE /terminals/{tab}
func handleEndTab(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	tab := r.PathValue("tab")
	if !tabName.MatchString(tab) {
		xbin.WriteError(w, http.StatusBadRequest, "tab: 1-64 letters, digits, _ or -")
		return
	}
	var ended []*tabTTY
	tabTTYs.Lock()
	for k, t := range tabTTYs.m {
		if t.user == c.user && t.tab == tab {
			if t.idle != nil {
				t.idle.Stop()
			}
			delete(tabTTYs.m, k)
			ended = append(ended, t)
		}
	}
	tabTTYs.Unlock()
	for _, t := range ended {
		endTab(t)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]int{"ended": len(ended)})
}
