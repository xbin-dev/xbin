// fakegh_stats_test.go — the fake GitHub's answer to the repo stats
// GraphQL query (stats.go): the repository's open pull requests and open
// issues (pull requests not counted, as GitHub's issues connection) and
// search's count of the pull requests merged since the query's date. A
// token that can't see the repo gets repository null (NOT_FOUND); one
// without a connection's permission gets that connection null (FORBIDDEN);
// search counts only what the token can read. f.ci.gqlFail nulls one part
// ("merged", "issues") with the error GitHub gives a timed-out part.
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (p *fPull) mergedAt() time.Time {
	if p.MergedAt.IsZero() {
		return time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	}
	return p.MergedAt
}

func (f *fakeGH) repoStatsGQL(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	owner, _ := vars["owner"].(string)
	name, _ := vars["name"].(string)
	q, _ := vars["merged"].(string)
	repo := owner + "/" + name
	f.mu.Lock()
	f.ci.searches = append(f.ci.searches, q)
	failPart := f.ci.gqlFail
	f.mu.Unlock()
	var errs []any
	gqlErr := func(typ, msg string, path ...any) {
		e := map[string]any{"message": msg, "path": path}
		if typ != "" {
			e["type"] = typ
		}
		errs = append(errs, e)
	}
	timeout := "Something went wrong while executing your query. This may be the result of a timeout, or it could be a GitHub bug."
	data := map[string]any{}

	if f.access(r, repo, "metadata") == "" {
		data["repository"] = nil
		gqlErr("NOT_FOUND", "Could not resolve to a Repository with the name '"+repo+"'.", "repository")
	} else {
		canPulls, canIssues := f.access(r, repo, "pull_requests") != "", f.access(r, repo, "issues") != ""
		f.mu.Lock()
		var openPulls, openIssues int
		for _, p := range f.ci.pulls[repo] {
			if p.State == "open" {
				openPulls++
			}
		}
		for _, i := range f.ci.issues[repo] {
			if !i.PR && i.State == "open" {
				openIssues++
			}
		}
		f.mu.Unlock()
		rp := map[string]any{"pullRequests": nil, "issues": nil}
		if canPulls {
			rp["pullRequests"] = map[string]any{"totalCount": openPulls}
		} else {
			gqlErr("FORBIDDEN", "Resource not accessible by integration", "repository", "pullRequests")
		}
		switch {
		case failPart == "issues":
			gqlErr("", timeout, "repository", "issues")
		case canIssues:
			rp["issues"] = map[string]any{"totalCount": openIssues}
		default:
			gqlErr("FORBIDDEN", "Resource not accessible by integration", "repository", "issues")
		}
		data["repository"] = rp
	}

	// search: repo:<o/n> is:pr is:merged merged:>=<time>
	var sRepo string
	var since time.Time
	isPR, isMerged := false, false
	for _, t := range strings.Fields(q) {
		k, v, _ := strings.Cut(t, ":")
		switch {
		case k == "repo":
			sRepo = v
		case t == "is:pr":
			isPR = true
		case t == "is:merged":
			isMerged = true
		case k == "merged" && strings.HasPrefix(v, ">="):
			since, _ = time.Parse(time.RFC3339, strings.TrimPrefix(v, ">="))
		}
	}
	n := 0
	if isPR && isMerged && !since.IsZero() && f.access(r, sRepo, "pull_requests") != "" {
		f.mu.Lock()
		for _, p := range f.ci.pulls[sRepo] {
			if p.Merged && !p.mergedAt().Before(since) {
				n++
			}
		}
		f.mu.Unlock()
	}
	if failPart == "merged" {
		data["merged"] = nil
		gqlErr("", timeout, "merged")
	} else {
		data["merged"] = map[string]any{"issueCount": n}
	}
	out := map[string]any{"data": data}
	if errs != nil {
		out["errors"] = errs
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
