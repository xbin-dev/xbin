// repos.go — GET /scm/repos and /scm/repo (docs/scm.md §Repos), and the
// one place a read or write picks its GitHub identity (withAuth).
package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// withAuth runs f as the identity a call asks for (as) on repo: the
// person's own token in their partition; the bot's installation token at
// global (botRepos and allowedAccounts apply); a person's partition's bot
// through global, under botForPeople. write picks a token that may write.
func (s *srv) withAuth(ctx context.Context, c who, as, repo string, write bool, f func(a ghAuth) error) error {
	if !validRepo(repo) {
		return refuse(refInvalid, "repo is owner/name")
	}
	as, err := s.resolveAs(c, as)
	if err != nil {
		return err
	}
	pol := s.policy()
	if err := pol.checkAccount(ownerOf(repo)); err != nil {
		return err
	}
	if as == asPerson {
		return s.asPersonDo(ctx, f)
	}
	if s.mode == modeUser {
		access := "read"
		if write {
			access = "write"
		}
		req := &normReq{repos: []string{repo}, access: access, minTTL: minTTLSec, purpose: "scm-github:" + access}
		t, err := s.relayBotToken(ctx, "self|"+access, tokenReq{Repo: repo, Access: access, Purpose: req.purpose}, req)
		if err != nil {
			return err
		}
		return f(bearerAuth("relaybot:"+strings.ToLower(ownerOf(repo)), t.Token))
	}
	if err := pol.checkBotRepos([]string{repo}); err != nil {
		return err
	}
	kind := "read"
	if write {
		kind = "write"
	}
	a, err := s.instAuth(ctx, ownerOf(repo), nameOf(repo), kind)
	if err != nil {
		return err
	}
	return f(a)
}

// ghRepo is GitHub's repository object (the parts used).
type ghRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	CloneURL      string          `json:"clone_url"`
	HTMLURL       string          `json:"html_url"`
	DefaultBranch string          `json:"default_branch"`
	Private       bool            `json:"private"`
	Archived      bool            `json:"archived"`
	Permissions   map[string]bool `json:"permissions"`
}

func (s *srv) repoOf(g ghRepo) repoInfo {
	host, _, _ := s.hosts()
	perm := "none"
	for _, p := range []struct{ gh, us string }{{"admin", "admin"}, {"maintain", "maintain"}, {"push", "write"}, {"triage", "triage"}, {"pull", "read"}} {
		if g.Permissions[p.gh] {
			perm = p.us
			break
		}
	}
	return repoInfo{Host: host, Owner: g.Owner.Login, Name: g.Name, CloneURL: g.CloneURL, DefaultBranch: g.DefaultBranch,
		Private: g.Private, Permission: perm, Archived: g.Archived, URL: g.HTMLURL}
}

// listing is the paging of a list this provider builds itself.
func listing(r *http.Request) (limit, offset int, err error) {
	q := r.URL.Query()
	limit = pageDefault
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 {
			return 0, 0, refuse(refInvalid, "limit is 1 to %d", pageMax)
		}
		limit = min(limit, pageMax)
	}
	if cur := q.Get("cursor"); cur != "" {
		b, derr := base64.RawURLEncoding.DecodeString(cur)
		n, aerr := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if derr != nil || aerr != nil || n < 0 || !strings.HasPrefix(string(b), "o:") {
			return 0, 0, refuse(refInvalid, "cursor isn't one this provider answered")
		}
		offset = n
	}
	return limit, offset, nil
}

