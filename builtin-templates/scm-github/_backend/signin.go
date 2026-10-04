// signin.go — a person's GitHub sign-in by the device flow, in their own
// partition (docs/scm.md §Sign-in): start, poll (in the background and on
// GET /scm/signin/{pollId}), register the identity at global, Forget.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// deviceFlow is the partition's one sign-in under way, and the outcome of
// recent ones (a poll id answers for 10 minutes after it ended).
type deviceFlow struct {
	mu      sync.Mutex
	cur     *pendingSignin
	ended   map[string]endedSignin
	running bool // the background poller
}

type pendingSignin struct {
	PollID     string `json:"pollId"`
	DeviceCode string `json:"deviceCode"` // kept in the vault only
	UserCode   string `json:"userCode"`
	URL        string `json:"url"`
	ExpiresAt  int64  `json:"expiresAt"`
	IntervalMs int64  `json:"intervalMs"`
	NextAt     int64  `json:"nextAt"`
}

type endedSignin struct {
	state string
	ident *identity
	err   string
	at    time.Time
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *pendingSignin) info() *signinInfo {
	return &signinInfo{URL: p.URL, UserCode: p.UserCode, ExpiresAt: p.ExpiresAt, PollID: p.PollID, IntervalMs: p.IntervalMs}
}

// needPerson refuses the sign-in routes where no person is asking.
func (s *srv) needPerson(c who) error {
	if s.mode == modeUser && (c.cls == clsPersonConsumer || c.cls == clsPersonPage) {
		return nil
	}
	ids, _ := s.identities(c)
	e := refuse(refIdentity, "a GitHub sign-in is a person's, in their own partition of this tile: no person is asking here")
	e.Identities = ids
	return e
}

// startSignin starts a device flow, or answers the one under way.
func (s *srv) startSignin(ctx context.Context) (*signinInfo, error) {
	f := s.signin
	f.mu.Lock()
	defer f.mu.Unlock()
	now := s.now()
	if f.cur == nil {
		var p pendingSignin
		if vaultJSON(s.vault, vaultSigninDev, &p) == nil && p.PollID != "" {
			f.cur = &p
		}
	}
	if f.cur != nil && now.UnixMilli() < f.cur.ExpiresAt {
		s.ensurePoller()
		return f.cur.info(), nil
	}
	pub := s.public()
	if !pub.Configured || pub.ClientID == "" {
		return nil, refuse(refSetup, "this scm-github isn't set up yet: a manager creates or pastes the GitHub App on its page")
	}
	_, _, web := s.hosts()
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
		Error           string `json:"error"`
	}
	if err := s.oauthPost(ctx, web+"/login/device/code", url.Values{"client_id": {pub.ClientID}, "scope": {""}}, &out); err != nil {
		return nil, err
	}
	switch {
	case out.Error == "device_flow_disabled" || out.Error == "unauthorized_client":
		return nil, refuse(refSetup, "the GitHub App's Device Flow is off: a manager enables it in the App's settings")
	case out.Error != "":
		return nil, refuse(refUpstream, "GitHub didn't start a sign-in: %s", clip(out.Error, 80))
	case out.DeviceCode == "":
		return nil, refuse(refUpstream, "GitHub didn't start a sign-in")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	if out.VerificationURI == "" {
		out.VerificationURI = web + "/login/device"
	}
	p := &pendingSignin{PollID: "p_" + randomID(16), DeviceCode: out.DeviceCode, UserCode: out.UserCode, URL: out.VerificationURI,
		ExpiresAt: now.Add(time.Duration(out.ExpiresIn) * time.Second).UnixMilli(), IntervalMs: out.Interval * 1000,
		NextAt: now.Add(time.Duration(out.Interval) * time.Second).UnixMilli()}
	if err := setVaultJSON(s.vault, vaultSigninDev, p); err != nil {
		return nil, err
	}
	f.cur = p
	s.ensurePoller()
	return p.info(), nil
}

// signinRefusal is 409 signin with a flow started (or the setup refusal).
func (s *srv) signinRefusal(ctx context.Context) error {
	info, err := s.startSignin(ctx)
	if err != nil {
		return err
	}
	e := refuse(refSignin, "sign in to GitHub: open the address and enter the code (the person only)")
	e.Signin = info
	return e
}

