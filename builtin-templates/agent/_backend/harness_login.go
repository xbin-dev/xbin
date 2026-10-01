// harness_login.go — a coding agent that isn't signed in (D147 §3.4,
// §4.2.6, §4.3.4; spec §9 item 4): an adapter that says so
// (_auth/status_update {kind: none}), refuses a prompt with -32000, or
// refuses to open a session at all (codex: ClientOptions.AwaitLogin) puts
// the session in state login and the run in waiting_input, parked on
// pendingState {kind: "login"} — not an error: a child's link stays open.
// The prompt that failed is kept in `held`. Out of it:
//
//   - /resume (a wake): the signed-out adapter is ended, a fresh one
//     started (it reads its sign-in again: a terminal /login) and the held
//     prompt resent — signed out still, the run parks again;
//   - harnessAuthenticate (POST /runs/{id}/harness/authenticate's engine
//     side): the adapter's own authenticate — an API key (in
//     _meta["api-key"] in the shape the adapter reads — {apiKey}, gemini's
//     the key itself (apiKeyMeta) — once: never stored, never logged), or a
//     device code, whose URL the adapter asks the person to open through a
//     url elicitation (honoured only now) — then the held prompt resent to
//     the same adapter. The code is the requester's alone: they get it in
//     the answer (and again from GET /runs/{id}/harness, or by asking
//     again); what is stored and published is only harness.login.device
//     {by} — whoever else sees the conversation could otherwise enter it
//     first, signing the sandbox's harness in as themselves.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// errHarnessLogin: the adapter is up but not signed in (its session is
// parked on its sign-in; ensureHarness returns it with this).
var errHarnessLogin = errors.New("the coding agent isn't signed in")

// hDeviceFor bounds a device-code sign-in (the person opening the page).
const hDeviceFor = 15 * time.Minute

// hLogin is §4.3.2 login: the login terminal's command, the ways AgTT can
// sign the adapter in, and while a device-code sign-in waits who started it
// (its page and code only for them: hsess.deviceFor).
type hLogin struct {
	Command string         `json:"command,omitempty"`
	Methods []hLoginMethod `json:"methods"`
	Device  *hDevice       `json:"device,omitempty"`
}

type hLoginMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // terminal | api-key | device-code
}

// hDevice is a device code: the page to open and the words that give the
// code (the requester's only), or — stored and published — By alone.
type hDevice struct {
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
	By      string `json:"by,omitempty"`
}

// authKind is how AgTT offers an adapter's auth method ("": not offered).
func authKind(m acp.AuthMethod) string {
	switch {
	case m.Type == "terminal" || m.Meta["terminal-auth"] != nil:
		return "terminal"
	case m.Meta["api-key"] != nil:
		return "api-key"
	case strings.Contains(strings.ToLower(m.ID+" "+m.Name), "device"):
		return "device-code"
	}
	return ""
}

// loginOf is the session's login: the command the adapter's terminal
// method runs (its terminal-auth argv, shell-quoted), else the catalog's.
func (s *hsess) loginOf() hLogin {
	l := hLogin{Methods: []hLoginMethod{}}
	var ms []acp.AuthMethod
	if s.c != nil {
		ms = s.c.AuthMethods()
	}
	for _, m := range ms {
		k := authKind(m)
		if k == "" {
			continue
		}
		l.Methods = append(l.Methods, hLoginMethod{ID: m.ID, Name: orStr(m.Name, m.ID), Kind: k})
		if k != "terminal" || l.Command != "" {
			continue
		}
		var argv []string
		if ta, ok := m.Meta["terminal-auth"].(map[string]any); ok {
			if cmd, _ := ta["command"].(string); cmd != "" {
				argv = []string{cmd}
				args, _ := ta["args"].([]any)
				for _, a := range args {
					if a, ok := a.(string); ok {
						argv = append(argv, a)
					}
				}
			}
		} else if m.Type == "terminal" && len(s.prov.Argv) > 0 {
			argv = append(append([]string(nil), s.prov.Argv...), m.Args...)
		}
		if len(argv) > 0 {
			l.Command = shellLine(s.prov.Env, argv)
		}
	}
	if l.Command == "" {
		l.Command = orStr(s.prov.LoginCmd, s.prov.Login)
	}
	return l
}

// shellLine is env and argv as one sh command line.
func shellLine(env map[string]string, argv []string) string {
	var parts []string
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+shellWord(env[k]))
	}
	for _, a := range argv {
		parts = append(parts, shellWord(a))
	}
	return strings.Join(parts, " ")
}

