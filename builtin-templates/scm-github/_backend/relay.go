// relay.go — global's routes for people's own partitions (API.md §Your
// GitHub sign-in): the identity directory, scoping and revoking a person's
// tokens (the OAuth API needs the App's client secret, which only global
// holds) and a person's bot tokens under botForPeople.
//
// A relay body is the person's own input: their frame or terminal in their
// partition reaches these routes exactly as the partition's backend does.
// So nothing in it is trusted — every field is checked against GitHub and
// the policy here; the person is the call's verified user, never a body
// field. An access token in a body is used in transit and never kept.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// identRec is state "ident/<xbin person>": the identity directory.
type identRec struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	PID   string `json:"pid"` // the person's partition id
	At    int64  `json:"at"`
}

func identKey(person string) string { return "ident/" + person }

func (s *srv) ident(person string) *identRec {
	var r identRec
	if s.state.Get(identKey(person), &r) != nil || r.Login == "" {
		return nil
	}
	return &r
}

// relayStart runs first on every relay call: a partition id other than the
// one the directory holds is another person under the same id (deleted and
// re-created) — the old one's identity and everything kept for it go.
func (s *srv) relayStart(c who) {
	old := s.ident(c.person)
	if old == nil || old.PID == c.pid {
		return
	}
	s.wipePerson(c.person)
}

// wipePerson forgets a person at global: their identity and what the
// events half keeps for them (subscriptions, undelivered events).
func (s *srv) wipePerson(person string) {
	_ = s.state.Delete(identKey(person))
	for _, f := range wipePerson {
		f(s, person)
	}
	// Bot tokens relayed to the person stay recorded: they are the App's,
	// not the person's, and "Revoke all bot tokens" must still reach them
	// (they're never reused, and die within the hour).
}