// ensurePoller runs the background poller (one per process). f.mu held.
func (s *srv) ensurePoller() {
	f := s.signin
	if !s.bgPoll || f.running {
		return
	}
	f.running = true
	go func() {
		for {
			f.mu.Lock()
			p := f.cur
			if p == nil {
				f.running = false
				f.mu.Unlock()
				return
			}
			wait := time.Until(time.UnixMilli(p.NextAt))
			f.mu.Unlock()
			if wait > 0 {
				time.Sleep(wait)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			s.pollOnce(ctx, p.PollID)
			cancel()
		}
	}()
}

// resumeSignin picks up a flow a restart interrupted.
func (s *srv) resumeSignin() {
	var p pendingSignin
	if vaultJSON(s.vault, vaultSigninDev, &p) != nil || p.PollID == "" {
		return
	}
	s.signin.mu.Lock()
	s.signin.cur = &p
	s.ensurePoller()
	s.signin.mu.Unlock()
}

// pollOnce asks GitHub once whether the person has entered the code —
// when it is due, and only one caller per interval: the background poller
// and GET /scm/signin/{pollId} both come here, and whichever claims the
// interval first (NextAt moved on under the lock) is the one that asks.
func (s *srv) pollOnce(ctx context.Context, pollID string) {
	f := s.signin
	f.mu.Lock()
	p := f.cur
	if p == nil || p.PollID != pollID {
		f.mu.Unlock()
		return
	}
	now := s.now()
	if now.UnixMilli() >= p.ExpiresAt {
		f.mu.Unlock()
		s.endSignin(pollID, endedSignin{state: "expired"})
		return
	}
	if now.UnixMilli() < p.NextAt {
		f.mu.Unlock()
		return // not due, or another caller is asking this interval
	}
	p.NextAt = now.Add(time.Duration(p.IntervalMs) * time.Millisecond).UnixMilli()
	cp := *p
	f.mu.Unlock()
	pub := s.public()
	_, _, web := s.hosts()
	var out oauthTokenResp
	err := s.oauthPost(ctx, web+"/login/oauth/access_token", url.Values{
		"client_id": {pub.ClientID}, "device_code": {cp.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	}, &out)
	next := func(interval int64) {
		f.mu.Lock()
		if f.cur != nil && f.cur.PollID == pollID {
			f.cur.IntervalMs = interval
			f.cur.NextAt = s.now().Add(time.Duration(interval) * time.Millisecond).UnixMilli()
			_ = setVaultJSON(s.vault, vaultSigninDev, f.cur)
		}
		f.mu.Unlock()
	}
	switch {
	case err != nil:
		next(cp.IntervalMs) // GitHub unreachable: try again next interval
	case out.Error == "authorization_pending":
		next(cp.IntervalMs)
	case out.Error == "slow_down":
		iv := cp.IntervalMs + 5000
		if out.Interval*1000 > iv {
			iv = out.Interval * 1000
		}
		next(iv)
	case out.Error == "expired_token":
		s.endSignin(pollID, endedSignin{state: "expired"})
	case out.Error == "access_denied":
		s.endSignin(pollID, endedSignin{state: "denied"})
	case out.Error == "device_flow_disabled":
		s.endSignin(pollID, endedSignin{state: "error", err: "the GitHub App's Device Flow is off: a manager enables it in the App's settings"})
	case out.Error != "":
		s.endSignin(pollID, endedSignin{state: "error", err: "GitHub: " + clip(out.Error, 80)})
	default:
		id, err := s.finishSignin(ctx, out)
		if err != nil {
			s.endSignin(pollID, endedSignin{state: "error", err: asRefusal(err).Message})
			return
		}
		s.endSignin(pollID, endedSignin{state: "done", ident: id})
	}
}

// finishSignin keeps the new sign-in: who it is (GET /user), the pair in
// the vault, and the identity registered at global.
func (s *srv) finishSignin(ctx context.Context, out oauthTokenResp) (*identity, error) {
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if _, err := s.gh.call(ctx, bearerAuth("", out.AccessToken), http.MethodGet, s.apiBase()+"/user", nil, &u); err != nil {
		return nil, err
	}
	if u.Login == "" {
		return nil, refuse(refUpstream, "GitHub didn't say who signed in")
	}
	if prev := s.personRecord(); prev != nil && prev.ID != u.ID {
		s.clearUser() // another account: nothing of the old one is handed out again
	}
	if err := s.storeUser(out, u.Login, u.ID, s.now()); err != nil {
		return nil, err
	}
	_ = s.register(ctx, out.AccessToken) // retried before the next scope if global was away
	return &identity{Kind: asPerson, Login: u.Login, ID: u.ID}, nil
}

// register tells global who this partition's person is on GitHub: global
// asks GitHub itself (a token only this App issued answers) and keeps the
// login, never the token.
func (s *srv) register(ctx context.Context, tok secretString) error {
	var got account
	if err := s.relay(ctx, http.MethodPost, "partition/identity", map[string]string{"accessToken": tok.Reveal()}, &got); err != nil {
		return err
	}
	rec := s.personRecord()
	if rec == nil {
		return nil
	}
	rec.Registered = got.ID == rec.ID
	return s.state.Put("person", rec)
}

func (s *srv) endSignin(pollID string, e endedSignin) {
	f := s.signin
	f.mu.Lock()
	defer f.mu.Unlock()
	e.at = s.now()
	if f.ended == nil {
		f.ended = map[string]endedSignin{}
	}
	for id, old := range f.ended {
		if e.at.Sub(old.at) > 10*time.Minute {
			delete(f.ended, id)
		}
	}
	if old, ok := f.ended[pollID]; ok && old.state == "done" {
		return // a sign-in that succeeded stays done, whatever a late answer says
	}
	f.ended[pollID] = e
	if f.cur != nil && f.cur.PollID == pollID {
		f.cur = nil
		_ = s.vault.Delete(vaultSigninDev)
	}
}

// signedIn is the person's identity when signed in here.
func (s *srv) signedIn() *identity {
	rec := s.personRecord()
	if rec == nil {
		return nil
	}
	if acc, _, err := s.userTokens(); err != nil || acc.Token == "" {
		return nil
	}
	return &identity{Kind: asPerson, Login: rec.Login, ID: rec.ID}
}

func (s *srv) handleSigninStart(w http.ResponseWriter, r *http.Request, c who) {
	if err := s.needPerson(c); err != nil {
		fail(w, err)
		return
	}
	if id := s.signedIn(); id != nil {
		writeJSON(w, http.StatusOK, signinState{State: "done", Identity: id})
		return
	}
	info, err := s.startSignin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, signinState{State: "pending", Signin: info})
}

// handleSigninGet answers the state, starting nothing.
func (s *srv) handleSigninGet(w http.ResponseWriter, r *http.Request, c who) {
	if err := s.needPerson(c); err != nil {
		fail(w, err)
		return
	}
	if id := s.signedIn(); id != nil {
		writeGET(w, r, signinState{State: "done", Identity: id})
		return
	}
	f := s.signin
	f.mu.Lock()
	cur := f.cur
	if cur == nil {
		var p pendingSignin
		if vaultJSON(s.vault, vaultSigninDev, &p) == nil && p.PollID != "" && s.now().UnixMilli() < p.ExpiresAt {
			cur = &p
		}
	}
	f.mu.Unlock()
	if cur != nil && s.now().UnixMilli() < cur.ExpiresAt {
		writeGET(w, r, signinState{State: "pending", Signin: cur.info()})
		return
	}
	writeGET(w, r, signinState{State: "none"})
}

// handleSigninPoll answers a flow's state, asking GitHub once if due.
func (s *srv) handleSigninPoll(w http.ResponseWriter, r *http.Request, c who) {
	if err := s.needPerson(c); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("pollId")
	f := s.signin
	f.mu.Lock()
	if f.cur == nil {
		var p pendingSignin
		if vaultJSON(s.vault, vaultSigninDev, &p) == nil && p.PollID != "" {
			f.cur = &p
		}
	}
	cur := f.cur
	due := cur != nil && cur.PollID == id && s.now().UnixMilli() >= cur.NextAt
	f.mu.Unlock()
	if due {
		s.pollOnce(r.Context(), id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != nil && f.cur.PollID == id {
		wait := max(f.cur.NextAt-s.now().UnixMilli(), 0)
		writeJSON(w, http.StatusOK, signinState{State: "pending", Signin: f.cur.info(), RetryAfterMs: wait})
		return
	}
	if e, ok := f.ended[id]; ok {
		writeJSON(w, http.StatusOK, signinState{State: e.state, Identity: e.ident, Error: e.err})
		return
	}
	fail(w, refuse(refNotFound, "no such sign-in (or it ended long ago)"))
}

// handleSigninForget is "Forget": the grant revoked at GitHub (through
// global: it needs the client secret), the sign-in cleared here, the
// identity dropped at global. Every person token handed out stops working.
func (s *srv) handleSigninForget(w http.ResponseWriter, r *http.Request, c who) {
	if err := s.needPerson(c); err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	if acc, _, err := s.userTokens(); err == nil && acc.Token != "" {
		_ = s.relay(ctx, http.MethodPost, "partition/revoke-grant", map[string]string{"accessToken": acc.Token}, nil)
	}
	s.clearUser()
	s.signin.mu.Lock()
	s.signin.cur = nil
	s.signin.mu.Unlock()
	_ = s.vault.Delete(vaultSigninDev)
	if err := s.relay(ctx, http.MethodDelete, "partition/identity", nil, nil); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
