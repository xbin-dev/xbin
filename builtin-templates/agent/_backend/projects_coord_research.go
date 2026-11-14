// projects_coord_research.go — the project coordinator's research
// subagents (API.md §The coordinator, "research"; D222), on the kit's
// machinery (coord_research.go, coord_reader.go): the coordinator runs in
// the web class, with no sandbox of its own, so it can't read code.
// research starts a subagent that can — read, ls, glob and grep over the
// project's sandbox, a read-only git, and the wiki's tools (it may write
// the wiki) — and answers the coordinator's question. Its binding is built
// here, never from the call's arguments: the project's sandbox (or a
// task's own) with its cwd at a repo's research checkout (a detached
// worktree of the base at origin's default branch, as last fetched; an
// adopted repo's own clone) or a task's worktree. The project's code isn't
// internal data (D222), so it gains nothing else the coordinator lacks.
//
// Every path must resolve (symlinks followed, in the sandbox) inside the
// project's directory or an adopted repo's clone (projects_coord_reader.go).
// At most researchMax run at once per project; each has researchTimeout to
// answer (its tools refuse after that).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() { turnEndHooks = append(turnEndHooks, researchEnded) }

// The project profile's limits (coordProject.Limits).
const (
	researchMax     = 4                // research subagents at once per project
	researchTimeout = 10 * time.Minute // its wait, and how long its tools answer
)

func researchSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{Name: "research",
		Description: "Start a research subagent that reads this project's code — files, search and read-only git in a repo's checkout or a task's worktree — and answers your question; it changes no code. " +
			"Use it for any question about the code: give it the question and the facts it needs. It reads and writes the project wiki, so what it verifies is kept. " +
			"It answers within 10 minutes (wait false: its answer arrives later as a message); at most 4 run at once.",
		Parameters: obj([]string{"question"}, map[string]any{
			"question": strProp("what to find out, with the context it needs (it sees nothing of this conversation)"),
			"repo":     strProp("one of the project's repos (owner/name) to read (default: every repo, each in its own directory)"),
			"task":     coordIntProp("a task's number: read its worktree (its branch, its changes) instead of the default branch"),
			"label":    strProp("a short name for it"),
			"wait":     map[string]any{"type": "boolean", "description": "wait for its answer (default true)"},
		})}}
}

// research runs a project coordinator's research call (the profile
// registers it through researchTool: it waits as a subagent does, so it
// needs the turn).
func (e *Engine) research(ctx context.Context, ts *turnState, tc toolCall, args map[string]any) (string, error) {
	run, cfg := ts.run, ts.cfg
	if cfg.denied("research") {
		return "", errors.New("research is not available in this conversation")
	}
	p, err := e.ag.coordinatorOf(e.db, run, cfg)
	if err != nil {
		return "", err
	}
	if e.halted() {
		return "", errors.New("the owner has halted this agent — no research until they resume it")
	}
	var a struct {
		Question string `json:"question"`
		Repo     string `json:"repo"`
		Task     int64  `json:"task"`
		Label    string `json:"label"`
		Wait     *bool  `json:"wait"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	q, err := researchQuestion(a.Question)
	if err != nil {
		return "", err
	}
	s := researchStart{Question: q, Label: a.Label, Wait: a.Wait == nil || *a.Wait,
		Limit: coordProject.Limits.Research, Timeout: coordProject.Limits.ResearchTimeout, Scope: "this project",
		Running: func(t *DB) int { return t.researchRunning(p.ID) },
		Config: func(child *Config) {
			child.Project = &ProjectRef{ID: p.ID, Role: projRoleResearch} // projectClamp holds it out of internal reach
		}}
	if err := researchRoom(e.db, s, true); err != nil {
		return "", err
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	b, err := researchPlace(pctx, e.db, p, a.Repo, a.Task)
	cancel()
	if err != nil {
		return "", err
	}
	s.Binding, s.System = b, researchSystem(p, b)
	return e.researchSpawn(ts, tc, s)
}

// researchRunning is how many of project pid's research subagents are at work.
func (d *DB) researchRunning(pid int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM runs WHERE origin=? AND origin_id=? AND parent_id<>0
		AND status IN ('running','queued','awaiting','sleeping','blocked')
		AND json_extract(CASE WHEN json_valid(config) THEN config ELSE '{}' END, '$.codeRead.project')=?`, originProject, pid, pid).Scan(&n)
	return n
}

