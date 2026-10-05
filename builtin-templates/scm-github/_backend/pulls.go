// pulls.go — pull requests (docs/scm.md §Pulls): open one (idempotent per
// head and per clientId), list, read (mergeable while GitHub computes it),
// change (draft ↔ ready through GraphQL), and the comment timeline. No
// merge and no approve route in protocol 1.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghPull struct {
	Number         int     `json:"number"`
	NodeID         string  `json:"node_id"`
	HTMLURL        string  `json:"html_url"`
	Title          string  `json:"title"`
	Body           *string `json:"body"`
	State          string  `json:"state"`
	Draft          bool    `json:"draft"`
	MergedAt       *string `json:"merged_at"`
	Mergeable      *bool   `json:"mergeable"`
	MergeableState string  `json:"mergeable_state"`
	Head           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	User              ghUser `json:"user"`
	AuthorAssociation string `json:"author_association"`
	Labels            []struct {
		Name string `json:"name"`
	} `json:"labels"`
	UpdatedAt string `json:"updated_at"`
}

// association maps GitHub's author_association to the contract's five:
// a first-timer or a mannequin is NONE.
func association(a string) string {
	switch a {
	case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
		return a
	}
	return "NONE"
}

func (s *srv) actorOf(u ghUser, assoc string) actor {
	slug := s.public().Slug
	if a, _ := s.app(); a != nil {
		slug = a.Slug
	}
	return actor{Login: u.Login, Association: association(assoc), Bot: u.Type == "Bot", Self: slug != "" && u.Login == slug+"[bot]"}
}

func (s *srv) pullOf(g ghPull, withBody bool) pullInfo {
	p := pullInfo{Number: g.Number, URL: g.HTMLURL, Title: g.Title, State: g.State, Draft: g.Draft, Mergeable: g.Mergeable,
		MergeableState: g.MergeableState, Head: refSide{Ref: g.Head.Ref, SHA: g.Head.SHA}, Base: refSide{Ref: g.Base.Ref, SHA: g.Base.SHA},
		Author: s.actorOf(g.User, g.AuthorAssociation), Labels: []string{}, UpdatedAt: ghTime(g.UpdatedAt)}
	if withBody && g.Body != nil {
		p.Body = *g.Body
	}
	if g.Head.Repo != nil {
		p.Head.Repo = g.Head.Repo.FullName
	}
	if g.MergedAt != nil && *g.MergedAt != "" {
		p.State = "merged"
	}
	if g.State == "open" && g.Mergeable == nil {
		p.MergeableState, p.RetryAfterMs = "unknown", 3000
	}
	for _, l := range g.Labels {
		p.Labels = append(p.Labels, l.Name)
	}
	return p
}

func pullNumber(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 {
		return 0, refuse(refInvalid, "a pull request's number")
	}
	return n, nil
}

