// person.go — a person's GitHub identity in their own partition: the
// device-flow tokens in the partition's vault, the refresh (direct, no
// client secret), the epoch every handed-out token of the person shares,
// and person tokens scoped through global.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Vault names in a person's partition.
const (
	vaultUserAccess  = "user-access"
	vaultUserRefresh = "user-refresh"
	vaultSigninDev   = "signin-device"
)

// personRec is state "person": the sign-in's summary, never a token.
type personRec struct {
	Login            string `json:"login"`
	ID               int64  `json:"id"`
	ExpiresAt        int64  `json:"expiresAt"`        // the access token's expiry
	RefreshExpiresAt int64  `json:"refreshExpiresAt"` // the sign-in's own (the refresh token's)
	Epoch            int64  `json:"epoch"`            // bumped at each refresh: every scoped token of the epoch rotates together
	Registered       bool   `json:"registered"`       // global knows this sign-in (POST /partition/identity)
}

// vaultTok is a token as the vault keeps it (one JSON string).
type vaultTok struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

// epochSlack: handed-out tokens expire this long before the parent token,
// so the parent is refreshed only once every one of them is due anyway.
const epochSlack = 60 * time.Minute

func (s *srv) personRecord() *personRec {
	var p personRec
	if s.state.Get("person", &p) != nil || p.Login == "" {
		return nil
	}
	return &p
}

// userTokens reads the pair from the vault.
func (s *srv) userTokens() (access, refresh vaultTok, err error) {
	if err = vaultJSON(s.vault, vaultUserAccess, &access); err != nil {
		return
	}
	err = vaultJSON(s.vault, vaultUserRefresh, &refresh)
	return
}

// clearUser forgets the sign-in in this partition (vault and state).
func (s *srv) clearUser() {
	_ = s.vault.Delete(vaultUserAccess)
	_ = s.vault.Delete(vaultUserRefresh)
	_ = s.state.Delete("person")
	// Scoped tokens already handed out stay recorded (a revoke by value or
	// purpose still reaches them through global); none is reused.
	s.bot.retire(func(k cacheKey) bool { return k.kind == asPerson })
	s.reposC.clear()
}

// ensureUser answers the person's parent access token, refreshing it when
// the epoch (its expiry less epochSlack) has less than need left. Not
// signed in: 409 signin, with a device flow started.
func (s *srv) ensureUser(ctx context.Context, need time.Duration) (secretString, *personRec, error) {
	rec := s.personRecord()
	acc, ref, err := s.userTokens()
	if rec == nil || err != nil || acc.Token == "" {
		return secretString{}, nil, s.signinRefusal(ctx)
	}
	now := s.now()
	if time.UnixMilli(acc.ExpiresAt).Add(-epochSlack).Sub(now) >= need {
		return newSecret(acc.Token), rec, nil
	}
	if ref.ExpiresAt != 0 && now.UnixMilli() >= ref.ExpiresAt {
		s.clearUser()
		return secretString{}, nil, s.signinRefusal(ctx)
	}
	return s.refresh(ctx, acc.Token)
}

// refresh trades the refresh token for a new pair (GitHub invalidates the
// old pair). One at a time: a second caller finds the pair already new.
func (s *srv) refresh(ctx context.Context, stale string) (secretString, *personRec, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	acc, ref, err := s.userTokens()
	rec := s.personRecord()
	if err != nil || rec == nil {
		return secretString{}, nil, s.signinRefusal(ctx)
	}
	if acc.Token != stale { // refreshed meanwhile
		return newSecret(acc.Token), rec, nil
	}
	tok, err := s.tradeRefresh(ctx, rec, ref)
	if errors.Is(err, errBadRefresh) {
		s.clearUser()
		return secretString{}, nil, s.signinRefusal(ctx)
	}
	if err != nil {
		return secretString{}, nil, err
	}
	return tok, s.personRecord(), nil
}

// errBadRefresh: GitHub no longer takes the refresh token (bad_refresh_token,
// or incorrect_client_credentials for a sign-in GitHub no longer knows:
// signinGone) — the sign-in is over. Never answered as is: each caller decides.
var errBadRefresh = errors.New("GitHub no longer takes the sign-in's refresh token")