func shellWord(a string) string {
	if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./=:@%+-,") == "" {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// loginTx parks the run on the sign-in: the session in state login (held
// kept when given), status waiting_input, pendingState {kind: "login",
// harness: {login}}. The caller stores hs.
func (s *hsess) loginTx(t *DB, hs *harnessSession, held *heldPrompt) error {
	b, _ := json.Marshal(s.loginOf())
	hs.State, hs.Login, hs.Error = hsLogin, string(b), ""
	if held != nil && held.Text != "" {
		hb, _ := json.Marshal(held)
		hs.Held = string(hb)
	}
	run, err := t.getRun(s.run)
	if err != nil {
		return err
	}
	if p := parsePending(run.Pending); run.Status == statusWaiting && p.Kind == "login" {
		return nil // parked on it already
	}
	ps := pendingState{Kind: "login", Park: newPark(), Harness: &hPark{Login: b}}
	raw, _ := json.Marshal(ps)
	if err := t.setStatus(run.ID, statusWaiting, 0, run.Result, string(raw)); err != nil {
		return err
	}
	s.e.emitStep(t, s.root, t.journal(run.ID, "note", map[string]string{"text": fmt.Sprintf("%s isn't signed in — sign in, then Retry", s.prov.Name)}))
	if run.ParentID == 0 {
		t.bumpActivity(run.ID)
	}
	s.e.emitRun(t, run.ID)
	signed := false
	_ = t.noteHarnessSeen(hs.Ref, s.prov.ID, nil, &signed)
	t.AfterCommit(s.toRest) // waiting on a person: it doesn't keep a person's partition up (harness_partition.go)
	return nil
}

// heldForLogin is the prompt a sign-in keeps: the one in flight, else the
// run's last user row.
func (s *hsess) heldForLogin(t *DB) *heldPrompt {
	if p := s.inflightPrompt(); p != nil {
		return p
	}
	var text string
	_ = t.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND role='user' ORDER BY seq DESC LIMIT 1`, s.run).Scan(&text)
	return &heldPrompt{Text: text}
}

// leaveLogin ends a signed-out adapter and clears the sign-in park (a
// wake: the fresh adapter reads its sign-in again), keeping held.
func (e *Engine) leaveLogin(ctx context.Context, run *Run, hs *harnessSession) {
	if s := e.harnessOf(run.ID); s != nil {
		s.stop()
	} else if hs.ExecID != "" && hsExecMayRun(hs.State) {
		if cfg, err := e.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
		}
	}
	_ = e.fenced(func(t *DB) error {
		if cur, _ := t.harnessSession(run.ID); cur != nil && (cur.State == hsLogin || cur.Login != "") {
			if hsRunning(cur.State) {
				cur.State = hsStopped
			}
			cur.Login = ""
			if err := t.putHarnessSession(cur); err != nil {
				return err
			}
		}
		r, err := t.getRun(run.ID)
		if err != nil || r.Status != statusWaiting || parsePending(r.Pending).Kind != "login" {
			return err
		}
		if err := t.setStatus(r.ID, statusIdle, 0, r.Result, ""); err != nil {
			return err
		}
		e.emitRun(t, r.ID)
		return nil
	})
}

// --- authenticate -------------------------------------------------------------------

// hAuthErr is a sign-in refused: the HTTP status the route answers, and why.
type hAuthErr struct {
	Code int
	Msg  string
}

func (e *hAuthErr) Error() string { return e.Msg }

// hAuthResult is authenticate's answer: state "ready" (signed in, 200), or
// the device code to open (202).
type hAuthResult struct {
	State  string   `json:"state,omitempty"`
	Device *hDevice `json:"device,omitempty"`
}

// hAuth is a sign-in AgTT started on a session, for the person by: a url
// question during it is its device code (sent on device, kept in dev while
// it waits — never stored).
type hAuth struct {
	device chan hDevice
	by     string
	method string
	dev    *hDevice // under hsess.mu
}

// beginAuth claims the session's one sign-in (false: one is under way).
func (s *hsess) beginAuth(a *hAuth) bool {
	s.mu.Lock()
	if s.authing != nil {
		s.mu.Unlock()
		return false
	}
	s.authing = a
	s.signing.Store(true)
	s.mu.Unlock()
	s.holdMoved(true) // AgTT awaits the adapter's answer: that holds a person's partition (harness_partition.go)
	return true
}

func (s *hsess) endAuth(a *hAuth) {
	s.mu.Lock()
	ended := s.authing == a
	if ended {
		s.authing = nil
		s.signing.Store(false)
	}
	s.mu.Unlock()
	s.holdMoved(ended)
}

func (s *hsess) auth() *hAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authing
}

// deviceFor is the device code of the sign-in under way when user started
// it (nil otherwise: someone else's, or none yet).
func (s *hsess) deviceFor(user string) *hDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.authing; a != nil && user != "" && a.by == user && a.dev != nil {
		d := *a.dev
		return &d
	}
	return nil
}

// deviceFor is run's device code for user (deviceFor), when this process
// drives its session.
func (e *Engine) deviceFor(runID int64, user string) *hDevice {
	if s := e.harnessOf(runID); s != nil {
		return s.deviceFor(user)
	}
	return nil
}

// harnessAuthenticate signs run's coding agent in through the adapter
// (D147 §4.2.6) — the engine side of POST /runs/{id}/harness/
// authenticate, whose route checks the caller first (participant, sandbox
// Use, confirm on a shared sandbox). It runs in the process driving the
// session (an adapter is started when none is live: its initialize, and
// session/new unless it refuses signed out). One sign-in at a time; errors
// are *hAuthErr (the route's status and words) or a handoff's.
//
// method is one of harness.login.methods of kind api-key (apiKey needed:
// it rides authenticate._meta["api-key"] once (apiKeyMeta) — never stored,
// never logged; the answer within 30 s → State "ready", the held prompt
// resent) or device-code (the adapter's URL within 30 s → Device, for by
// alone — harness.login.device says only who; the run leaves login by
// itself when the person finishes). by, the person asking, asking again
// for the device code of the sign-in they started gets it again.
func (e *Engine) harnessAuthenticate(ctx context.Context, run *Run, method, apiKey, by string) (*hAuthResult, error) {
	cfg, err := e.db.runConfig(run.ID)
	if err != nil || cfg.Harness == nil {
		return nil, &hAuthErr{409, "not a coding-agent conversation"}
	}
	name := e.db.harnessRunName(run.ID, cfg.Harness.Provider)
	if hs, _ := e.db.harnessSession(run.ID); hs == nil || hs.State != hsLogin {
		return nil, &hAuthErr{409, name + " is signed in"}
	}
	s, err := e.ensureHarness(ctx, run)
	switch {
	case errors.Is(err, errHandoff) || errors.Is(err, errFenced):
		return nil, err
	case err != nil && !errors.Is(err, errHarnessLogin):
		return nil, &hAuthErr{502, err.Error()}
	}
	var m *acp.AuthMethod
	kind := ""
	var ids []string
	for _, am := range s.c.AuthMethods() {
		k := authKind(am)
		if k != "api-key" && k != "device-code" {
			continue
		}
		ids = append(ids, am.ID)
		if am.ID == method {
			mm := am
			m, kind = &mm, k
		}
	}
	switch {
	case m == nil:
		return nil, &hAuthErr{400, "method: one of " + strings.Join(ids, ", ")}
	case kind == "api-key" && apiKey == "":
		return nil, &hAuthErr{400, "apiKey: needed for " + orStr(m.Name, m.ID)}
	case kind != "api-key" && apiKey != "":
		return nil, &hAuthErr{400, "apiKey: only for an API-key method"}
	}
	a := &hAuth{device: make(chan hDevice, 1), by: by, method: m.ID}
	if !s.beginAuth(a) {
		if cur := s.auth(); cur != nil && cur.method == m.ID {
			if d := s.deviceFor(by); d != nil {
				return &hAuthResult{Device: &hDevice{URL: d.URL, Message: d.Message}}, nil
			}
		}
		return nil, &hAuthErr{409, "a sign-in to " + name + " is already under way"}
	}
	if kind == "api-key" {
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.c.Authenticate(actx, m.ID, apiKeyMeta(s.prov.ID, apiKey)) // in the adapter's own shape (gemini: the key itself)
		cancel()
		if err != nil {
			s.endAuth(a)
			return nil, authFailure(err, name)
		}
		e.signedIn(s)
		s.endAuth(a) // after its outcome is stored (as below)
		return &hAuthResult{State: "ready"}, nil
	}
	actx, cancel := context.WithTimeout(context.Background(), hDeviceFor)
	done := make(chan error, 1)
	go func() {
		err := s.c.Authenticate(actx, m.ID, nil)
		cancel()
		// the sign-in stays under way until its outcome is stored: the
		// adapter's elicitation/complete, applied meanwhile, must not read
		// as a predecessor's sign-in (onURLComplete: a second wake that
		// restarts the adapter just signed in), nor its auth status as a
		// sign-out (harness_map.go)
		if err == nil {
			e.signedIn(s)
		} else {
			s.deviceEnded(err)
		}
		s.endAuth(a)
		done <- err
	}()
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	select {
	case d := <-a.device:
		return &hAuthResult{Device: &d}, nil
	case err := <-done:
		if err != nil {
			return nil, authFailure(err, name)
		}
		return &hAuthResult{State: "ready"}, nil
	case <-t.C:
		cancel()
		return nil, &hAuthErr{504, name + " didn't start its sign-in"}
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

// authFailure is an authenticate the adapter refused, in its own words.
func authFailure(err error, name string) error {
	var re *acp.Error
	switch {
	case errors.As(err, &re):
		return &hAuthErr{502, re.Message}
	case errors.Is(err, context.DeadlineExceeded):
		return &hAuthErr{504, name + " didn't answer its sign-in"}
	}
	return &hAuthErr{502, err.Error()}
}

// onDevice is a url question during a sign-in AgTT started: the device
// code — accepted, kept for the person who asked (a.dev, the answer);
// harness.login.device says only who.
func (s *hsess) onDevice(ev acp.Event, q acp.Elicitation, a *hAuth) {
	go func() { _ = s.c.RespondElicitation(q.EID, "accept", nil, "agtt") }()
	d := hDevice{URL: q.URL, Message: q.Message}
	s.mu.Lock()
	a.dev = &hDevice{URL: q.URL, Message: q.Message, By: a.by}
	s.mu.Unlock()
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error { return s.setDeviceTx(t, hs, &hDevice{By: a.by}) })
	select {
	case a.device <- d:
	default:
	}
	s.publishSummary()
}

// storedState is st (the client's State, its own copy) as harness_sessions
// .snapshot keeps it: a url question's page and words left out — the only
// url questions AgTT accepts are device codes (onDevice), the requester's
// alone. A successor restores the question by its ids, which stay.
func storedState(st acp.SessionState) acp.SessionState {
	for i := range st.Elicitations {
		if st.Elicitations[i].Mode == "url" {
			st.Elicitations[i].URL, st.Elicitations[i].Message = "", ""
		}
	}
	return st
}

// setDeviceTx puts d (nil: none) in the session's login and the park's —
// stored and published, so never the code itself (onDevice).
func (s *hsess) setDeviceTx(t *DB, hs *harnessSession, d *hDevice) error {
	var l hLogin
	if hs.Login == "" || json.Unmarshal([]byte(hs.Login), &l) != nil {
		l = s.loginOf()
	}
	l.Device = d
	b, _ := json.Marshal(l)
	hs.Login = string(b)
	run, err := t.getRun(s.run)
	if err != nil {
		return err
	}
	if p := parsePending(run.Pending); run.Status == statusWaiting && p.Kind == "login" && p.Harness != nil {
		p.Harness.Login = b
		raw, _ := json.Marshal(p)
		if err := t.setStatus(run.ID, run.Status, 0, run.Result, string(raw)); err != nil {
			return err
		}
		s.e.emitRun(t, run.ID)
	}
	return nil
}

// deviceEnded: the device-code sign-in didn't complete — said, the code
// taken away; the run stays parked on its sign-in.
func (s *hsess) deviceEnded(err error) {
	msg := err.Error()
	var re *acp.Error
	if errors.As(err, &re) {
		msg = re.Message
	}
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
		s.e.emitStep(t, s.root, t.journal(s.run, "note", map[string]string{"text": fmt.Sprintf("the sign-in to %s didn't complete: %s", s.prov.Name, msg)}))
		if hs.State != hsLogin {
			return nil
		}
		return s.setDeviceTx(t, hs, nil)
	})
	s.publishSummary()
}

// signedIn: the adapter signed in through AgTT — the session live (its
// session open now, when it had refused one), the sign-in park cleared,
// the held prompt resent (a wake).
func (e *Engine) signedIn(s *hsess) {
	sid, loadable := s.c.Session()
	resend := false
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
		hs.State, hs.Login, hs.Error = hsLive, "", ""
		if sid != "" {
			hs.ACPSession, hs.Loadable = sid, loadable
			if cfg, err := t.runConfig(s.run); err == nil && cfg.Harness != nil && s.newSess {
				noteStartMode(hs, cfg.Harness.Mode, s.c.State()) // the session the sign-in opened
			}
		}
		signed := true
		_ = t.noteHarnessSeen(hs.Ref, s.prov.ID, &signed, &signed)
		run, err := t.getRun(s.run)
		if err != nil {
			return err
		}
		if run.Status == statusWaiting && parsePending(run.Pending).Kind == "login" {
			if err := t.setStatus(run.ID, statusIdle, 0, run.Result, ""); err != nil {
				return err
			}
			e.emitRun(t, run.ID)
		}
		if hs.Held != "" {
			if _, _, err := t.enqueue(s.run, inboxWake, inboxBody{Reason: "signed in"}, ""); err != nil {
				return err
			}
			resend = true
		}
		return nil
	})
	s.publishSummary()
	if resend {
		e.Poke(s.run)
	} else {
		s.armIdle()
	}
}
