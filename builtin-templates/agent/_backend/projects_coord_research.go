// projects_coord_research.go — the coordinator's research subagents (API.md
// §The coordinator, "research"; D222): the coordinator runs in the web
// class, with no sandbox of its own, so it can't read code. research starts
// a subagent that can — read, ls, glob and grep over the project's
// sandbox, a read-only git, and the wiki's tools (it may write the wiki) —
// and answers the coordinator's question. Its config is built here, never
// from the call's arguments: a Config.CodeRead binding to the project's
// sandbox (or a task's own) with its cwd at a repo's research checkout (a
// detached worktree of the base at origin's default branch, as last
// fetched; an adopted repo's own clone) or a task's worktree. Its class and
// lane are its parent's (childConfig), so it gains nothing the coordinator
// lacks but the project's code, which isn't internal data (D222).
//
// Whatever a model calls, a CodeRead run gets only its tools (runCodeReadTool):
// no bash, write, edit, web, coding agents or subagents. Every path must
// resolve (symlinks followed, in the sandbox) inside the project's
// directory or an adopted repo's clone; what it reads comes back redacted.
// At most researchMax run at once per project; each has researchTimeout to
// answer (its tools refuse after that) — research children don't count
// against the coordinator tree's lifetime spawn budget (they can't spawn).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

func init() { turnEndHooks = append(turnEndHooks, researchEnded) }

// CodeReadBinding is Config.CodeRead: a research subagent's read-only reach
// into a project's code. Set when it is made, never changed.
type CodeReadBinding struct {
	Project int64    `json:"project"`
	Ref     string   `json:"ref"`             // the sandbox: the project's, or a task's own
	Cwd     string   `json:"cwd"`             // where it starts (paths resolve against it)
	Roots   []string `json:"roots"`           // every path must resolve inside one of these
	Repo    string   `json:"repo,omitempty"`  // owner/name, when it works in one repo
	Task    int64    `json:"task,omitempty"`  // a task's worktree
	Heads   []string `json:"heads,omitempty"` // "<repo> <sha>": what its checkouts are at
	// Checkout is its own directory of research checkouts (removed when it
	// answers), Bases the base repos they are worktrees of.
	Checkout string   `json:"checkout,omitempty"`
	Bases    []string `json:"bases,omitempty"`
}

const (
	researchMax     = 4                // research subagents at once per project
	researchTimeout = 10 * time.Minute // its wait, and how long its tools answer
	researchQMax    = 8 << 10
)

// codeReadTools are the tools a research subagent may call.
var codeReadTools = map[string]bool{"read": true, "ls": true, "glob": true, "grep": true, "git": true, "note": true, "message_get": true}

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

