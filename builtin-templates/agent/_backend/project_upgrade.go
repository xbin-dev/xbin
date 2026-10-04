// project_upgrade.go — "Make this a project…" (API.md §Big tasks, upgrades
// and pull requests): GET /runs/{id}/project/detect looks for git repos
// where a conversation's sandbox works (one command in the sandbox) and
// says which bound scm provider hosts each; POST /runs/{id}/project makes
// a project of them in the conversation's home — its repos the
// conversation's own clones (mode adopted, never removed), its sandbox the
// conversation's — and the conversation its task 1 (origin project, its
// checkouts mode main, its workspace ready), on its branch or a new one;
// then a credential for it. Everything the sandbox says is untrusted: a
// remote's userinfo, query and fragment are dropped before it is answered,
// stored or logged, and each repo is checked again at the provider.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { routeTables = append(routeTables, upgradeRoutes) }

func upgradeRoutes() []routeDef {
	return []routeDef{
		{"GET /runs/{id}/project/detect", needOwner, handleProjectDetect},
		{"POST /runs/{id}/project", needOwner, handleUpgrade},
	}
}

// detectCand is one git repo found in a conversation's sandbox.
type detectCand struct {
	Path           string `json:"path"`
	Remote         string `json:"remote"` // origin, without userinfo
	Host           string `json:"host"`
	Repo           string `json:"repo"`          // owner/name ("" when the remote names none)
	SCM            string `json:"scm"`           // the bound provider that hosts it ("" none)
	DefaultBranch  string `json:"defaultBranch"` // origin/HEAD's branch, as the clone knows it
	Branch         string `json:"branch"`        // checked out ("" detached)
	Dirty          int    `json:"dirty"`         // changed files
	SSH            bool   `json:"ssh"`
	HasCredentials bool   `json:"hasCredentials"` // the remote carried a credential (switchHttps replaces it)
	top            string // what git says the clone's toplevel is
}

// describeFn prints a clone's facts as one tab-separated CAND line.
const describeFn = `describe() {
  top=$(git -C "$1" rev-parse --show-toplevel 2>/dev/null || true)
  remote=$(git -C "$1" remote get-url origin 2>/dev/null || true)
  def=$(git -C "$1" symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null || true)
  br=$(git -C "$1" branch --show-current 2>/dev/null || true)
  dirty=$(git -C "$1" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
  printf 'CAND\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$top" "$remote" "$def" "$br" "$dirty"
}
`

// detectScript finds the clones at or under CWD (its toplevel, else every
// .git up to three levels down) and describes each.
const detectScript = describeFn + `[ -d "$CWD" ] || exit 0
top=$(git -C "$CWD" rev-parse --show-toplevel 2>/dev/null || true)
if [ -n "$top" ]; then
  describe "$top"
else
  find "$CWD" -maxdepth 3 -name .git -prune -print 2>/dev/null | sed 's#/\.git$##' | head -n 20 | while IFS= read -r t; do
    [ -n "$t" ] && describe "$t"
  done
fi
`

// verifyScript describes each clone T_i (N of them) a request names, and
// prints the name of each entry in the sandbox's working directory W (an
// ENT line: what the project's directory must not be).
const verifyScript = describeFn + `i=0
while [ "$i" -lt "$N" ]; do
  eval "T=\${T_$i}"
  describe "$T"
  i=$((i+1))
done
if [ -n "$W" ] && [ -d "$W" ]; then
  for e in "$W"/* "$W"/.[!.]* "$W"/..?*; do
    if [ -e "$e" ] || [ -L "$e" ]; then printf 'ENT\t%s\n' "\${e##*/}"; fi
  done | head -n 5000
fi
`

// parseEnts reads the ENT lines: the names taken in the working directory.
func parseEnts(out string) map[string]bool {
	ents := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(l, "ENT\t"); ok && name != "" {
			ents[name] = true
		}
	}
	return ents
}

// pathsClash: directory a is b, holds it or lies inside it.
func pathsClash(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// parseCands reads the CAND lines (at most 20).
func parseCands(out string) []detectCand {
	var cs []detectCand
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) != 7 || f[0] != "CAND" || !strings.HasPrefix(f[1], "/") || len(cs) >= 20 {
			continue
		}
		c := detectCand{Path: clip(f[1], 1024), top: clip(f[2], 1024), Branch: clip(f[5], 200)}
		c.Remote, c.Host, c.Repo, c.SSH, c.HasCredentials = parseRemote(f[3])
		c.DefaultBranch = clip(strings.TrimPrefix(f[4], "origin/"), 200)
		c.Dirty, _ = strconv.Atoi(strings.TrimSpace(f[6]))
		cs = append(cs, c)
	}
	return cs
}