// tradeRefresh is the refresh itself, refreshMu held: the new pair kept.
func (s *srv) tradeRefresh(ctx context.Context, rec *personRec, ref vaultTok) (secretString, error) {
	pub := s.public()
	_, _, web := s.hosts()
	form := url.Values{"client_id": {pub.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {ref.Token}}
	var out oauthTokenResp
	if err := s.oauthPost(ctx, web+"/login/oauth/access_token", form, &out); err != nil {
		return secretString{}, err
	}
	switch out.Error {
	case "":
	case "bad_refresh_token":
		return secretString{}, errBadRefresh
	case "incorrect_client_credentials":
		if s.signinGone(ctx) {
			return secretString{}, errBadRefresh
		}
		fallthrough
	default:
		return secretString{}, refuse(refUpstream, "GitHub didn't refresh the sign-in: %s", clip(out.Error, 80))
	}
	if err := s.storeUser(out, rec.Login, rec.ID, s.now()); err != nil {
		return secretString{}, err
	}
	// Scoped tokens of the old epoch may die with their parent: none is
	// handed out again, but each stays recorded so a revoke still reaches it.
	s.bot.retire(func(k cacheKey) bool { return k.kind == asPerson })
	return newSecret(out.AccessToken.Reveal()), nil
}

// signinGone: GitHub answers a refresh with incorrect_client_credentials,
// not bad_refresh_token, once the sign-in's grant is revoked (Forget, or
// the person revoking the App in their GitHub settings) — and the same for
// a wrong client id. So global asks GitHub about the sign-in's access
// token: a token GitHub doesn't know (404) is the sign-in over. Anything
// else — GitHub still knows it, any other refusal, no answer — isn't
// known: false, nothing cleared. (GitHub's check answers 404 for basic
// auth it refuses too — live; the client id is GitHub's own, from setup.)
func (s *srv) signinGone(ctx context.Context) bool {
	var acc vaultTok
	if vaultJSON(s.vault, vaultUserAccess, &acc) != nil || acc.Token == "" {
		return false
	}
	var out struct {
		Known *bool `json:"known"`
	}
	if err := s.relay(ctx, http.MethodPost, "partition/check-token", map[string]string{"accessToken": acc.Token}, &out); err != nil {
		return false
	}
	return out.Known != nil && !*out.Known
}

// forgetSlack: Forget refreshes an access token this close to its expiry
// first, so GitHub still knows the token the grant is revoked with.
const forgetSlack = time.Minute

// forgetToken answers the access token Forget revokes the grant with: the
// vault's, refreshed first once it has expired — GitHub no longer knows an
// expired token, so revoking with it would leave the grant (and its live
// refresh token) authorised. Empty: nothing is left to revoke it with (no
// sign-in, the refresh token expired or refused). Any other refresh failure
// is answered, nothing cleared.
func (s *srv) forgetToken(ctx context.Context) (string, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var acc, ref vaultTok
	if vaultJSON(s.vault, vaultUserAccess, &acc) != nil || acc.Token == "" {
		return "", nil
	}
	_ = vaultJSON(s.vault, vaultUserRefresh, &ref)
	now := s.now()
	rec := s.personRecord()
	if acc.ExpiresAt == 0 || now.Before(time.UnixMilli(acc.ExpiresAt).Add(-forgetSlack)) || ref.Token == "" || rec == nil {
		// Live, or nothing to refresh it with: revoked as it is (a token
		// GitHub no longer knows counts as revoked).
		return acc.Token, nil
	}
	if ref.ExpiresAt != 0 && now.UnixMilli() >= ref.ExpiresAt {
		return "", nil
	}
	tok, err := s.tradeRefresh(ctx, rec, ref)
	if errors.Is(err, errBadRefresh) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tok.Reveal(), nil
}

// oauthTokenResp is /login/oauth/access_token's answer (GitHub answers 200
// with an error field for the device flow's and the refresh's refusals).
type oauthTokenResp struct {
	AccessToken           secretString `json:"access_token"`
	ExpiresIn             int64        `json:"expires_in"`
	RefreshToken          secretString `json:"refresh_token"`
	RefreshTokenExpiresIn int64        `json:"refresh_token_expires_in"`
	Interval              int64        `json:"interval"`
	Error                 string       `json:"error"`
}

// oauthPost posts a form to GitHub's web OAuth endpoints, asking for JSON.
func (s *srv) oauthPost(ctx context.Context, u string, form url.Values, out any) error {
	r, err := s.gh.form(ctx, u, form)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		if r.Status >= 300 {
			return ghError(r, s.now())
		}
		return refuse(refUpstream, "GitHub's sign-in answer wasn't the JSON expected")
	}
	return nil
}