// checkUserToken asks GitHub whose token this is: only a token this App
// issued answers (a PAT or another App's is 404), with its user.
func (s *srv) checkUserToken(ctx context.Context, tok secretString) (*account, int64, error) {
	if tok.Empty() {
		return nil, 0, refuse(refInvalid, "accessToken is required")
	}
	auth, cid, err := s.basicAuth()
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		ExpiresAt *time.Time `json:"expires_at"`
		User      struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"user"`
	}
	_, err = s.gh.call(ctx, auth, http.MethodPost, s.apiBase()+"/applications/"+pathEsc(cid)+"/token", map[string]string{"access_token": tok.Reveal()}, &out)
	if isRefusal(err, refNotFound) || isRefusal(err, refInvalid) {
		e := refuse(refIdentity, "that isn't a sign-in this GitHub App issued")
		e.Identities = []string{asPerson}
		return nil, 0, e
	}
	if err != nil {
		return nil, 0, err
	}
	if out.User.Login == "" {
		return nil, 0, refuse(refUpstream, "GitHub didn't say whose token it is")
	}
	var exp int64
	if out.ExpiresAt != nil {
		exp = out.ExpiresAt.UnixMilli()
	}
	return &account{Login: out.User.Login, ID: out.User.ID}, exp, nil
}

// handleRelayIdentity registers the person's GitHub login, as GitHub says
// it (never GET /user: any token the person holds would name a login).
func (s *srv) handleRelayIdentity(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	var body struct {
		AccessToken secretString `json:"accessToken"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	acct, _, err := s.checkUserToken(r.Context(), body.AccessToken)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.state.Put(identKey(c.person), identRec{Login: acct.Login, ID: acct.ID, PID: c.pid, At: s.now().UnixMilli()}); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

func (s *srv) handleRelayIdentityDelete(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	s.wipePerson(c.person)
	w.WriteHeader(http.StatusNoContent)
}

// handleRelayScope scopes a person's token to repos and permissions it
// computes itself: the preset for access, narrowed, workflows only under
// allowWorkflows, the owner within allowedAccounts.
func (s *srv) handleRelayScope(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	var body struct {
		AccessToken secretString      `json:"accessToken"`
		Owner       string            `json:"owner"`
		Repos       []string          `json:"repos"`
		Permissions map[string]string `json:"permissions"`
		Access      string            `json:"access"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	req, err := normToken(tokenReq{Repos: body.Repos, Access: body.Access})
	if err != nil {
		fail(w, err)
		return
	}
	if !strings.EqualFold(ownerOf(req.repos[0]), body.Owner) {
		fail(w, refuse(refInvalid, "owner and the repos' owner differ"))
		return
	}
	pol := s.policy()
	if err := pol.checkAccount(body.Owner); err != nil {
		fail(w, err)
		return
	}
	perms, err := narrow(body.Access, body.Permissions, pol.AllowWorkflows)
	if err != nil {
		fail(w, err)
		return
	}
	acct, parentExp, err := s.checkUserToken(ctx, body.AccessToken)
	if err != nil {
		fail(w, err)
		return
	}
	id := s.ident(c.person)
	if id == nil || id.ID != acct.ID {
		e := refuse(refIdentity, "that token isn't this person's registered GitHub sign-in (sign in again)")
		e.Identities = []string{asPerson}
		fail(w, e)
		return
	}
	auth, cid, err := s.basicAuth()
	if err != nil {
		fail(w, err)
		return
	}
	names := make([]string, len(req.repos))
	for i, rp := range req.repos {
		names[i] = nameOf(rp)
	}
	var out struct {
		Token       secretString      `json:"token"`
		ExpiresAt   *time.Time        `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	_, err = s.gh.call(ctx, auth, http.MethodPost, s.apiBase()+"/applications/"+pathEsc(cid)+"/token/scoped", map[string]any{
		"access_token": body.AccessToken.Reveal(), "target": body.Owner, "repositories": names, "permissions": perms,
	}, &out)
	if err != nil {
		fail(w, err)
		return
	}
	exp := parentExp
	if out.ExpiresAt != nil {
		exp = out.ExpiresAt.UnixMilli()
	}
	if pol.PersonTTLMin > 0 {
		capAt := s.now().Add(time.Duration(pol.PersonTTLMin) * time.Minute).UnixMilli()
		if exp == 0 || capAt < exp {
			exp = capAt
		}
	}
	got := out.Permissions
	if len(got) == 0 {
		got = perms
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"token": out.Token.Reveal(), "expiresAt": exp, "repos": req.repos, "permissions": got})
}

// handleRelayRevokeToken revokes one of the person's tokens (best effort).
func (s *srv) handleRelayRevokeToken(w http.ResponseWriter, r *http.Request, c who) {
	s.relayRevoke(w, r, c, "/token")
}

// handleRelayRevokeGrant revokes the person's whole grant ("Forget").
func (s *srv) handleRelayRevokeGrant(w http.ResponseWriter, r *http.Request, c who) {
	s.relayRevoke(w, r, c, "/grant")
}

func (s *srv) relayRevoke(w http.ResponseWriter, r *http.Request, c who, what string) {
	s.relayStart(c)
	var body struct {
		AccessToken secretString `json:"accessToken"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.AccessToken.Empty() {
		fail(w, refuse(refInvalid, "accessToken is required"))
		return
	}
	auth, cid, err := s.basicAuth()
	if err != nil {
		fail(w, err)
		return
	}
	rr, err := s.gh.do(r.Context(), auth, http.MethodDelete, s.apiBase()+"/applications/"+pathEsc(cid)+what, map[string]string{"access_token": body.AccessToken.Reveal()})
	if err != nil && !isRefusal(err, refLimit) {
		fail(w, err)
		return
	}
	if rr != nil && rr.Status >= 500 {
		fail(w, ghError(rr, s.now()))
		return
	}
	w.WriteHeader(http.StatusNoContent) // a token GitHub no longer knows is revoked enough
}

// handleRelayBotToken hands a person's partition a bot token, under
// botForPeople: off refuses; own-access checks the person's own access to
// every repo (their registered login, the collaborator permission API);
// on lets them. The whole policy applies either way.
func (s *srv) handleRelayBotToken(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	var t tokenReq
	if err := readBody(r, &t); err != nil {
		fail(w, err)
		return
	}
	req, err := normToken(t)
	if err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	pol := s.policy()
	switch pol.BotForPeople {
	case "on":
	case "own-access":
		id := s.ident(c.person)
		if id == nil {
			fail(w, refuse(refSignin, "sign in to GitHub first: the bot is handed to people for the repos they can reach themselves"))
			return
		}
		if err := pol.checkBotRepos(req.repos); err != nil {
			fail(w, err)
			return
		}
		for _, rp := range req.repos {
			if err := s.personCan(ctx, id.Login, rp, req.access); err != nil {
				fail(w, err)
				return
			}
		}
	default:
		e := refuse(refIdentity, "this scm-github's policy (botForPeople) doesn't give people's partitions the bot")
		e.Identities = []string{asPerson}
		fail(w, e)
		return
	}
	resp, err := s.botTokenReuse(ctx, "relay|"+c.person+"|"+c.pid, req, false)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, tokenJSON(resp))
}

// personCan checks a login's own access to a repo with the bot's view:
// write (or read for access read) at least.
func (s *srv) personCan(ctx context.Context, login, repo, access string) error {
	auth, err := s.instAuth(ctx, ownerOf(repo), nameOf(repo), "read")
	if err != nil {
		return err
	}
	var out struct {
		Permission string `json:"permission"` // admin | write | read | none
	}
	_, err = s.gh.call(ctx, auth, http.MethodGet, s.apiBase()+"/repos/"+pathEsc(ownerOf(repo))+"/"+pathEsc(nameOf(repo))+"/collaborators/"+pathEsc(login)+"/permission", nil, &out)
	if err != nil && !isRefusal(err, refNotFound) {
		return err
	}
	need := 1
	if access == "write" {
		need = 2
	}
	have := map[string]int{"read": 1, "triage": 1, "write": 2, "maintain": 2, "admin": 2}[out.Permission]
	if have < need {
		return refuse(refNotAllowed, "%s can't %s %s on GitHub, so the bot isn't handed to them for it", login, map[bool]string{true: "push to", false: "read"}[need == 2], repo)
	}
	return nil
}