// researchTool runs a coordinator's research call (execTools: it waits as
// a subagent does, so it needs the turn).
func (e *Engine) researchTool(ctx context.Context, ts *turnState, tc toolCall) {
	args, _ := callArgs(tc, ts.own)
	out, err := e.research(ctx, ts, tc, args)
	if err != nil {
		out = "error: " + err.Error()
	}
	if out != "" {
		e.settleResult(ts, tc, out)
	}
}

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
	q := strings.TrimSpace(a.Question)
	switch {
	case q == "":
		return "", errors.New("question: what to find out")
	case len(q) > researchQMax:
		return "", fmt.Errorf("question: at most %d bytes", researchQMax)
	}
	if n := e.db.researchRunning(p.ID); n >= researchMax {
		return "", fmt.Errorf("%d research subagents of this project are running (at most %d): wait for one (subagent_wait), or ask one of them more (subagent_message)", n, researchMax)
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	b, err := researchPlace(pctx, e.db, p, a.Repo, a.Task)
	cancel()
	if err != nil {
		return "", err
	}
	wait := a.Wait == nil || *a.Wait
	label := clip(oneLineTitle(a.Label), 80)
	title := "research: " + orStr(label, clip(oneLineTitle(q), 60))
	child := childConfig(cfg, researchSystem(p, b))
	child.CodeRead = b
	child.Project = &ProjectRef{ID: p.ID, Role: projRoleResearch} // projectClamp holds it out of internal reach
	child.Subagents, child.Sandbox, child.Attached, child.Engine, child.Harness = false, nil, nil, "", nil
	if child.Features == nil {
		child.Features = map[string]bool{}
	}
	for _, f := range []string{"skills", "files", "repl", "workflow", "watcher", "threads"} {
		child.Features[f] = false
	}
	var childID int64
	err = e.fenced(func(t *DB) error {
		if n := t.researchRunning(p.ID); n >= researchMax { // again, in the transaction
			return fmt.Errorf("%d research subagents of this project are running (at most %d)", n, researchMax)
		}
		cfgJSON, _ := json.Marshal(child)
		var err error
		if childID, err = t.createRunStamped(title, string(cfgJSON), run.ID, statusRunning, runStamp{}); err != nil {
			return err
		}
		if _, err := t.addMessage(&Message{RunID: childID, Role: "system", Content: child.System}); err != nil {
			return err
		}
		tm := &Message{RunID: childID, Role: "user", Content: q}
		if _, err := t.addMessage(tm); err != nil {
			return err
		}
		if err := t.recordAsk(tm, "parent", fmt.Sprintf("#%d", run.ID)); err != nil {
			return err
		}
		mode, tcID, deadline := "bg", "", int64(0)
		if wait {
			mode, tcID, deadline = "fg", tc.ID, e.unix()+int64(researchTimeout/time.Second)+30
		}
		var linkID int64
		if err := t.q.QueryRow(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, run.ID, childID, tcID, mode, deadline, orStr(label, "research"), e.unix()).Scan(&linkID); err != nil {
			return err
		}
		e.emitStep(t, ts.root, t.journal(run.ID, "spawn", map[string]any{"runId": childID, "linkId": linkID, "toolCallId": tc.ID,
			"task": clip(q, 200), "mode": mode, "research": true}))
		if wait {
			if ok, _ := t.setToolPlaceholder(run.ID, tc.ID, fmt.Sprintf("(waiting for research subagent #%d…)", childID)); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, ts.root, run.ID, id)
				}
			}
		}
		e.emitRun(t, childID)
		e.emitLink(t, ts.root, linkID)
		t.AfterCommit(func() { e.Poke(childID) })
		return nil
	})
	if err != nil {
		return "", err
	}
	if wait {
		return "", nil // the placeholder waits; resolveAwait answers it
	}
	return fmt.Sprintf("started research subagent #%d in the background; its answer arrives as a message when it is done", childID), nil
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
	s.WriteString(`Tools: read, ls, glob and grep for the files; git (read-only: log, show, diff, blame, grep, ls-files, status) for history; wiki_search, wiki_read, wiki_list and wiki_history for the project wiki — notes earlier agents and people kept, which may be wrong: verify them against the code.
Answer with the facts the coordinator needs, each with its file path (and line) or commit, identifiers quoted verbatim; say what you could not determine.
Keep what you verified for the next agent: when you confirm something worth knowing — how to build, test or run it, where a thing lives, a trap — write it to the wiki (wiki_write, wiki_edit), on the page it belongs to rather than a near-duplicate (read a page before you change it), and keep the index page a short map of the pages. Never record task status or anything that will be stale next week.
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

// --- its tools -------------------------------------------------------------------------------------

func gitSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{Name: "git",
		Description: "Run a read-only git command in the code you are reading: log, show, diff, blame, grep or ls-files — argv only, no shell. " +
			"Each subcommand takes its read-only options by exact name, one flag per argument, a long option's value attached (--date=iso); paths stay relative to the repository. " +
			"diff compares commits (A B, A..B) or the index (--cached); blame names its revision ([\"blame\", \"HEAD\", \"--\", \"file\"]).",
		Parameters: obj([]string{"args"}, map[string]any{
			"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
				"description": "the subcommand and its arguments, e.g. [\"log\", \"--oneline\", \"-20\", \"--\", \"src/\"]"},
			"path": strProp("the repository's directory (default: the working directory)"),
		})}}
}

// codeReadToolSpecs is a research subagent's whole tool list.
func codeReadToolSpecs(cfg Config, run *Run) []toolSpec {
	specs := []toolSpec{
		{Type: "function", Function: funcDef{Name: "note",
			Description: "Record a short visible note in the run timeline (for the human watching). No effect on control flow.",
			Parameters:  obj([]string{"text"}, map[string]any{"text": strProp("the note")})}},
		finishSpec(run.Depth),
	}
	for _, s := range sandboxFileSpecs() {
		if codeReadTools[s.Function.Name] {
			specs = append(specs, s)
		}
	}
	specs = append(specs, gitSpec())
	specs = append(specs, wikiToolSpecs(true)...)
	kept := specs[:0:0]
	for _, s := range specs {
		if !cfg.denied(s.Function.Name) {
			kept = append(kept, s)
		}
	}
	return kept
}

// runCodeReadTool is runTool for a research subagent: its tools only.
func (ag *Agent) runCodeReadTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	if !codeReadTools[name] && !wikiToolNames[name] {
		return "", fmt.Errorf("%s is not available to a research subagent: it reads the project's code and its wiki, and changes nothing else", name)
	}
	if created := time.Unix(run.Created, 0); run.Created > 0 && time.Since(created) > researchTimeout {
		return "", errors.New("your 10 minutes are up: answer now with what you found, and what you could not determine")
	}
	switch name {
	case "note":
		return "noted", nil
	case "message_get":
		return ag.toolMessageGet(run, args)
	}
	if wikiToolNames[name] {
		return ag.runWikiTool(ctx, run, cfg, name, args)
	}
	use, err := ag.codeReadUse(ctx, run, cfg)
	if err != nil {
		return "", err
	}
	b := cfg.CodeRead
	resolve := func(p string, def bool) (string, error) {
		if strings.TrimSpace(p) == "" && !def {
			return "", errors.New("path: the file")
		}
		at, err := use.path(p)
		if err != nil {
			return "", err
		}
		real, err := codeReadReal(ctx, use, at)
		if err != nil {
			return "", err
		}
		if !inRoots(real, b.Roots) {
			return "", fmt.Errorf("%s is outside the project's code (%s): research reads only there", at, strings.Join(b.Roots, ", "))
		}
		return real, nil
	}
	var out string
	switch name {
	case "read", "ls", "glob", "grep":
		// a glob is matched under path: one that is absolute, at a home or climbing out would move it
		if why := codeReadPattern(name+"'s pattern", str(args["pattern"])); name == "glob" && why != "" {
			return "", errors.New(why)
		}
		if why := codeReadPattern("glob", str(args["glob"])); name == "grep" && why != "" {
			return "", errors.New(why)
		}
		key := "path"
		p, err := resolve(str(args[key]), name != "read")
		if err != nil {
			return "", err
		}
		a := map[string]any{}
		for k, v := range args {
			a[k] = v
		}
		a[key] = p
		switch name {
		case "read":
			out, err = sbxRead(ctx, use, a)
		case "ls":
			out, err = sbxLs(ctx, use, a)
		case "glob":
			out, err = sbxGlob(ctx, use, a)
		case "grep":
			out, err = sbxGrep(ctx, use, a)
		}
		if err != nil {
			return "", err
		}
	case "git":
		dir, err := resolve(str(args["path"]), true)
		if err != nil {
			return "", err
		}
		if out, err = codeReadGit(ctx, use, dir, b.Roots, args["args"]); err != nil {
			return "", err
		}
	}
	return projRedact(out), nil
}

// codeReadUse is the sandbox a research subagent reads, as its binding names
// it — checked on every call: its root is still the project's coordinator,
// the sandbox still the project's (or one of its tasks'), and the class's
// firewall allows it. Called as the project's owner, as the worker is.
func (ag *Agent) codeReadUse(ctx context.Context, run *Run, cfg Config) (*sbxUse, error) {
	r, err := ag.wikiRoleOf(run, cfg) // its root, resolved as the project's coordinator
	if err != nil {
		return nil, err
	}
	p, b := r.p, cfg.CodeRead
	if b.Ref != p.SandboxRef {
		var n int
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND n=? AND sandbox_ref=?`, p.ID, b.Task, b.Ref).Scan(&n)
		if n == 0 {
			return nil, errors.New("the sandbox you were reading is no longer the project's: tell the coordinator, who can start research again")
		}
	}
	conn, id, err := sbxDialRef(b.Ref, sbxUserOf(binderWho(p.Owner)))
	if err != nil {
		return nil, err
	}
	hello, err := managerHello(ctx, conn.M)
	if err != nil {
		return nil, err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if why := partitionBoxRefusal(box); why != "" {
		return nil, &sbxError{Refusal: "not-allowed", Msg: why}
	}
	if why := taintRefusal(classOf(cfg), box); why != "" {
		return nil, &sbxError{Refusal: "not-allowed", Msg: why}
	}
	return &sbxUse{Conn: conn, ID: id, Binding: SandboxBinding{Ref: b.Ref, Name: box.Name, Cwd: b.Cwd}, Box: box, Hello: hello, Cwd: b.Cwd}, nil
}

// codeReadReal is p with every symlink on its way resolved, in the sandbox.
func codeReadReal(ctx context.Context, use *sbxUse, p string) (string, error) {
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Cmd: `realpath -e -- "$X" 2>/dev/null || readlink -e -- "$X"`, Env: map[string]string{"X": p},
		TimeoutMs: 20000, MaxOutput: 8 << 10})
	if err != nil {
		return "", err
	}
	if !res.ok() {
		return "", &sbxError{Refusal: "not-found", Msg: p + " doesn't exist in the sandbox"}
	}
	lines, _ := runLines(res.Stdout)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("%s: the sandbox couldn't resolve it", p)
	}
	return path.Clean(lines[0]), nil
}

// inRoots: p is one of roots or inside one.
func inRoots(p string, roots []string) bool {
	for _, r := range roots {
		r = strings.TrimSuffix(r, "/")
		if r != "" && (p == r || strings.HasPrefix(p, r+"/")) {
			return true
		}
	}
	return false
}