// scpRemote is git's scp-like form: [user@]host:path.
var scpRemote = regexp.MustCompile(`^(?:[^@/:\s]+@)?([A-Za-z0-9.-]+):([^\s]+)$`)

// parseRemote reads a remote URL from a sandbox: the URL without its
// userinfo, query and fragment (redacted too), its host, the owner/name it
// names, whether it is ssh, and whether it carried a credential (https
// userinfo).
func parseRemote(raw string) (clean, host, repo string, ssh, creds bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", false, false
	}
	var p string
	if m := scpRemote.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		host, p, ssh = strings.ToLower(m[1]), m[2], true
		clean = "ssh://" + host + "/" + strings.TrimPrefix(p, "/")
	} else {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", "", false, false
		}
		switch strings.ToLower(u.Scheme) {
		case "ssh", "git+ssh":
			ssh = true
		case "http", "https":
			creds = u.User != nil
		}
		u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
		host, p, clean = strings.ToLower(u.Hostname()), u.Path, u.String()
	}
	clean = clip(projRedact(clean), 1024)
	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.Trim(p, "/"), ".git"), "/"), "/")
	if len(parts) == 2 && validRepo(parts[0]+"/"+parts[1]) {
		repo = parts[0] + "/" + parts[1]
	}
	return clean, host, repo, ssh, creds
}

// upgradeTarget is the conversation a detect or an upgrade is about, its
// sandbox and the directory it works in — or why it can't be one.
func upgradeTarget(r *http.Request) (*Run, Config, string, string, error) {
	c := callerOf(r)
	run, err := projAg().db.getRun(pathID(r))
	if err != nil {
		return nil, Config{}, "", "", perr(404, "no such run")
	}
	cfg, err := projAg().db.runConfig(run.ID)
	if err != nil {
		return nil, Config{}, "", "", err
	}
	ref, cwd := "", ""
	if cfg.Sandbox != nil {
		ref, cwd = cfg.Sandbox.Ref, cfg.Sandbox.Cwd
	} else if cfg.Harness != nil && cfg.Harness.Ref != "" {
		ref, cwd = cfg.Harness.Ref, cfg.Harness.Cwd
	}
	switch {
	case hostedID(run.ID):
		return nil, cfg, "", "", perr(409, "a non-secure (hosted) conversation doesn't become a project")
	case run.ParentID != 0:
		return nil, cfg, "", "", perr(409, "a subagent's run doesn't become a project: its conversation does")
	case run.Origin == originProject:
		return nil, cfg, "", "", perr(409, "this conversation is a project's already")
	case !isChat(run.Origin):
		return nil, cfg, "", "", perr(409, "only a conversation becomes a project (not an automation's run)")
	case globalMode():
		return nil, cfg, "", "", perr(409, "the agent's shared space holds team definitions only, which have no tasks: make a project of a conversation in your own space")
	case run.Owner != c.tag():
		return nil, cfg, "", "", perr(403, "only the conversation's owner makes it a project")
	case ref == "":
		return nil, cfg, "", "", perr(409, "this conversation has no sandbox: bind one first")
	}
	return run, cfg, ref, cwd, nil
}

