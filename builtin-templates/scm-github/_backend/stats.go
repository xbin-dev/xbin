// stats.go — GET /scm/repo/stats (docs/scm.md §Repo stats, D216): a repo
// at a glance — its open pull requests and issues, those merged in the
// last seven days, CI on the default branch, when it was last pushed — in
// one read a consumer's overview makes every few minutes, instead of
// counting through the list routes. The repo itself is one REST read (the
// identity's access, as GET /scm/repo); the counts one GraphQL query; the
// checks GET /scm/checks's on the default branch. A part GitHub doesn't
// answer is absent. Kept 5 minutes per (identity, repo); an answer a part
// of which failed in a way a retry may mend, one minute.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	statsTTL        = 5 * time.Minute
	statsRetryTTL   = time.Minute // an answer missing a part that may come back
	statsMergedDays = 7
)

// statsQuery counts with the repository's connections (totalCount: exact,
// not the search index) and, for the merged ones, search's issueCount —
// GraphQL is the only way to count pull requests by when they merged
// without listing them. One point of GitHub's GraphQL limit (5000 an hour
// per identity), none of REST search's 30 a minute that GET /scm/issues?q=
// spends.
const statsQuery = `query repoStats($owner: String!, $name: String!, $merged: String!) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN) { totalCount }
    issues(states: OPEN) { totalCount }
  }
  merged: search(query: $merged, type: ISSUE, first: 1) { issueCount }
}`

type statsEntry struct {
	st    *repoStats
	retry bool // a part failed transiently: kept statsRetryTTL only
}

func (s *srv) handleRepoStats(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	v, err := s.repoStats(r.Context(), c, q.Get("repo"), q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

// repoStats answers the stats as the identity the call asks for (withAuth:
// the same identities, policy and refusals as every read — checked before
// the cache, so a cached answer never outlives the caller's right to it).
// The repo read failing fails the call (the identity can't see it, the
// limit is spent); the counts or the checks failing leave them out.
func (s *srv) repoStats(ctx context.Context, c who, repo, as string) (*repoStats, error) {
	var out *repoStats
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		key := a.key + " " + strings.ToLower(repo)
		now := s.now()
		if v, ok := s.statsC.get(key, now, statsTTL); ok {
			if e := v.(statsEntry); !e.retry || now.Sub(time.UnixMilli(e.st.At)) < statsRetryTTL {
				out = e.st
				return nil
			}
		}
		var g ghRepo
		if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo), nil, &g); err != nil {
			return err
		}
		st := &repoStats{Repo: repo, URL: g.HTMLURL, DefaultBranch: g.DefaultBranch, PushedAt: ghTime(g.PushedAt), At: now.UnixMilli()}
		var wg sync.WaitGroup
		var retryCounts, retryChecks bool
		wg.Add(2)
		go func() { defer wg.Done(); retryCounts = s.statsCounts(ctx, a, repo, now, st) }()
		go func() { defer wg.Done(); retryChecks = s.statsChecks(ctx, a, repo, g, st) }()
		wg.Wait()
		s.statsC.put(key, statsEntry{st: st, retry: retryCounts || retryChecks}, now)
		out = st
		return nil
	})
	return out, err
}

// statsCounts fills openPulls, openIssues and mergedPulls from one GraphQL
// query; GitHub answers a field it couldn't (no permission, a timeout)
// null, and it stays absent. It answers whether a retry might fill one.
func (s *srv) statsCounts(ctx context.Context, a ghAuth, repo string, now time.Time, st *repoStats) (retry bool) {
	since := now.Add(-statsMergedDays*24*time.Hour).UTC().Format("2006-01-02T15:04:05") + "+00:00"
	data, errs, err := s.gh.graphqlRaw(ctx, a, s.apiBase(), statsQuery, map[string]any{
		"owner": ownerOf(repo), "name": nameOf(repo), "merged": "repo:" + repo + " is:pr is:merged merged:>=" + since})
	if err != nil {
		return transient(err)
	}
	type total struct {
		TotalCount int `json:"totalCount"`
	}
	var d struct {
		Repository *struct {
			PullRequests *total `json:"pullRequests"`
			Issues       *total `json:"issues"`
		} `json:"repository"`
		Merged *struct {
			IssueCount int `json:"issueCount"`
		} `json:"merged"`
	}
	if len(data) == 0 || string(data) == "null" || json.Unmarshal(data, &d) != nil {
		return true
	}
	if r := d.Repository; r != nil {
		if r.PullRequests != nil {
			st.OpenPulls = &r.PullRequests.TotalCount
		}
		if r.Issues != nil {
			st.OpenIssues = &r.Issues.TotalCount
		}
	}
	if d.Merged != nil {
		st.MergedPulls = &mergedPulls{Days: statsMergedDays, Count: d.Merged.IssueCount}
	}
	for _, e := range errs {
		if e.Type != "FORBIDDEN" && e.Type != "NOT_FOUND" {
			return true // RATE_LIMITED, a timeout, GitHub's own trouble
		}
	}
	return false
}

// statsChecks fills defaultBranchChecks: GET /scm/checks on the default
// branch (checksAt), its head commit, its state and GitHub's checks page
// for that commit.
func (s *srv) statsChecks(ctx context.Context, a ghAuth, repo string, g ghRepo, st *repoStats) (retry bool) {
	if g.DefaultBranch == "" {
		return false
	}
	ch, err := s.checksAt(ctx, a, repo, g.DefaultBranch)
	if err != nil {
		return transient(err) // an empty repo's branch has no commit: absent, and that's its answer
	}
	bc := &branchChecks{SHA: ch.SHA, State: ch.State}
	if g.HTMLURL != "" {
		bc.URL = g.HTMLURL + "/commit/" + ch.SHA + "/checks"
	}
	st.DefaultBranchChecks = bc
	return false
}

// transient: a refusal a retry may mend (the rate limit, GitHub down, an
// answer it couldn't map), unlike the host's no (not found, not allowed).
func transient(err error) bool {
	return isRefusal(err, refLimit) || isRefusal(err, refUnavailable) || isRefusal(err, refUpstream)
}