// researchSystem is a research subagent's base prompt.
func researchSystem(p *Project, b *CodeReadBinding) string {
	var s strings.Builder
	fmt.Fprintf(&s, "You are a research subagent of the project %q's coordinator: you answer its question by reading the project's code. You change nothing in the code.\n", p.Name)
	where := "the default branch of the project's repos, as last fetched"
	if b.Task > 0 {
		where = fmt.Sprintf("task #%d's worktree (its branch, with its uncommitted changes)", b.Task)
	}
	fmt.Fprintf(&s, "You start in %s — %s. Paths resolve against it; you may read anything under %s.\n", b.Cwd, where, strings.Join(b.Roots, " or "))
	if len(b.Heads) > 0 {
		fmt.Fprintf(&s, "Checkouts: %s.\n", strings.Join(b.Heads, "; "))
	}
	fmt.Fprintf(&s, "Tools: read, ls, glob and grep for the files; git (read-only: %s) for history; ", gitSubWords(", ", ", "))
	s.WriteString(`wiki_search, wiki_read, wiki_list and wiki_history for the project wiki — notes earlier agents and people kept, which may be wrong: verify them against the code.
Answer with the facts the coordinator needs, each with its file path (and line) or commit, identifiers quoted verbatim; say what you could not determine.
Keep what you verified for the next agent: when you confirm something worth knowing — how to build, test or run it, where a thing lives, a trap — write it to the wiki (wiki_write, wiki_edit), on the page it belongs to rather than a near-duplicate (read a page before you change it), and keep the index page (slug ` + "`index`" + `) a short map of the pages. Never record task status or anything that will be stale next week.
` + wikiProvenance + `
You have 10 minutes; after that your tools stop answering.`)
	return s.String()
}

// --- where it reads ------------------------------------------------------------------------------

// researchScript lays out a research subagent's own checkouts: each W_i a
// detached worktree of base B_i at origin's DEF_i (W_i empty: B_i read as
// it stands, its HEAD said), and prints AT <i> <sha> or SKIP <i> <why>,
// then ROOT <dir> for each R_j, resolved. First it removes the research
// directories older than half an hour (RD): their subagents' time is long
// over, and one that lost its own removal goes with them.
const researchScript = `if [ -d "$RD" ]; then
  find "$RD" -mindepth 1 -maxdepth 1 -type d -mmin +30 2>/dev/null | while read -r old; do
    rm -rf -- "$old"
  done
  i=0
  while [ "$i" -lt "$N" ]; do eval "B=\${B_$i}"; [ -e "$B/HEAD" ] && git -C "$B" worktree prune 2>/dev/null; i=$((i+1)); done
fi
i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}; W=\${W_$i}; DEF=\${DEF_$i}"
  if [ ! -e "$B/HEAD" ] && [ ! -e "$B/.git" ]; then echo "SKIP $i not-cloned"; i=$((i+1)); continue; fi
  if [ -z "$W" ]; then echo "AT $i $(git -C "$B" rev-parse HEAD 2>/dev/null || echo -)"; i=$((i+1)); continue; fi
  rev=$(git -C "$B" rev-parse -q --verify "refs/remotes/origin/$DEF^{commit}" || true)
  if [ -z "$rev" ]; then echo "SKIP $i no-ref"; i=$((i+1)); continue; fi
  mkdir -p "$(dirname "$W")"
  git -C "$B" worktree add -q --detach "$W" "$rev"
  echo "AT $i $rev"
  i=$((i+1))
done
j=0
while [ "$j" -lt "$NR" ]; do
  eval "R=\${R_$j}"
  if [ -e "$R" ]; then echo "ROOT $(realpath -e -- "$R" 2>/dev/null || readlink -f -- "$R")"; fi
  j=$((j+1))
done
`

// researchRemoveScript removes a research subagent's checkouts (C) and
// prunes their bases' worktree lists (B_i).
const researchRemoveScript = `rm -rf -- "$C"
i=0
while [ "$i" -lt "$N" ]; do eval "B=\${B_$i}"; [ -e "$B/HEAD" ] && git -C "$B" worktree prune 2>/dev/null; i=$((i+1)); done
true
`