func (s *srv) handlePullCreate(w http.ResponseWriter, r *http.Request, c who) {
	var req pullReq
	if err := readBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	switch {
	case req.Head == "" || len(req.Head) > 255:
		fail(w, refuse(refInvalid, "head is the branch to merge"))
		return
	case strings.TrimSpace(req.Title) == "" || len(req.Title) > 1024:
		fail(w, refuse(refInvalid, "title is required (at most 1024 bytes)"))
		return
	case len(req.Body) > 65536:
		fail(w, refuse(refInvalid, "body is at most 65536 bytes"))
		return
	}
	ctx := r.Context()
	idKey, idReq := "", ""
	if req.ClientID != "" {
		b, _ := json.Marshal(req)
		idKey, idReq = c.consumerKey()+"|"+req.ClientID, string(b)
		if e, ok, err := s.pulls.lookup(idKey, idReq); err != nil {
			fail(w, err)
			return
		} else if ok {
			p, err := s.getPull(ctx, c, e.repo, e.number, req.As)
			if err != nil {
				fail(w, err)
				return
			}
			p.Existing = true
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	var out pullInfo
	status := http.StatusCreated
	err := s.withAuth(ctx, c, req.As, req.Repo, true, func(a ghAuth) error {
		base := req.Base
		if base == "" {
			var g ghRepo
			if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(req.Repo), nil, &g); err != nil {
				return err
			}
			base = g.DefaultBranch
		}
		var g ghPull
		rr, err := s.gh.call(ctx, a, http.MethodPost, s.repoBase(req.Repo)+"/pulls", map[string]any{
			"title": req.Title, "head": req.Head, "base": base, "body": req.Body, "draft": req.Draft,
		}, &g)
		if err != nil && rr != nil && rr.Status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(string(rr.Body)), "already exists") {
			ex, ferr := s.findOpenPull(ctx, a, req.Repo, req.Head)
			if ferr != nil {
				return ferr
			}
			out, status = s.pullOf(*ex, true), http.StatusOK
			out.Existing = true
			return nil
		}
		if err != nil {
			return err
		}
		out = s.pullOf(g, true)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	if idKey != "" {
		s.pulls.remember(idKey, idReq, req.Repo, out.Number, s.now())
	}
	writeJSON(w, status, out)
}

// findOpenPull is the open pull request for a head branch.
func (s *srv) findOpenPull(ctx context.Context, a ghAuth, repo, head string) (*ghPull, error) {
	var list []ghPull
	u := s.repoBase(repo) + "/pulls?state=open&head=" + url.QueryEscape(ownerOf(repo)+":"+head)
	if _, err := s.gh.call(ctx, a, http.MethodGet, u, nil, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, refuse(refUpstream, "GitHub says a pull request exists for %s but lists none open", head)
	}
	return &list[0], nil
}

func (s *srv) handlePulls(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	v, err := s.listPulls(r.Context(), c, q)
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

// listPulls passes GitHub's own paging through (the cursor is its page);
// closed and merged are filtered from GitHub's "closed".
func (s *srv) listPulls(ctx context.Context, c who, q url.Values) (*page[pullInfo], error) {
	repo, state := q.Get("repo"), q.Get("state")
	ghState := state
	switch state {
	case "", "open":
		ghState = "open"
	case "closed", "merged":
		ghState = "closed"
	case "all":
	default:
		return nil, refuse(refInvalid, "state is open, closed, merged or all")
	}
	limit, pg, err := ghPaging(q)
	if err != nil {
		return nil, err
	}
	u := url.Values{"state": {ghState}, "per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(pg)}}
	if h := q.Get("head"); h != "" {
		u.Set("head", ownerOf(repo)+":"+h)
	}
	if b := q.Get("base"); b != "" {
		u.Set("base", b)
	}
	out := &page[pullInfo]{Items: []pullInfo{}}
	err = s.withAuth(ctx, c, q.Get("as"), repo, false, func(a ghAuth) error {
		var list []ghPull
		rr, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/pulls?"+u.Encode(), nil, &list)
		if err != nil {
			return err
		}
		for _, g := range list {
			p := s.pullOf(g, false)
			if (state == "closed" && p.State != "closed") || (state == "merged" && p.State != "merged") {
				continue
			}
			out.Items = append(out.Items, p)
		}
		if nextLink(rr.Header) != "" {
			out.Next = cursorAt(pg + 1)
		}
		return nil
	})
	return out, err
}

// ghPaging maps limit and cursor onto GitHub's per_page and page.
func ghPaging(q url.Values) (limit, pg int, err error) {
	r := &http.Request{URL: &url.URL{RawQuery: q.Encode()}}
	limit, off, err := listing(r)
	if err != nil {
		return 0, 0, err
	}
	if off == 0 {
		off = 1
	}
	return limit, off, nil
}

func (s *srv) handlePull(w http.ResponseWriter, r *http.Request, c who) {
	n, err := pullNumber(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	p, err := s.getPull(r.Context(), c, q.Get("repo"), n, q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, p)
}

func (s *srv) getPull(ctx context.Context, c who, repo string, n int, as string) (*pullInfo, error) {
	var out pullInfo
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		var g ghPull
		if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/pulls/"+strconv.Itoa(n), nil, &g); err != nil {
			return err
		}
		out = s.pullOf(g, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// handlePullPatch changes a pull request: title, body and state through
// REST; draft through GraphQL (GitHub's REST can't mark one ready).
func (s *srv) handlePullPatch(w http.ResponseWriter, r *http.Request, c who) {
	n, err := pullNumber(r)
	if err != nil {
		fail(w, err)
		return
	}
	var p pullPatch
	if err := readBody(r, &p); err != nil {
		fail(w, err)
		return
	}
	if p.State != nil && *p.State != "open" && *p.State != "closed" {
		fail(w, refuse(refInvalid, "state is open or closed (protocol 1 has no merge)"))
		return
	}
	ctx := r.Context()
	var out pullInfo
	err = s.withAuth(ctx, c, p.As, p.Repo, true, func(a ghAuth) error {
		u := s.repoBase(p.Repo) + "/pulls/" + strconv.Itoa(n)
		var g ghPull
		patch := map[string]any{}
		if p.Title != nil {
			patch["title"] = *p.Title
		}
		if p.Body != nil {
			patch["body"] = *p.Body
		}
		if p.State != nil {
			patch["state"] = *p.State
		}
		if len(patch) > 0 {
			if _, err := s.gh.call(ctx, a, http.MethodPatch, u, patch, &g); err != nil {
				return err
			}
		} else if _, err := s.gh.call(ctx, a, http.MethodGet, u, nil, &g); err != nil {
			return err
		}
		if p.Draft != nil && *p.Draft != g.Draft {
			m := "markPullRequestReadyForReview"
			if *p.Draft {
				m = "convertPullRequestToDraft"
			}
			q := "mutation($id: ID!) { " + m + "(input: {pullRequestId: $id}) { pullRequest { isDraft } } }"
			ga := a
			if strings.HasPrefix(a.key, "inst:") {
				// The tile's own write token can't: GitHub's GraphQL wants
				// contents: write of an installation token for these two.
				var err error
				if ga, err = s.instAuth(ctx, ownerOf(p.Repo), nameOf(p.Repo), "draft"); err != nil {
					return err
				}
			}
			if err := s.gh.graphql(ctx, ga, s.apiBase(), q, map[string]any{"id": g.NodeID}, nil); err != nil {
				return err
			}
			if _, err := s.gh.call(ctx, a, http.MethodGet, u, nil, &g); err != nil {
				return err
			}
		}
		out = s.pullOf(g, true)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type ghComment struct {
	ID                int64  `json:"id"`
	User              ghUser `json:"user"`
	AuthorAssociation string `json:"author_association"`
	Body              string `json:"body"`
	HTMLURL           string `json:"html_url"`
	CreatedAt         string `json:"created_at"`
	SubmittedAt       string `json:"submitted_at"` // reviews
	State             string `json:"state"`        // reviews
	Path              string `json:"path"`         // review comments
	Line              *int   `json:"line"`
	OriginalLine      *int   `json:"original_line"`
}

func (s *srv) commentOf(g ghComment, kind string) comment {
	c := comment{ID: kind[:1] + ":" + strconv.FormatInt(g.ID, 10), Kind: kind, Author: s.actorOf(g.User, g.AuthorAssociation),
		Body: g.Body, URL: g.HTMLURL, CreatedAt: ghTime(g.CreatedAt)}
	switch kind {
	case "review":
		c.State, c.CreatedAt = strings.ToLower(g.State), ghTime(g.SubmittedAt)
	case "review-comment":
		c.ID = "rc:" + strconv.FormatInt(g.ID, 10)
		c.Path = g.Path
		if g.Line != nil {
			c.Line = *g.Line
		} else if g.OriginalLine != nil {
			c.Line = *g.OriginalLine
		}
	}
	return c
}

func (s *srv) handleComments(w http.ResponseWriter, r *http.Request, c who) {
	n, err := pullNumber(r)
	if err != nil {
		fail(w, err)
		return
	}
	limit, offset, err := listing(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	all, err := s.timeline(r.Context(), c, q.Get("repo"), n, since, q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, pageOf(all, limit, offset))
}

// timeline merges a pull request's issue comments, reviews and review
// comments by time, oldest first (since: created at or after).
func (s *srv) timeline(ctx context.Context, c who, repo string, n int, since int64, as string) ([]comment, error) {
	var all []comment
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		all = nil
		base := s.repoBase(repo)
		sinceQ := ""
		if since > 0 {
			sinceQ = "&since=" + url.QueryEscape(time.UnixMilli(since).UTC().Format(time.RFC3339))
		}
		for _, l := range []struct{ url, kind string }{
			{base + "/issues/" + strconv.Itoa(n) + "/comments?per_page=100" + sinceQ, "comment"},
			{base + "/pulls/" + strconv.Itoa(n) + "/reviews?per_page=100", "review"},
			{base + "/pulls/" + strconv.Itoa(n) + "/comments?per_page=100" + sinceQ, "review-comment"},
		} {
			items, err := getAll[ghComment](ctx, s.gh, a, l.url, "", 1000)
			if err != nil {
				return err
			}
			for _, g := range items {
				if l.kind == "review" && g.State == "PENDING" {
					continue
				}
				all = append(all, s.commentOf(g, l.kind))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []comment{}
	for _, cm := range all {
		if cm.CreatedAt >= since {
			out = append(out, cm)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *srv) handleCommentCreate(w http.ResponseWriter, r *http.Request, c who) {
	n, err := pullNumber(r)
	if err != nil {
		fail(w, err)
		return
	}
	var body struct {
		Repo string `json:"repo"`
		Body string `json:"body"`
		As   string `json:"as"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if strings.TrimSpace(body.Body) == "" || len(body.Body) > 65536 {
		fail(w, refuse(refInvalid, "body is required (at most 65536 bytes)"))
		return
	}
	ctx := r.Context()
	var out comment
	err = s.withAuth(ctx, c, body.As, body.Repo, true, func(a ghAuth) error {
		var g ghComment
		if _, err := s.gh.call(ctx, a, http.MethodPost, s.repoBase(body.Repo)+"/issues/"+strconv.Itoa(n)+"/comments", map[string]string{"body": body.Body}, &g); err != nil {
			return err
		}
		out = s.commentOf(g, "comment")
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
