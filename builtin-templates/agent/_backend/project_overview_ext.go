// project_overview_ext.go — the overview's cells read from other tiles
// (D214): the repos at the project's scm provider and the project's
// sandbox at its manager. Both are read through the clients every other
// project read uses (scmFor → the provider's routes; sbxDialRef → the
// manager's), as the project — its identity at the provider, its owner at
// the manager — the way GET /projects/{pid}/status reads them.
//
//   - A repo's row comes from the provider's GET /scm/repo/stats (cap
//     `stats`, D216), kept 5 minutes per (provider, repo, identity). A
//     provider without the cap, or one that answers 501, gives the repo's
//     url and default branch as the project knows them and the default
//     branch's checks (GET /scm/checks on it), kept as long. A refusal is
//     the row's err, kept a minute.
//   - The sandbox is the manager's GET of it: its name, state and size,
//     `diskBytes` and `usage` (D215) passed through when the manager sends
//     them, and how many commands the agent runs in it now.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// OvSCM is the overview's scm cell.
type OvSCM struct {
	Provider *OvProvider `json:"provider"`
	Stats    bool        `json:"stats"`
	Repos    []OvRepo    `json:"repos"`
	Pulls    OvPulls     `json:"pulls"`
}

type OvProvider struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Kind  string `json:"kind,omitempty"`
}

// OvRepo is one of the project's repos at its provider.
type OvRepo struct {
	Repo          string    `json:"repo"`
	URL           string    `json:"url,omitempty"`
	DefaultBranch string    `json:"defaultBranch,omitempty"`
	Checks        *OvChecks `json:"checks"`
	OpenPulls     *int      `json:"openPulls,omitempty"`
	OpenIssues    *int      `json:"openIssues,omitempty"`
	Merged7d      *int      `json:"merged7d,omitempty"`
	Err           string    `json:"err,omitempty"`
}

type OvChecks struct {
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// OvPulls are the project's own tasks' pull requests (from the board).
type OvPulls struct {
	Open    int `json:"open"`
	Failing int `json:"failing"`
	Review  int `json:"review"`
}

// The repo rows' cache.
var (
	ovRepoTTL     = 5 * time.Minute
	ovRepoFailTTL = time.Minute
	ovRepoCache   = struct {
		sync.Mutex
		m map[string]ovRepoEntry
	}{m: map[string]ovRepoEntry{}}
)

type ovRepoEntry struct {
	row         OvRepo
	unsupported bool // the provider answered 501 to the stats read
	until       time.Time
}

// forgetOverviewRepos empties the cache (tests).
func forgetOverviewRepos() {
	ovRepoCache.Lock()
	ovRepoCache.m = map[string]ovRepoEntry{}
	ovRepoCache.Unlock()
}

// scmStatser is a client that reads repo stats (scmConn).
type scmStatser interface {
	RepoStats(ctx context.Context, repo, as string) (*scmRepoStats, error)
}

// scmRepoStatsOf reads repo's stats through api; a client without the read
// answers as a provider without the cap does (501 unsupported).
func scmRepoStatsOf(ctx context.Context, api scmAPI, repo, as string) (*scmRepoStats, error) {
	s, ok := api.(scmStatser)
	if !ok {
		return nil, &scmError{Status: 501, Refusal: scmRefUnsupported, Message: api.Provider() + " doesn't serve repo stats"}
	}
	return s.RepoStats(ctx, repo, as)
}

func scmUnsupported(err error) bool {
	var e *scmError
	return errors.As(err, &e) && (e.Status == 501 || e.Refusal == scmRefUnsupported)
}

// repoWebURL is a repo's page from its https clone URL ("" for another kind).
func repoWebURL(clone string) string {
	if !strings.HasPrefix(clone, "https://") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(clone, "/"), ".git")
}

// overviewSCM is the scm cell but its pulls (the board's, filled by the caller).
func overviewSCM(ctx context.Context, d *DB, p *Project) OvSCM {
	out := OvSCM{Repos: []OvRepo{}}
	repos, _ := d.projectRepos(p.ID)
	live := repos[:0]
	for _, rp := range repos {
		if rp.State != "removing" {
			live = append(live, rp)
		}
	}
	api, err := scmFor(p.SCM)
	var h *scmHello
	if err == nil {
		h, err = api.Hello(ctx)
	}
	if err != nil || h == nil {
		why := "the scm provider didn't answer"
		if err != nil {
			why = clip(err.Error(), 200)
		}
		for _, rp := range live {
			out.Repos = append(out.Repos, OvRepo{Repo: rp.Repo, URL: repoWebURL(rp.URL), DefaultBranch: rp.DefaultBranch, Err: why})
		}
		return out
	}
	out.Provider = &OvProvider{Name: orStr(h.SCM.Name, p.SCM), Title: orStr(h.SCM.Title, orStr(h.SCM.Name, p.SCM)), Kind: h.SCM.Kind}
	out.Stats = h.has(scmCapStats)
	as := projectAs(p)
	rows := make([]ovRepoEntry, len(live))
	var wg sync.WaitGroup
	for i, rp := range live {
		wg.Add(1)
		go func(i int, rp ProjectRepo) {
			defer wg.Done()
			rows[i] = overviewRepo(ctx, api, h, rp, as)
		}(i, rp)
	}
	wg.Wait()
	for _, e := range rows {
		if e.unsupported {
			out.Stats = false
		}
		out.Repos = append(out.Repos, e.row)
	}
	return out
}

