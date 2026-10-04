// fakegh_ci_test.go — the fake GitHub's content and CI: repos and branches,
// pull requests (422 when one is open for the head, mergeable null until
// computed), GraphQL draft ↔ ready, issues, comments, reviews, review
// comments, search, check runs, commit statuses, workflow runs and their
// jobs, job logs (a 302 to a second path; 404 while running), annotations
// and reruns. Tests fill f.ci under f.mu.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type fPull struct {
	Number    int
	Title     string
	Body      string
	Head      string
	HeadSHA   string
	Base      string
	State     string // open | closed
	Draft     bool
	Merged    bool
	Mergeable *bool
	User      string
}

type fIssue struct {
	Number int
	Title  string
	Body   string
	State  string
	Labels []string
	User   string
	PR     bool
	At     time.Time
}

type fakeCI struct {
	branches   map[string]string           // "owner/name|branch" → sha
	protected  map[string]bool             // "owner/name|branch"
	pulls      map[string][]*fPull         // repo → …
	issues     map[string][]*fIssue        // repo → … (pull requests too, as GitHub lists them)
	comments   map[string][]map[string]any // "repo|n" → issue comments
	reviews    map[string][]map[string]any // "repo|n"
	revComms   map[string][]map[string]any // "repo|n"
	checkRuns  map[string][]map[string]any // sha → check runs
	statuses   map[string][]map[string]any // sha → statuses
	runs       map[string][]map[string]any // sha → workflow runs
	jobs       map[string][]map[string]any // run id → jobs
	logs       map[string]string           // job id → log text
	annots     map[string][]map[string]any // check run id → annotations
	reruns     []string                    // "POST <path> <token kind>"
	graphql    []string                    // mutations run
	runByID    map[string]map[string]any   // run id → run
	jobByID    map[string]map[string]any   // job id → job
	checkSuite map[string]map[string]any   // unused: fixtures may add
	extra      map[string]func(http.ResponseWriter, *http.Request)
	searchAll  bool     // search answers every readable repo, whatever q names
	searches   []string // the q of every search
}

func newFakeCI() *fakeCI {
	return &fakeCI{branches: map[string]string{"acme/web|main": strings.Repeat("a", 40), "acme/web|feature": strings.Repeat("b", 40)},
		protected: map[string]bool{"acme/web|main": true}, pulls: map[string][]*fPull{}, issues: map[string][]*fIssue{},
		comments: map[string][]map[string]any{}, reviews: map[string][]map[string]any{}, revComms: map[string][]map[string]any{},
		checkRuns: map[string][]map[string]any{}, statuses: map[string][]map[string]any{}, runs: map[string][]map[string]any{},
		jobs: map[string][]map[string]any{}, logs: map[string]string{}, annots: map[string][]map[string]any{},
		runByID: map[string]map[string]any{}, jobByID: map[string]map[string]any{}}
}

func (f *fakeGH) pullJSON(repo string, p *fPull) map[string]any {
	owner := strings.Split(repo, "/")[0]
	var merged any
	if p.Merged {
		merged = "2026-10-03T10:00:00Z"
	}
	return map[string]any{"number": p.Number, "node_id": "PR_" + strconv.Itoa(p.Number), "html_url": f.srv.URL + "/" + repo + "/pull/" + strconv.Itoa(p.Number),
		"title": p.Title, "body": p.Body, "state": p.State, "draft": p.Draft, "merged_at": merged, "mergeable": p.Mergeable,
		"mergeable_state": map[bool]string{true: "clean", false: "unknown"}[p.Mergeable != nil],
		"head":            map[string]any{"ref": p.Head, "sha": p.HeadSHA, "repo": map[string]any{"full_name": repo}, "label": owner + ":" + p.Head},
		"base":            map[string]any{"ref": p.Base, "sha": strings.Repeat("a", 40)},
		"user":            map[string]any{"login": p.User, "type": map[bool]string{true: "Bot", false: "User"}[strings.HasSuffix(p.User, "[bot]")]}, "author_association": "MEMBER", "labels": []any{},
		"updated_at": "2026-10-03T10:00:00Z"}
}

