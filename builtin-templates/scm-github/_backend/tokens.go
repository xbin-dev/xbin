// tokens.go — POST /scm/token and /scm/token/revoke (docs/scm.md
// §Credentials): which identity a caller may ask for, the request's
// checks, and the explicit body that carries a token.
package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

// normReq is a token request checked and normalised.
type normReq struct {
	repos   []string
	access  string
	perms   map[string]string
	minTTL  int
	purpose string
}

// normToken checks a token request: one owner, ≤ reposPerToken repos,
// access read or write, minTtlSec within what GitHub's tokens can meet.
func normToken(t tokenReq) (*normReq, error) {
	repos := t.Repos
	if t.Repo != "" {
		repos = append([]string{t.Repo}, repos...)
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range repos {
		if !validRepo(r) {
			return nil, refuse(refInvalid, "a repo is owner/name (%q isn't)", clip(r, 120))
		}
		if !seen[strings.ToLower(r)] {
			seen[strings.ToLower(r)] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, refuse(refInvalid, "name a repo (repo) or repos")
	}
	if len(out) > reposPerToken {
		return nil, refuse(refInvalid, "at most %d repos per token", reposPerToken)
	}
	owner := ownerOf(out[0])
	for _, r := range out[1:] {
		if !strings.EqualFold(ownerOf(r), owner) {
			return nil, refuse(refInvalid, "a token's repos belong to one owner (%s and %s): ask once per owner", owner, ownerOf(r))
		}
	}
	if t.Access != "read" && t.Access != "write" {
		return nil, refuse(refInvalid, "access is read or write")
	}
	ttl := t.MinTTLSec
	if ttl == 0 {
		ttl = minTTLSec
	}
	if ttl < minTTLSec || ttl > maxTTLSec {
		return nil, refuse(refInvalid, "minTtlSec is %d to %d (GitHub's tokens live an hour)", minTTLSec, maxTTLSec)
	}
	if len(t.Purpose) > 200 {
		return nil, refuse(refInvalid, "purpose is at most 200 bytes")
	}
	return &normReq{repos: sortedRepos(out), access: t.Access, perms: t.Permissions, minTTL: ttl, purpose: t.Purpose}, nil
}

// identities says which `as` a caller may use here, and its default
// (docs/scm.md §Identities).
func (s *srv) identities(c who) (ids []string, def string) {
	switch {
	case s.mode == modeUser && (c.cls == clsPersonConsumer || c.cls == clsPersonPage):
		ids, def = []string{asPerson}, asPerson
		if s.policy().BotForPeople != "off" {
			ids = append(ids, asBot)
		}
	case s.mode == modeLegacy && (c.cls == clsPersonConsumer || c.cls == clsPersonPage):
		// No partition can hold a person's sign-in: the bot only, and
		// only when botForPeople is on (own-access has no token to check).
		ids, def = []string{}, asBot
		if s.policy().BotForPeople == "on" {
			ids = append(ids, asBot)
		}
	default:
		ids, def = []string{asBot}, asBot
	}
	return
}

// resolveAs picks the identity of a call: as asked, or the caller's
// default; 403 identity (with what it may use) otherwise.
func (s *srv) resolveAs(c who, as string) (string, error) {
	ids, def := s.identities(c)
	if as == "" {
		as = def
	}
	if as != asPerson && as != asBot {
		return "", refuse(refInvalid, "as is person or bot")
	}
	if !slices.Contains(ids, as) {
		e := refuse(refIdentity, "%s", identityWhy(s.mode, c, as))
		e.Identities = ids
		return "", e
	}
	return as, nil
}

func identityWhy(mode string, c who, as string) string {
	switch {
	case as == asPerson && mode != modeUser:
		return "no person's sign-in is here: a person's identity is in their own partition of this tile, reached by their partition of the consumer"
	case as == asBot && (c.cls == clsPersonConsumer || c.cls == clsPersonPage):
		return "this scm-github's policy (botForPeople) doesn't give people's partitions the bot"
	}
	return "not as that identity here"
}

func (s *srv) handleToken(w http.ResponseWriter, r *http.Request, c who) {
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
	as, err := s.resolveAs(c, t.As)
	if err != nil {
		fail(w, err)
		return
	}
	var resp *tokenResp
	switch {
	case as == asPerson:
		resp, err = s.personToken(r.Context(), c.consumerKey(), req)
	case s.mode == modeUser:
		resp, err = s.relayBotToken(r.Context(), c.consumerKey(), t, req)
	default:
		resp, err = s.botToken(r.Context(), c.consumerKey(), req)
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, tokenJSON(resp))
}

// handleRevoke forgets a token this consumer was given ({token}, matched
// by hash) or every live one of a purpose, revoking each upstream (best
// effort).
func (s *srv) handleRevoke(w http.ResponseWriter, r *http.Request, c who) {
	var body struct {
		Token   secretString `json:"token"`
		Purpose string       `json:"purpose"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	consumer := c.consumerKey()
	ctx := r.Context()
	switch {
	case !body.Token.Empty():
		key, ok := s.bot.byValue(consumer, body.Token.Reveal())
		if !ok {
			fail(w, refuse(refNotFound, "no live token of this consumer matches"))
			return
		}
		s.bot.forgetValue(body.Token.Reveal())
		s.revokeOne(ctx, key.kind, body.Token)
	case body.Purpose != "":
		gone := s.bot.take(s.now(), func(k cacheKey) bool { return k.consumer == consumer && k.purpose == body.Purpose })
		if len(gone) == 0 {
			fail(w, refuse(refNotFound, "no live token of this consumer has that purpose"))
			return
		}
		for _, e := range gone {
			s.revokeOne(ctx, e.key.kind, e.tok.token)
		}
	default:
		fail(w, refuse(refInvalid, "name the token or the purpose"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeOne revokes a token upstream: a bot token itself; a person's
// scoped token through global (the OAuth API needs the client secret).
func (s *srv) revokeOne(ctx context.Context, kind string, tok secretString) {
	if kind == asPerson {
		_ = s.relay(ctx, http.MethodPost, "partition/revoke-token", map[string]string{"accessToken": tok.Reveal()}, nil)
		return
	}
	s.revokeBotToken(ctx, tok)
}