// overviewRepo is one repo's row, from the cache or the provider.
func overviewRepo(ctx context.Context, api scmAPI, h *scmHello, rp ProjectRepo, as string) ovRepoEntry {
	key := api.Provider() + "\x00" + strings.ToLower(rp.Repo) + "\x00" + as
	ovRepoCache.Lock()
	e, ok := ovRepoCache.m[key]
	ovRepoCache.Unlock()
	if ok && time.Now().Before(e.until) {
		return e
	}
	e = ovRepoEntry{row: OvRepo{Repo: rp.Repo, URL: repoWebURL(rp.URL), DefaultBranch: rp.DefaultBranch}}
	ttl := ovRepoTTL
	read := false
	if h.has(scmCapStats) {
		st, err := scmRepoStatsOf(ctx, api, rp.Repo, as)
		switch {
		case err == nil:
			e.row.fromStats(st)
			read = true
		case scmUnsupported(err):
			e.unsupported = true
		default:
			e.row.Err, ttl, read = clip(err.Error(), 200), ovRepoFailTTL, true
		}
	}
	if !read && h.has(scmCapChecks) && e.row.DefaultBranch != "" {
		c, err := api.Checks(ctx, rp.Repo, e.row.DefaultBranch, "", as)
		switch {
		case err != nil:
			e.row.Err, ttl = clip(err.Error(), 200), ovRepoFailTTL
		case c != nil:
			e.row.Checks = &OvChecks{State: orStr(c.State, scmCINone), URL: checksURL(c)}
		}
	}
	if ctx.Err() != nil {
		return e // the read gave up: nothing learned to keep
	}
	e.until = time.Now().Add(ttl)
	ovRepoCache.Lock()
	ovRepoCache.m[key] = e
	ovRepoCache.Unlock()
	return e
}

// fromStats fills the row from the provider's stats.
func (r *OvRepo) fromStats(st *scmRepoStats) {
	if st.URL != "" {
		r.URL = st.URL
	}
	if st.DefaultBranch != "" {
		r.DefaultBranch = st.DefaultBranch
	}
	r.OpenPulls, r.OpenIssues = st.OpenPulls, st.OpenIssues
	if m := st.MergedPulls; m != nil && (m.Days == 7 || m.Days == 0) { // the contract's window is 7 days
		n := m.Count
		r.Merged7d = &n
	}
	if c := st.DefaultBranchChecks; c != nil {
		r.Checks = &OvChecks{State: orStr(c.State, scmCINone), URL: c.URL}
	}
}

// checksURL is where a commit's checks are seen: its first failing run or
// check when they failed, else its first workflow run's.
func checksURL(c *scmChecks) string {
	if c.State == "failure" {
		for _, w := range c.WorkflowRuns {
			if w.Conclusion == "failure" && w.URL != "" {
				return w.URL
			}
		}
		for _, ch := range c.Checks {
			if ch.Conclusion == "failure" {
				return orStr(ch.DetailsURL, ch.URL)
			}
		}
	}
	for _, w := range c.WorkflowRuns {
		if w.URL != "" {
			return w.URL
		}
	}
	return ""
}

// --- the sandbox ------------------------------------------------------------------------------

// OvSandbox is the overview's sandbox cell (nil: the project has none).
type OvSandbox struct {
	Ref       string          `json:"ref"`
	Name      string          `json:"name,omitempty"`
	State     string          `json:"state"`
	VCPUs     int             `json:"vcpus,omitempty"`
	MemMiB    int             `json:"memMiB,omitempty"`
	DiskGiB   int             `json:"diskGiB,omitempty"`
	DiskBytes *int64          `json:"diskBytes,omitempty"`
	Commands  int             `json:"commands"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	Err       string          `json:"err,omitempty"`
}

// ovUsageMax bounds a manager's usage object passed through.
const ovUsageMax = 64 << 10

// overviewSandbox reads the project's sandbox at its manager, as its owner.
func overviewSandbox(ctx context.Context, d *DB, p *Project) *OvSandbox {
	if p.SandboxRef == "" {
		return nil
	}
	sb := &OvSandbox{Ref: p.SandboxRef, State: "unknown"}
	_ = d.q.QueryRow(`SELECT count(*) FROM sandbox_jobs WHERE ref=? AND state IN ('starting','running')`, p.SandboxRef).Scan(&sb.Commands)
	conn, id, err := sbxDialRef(p.SandboxRef, sbxUserOf(binderWho(p.Owner)))
	var box *sbxSandbox
	if err == nil {
		box, err = conn.Get(ctx, id)
	}
	if err != nil {
		sb.Err = clip(err.Error(), 200)
		return sb
	}
	sb.Name, sb.State = box.Name, orStr(box.State, "unknown")
	sb.VCPUs, sb.MemMiB, sb.DiskGiB = box.Size.VCPUs, box.Size.MemMiB, box.Size.DiskGiB
	var extra struct {
		DiskBytes *int64          `json:"diskBytes"`
		Usage     json.RawMessage `json:"usage"`
	}
	if len(box.Raw) > 0 && json.Unmarshal(box.Raw, &extra) == nil {
		if extra.DiskBytes != nil && *extra.DiskBytes >= 0 {
			sb.DiskBytes = extra.DiskBytes
		}
		if u := extra.Usage; len(u) > 1 && len(u) <= ovUsageMax && u[0] == '{' {
			sb.Usage = u
		}
	}
	return sb
}