// researchPlace is a research subagent's binding: task n's worktree (its
// sandbox, the repo's checkout or its directory), else repo's research
// checkout — every repo's, under one directory, when no repo is named and
// the project has several.
func researchPlace(ctx context.Context, d *DB, p *Project, repo string, n int64) (*CodeReadBinding, error) {
	if p.Dir == "" || p.SandboxRef == "" {
		return nil, errors.New("the project's sandbox isn't ready yet: research reads the code there")
	}
	all, err := taskReposAll(p)
	if err != nil {
		return nil, err
	}
	var live []ProjectRepo
	for _, r := range all {
		if r.State == "ready" {
			live = append(live, r)
		}
	}
	b := &CodeReadBinding{Project: p.ID, Ref: p.SandboxRef}
	var pick []ProjectRepo
	if strings.TrimSpace(repo) != "" {
		r, err := projAg().coordRepo(p, repo)
		if err != nil {
			return nil, err
		}
		if r.State != "ready" {
			return nil, fmt.Errorf("%s isn't cloned yet (%s)", r.Repo, orStr(r.State, "pending"))
		}
		pick, b.Repo = []ProjectRepo{r}, r.Repo
	} else {
		pick = live
	}
	if len(pick) == 0 {
		return nil, errors.New("the project has no cloned repo yet")
	}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	roots := []string{p.Dir}
	if n > 0 {
		k, _, err := d.projectTaskOf(p, n)
		if err != nil {
			return nil, err
		}
		if k.WS != wsReady || k.Dir == "" {
			return nil, fmt.Errorf("task #%d's workspace isn't ready (%s)", k.N, k.WS)
		}
		b.Task, b.Ref, b.Cwd = k.N, taskRef(p, k), k.cwd()
		cos := map[string]ProjectCheckout{}
		for _, c := range d.checkouts(k.ID) {
			cos[c.Repo] = c
		}
		if b.Repo != "" {
			c, ok := cos[pick[0].Slug]
			if !ok || c.State == "removed" || c.Path == "" {
				return nil, fmt.Errorf("task #%d has no checkout of %s", k.N, pick[0].Repo)
			}
			b.Cwd = c.Path
		}
		env["N"] = strconv.Itoa(len(pick))
		for i, r := range pick {
			env[fmt.Sprintf("B_%d", i)] = orStr(cos[r.Slug].Path, k.Dir+"/"+r.Slug) // its checkout: AT … its HEAD
			env[fmt.Sprintf("W_%d", i)] = ""
		}
	} else {
		// its own checkouts: research of two people's coordinators never
		// shares one, nor resets it under the other
		mine := researchDir(p) + "/" + researchToken()
		env["N"], env["RD"] = strconv.Itoa(len(pick)), researchDir(p)
		for i, r := range pick {
			env[fmt.Sprintf("B_%d", i)] = basePath(p, r)
			env[fmt.Sprintf("DEF_%d", i)] = r.DefaultBranch
			if r.Mode == repoAdopted && r.BasePath != "" {
				env[fmt.Sprintf("W_%d", i)] = "" // a person's own clone: read as it stands
				roots = append(roots, r.BasePath)
			} else {
				env[fmt.Sprintf("W_%d", i)] = mine + "/" + r.Slug
				b.Checkout, b.Bases = mine, append(b.Bases, basePath(p, r))
			}
		}
		switch {
		case len(pick) > 1:
			b.Cwd = mine
		case pick[0].Mode == repoAdopted && pick[0].BasePath != "":
			b.Cwd = pick[0].BasePath
		default:
			b.Cwd = mine + "/" + pick[0].Slug
		}
	}
	env["NR"] = strconv.Itoa(len(roots))
	for j, r := range roots {
		env[fmt.Sprintf("R_%d", j)] = r
	}
	s, err := openWsbx(ctx, p, b.Ref)
	if err != nil {
		return nil, fmt.Errorf("the project's sandbox: %v", err)
	}
	out, err := s.must(ctx, "laying out the research checkouts", researchScript, env, "", 80*time.Second)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 3 && f[0] == "AT":
			if i, err := strconv.Atoi(f[1]); err == nil && i < len(pick) {
				b.Heads = append(b.Heads, pick[i].Repo+" "+shortSHA(f[2]))
			}
		case len(f) == 3 && f[0] == "SKIP":
			if i, err := strconv.Atoi(f[1]); err == nil && i < len(pick) && len(pick) == 1 {
				return nil, fmt.Errorf("%s has no checkout to read yet (%s)", pick[i].Repo, f[2])
			}
		case len(f) >= 2 && f[0] == "ROOT":
			if r := strings.TrimSpace(strings.TrimPrefix(l, "ROOT ")); strings.HasPrefix(r, "/") && !hasStr(b.Roots, r) {
				b.Roots = append(b.Roots, r)
			}
		}
	}
	for _, r := range roots { // as named, too: the cwd and the paths the prompt gives are these
		if !hasStr(b.Roots, r) {
			b.Roots = append(b.Roots, r)
		}
	}
	return b, nil
}

// researchDir is where a project's research checkouts live, a directory
// per research subagent.
func researchDir(p *Project) string { return p.Dir + "/.xbin/research" }

// researchToken names one research subagent's directory of checkouts.
func researchToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "r-" + hex.EncodeToString(b[:])
}

// researchEnded (turnEndHooks): a research subagent answered — its own
// checkouts go (in the background, as the project's owner: best effort;
// the next research sweeps what is left).
func researchEnded(t *DB, run *Run, why, outcome, result string) {
	if run.ParentID == 0 || run.Origin != originProject || !t.features {
		return
	}
	cfg, err := t.runConfig(run.ID)
	if err != nil || cfg.CodeRead == nil || cfg.CodeRead.Checkout == "" || !strings.HasPrefix(cfg.CodeRead.Checkout, "/") {
		return
	}
	b := *cfg.CodeRead
	t.AfterCommit(func() { go researchRemove(b) })
}

// researchRemove removes binding b's checkouts.
func researchRemove(b CodeReadBinding) {
	ag := projAg()
	if ag == nil {
		return
	}
	p, err := ag.db.getProject(b.Project)
	if err != nil || p.Dir == "" || !strings.HasPrefix(b.Checkout, researchDir(p)+"/r-") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := openWsbx(ctx, p, b.Ref)
	if err != nil {
		return
	}
	env := map[string]string{"C": b.Checkout, "N": strconv.Itoa(len(b.Bases))}
	for i, base := range b.Bases {
		env[fmt.Sprintf("B_%d", i)] = base
	}
	if _, err := s.must(ctx, "removing a research checkout", researchRemoveScript, env, "", time.Minute); err != nil {
		logf("project %d: removing research checkout %s: %v", p.ID, b.Checkout, err)
	}
}