// storeUser keeps a new pair and the person's summary (epoch + 1).
func (s *srv) storeUser(out oauthTokenResp, login string, id int64, now time.Time) error {
	if out.AccessToken.Empty() {
		return refuse(refUpstream, "GitHub answered no token")
	}
	exp := now.Add(time.Duration(out.ExpiresIn) * time.Second).UnixMilli()
	if out.ExpiresIn == 0 {
		// "Expire user authorization tokens" is off: the token never
		// expires; the epoch still rotates handed-out tokens every 8 h.
		exp = now.Add(8 * time.Hour).UnixMilli()
	}
	var refExp int64
	if out.RefreshTokenExpiresIn > 0 {
		refExp = now.Add(time.Duration(out.RefreshTokenExpiresIn) * time.Second).UnixMilli()
	}
	if err := setVaultJSON(s.vault, vaultUserAccess, vaultTok{out.AccessToken.Reveal(), exp}); err != nil {
		return err
	}
	if err := setVaultJSON(s.vault, vaultUserRefresh, vaultTok{out.RefreshToken.Reveal(), refExp}); err != nil {
		return err
	}
	prev := s.personRecord()
	epoch := int64(1)
	if prev != nil {
		epoch = prev.Epoch + 1
	}
	reg := prev != nil && prev.Registered && prev.ID == id
	return s.state.Put("person", personRec{Login: login, ID: id, ExpiresAt: exp, RefreshExpiresAt: refExp, Epoch: epoch, Registered: reg})
}

// asPersonDo runs f with the person's token for the partition's own calls
// (reads, writes); a 401 refreshes once and retries.
func (s *srv) asPersonDo(ctx context.Context, f func(a ghAuth) error) error {
	tok, rec, err := s.ensureUser(ctx, 0)
	if err != nil {
		return err
	}
	err = f(bearerAuth("user:"+rec.Login, tok))
	var e *scmErr
	if errors.As(err, &e) && e.Upstream != nil && e.Upstream.Status == http.StatusUnauthorized {
		if tok, rec, err = s.refresh(ctx, tok.Reveal()); err != nil {
			return err
		}
		return f(bearerAuth("user:"+rec.Login, tok))
	}
	return err
}

// scopedResp is the relay's /partition/scope answer.
type scopedResp struct {
	Token       secretString      `json:"token"`
	ExpiresAt   int64             `json:"expiresAt"`
	Repos       []string          `json:"repos"`
	Permissions map[string]string `json:"permissions"`
}