func (f *fakeGH) ciRoutes(mux *http.ServeMux) {
	repoOf := func(r *http.Request) string { return r.PathValue("o") + "/" + r.PathValue("r") }
	// guard answers 404 (GitHub's word for "you can't see it") without perm.
	guard := func(perm, level string, h func(w http.ResponseWriter, r *http.Request, repo string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			repo := repoOf(r)
			got := f.access(r, repo, perm)
			if got == "" || (level == "write" && got != "write") {
				if got != "" {
					f.msg(w, r, 403, "Resource not accessible by integration")
					return
				}
				f.msg(w, r, 404, "Not Found")
				return
			}
			h(w, r, repo)
		}
	}
	mux.HandleFunc("GET /repos/{o}/{r}", guard("metadata", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		perm := f.access(r, repo, "contents")
		f.reply(w, r, 200, f.repoJSON(repo, map[string]string{"write": "write", "read": "read"}[perm]))
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/branches/{b}", guard("metadata", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		sha, ok := f.ci.branches[repo+"|"+r.PathValue("b")]
		prot := f.ci.protected[repo+"|"+r.PathValue("b")]
		f.mu.Unlock()
		if !ok {
			f.msg(w, r, 404, "Branch not found")
			return
		}
		f.reply(w, r, 200, map[string]any{"name": r.PathValue("b"), "protected": prot, "commit": map[string]any{"sha": sha}})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{ref}", guard("contents", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		ref := r.PathValue("ref")
		f.mu.Lock()
		sha, ok := f.ci.branches[repo+"|"+ref]
		f.mu.Unlock()
		if !ok && len(ref) == 40 {
			sha, ok = ref, true
		}
		if !ok {
			f.msg(w, r, 422, "No commit found for SHA: "+ref)
			return
		}
		f.reply(w, r, 200, map[string]any{"sha": sha})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/pulls", guard("pull_requests", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		q := r.URL.Query()
		f.mu.Lock()
		var out []any
		for _, p := range f.ci.pulls[repo] {
			if st := q.Get("state"); st != "all" && st != "" && st != p.State || st == "" && p.State != "open" {
				continue
			}
			if h := q.Get("head"); h != "" && h != strings.Split(repo, "/")[0]+":"+p.Head {
				continue
			}
			if b := q.Get("base"); b != "" && b != p.Base {
				continue
			}
			out = append(out, f.pullJSON(repo, p))
		}
		f.mu.Unlock()
		f.paged(w, r, "", out)
	}))
	mux.HandleFunc("POST /repos/{o}/{r}/pulls", guard("pull_requests", "write", func(w http.ResponseWriter, r *http.Request, repo string) {
		var b struct {
			Title, Head, Base, Body string
			Draft                   bool
		}
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range f.ci.pulls[repo] {
			if p.Head == b.Head && p.State == "open" {
				w.WriteHeader(422)
				io.WriteString(w, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for acme:`+b.Head+`."}]}`)
				return
			}
		}
		sha := f.ci.branches[repo+"|"+b.Head]
		p := &fPull{Number: 40 + len(f.ci.pulls[repo]) + 2, Title: b.Title, Body: b.Body, Head: b.Head, HeadSHA: sha, Base: b.Base, State: "open", Draft: b.Draft, User: "acme-xbin[bot]"}
		f.ci.pulls[repo] = append(f.ci.pulls[repo], p)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(f.pullJSON(repo, p))
	}))
	pull := func(repo string, r *http.Request) *fPull {
		n, _ := strconv.Atoi(r.PathValue("n"))
		for _, p := range f.ci.pulls[repo] {
			if p.Number == n {
				return p
			}
		}
		return nil
	}
	mux.HandleFunc("GET /repos/{o}/{r}/pulls/{n}", guard("pull_requests", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		p := pull(repo, r)
		var v map[string]any
		if p != nil {
			v = f.pullJSON(repo, p)
		}
		f.mu.Unlock()
		if p == nil {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, v)
	}))
	mux.HandleFunc("PATCH /repos/{o}/{r}/pulls/{n}", guard("pull_requests", "write", func(w http.ResponseWriter, r *http.Request, repo string) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		p := pull(repo, r)
		if p != nil {
			if v, ok := b["title"]; ok {
				p.Title = v
			}
			if v, ok := b["body"]; ok {
				p.Body = v
			}
			if v, ok := b["state"]; ok {
				p.State = v
			}
		}
		var v map[string]any
		if p != nil {
			v = f.pullJSON(repo, p)
		}
		f.mu.Unlock()
		if p == nil {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, v)
	}))
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		if f.instTok(r) == nil && f.userTok(r) == nil {
			f.msg(w, r, 401, "Bad credentials")
			return
		}
		var b struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		id, _ := b.Variables["id"].(string)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ci.graphql = append(f.ci.graphql, b.Query)
		for _, ps := range f.ci.pulls {
			for _, p := range ps {
				if "PR_"+strconv.Itoa(p.Number) == id {
					p.Draft = strings.Contains(b.Query, "convertPullRequestToDraft")
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"x": map[string]any{"pullRequest": map[string]any{"isDraft": p.Draft}}}})
					return
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to a node"}}})
	})
	issueJSON := func(repo string, i *fIssue) map[string]any {
		v := map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "html_url": f.srv.URL + "/" + repo + "/issues/" + strconv.Itoa(i.Number),
			"repository_url": f.srv.URL + "/repos/" + repo,
			"user":           map[string]any{"login": i.User, "type": "User"}, "author_association": "NONE", "updated_at": i.At.Format(time.RFC3339)}
		var ls []any
		for _, l := range i.Labels {
			ls = append(ls, map[string]any{"name": l})
		}
		v["labels"] = ls
		if i.PR {
			v["pull_request"] = map[string]any{"url": "x"}
		}
		return v
	}
	mux.HandleFunc("GET /repos/{o}/{r}/issues", guard("issues", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		q := r.URL.Query()
		f.mu.Lock()
		var out []any
		for _, i := range f.ci.issues[repo] {
			if st := q.Get("state"); st != "all" && st != i.State {
				continue
			}
			if l := q.Get("labels"); l != "" && !contains(i.Labels, l) {
				continue
			}
			out = append(out, issueJSON(repo, i))
		}
		f.mu.Unlock()
		f.paged(w, r, "", out)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/issues/{n}", guard("issues", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		f.mu.Lock()
		var v map[string]any
		for _, i := range f.ci.issues[repo] {
			if i.Number == n {
				v = issueJSON(repo, i)
			}
		}
		f.mu.Unlock()
		if v == nil {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, v)
	}))
	// GitHub's issue search, as GitHub reads q: repo:/org: qualifiers are
	// ORed, is:/state:/label: narrow, the rest are words every hit has in
	// its title or body. Only repos the token can read answer.
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		var repos, orgs, words []string
		var isIssue bool
		state := ""
		q := r.URL.Query().Get("q")
		f.mu.Lock()
		f.ci.searches = append(f.ci.searches, q)
		searchAll := f.ci.searchAll
		f.mu.Unlock()
		for _, t := range strings.Fields(q) {
			k, v, ok := strings.Cut(t, ":")
			switch {
			case ok && k == "repo":
				repos = append(repos, v)
			case ok && k == "org":
				orgs = append(orgs, v)
			case ok && k == "is":
				isIssue = isIssue || v == "issue"
			case ok && k == "state":
				state = v
			case ok:
			default:
				words = append(words, strings.ToLower(t))
			}
		}
		f.mu.Lock()
		all := map[string][]*fIssue{}
		for repo, is := range f.ci.issues {
			all[repo] = is
		}
		f.mu.Unlock()
		var out []any
		for repo, is := range all {
			owner, _, _ := strings.Cut(repo, "/")
			if !searchAll && !contains(repos, repo) && !contains(orgs, owner) || f.access(r, repo, "issues") == "" {
				continue
			}
		next:
			for _, i := range is {
				if isIssue && i.PR || state != "" && i.State != state {
					continue
				}
				for _, wd := range words {
					if !strings.Contains(strings.ToLower(i.Title+" "+i.Body), wd) {
						continue next
					}
				}
				f.mu.Lock()
				out = append(out, issueJSON(repo, i))
				f.mu.Unlock()
			}
		}
		f.paged(w, r, "items", out)
	})
	list := func(m func() map[string][]map[string]any) func(w http.ResponseWriter, r *http.Request, repo string) {
		return func(w http.ResponseWriter, r *http.Request, repo string) {
			f.mu.Lock()
			items := m()[repo+"|"+r.PathValue("n")]
			out := make([]any, len(items))
			for i, it := range items {
				out[i] = it
			}
			f.mu.Unlock()
			f.paged(w, r, "", out)
		}
	}
	mux.HandleFunc("GET /repos/{o}/{r}/issues/{n}/comments", guard("issues", "read", list(func() map[string][]map[string]any { return f.ci.comments })))
	mux.HandleFunc("GET /repos/{o}/{r}/pulls/{n}/reviews", guard("pull_requests", "read", list(func() map[string][]map[string]any { return f.ci.reviews })))
	mux.HandleFunc("GET /repos/{o}/{r}/pulls/{n}/comments", guard("pull_requests", "read", list(func() map[string][]map[string]any { return f.ci.revComms })))
	mux.HandleFunc("POST /repos/{o}/{r}/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		repo := repoOf(r)
		if f.access(r, repo, "issues") != "write" && f.access(r, repo, "pull_requests") != "write" {
			f.msg(w, r, 403, "Resource not accessible by integration")
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		key := repo + "|" + r.PathValue("n")
		c := map[string]any{"id": 9000 + len(f.ci.comments[key]), "body": b["body"], "user": map[string]any{"login": "acme-xbin[bot]", "type": "Bot"},
			"author_association": "NONE", "html_url": f.srv.URL + "/c", "created_at": f.now().Format(time.RFC3339)}
		f.ci.comments[key] = append(f.ci.comments[key], c)
		f.mu.Unlock()
		f.reply(w, r, 201, c)
	})
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}/check-runs", guard("checks", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		cr := f.ci.checkRuns[r.PathValue("sha")]
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"total_count": len(cr), "check_runs": orEmpty(cr)})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/commits/{sha}/status", guard("statuses", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		st := f.ci.statuses[r.PathValue("sha")]
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"state": "pending", "total_count": len(st), "statuses": orEmpty(st)})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs", guard("actions", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		rs := f.ci.runs[r.URL.Query().Get("head_sha")]
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"total_count": len(rs), "workflow_runs": orEmpty(rs)})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs/{id}", guard("actions", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		run := f.ci.runByID[r.PathValue("id")]
		f.mu.Unlock()
		if run == nil {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, run)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs/{id}/jobs", guard("actions", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		js := f.ci.jobs[r.PathValue("id")]
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"total_count": len(js), "jobs": orEmpty(js)})
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/jobs/{id}", guard("actions", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		j := f.ci.jobByID[r.PathValue("id")]
		f.mu.Unlock()
		if j == nil {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, j)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/jobs/{id}/logs", guard("actions", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		j := f.ci.jobByID[r.PathValue("id")]
		_, has := f.ci.logs[r.PathValue("id")]
		f.mu.Unlock()
		if j == nil || j["status"] != "completed" || !has {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.mu.Lock()
		target, ext := strings.CutPrefix(f.ci.logs[r.PathValue("id")], "REDIRECT:")
		f.mu.Unlock()
		if ext {
			http.Redirect(w, r, target+"?sig=SECRETSIG", http.StatusFound)
			return
		}
		http.Redirect(w, r, f.srv.URL+"/_blob/logs/"+r.PathValue("id")+"?sig=SECRETSIG", http.StatusFound)
	}))
	mux.HandleFunc("GET /_blob/logs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" && !strings.HasPrefix(f.srv.URL, "http://127.0.0.1") {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		l := f.ci.logs[r.PathValue("id")]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, l)
	})
	mux.HandleFunc("GET /repos/{o}/{r}/check-runs/{id}/annotations", guard("checks", "read", func(w http.ResponseWriter, r *http.Request, repo string) {
		f.mu.Lock()
		as := f.ci.annots[r.PathValue("id")]
		out := make([]any, len(as))
		for i, a := range as {
			out[i] = a
		}
		f.mu.Unlock()
		f.paged(w, r, "", out)
	}))
	rerun := func(what string) http.HandlerFunc {
		return guard("actions", "write", func(w http.ResponseWriter, r *http.Request, repo string) {
			kind := "user"
			if f.instTok(r) != nil {
				kind = "installation"
			}
			f.mu.Lock()
			f.ci.reruns = append(f.ci.reruns, what+" "+r.PathValue("id")+" "+kind)
			f.mu.Unlock()
			w.WriteHeader(201)
			io.WriteString(w, "{}")
		})
	}
	mux.HandleFunc("POST /repos/{o}/{r}/actions/runs/{id}/rerun", rerun("rerun"))
	mux.HandleFunc("POST /repos/{o}/{r}/actions/runs/{id}/rerun-failed-jobs", rerun("rerun-failed-jobs"))
}

func orEmpty(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}
