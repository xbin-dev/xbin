// issues.go — issues (docs/scm.md §Issues): a list (pull requests left
// out; q searches) and one issue with its comments.
package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ghIssue struct {
	Number            int     `json:"number"`
	Title             string  `json:"title"`
	Body              *string `json:"body"`
	State             string  `json:"state"`
	HTMLURL           string  `json:"html_url"`
	User              ghUser  `json:"user"`
	AuthorAssociation string  `json:"author_association"`
	Labels            []struct {
		Name string `json:"name"`
	} `json:"labels"`
	UpdatedAt   string    `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
}

func (s *srv) issueOf(g ghIssue) issueInfo {
	i := issueInfo{Number: g.Number, Title: g.Title, State: g.State, Labels: []string{}, Author: s.actorOf(g.User, g.AuthorAssociation),
		URL: g.HTMLURL, UpdatedAt: ghTime(g.UpdatedAt)}
	if g.Body != nil {
		i.Body = *g.Body
	}
	for _, l := range g.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

func (s *srv) handleIssues(w http.ResponseWriter, r *http.Request, c who) {
	v, err := s.listIssues(r.Context(), c, r.URL.Query())
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

func (s *srv) listIssues(ctx context.Context, c who, q url.Values) (*page[issueInfo], error) {
	repo, state := q.Get("repo"), q.Get("state")
	switch state {
	case "":
		state = "open"
	case "open", "closed", "all":
	default:
		return nil, refuse(refInvalid, "state is open, closed or all")
	}
	limit, pg, err := ghPaging(q)
	if err != nil {
		return nil, err
	}
	var u string
	words := strings.TrimSpace(q.Get("q"))
	if words != "" {
		// GitHub's search: its own rate limit, its own answer shape.
		sq := "repo:" + repo + " is:issue " + words
		if state != "all" {
			sq += " state:" + state
		}
		for _, l := range strings.Split(q.Get("labels"), ",") {
			if l = strings.TrimSpace(l); l != "" {
				sq += ` label:"` + strings.ReplaceAll(l, `"`, "") + `"`
			}
		}
		u = s.apiBase() + "/search/issues?" + url.Values{"q": {sq}, "per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(pg)}}.Encode()
	} else {
		v := url.Values{"state": {state}, "per_page": {strconv.Itoa(limit)}, "page": {strconv.Itoa(pg)}}
		if l := q.Get("labels"); l != "" {
			v.Set("labels", l)
		}
		if since, _ := strconv.ParseInt(q.Get("since"), 10, 64); since > 0 {
			v.Set("since", time.UnixMilli(since).UTC().Format(time.RFC3339))
		}
		u = s.repoBase(repo) + "/issues?" + v.Encode()
	}
	out := &page[issueInfo]{Items: []issueInfo{}}
	err = s.withAuth(ctx, c, q.Get("as"), repo, false, func(a ghAuth) error {
		rr, err := s.gh.call(ctx, a, http.MethodGet, u, nil, nil)
		if err != nil {
			return err
		}
		var list []ghIssue
		if words != "" {
			var wrap struct {
				Items []ghIssue `json:"items"`
			}
			err = jsonUnmarshal(rr.Body, &wrap)
			list = wrap.Items
		} else {
			err = jsonUnmarshal(rr.Body, &list)
		}
		if err != nil {
			return refuse(refUpstream, "GitHub's issue list wasn't the JSON expected")
		}
		for _, g := range list {
			if g.PullRequest != nil {
				continue
			}
			out.Items = append(out.Items, s.issueOf(g))
		}
		if nextLink(rr.Header) != "" {
			out.Next = cursorAt(pg + 1)
		}
		return nil
	})
	return out, err
}

func (s *srv) handleIssue(w http.ResponseWriter, r *http.Request, c who) {
	n, err := pullNumber(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	v, err := s.getIssue(r.Context(), c, q.Get("repo"), n, q.Get("comments") == "1" || q.Get("comments") == "true", q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

func (s *srv) getIssue(ctx context.Context, c who, repo string, n int, comments bool, as string) (*issueInfo, error) {
	var out issueInfo
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		var g ghIssue
		if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/issues/"+strconv.Itoa(n), nil, &g); err != nil {
			return err
		}
		if g.PullRequest != nil {
			return refuse(refNotFound, "#%d is a pull request: read it with /scm/pulls/%d", n, n)
		}
		out = s.issueOf(g)
		if comments {
			items, err := getAll[ghComment](ctx, s.gh, a, s.repoBase(repo)+"/issues/"+strconv.Itoa(n)+"/comments?per_page=100", "", 1000)
			if err != nil {
				return err
			}
			out.Comments = []comment{}
			for _, cm := range items {
				out.Comments = append(out.Comments, s.commentOf(cm, "comment"))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