// detectIn runs script in sandbox ref (for w; W in its env the sandbox's
// working directory) and answers what it found, the sandbox and what the
// script printed.
func detectIn(ctx context.Context, w who, ref, script string, env map[string]string) ([]detectCand, *sbxSandbox, string, error) {
	conn, id, err := sbxDialRef(ref, sbxUserOf(w))
	if err != nil {
		return nil, nil, "", err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	if !sandboxAccess(w, box).Use {
		return nil, nil, "", perr(403, "you may not use this sandbox (%s)", box.Name)
	}
	s := &wsbx{conn: conn, id: id, ref: ref, box: box}
	all := map[string]string{"W": box.Workdir}
	for k, v := range env {
		all[k] = v
	}
	out, err := s.must(ctx, "looking for git repos", script, all, "", 60*time.Second)
	if err != nil {
		return nil, nil, "", err
	}
	return parseCands(out), box, out, nil
}

// scmForHost is the bound scm provider that hosts host ("": none).
func scmForHost(ctx context.Context, host string) string {
	if host == "" {
		return ""
	}
	for _, name := range scmBound() {
		api, err := scmFor(name)
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		h, err := api.Hello(cctx)
		cancel()
		if err != nil {
			continue
		}
		for _, x := range h.Hosts {
			if strings.EqualFold(x, host) {
				return api.Provider()
			}
		}
	}
	return ""
}

// handleProjectDetect: GET /runs/{id}/project/detect — the git repos where
// the conversation's sandbox works.
func handleProjectDetect(w http.ResponseWriter, r *http.Request) {
	_, _, ref, cwd, err := upgradeTarget(r)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	conn, id, err := sbxDialRef(ref, sbxUserOf(callerOf(r)))
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if cwd == "" {
		box, err := conn.Get(ctx, id)
		if err != nil {
			writeProjErr(w, err)
			return
		}
		cwd = box.Workdir
	}
	cands, _, _, err := detectIn(ctx, callerOf(r), ref, detectScript, map[string]string{"CWD": cwd})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if cands == nil {
		cands = []detectCand{}
	}
	for i := range cands {
		cands[i].SCM = scmForHost(ctx, cands[i].Host)
	}
	xbin.WriteJSON(w, 200, map[string]any{"sandbox": ref, "cwd": cwd, "candidates": cands})
}

// upgradeBody is POST /runs/{id}/project.
type upgradeBody struct {
	Name  string `json:"name"`
	SCM   string `json:"scm"`
	Repos []struct {
		Path string `json:"path"`
		Repo string `json:"repo"`
	} `json:"repos"`
	Branch      string          `json:"branch"` // new (default) | keep
	SwitchHTTPS []string        `json:"switchHttps"`
	Policy      json.RawMessage `json:"policy"`
}

// upgradeGitScript makes each clone T_i the project's: origin switched to
// https (SW_i 1, U_i the URL), a branch push sets up its upstream and
// pushes are kept in the reflog, and — NEW 1 — the task's new branch BR.
const upgradeGitScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "T=\${T_$i}; SW=\${SW_$i}; U=\${U_$i}"
  if [ "$SW" = 1 ]; then git -C "$T" remote set-url origin "$U"; fi
  git -C "$T" config push.autoSetupRemote true
  git -C "$T" config core.logAllRefUpdates true
  if [ "$NEW" = 1 ]; then git -C "$T" switch -q -c "$BR"; fi
  i=$((i+1))
done
`

// handleUpgrade: POST /runs/{id}/project — the conversation becomes task 1
// of a new project (201 {project, task}).
func handleUpgrade(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	run, cfg, ref, _, err := upgradeTarget(r)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	// one upgrade of a conversation at a time (a double submit): the
	// second would change the clones' branch under the first
	if _, busy := upgrading.LoadOrStore(run.ID, true); busy {
		writeProjErr(w, &projErr{code: 409, refusal: refusalBusy, msg: "this conversation is being made a project already"})
		return
	}
	defer upgrading.Delete(run.ID)
	var body upgradeBody
	if !decodeBody(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	branch := orStr(body.Branch, "new")
	switch {
	case body.Name == "" || len([]rune(body.Name)) > 80:
		xbin.WriteError(w, 400, "need {name} (at most 80 characters)")
		return
	case len(body.Repos) == 0 || len(body.Repos) > 20:
		xbin.WriteError(w, 400, "repos: 1 to 20 of the conversation's clones ({path, repo}; GET …/project/detect finds them)")
		return
	case branch != "keep" && branch != "new":
		xbin.WriteError(w, 400, `branch: "keep" or "new"`)
		return
	case !resting(run.Status) && run.Status != "":
		writeProjErr(w, &projErr{code: 409, refusal: refusalBusy, msg: "this conversation is at work: make it a project once it rests"})
		return
	}
	if len(body.Policy) == 0 || string(body.Policy) == "null" {
		body.Policy = json.RawMessage("{}")
	}
	if why := checkPolicy(body.Policy); why != "" {
		xbin.WriteError(w, 400, why)
		return
	}
	if why := runShared(run); why != "" {
		xbin.WriteError(w, 409, why)
		return
	}
	pol := policyOf(body.Policy)
	cls := currentClasses().classOf(cfg)
	switch {
	case cls.has(tsInternal):
		writeProjErr(w, &projErr{code: 409, refusal: refusalClassInternal, msg: fmt.Sprintf(
			"this conversation's class (%s) has internal reach: a project's task reads text from the scm provider and never has internal reach", orStr(cls.Name, cls.ID))})
		return
	case cfg.HeldInternal:
		writeProjErr(w, &projErr{code: 409, refusal: refusalClassInternal, msg: "this conversation has held internal data: a project's task reads text from the scm provider and pushes to it, so it never has"})
		return
	case !cls.usableBy(c):
		xbin.WriteError(w, 403, fmt.Sprintf("this conversation's class (%s) is for the agent's managers", orStr(cls.Name, cls.ID)))
		return
	}
	if _, err := taskClassFor(c, &Project{SandboxRef: ref}, pol.TaskClass); err != nil {
		writeProjErr(w, err)
		return
	}
	for _, rq := range body.Repos {
		if rq.Repo != "" {
			if why := botRefusal(c, strings.TrimSuffix(strings.TrimSpace(rq.Repo), ".git")); why != "" {
				xbin.WriteError(w, 403, why)
				return
			}
		}
	}
	if haltBlocks(w, r, 0) {
		return
	}
	api, err := scmFor(body.SCM)
	if err != nil {
		xbin.WriteError(w, 400, fmt.Sprintf("scm: %q isn't an scm provider bound to this agent (GET /projects/scm lists them)", body.SCM))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := checkProjectSandbox(ctx, c, ref); err != nil {
		writeProjErr(w, err)
		return
	}
	hello, err := api.Hello(ctx)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	p := &Project{Name: body.Name, Kind: projPersonal, Owner: c.tag(), Visibility: visPrivate, TeamRole: roleViewer, SCM: api.Provider(),
		SandboxRef: ref, Policy: body.Policy, State: projActive, CreatedBy: c.tag()}
	repos, cands, box, ents, err := upgradeRepos(ctx, c, api, hello, p, ref, body)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	for _, cd := range cands {
		if cd.Path == box.Workdir || strings.HasPrefix(box.Workdir, cd.Path+"/") {
			// every project directory would be inside the clone
			xbin.WriteError(w, 409, fmt.Sprintf("%s holds the sandbox's working directory (%s), where the project's directory goes: "+
				"a project's tasks would work inside that clone — move the clone under the working directory first", cd.Path, box.Workdir))
			return
		}
	}
	p.Host = cands[0].Host
	keepBranch := ""
	if branch == "keep" {
		for i, cd := range cands {
			switch {
			case cd.Branch == "":
				xbin.WriteError(w, 409, fmt.Sprintf("%s isn't on a branch: start a new one (branch: \"new\")", cd.Path))
				return
			case cd.Branch == repos[i].DefaultBranch:
				xbin.WriteError(w, 409, fmt.Sprintf("%s is on its default branch (%s), which a task never pushes: start a new one (branch: \"new\")", cd.Path, cd.Branch))
				return
			case i > 0 && cd.Branch != keepBranch:
				xbin.WriteError(w, 409, "the repos are on different branches, and a task works on one: start a new one (branch: \"new\")")
				return
			}
			keepBranch = cd.Branch
		}
	}
	// the project and its repos first: the new branch's name needs its uid
	err = projAg().db.Tx(func(t *DB) error {
		// its directory is a new one, never a clone's (a project named after
		// its clone), one holding a clone or inside one: P1 lays out .repos,
		// tasks and .xbin there, and a fork empties P/tasks
		p.Slug = freeSlug(orStr(slugOf(p.Name, 40), "project"), 40, func(s string) bool {
			if ents[s] {
				return true
			}
			for _, cd := range cands {
				if pathsClash(box.Workdir+"/"+s, cd.Path) {
					return true
				}
			}
			var n int
			_ = t.q.QueryRow(`SELECT count(*) FROM projects WHERE slug=? AND state<>'deleting'`, s).Scan(&n)
			return n > 0
		})
		p.Dir = box.Workdir + "/" + p.Slug
		if err := t.insertProject(p); err != nil {
			return err
		}
		if err := t.shareClash(p.ID, p.SandboxRef); err != nil {
			return err
		}
		for i := range repos {
			repos[i].ProjectID = p.ID
			repos[i].Slug = t.repoSlug(p.ID, repos[i].Repo, "")
			if err := t.insertRepo(&repos[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	title := clipRunes(orStr(strings.TrimSpace(run.Title), p.Name), 80)
	slug := orStr(slugOf(title, 32), "task")
	br := orStr(keepBranch, p.branchPrefix(pol)+"/1-"+slug)
	if err := upgradeGit(ctx, c, ref, cands, repos, body.SwitchHTTPS, branch == "new", br); err != nil {
		upgradeGitUndo(ctx, c, ref, cands, branch == "new", br)
		dropUpgraded(p.ID)
		writeProjErr(w, err)
		return
	}
	k, runs, err := adoptTask(p, run, repos, cands, title, slug, br, c)
	if err != nil {
		upgradeGitUndo(ctx, c, ref, cands, branch == "new", br)
		dropUpgraded(p.ID)
		writeProjErr(w, err)
		return
	}
	projAg().afterACLChange(p.ID, runs)
	p, _ = projAg().db.getProject(p.ID)
	k, _ = projAg().db.taskByID(k.ID)
	xbin.WriteJSON(w, 201, map[string]any{"project": projAg().db.projectView(p, projAg().db.projectLevel(c, p.ID)),
		"task": projAg().db.projTaskView(p, k)})
}

// runShared says why run is shared ("": it isn't): its sharing would
// become the project's, which a person decides on the project.
func runShared(run *Run) string {
	var members int
	_ = projAg().db.q.QueryRow(`SELECT count(*) FROM run_members WHERE run_id=? AND user<>?`, run.ID, run.Owner).Scan(&members)
	if run.Visibility == visTeam || members > 0 {
		return "this conversation is shared: a project's sharing is the project's — unshare the conversation, make it a project, then share the project"
	}
	return ""
}

// upgradeRepos checks each clone a request names — in the sandbox (its
// toplevel, its origin at the provider's host and naming the repo) and at
// the provider (the bot rule, that it sees the repo) — and answers the
// rows (mode adopted, their base the clone), what the sandbox said of each,
// the sandbox, and the names taken in its working directory.
func upgradeRepos(ctx context.Context, c who, api scmAPI, hello *scmHello, p *Project, ref string, body upgradeBody) ([]ProjectRepo, []detectCand, *sbxSandbox, map[string]bool, error) {
	env := map[string]string{"N": strconv.Itoa(len(body.Repos))}
	seen := map[string]bool{}
	for i, rq := range body.Repos {
		pth := path.Clean(strings.TrimSpace(rq.Path))
		if !strings.HasPrefix(rq.Path, "/") || pth != strings.TrimRight(strings.TrimSpace(rq.Path), "/") || pth == "/" {
			return nil, nil, nil, nil, perr(400, "repos: path %q: an absolute, clean path in the sandbox", rq.Path)
		}
		if seen[pth] {
			return nil, nil, nil, nil, perr(400, "repos: %s twice", pth)
		}
		seen[pth] = true
		env[fmt.Sprintf("T_%d", i)] = pth
	}
	cands, box, out, err := detectIn(ctx, c, ref, verifyScript, env)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(cands) != len(body.Repos) {
		return nil, nil, nil, nil, perr(400, "repos: not every path is a git clone in the sandbox")
	}
	var repos []ProjectRepo
	for i, rq := range body.Repos {
		cd := cands[i]
		want := strings.TrimSuffix(strings.TrimSpace(rq.Repo), ".git")
		hosted := false
		for _, h := range hello.Hosts {
			hosted = hosted || strings.EqualFold(h, cd.Host)
		}
		switch {
		case cd.top == "":
			return nil, nil, nil, nil, perr(400, "repos: %s isn't a git clone", cd.Path)
		case cd.top != cd.Path:
			return nil, nil, nil, nil, perr(400, "repos: %s isn't a clone's top directory (that is %s)", cd.Path, clip(cd.top, 200))
		case cd.Repo == "" || !hosted:
			return nil, nil, nil, nil, perr(400, "repos: %s's origin (%s) isn't a repo at %s", cd.Path, orStr(cd.Remote, "none"), strings.Join(hello.Hosts, ", "))
		case want != "" && !strings.EqualFold(want, cd.Repo):
			return nil, nil, nil, nil, perr(400, "repos: %s's origin is %s, not %s", cd.Path, cd.Repo, want)
		}
		if p.Host != "" && !strings.EqualFold(p.Host, cd.Host) {
			return nil, nil, nil, nil, perr(400, "repos: a project's repos are on one host (%s and %s)", p.Host, cd.Host)
		}
		p.Host = cd.Host
		row, err := resolveRepo(ctx, c, api, p, newRepoReq{Repo: cd.Repo})
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for _, have := range repos {
			if strings.EqualFold(have.Repo, row.Repo) {
				return nil, nil, nil, nil, perr(400, "repos: %s twice", row.Repo)
			}
		}
		// the clone itself is the base: never removed, its checkout task 1's;
		// later tasks take worktrees of it (a clone of a non-bare base would
		// borrow the wrong objects directory)
		row.Mode, row.BasePath, row.State, row.Checkout = repoAdopted, cd.Path, "ready", coWorktree
		if row.DefaultBranch == "" {
			row.DefaultBranch = cd.DefaultBranch
		}
		repos = append(repos, *row)
	}
	return repos, cands, box, parseEnts(out), nil
}

// upgrading holds the conversations an upgrade is under way for (run id).
var upgrading sync.Map

// upgradeUndoScript takes back the new branch BR of each clone T_i still
// on it: back to where the clone was (git's previous HEAD), the branch
// deleted (-d: it holds no commit of its own yet). The origin switched to
// https stays: the same repo, and a credential the old one carried was
// never kept.
const upgradeUndoScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "T=\${T_$i}"
  if [ "$(git -C "$T" branch --show-current 2>/dev/null)" = "$BR" ]; then
    git -C "$T" checkout -q - && git -C "$T" branch -q -d "$BR" || echo "left $T on $BR" >&2
  fi
  i=$((i+1))
done
`

// upgradeGitUndo takes back what upgradeGit did to the branches of an
// upgrade that then failed (newBranch: it made one) — best effort.
func upgradeGitUndo(ctx context.Context, c who, ref string, cands []detectCand, newBranch bool, br string) {
	if !newBranch {
		return
	}
	conn, id, err := sbxDialRef(ref, sbxUserOf(c))
	if err == nil {
		env := map[string]string{"N": strconv.Itoa(len(cands)), "BR": br}
		for i, cd := range cands {
			env[fmt.Sprintf("T_%d", i)] = cd.Path
		}
		s := &wsbx{conn: conn, id: id, ref: ref}
		_, err = s.must(context.WithoutCancel(ctx), "taking the new branch back", upgradeUndoScript, env, "", 60*time.Second)
	}
	if err != nil {
		logf("a failed upgrade in %s: taking its new branch %s back: %v", ref, br, err)
	}
}

// upgradeGit makes the clones the project's (upgradeGitScript).
func upgradeGit(ctx context.Context, c who, ref string, cands []detectCand, repos []ProjectRepo, switchHTTPS []string, newBranch bool, br string) error {
	conn, id, err := sbxDialRef(ref, sbxUserOf(c))
	if err != nil {
		return err
	}
	s := &wsbx{conn: conn, id: id, ref: ref}
	env := map[string]string{"N": strconv.Itoa(len(cands)), "BR": br, "NEW": "0"}
	if newBranch {
		env["NEW"] = "1"
	}
	for i, cd := range cands {
		env[fmt.Sprintf("T_%d", i)], env[fmt.Sprintf("U_%d", i)], env[fmt.Sprintf("SW_%d", i)] = cd.Path, repos[i].URL, "0"
		if hasStr(switchHTTPS, cd.Path) {
			if !strings.HasPrefix(repos[i].URL, "https://") && !strings.HasPrefix(repos[i].URL, "file://") {
				return perr(502, "the scm provider gave no https clone URL for %s", repos[i].Repo)
			}
			env[fmt.Sprintf("SW_%d", i)] = "1"
		}
	}
	if _, err := s.must(ctx, "making the clones the project's", upgradeGitScript, env, "", 60*time.Second); err != nil {
		return perr(409, "%v", err)
	}
	return nil
}

// adoptTask makes run task 1 of p: its row (its workspace ready: the
// clones are there), its checkouts (mode main), its origin project and
// Config.Project, the project's sharing on it; then the project's sandbox
// job (its label and directory) and a credential.
func adoptTask(p *Project, run *Run, repos []ProjectRepo, cands []detectCand, title, slug, br string, c who) (*ProjectTask, []int64, error) {
	pol := policyOf(p.Policy)
	var k *ProjectTask
	var runs []int64
	err := projAg().db.Tx(func(t *DB) error {
		cur, err := t.getRun(run.ID)
		if err != nil {
			return err
		}
		if cur.Origin != run.Origin || (!resting(cur.Status) && cur.Status != "") {
			return &projErr{code: 409, refusal: refusalBusy, msg: "the conversation changed meanwhile: try again"}
		}
		var slugs []string
		for _, r := range repos {
			slugs = append(slugs, r.Slug)
		}
		reposJSON, _ := json.Marshal(slugs)
		k = &ProjectTask{ProjectID: p.ID, N: 1, RunID: run.ID, Title: title, Slug: slug, Size: sizeSmall, Branch: br, Repos: slugs,
			SandboxRef: p.SandboxRef, Dir: p.Dir + "/tasks/1-" + slug, PortsBase: portsOf(pol, 1).Base, WS: wsReady, Phase: phaseOpen,
			TurnBy: srcHuman, CreatedBy: c.tag(), CreatedMs: nowMs(), UpdatedMs: nowMs()}
		if err := t.q.QueryRow(`INSERT INTO project_tasks (project_id, n, run_id, title, slug, size, branch, issue, repos,
			sandbox_ref, dir, ports_base, ws, phase, turn_by, from_run, created_by, created_ms, updated_ms)
			VALUES (?, 1, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?) RETURNING id`,
			p.ID, run.ID, k.Title, k.Slug, k.Size, k.Branch, string(reposJSON), k.SandboxRef, k.Dir, k.PortsBase, k.WS, k.Phase,
			k.TurnBy, k.CreatedBy, k.CreatedMs, k.UpdatedMs).Scan(&k.ID); err != nil {
			return err
		}
		k.PRs, k.CIFixes = json.RawMessage("[]"), json.RawMessage("{}")
		for i, r := range repos {
			if err := t.putCheckout(ProjectCheckout{TaskID: k.ID, Repo: r.Slug, Path: cands[i].Path, Mode: coMain, State: "ready"}); err != nil {
				return err
			}
		}
		if _, err := t.q.Exec(`UPDATE runs SET origin=?, origin_id=? WHERE id=?`, originProject, p.ID, run.ID); err != nil {
			return err
		}
		if err := storeBinding(t, run.ID, func(cf *Config) error {
			cf.Project = &ProjectRef{ID: p.ID, Role: projRoleTask, N: 1}
			return nil
		}); err != nil {
			return err
		}
		if runs, err = t.copyACLToTasks(p.ID); err != nil {
			return err
		}
		if _, err := t.queueJob(p.ID, 0, "", pjSandbox, c.tag(), 0); err != nil {
			return err
		}
		if projectJobKinds[pjCreds] != nil {
			if _, err := t.queueJob(p.ID, k.ID, "", pjCreds, c.tag(), 0); err != nil {
				return err
			}
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "project made of a conversation", "by": c.tag()}, false, "")
		addProjectEvent(t, p.ID, 1, pevTaskCreated, map[string]any{"text": "the conversation it was made of: " + title, "by": c.tag()}, false, "")
		t.touchProject(p.ID)
		emitProject(t, p.ID, "project", 0)
		onTaskChange(t, p, k, "created")
		return nil
	})
	return k, runs, err
}

// dropUpgraded takes back a project an upgrade made before it failed (no
// task yet: its rows only).
func dropUpgraded(pid int64) {
	err := projAg().db.Tx(func(t *DB) error {
		var n int
		_ = t.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=?`, pid).Scan(&n)
		if n > 0 {
			return errors.New("it has a task")
		}
		for _, q := range []string{`DELETE FROM project_repos WHERE project_id=?`, `DELETE FROM project_events WHERE project_id=?`,
			`DELETE FROM project_jobs WHERE project_id=?`, `DELETE FROM projects WHERE id=?`} {
			if _, err := t.q.Exec(q, pid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		logf("project %d: taking back a failed upgrade: %v", pid, err)
	}
	projACL.flush(pid)
}