// personToken hands out a token of the person's: a scoped copy of their
// sign-in (global scopes it — the OAuth API needs the client secret),
// expiring at the epoch's end so the parent refreshes only when every
// token of it is due.
func (s *srv) personToken(ctx context.Context, consumer string, req *normReq) (*tokenResp, error) {
	need := tokenMargin(req.minTTL)
	gen, err := s.partitionCheck(asPerson, req)
	if err != nil {
		return nil, err
	}
	key := cacheKey{consumer: consumer, purpose: req.purpose, repos: strings.Join(req.repos, ","), perms: req.access + ":" + permsKey(req.perms), kind: asPerson, gen: gen}
	if t := s.bot.get(key, s.now(), need); t != nil {
		return t.resp, nil
	}
	if err := s.bot.room(consumer, s.now()); err != nil {
		return nil, err
	}
	tok, rec, err := s.ensureUser(ctx, need)
	if err != nil {
		return nil, err
	}
	if !rec.Registered {
		if err := s.register(ctx, tok); err != nil {
			return nil, err
		}
	}
	var sc scopedResp
	err = s.relay(ctx, http.MethodPost, "partition/scope", map[string]any{
		"accessToken": tok.Reveal(), "owner": ownerOf(req.repos[0]), "repos": req.repos,
		"permissions": req.perms, "access": req.access,
	}, &sc)
	if err != nil {
		return nil, err
	}
	exp := time.UnixMilli(rec.ExpiresAt).Add(-epochSlack)
	if sc.ExpiresAt > 0 && time.UnixMilli(sc.ExpiresAt).Before(exp) {
		exp = time.UnixMilli(sc.ExpiresAt)
	}
	host, _, _ := s.hosts()
	refreshAfter := exp.Add(-10 * time.Minute)
	if refreshAfter.Before(s.now()) {
		refreshAfter = s.now()
	}
	resp := &tokenResp{
		Host: host, Username: "x-access-token", Token: sc.Token,
		ExpiresAt: exp.UnixMilli(), RefreshAfter: refreshAfter.UnixMilli(),
		Identity:    identity{Kind: asPerson, Login: rec.Login, ID: rec.ID},
		Author:      author{Name: rec.Login, Email: fmt.Sprintf("%d+%s@users.noreply.%s", rec.ID, rec.Login, host)},
		Repos:       sc.Repos,
		Permissions: sc.Permissions,
	}
	if len(resp.Repos) == 0 {
		resp.Repos = req.repos
	}
	s.bot.put(key, &cachedToken{token: sc.Token, expiresAt: exp, perms: sc.Permissions, repos: resp.Repos, resp: resp})
	return resp, nil
}

// relayBotToken gets a bot token for a person's partition from global,
// which applies botForPeople and the whole policy itself.
func (s *srv) relayBotToken(ctx context.Context, consumer string, t tokenReq, req *normReq) (*tokenResp, error) {
	gen, err := s.partitionCheck(asBot, req)
	if err != nil {
		return nil, err
	}
	key := cacheKey{consumer: consumer, purpose: req.purpose, repos: strings.Join(req.repos, ","), perms: req.access + ":" + permsKey(req.perms), kind: asBot, gen: gen}
	if c := s.bot.get(key, s.now(), tokenMargin(req.minTTL)); c != nil {
		return c.resp, nil
	}
	if err := s.bot.room(consumer, s.now()); err != nil {
		return nil, err
	}
	t.As = asBot
	var resp tokenResp
	if err := s.relay(ctx, http.MethodPost, "partition/bot-token", t, &resp); err != nil {
		return nil, err
	}
	s.bot.put(key, &cachedToken{token: resp.Token, expiresAt: time.UnixMilli(resp.ExpiresAt), perms: resp.Permissions, repos: resp.Repos, resp: &resp})
	return &resp, nil
}

// partitionCheck runs in a person's partition before any token is reused
// or asked for: the request against the policy as conf "public" has it
// (global checks again whatever it relays), and the token generation —
// one that moved (a policy change, "Revoke all bot tokens", a new App)
// ends reuse of everything handed out under the old one. It answers the
// generation, part of the cache key.
func (s *srv) partitionCheck(kind string, req *normReq) (int64, error) {
	pub := s.public()
	s.mu.Lock()
	moved := s.seenGen != pub.TokenGen
	s.seenGen = pub.TokenGen
	s.mu.Unlock()
	if moved {
		s.bot.clear()
	}
	pol := s.policy()
	if kind == asBot {
		if err := pol.checkBotRepos(req.repos); err != nil {
			return 0, err
		}
	} else if err := pol.checkAccount(ownerOf(req.repos[0])); err != nil {
		return 0, err
	}
	if _, err := narrow(req.access, req.perms, pol.AllowWorkflows); err != nil {
		return 0, err
	}
	return pub.TokenGen, nil
}