func cursorAt(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

// pageOf cuts one page out of a whole list.
func pageOf[T any](all []T, limit, offset int) page[T] {
	p := page[T]{Items: []T{}}
	if offset < len(all) {
		end := min(offset+limit, len(all))
		p.Items = all[offset:end]
		if end < len(all) {
			p.Next = cursorAt(end)
		}
	}
	return p
}

func (s *srv) handleRepos(w http.ResponseWriter, r *http.Request, c who) {
	limit, offset, err := listing(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	all, err := s.repoList(r.Context(), c, q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	if words := strings.ToLower(strings.TrimSpace(q.Get("q"))); words != "" {
		var hit []repoInfo
		for _, rp := range all {
			if strings.Contains(strings.ToLower(rp.Owner+"/"+rp.Name), words) {
				hit = append(hit, rp)
			}
		}
		all = hit
	}
	writeGET(w, r, pageOf(all, limit, offset))
}

const maxRepoList = 1000

// repoList is every repo the identity reaches through the App (cached 5
// min): the person's installations they can see, or the bot's.
func (s *srv) repoList(ctx context.Context, c who, as string) ([]repoInfo, error) {
	as, err := s.resolveAs(c, as)
	if err != nil {
		return nil, err
	}
	pol := s.policy()
	if as == asBot && s.mode == modeUser {
		e := refuse(refIdentity, "the bot's list of repos is the global instance's: name a repo instead")
		e.Identities = []string{asPerson}
		return nil, e
	}
	cacheKey := "bot"
	if as == asPerson {
		rec := s.personRecord()
		if rec == nil {
			return nil, s.signinRefusal(ctx)
		}
		cacheKey = "person:" + rec.Login
	}
	if v, ok := s.reposC.get(cacheKey, s.now(), 5*time.Minute); ok {
		return v.([]repoInfo), nil
	}
	type ghInst struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	var out []repoInfo
	add := func(rs []ghRepo) {
		for _, g := range rs {
			if as == asBot && !pol.botRepoAllowed(g.FullName) {
				continue
			}
			out = append(out, s.repoOf(g))
		}
	}
	if as == asPerson {
		err = s.asPersonDo(ctx, func(a ghAuth) error {
			out = nil
			insts, err := getAll[ghInst](ctx, s.gh, a, s.apiBase()+"/user/installations?per_page=100", "installations", 100)
			if err != nil {
				return err
			}
			for _, in := range insts {
				if !pol.accountAllowed(in.Account.Login) {
					continue
				}
				rs, err := getAll[ghRepo](ctx, s.gh, a, s.apiBase()+"/user/installations/"+strconv.FormatInt(in.ID, 10)+"/repositories?per_page=100", "repositories", maxRepoList)
				if err != nil {
					return err
				}
				add(rs)
			}
			return nil
		})
	} else {
		var auth ghAuth
		if auth, err = s.appAuth(); err != nil {
			return nil, err
		}
		var insts []ghInst
		if insts, err = getAll[ghInst](ctx, s.gh, auth, s.apiBase()+"/app/installations?per_page=100", "", 500); err != nil {
			return nil, err
		}
		for _, in := range insts {
			if !pol.accountAllowed(in.Account.Login) {
				continue
			}
			ia, err := s.instAuthByID(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			rs, err := getAll[ghRepo](ctx, s.gh, ia, s.apiBase()+"/installation/repositories?per_page=100", "repositories", maxRepoList)
			if err != nil {
				return nil, err
			}
			add(rs)
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Owner+"/"+out[i].Name) < strings.ToLower(out[j].Owner+"/"+out[j].Name)
	})
	if len(out) > maxRepoList {
		out = out[:maxRepoList]
	}
	if out == nil {
		out = []repoInfo{}
	}
	s.reposC.put(cacheKey, out, s.now())
	return out, nil
}

// instAuthByID is the read token of an installation known by id.
func (s *srv) instAuthByID(ctx context.Context, inst int64) (ghAuth, error) {
	perms := presetRead()
	key := cacheKey{consumer: "internal", purpose: "read", inst: inst, perms: permsKey(perms)}
	if e := s.intl.get(key, s.now(), 15*time.Minute); e != nil {
		return bearerAuth("inst:"+strconv.FormatInt(inst, 10), e.token), nil
	}
	t, err := s.mint(ctx, inst, nil, perms)
	if err != nil {
		return ghAuth{}, err
	}
	s.intl.put(key, &cachedToken{token: t.Token, expiresAt: t.ExpiresAt, perms: t.Permissions})
	return bearerAuth("inst:"+strconv.FormatInt(inst, 10), t.Token), nil
}

func (s *srv) handleRepo(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	v, err := s.getRepo(r.Context(), c, q.Get("repo"), q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

// repoBase is a repo's API address.
func (s *srv) repoBase(repo string) string {
	return s.apiBase() + "/repos/" + pathEsc(ownerOf(repo)) + "/" + pathEsc(nameOf(repo))
}

func (s *srv) getRepo(ctx context.Context, c who, repo, as string) (*repoInfo, error) {
	var out repoInfo
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		var g ghRepo
		if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo), nil, &g); err != nil {
			return err
		}
		out = s.repoOf(g)
		var b struct {
			Protected bool `json:"protected"`
		}
		// protected is "protection the identity can't bypass" (docs/scm.md
		// §Repos). GitHub's branch flag is any protection at all; an admin
		// may bypass classic protection unless it enforces admins, which
		// only the Administration permission can read — the App has none.
		// So a person who is an admin gets no answer (can't tell); the bot
		// and anyone else get the flag. Rulesets' bypass lists aren't read.
		if g.DefaultBranch != "" {
			if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/branches/"+pathEsc(g.DefaultBranch), nil, &b); err == nil &&
				!(b.Protected && g.Permissions["admin"] && strings.HasPrefix(a.key, "user:")) {
				out.Protected = &b.Protected
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ghTime parses GitHub's RFC 3339 times into unix milliseconds (0: none).
func ghTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
