package main

// deploy.go — `bx deploy`, `bx promote` and `bx rollback` (11-contract
// §9.2): put a checkpoint on a deployment, give one deployment exactly
// another's code, or put back the one before. A code move never takes its
// target from the env (DR3): --to names it, or the tile ref's qualifier
// (apps/crm+dev), and promote names both deployments. The plumbing is
// deployclient.go's; this file also holds what every changing command prints
// before it acts (the impact report, 10-ux §8.1), the line a finished code
// move ends with, how a move onto a protected primary names its code before
// its dry run, and what shows code: `bx deployment log` and `bx deployment
// diff`.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

func init() {
	moreCmds["deploy"] = dcCommand(cmdDeploy)
	moreCmds["rollback"] = dcCommand(cmdRollback)
	moreCmds["promote"] = dcCommand(cmdPromote)
	dcUsage["deploy"] = "bx deploy [<tile>] --to <name> [--checkpoint c:<id>] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["rollback"] = "bx rollback [<tile>] --to <name> [--checkpoint c:<id>] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["promote"] = "bx promote [<tile>] <from> <to> [--dry-run] [--yes] [--json] [--no-wait]"
}

var codeMoveFlags = []string{"--to", "--checkpoint", "--yes", "--dry-run", "--json", "--wait"}

// cmdDeploy puts a checkpoint on a deployment: --checkpoint, or a fresh
// checkpoint of the work tree, the one the dry run reported (sent as
// expect). Onto live reload's target, live reload pauses.
func cmdDeploy(args []string) error { return codeMove("deploy", args) }

// cmdRollback puts back an earlier checkpoint: --checkpoint, or the newest
// "ok" entry of the deployment's deploy log with other code, the one the dry
// run reported (sent as checkpoint). Data stays (flow D).
func cmdRollback(args []string) error { return codeMove("rollback", args) }

func codeMove(how string, args []string) error {
	a, err := parseDeploymentArgs(how, args, codeMoveFlags...)
	if err != nil {
		return err
	}
	ref, _, err := a.split(how, 1)
	if err != nil {
		return err
	}
	if a.to == "" && !strings.Contains(ref, "+") {
		return usageError(how, "--to names the deployment (a code move never defaults its target)")
	}
	op := deployOp{cmd: how, route: how, how: how, can: how, perDep: true, body: map[string]any{"tile": ref}}
	if a.to != "" {
		op.body["deployment"] = a.to
	}
	if a.checkpoint != "" {
		op.body["checkpoint"] = a.checkpoint
	}
	op.target = func(st *deployState) string { return firstOf(a.to, st.Selected) }
	return runDeployOp(op, a)
}

// cmdPromote gives <to> exactly <from>'s current code (P10): its
// checkpoint, or a fresh checkpoint of the work tree when it follows it, the
// one the dry run reported (sent as expect). Data stays. Both deployments
// are named: promote never takes one from the env or a qualifier.
func cmdPromote(args []string) error {
	a, err := parseDeploymentArgs("promote", args, "--yes", "--dry-run", "--json", "--wait")
	if err != nil {
		return err
	}
	ref, names, err := a.split("promote", 3)
	if err != nil {
		return err
	}
	if len(names) != 2 {
		return usageError("promote", "name both deployments: bx promote [<tile>] <from> <to>")
	}
	from, to := names[0], names[1]
	for _, n := range names {
		if !dcNameRe.MatchString(n) {
			return usageError("promote", "%q: deployment names are lowercase letters, digits and \"-\", start with a letter, at most 24 characters", n)
		}
	}
	if from == to {
		return usageError("promote", "%s → %s: promote moves code between two deployments", from, to)
	}
	op := deployOp{cmd: "promote", route: "promote", how: "promote", can: "promoteTo", perDep: true,
		body:   map[string]any{"tile": ref, "from": from, "to": to},
		target: func(*deployState) string { return to }}
	return runDeployOp(op, a)
}

// reviewedCode names the code a move onto a protected primary ships before
// its dry run, since the server refuses one that names none, dry runs
// included (P21) (11-contract §1.2): a work-tree capture through a diff's
// X-XBin-Checkpoint-To, a pinned deployment's checkpoint, a roll back's
// target from the deploy log. They go in the dry run and the request, with
// seq. Nothing for any other move: the dry run reports its code.
func reviewedCode(op deployOp, a dcArgs, st *deployState, x string) (map[string]any, error) {
	p := st.primary()
	if !st.Record || !st.ProtectedPrimary || (x != p && op.how != "primary") || op.body["restart"] != nil {
		return nil, nil
	}
	seq := map[string]any{"seq": st.Seq}
	named := func(key, id string, err error) (map[string]any, error) {
		seq[key] = id
		return seq, err
	}
	switch op.how {
	case "deploy", "rollback":
		if a.checkpoint != "" {
			return seq, nil
		}
		if op.how == "rollback" {
			id, err := previousCheckpoint(st, x)
			return named("checkpoint", id, err)
		}
		id, err := capturedCode(st.Tile, "deployment:"+p, "work-tree")
		return named("checkpoint", id, err)
	case "reload-now":
		id, err := capturedCode(st.Tile, "deployment:"+p, "work-tree")
		return named("expect", id, err)
	case "promote", "primary":
		src := x // primary: the new primary's code
		if op.how == "promote" {
			src, _ = op.body["from"].(string)
		}
		if d := st.deployment(src); d != nil && d.Checkpoint != nil && d.Checkpoint.ID != "" {
			return named("expect", d.Checkpoint.ID, nil)
		}
		id, err := capturedCode(st.Tile, "deployment:"+p, "deployment:"+src)
		return named("expect", id, err)
	}
	return nil, nil
}

// capturedCode is the checkpoint a diff resolves its to side to: the
// capture, for the work tree (11-contract §1.11).
func capturedCode(tile, from, to string) (string, error) {
	q := url.Values{"tile": {tile}, "from": {from}, "to": {to}, "stat": {"1"}}
	b, h, err := dcRequest("GET", "/api/xbin/deployments/diff?"+q.Encode(), nil, diffTimeout)
	if err != nil {
		return "", err
	}
	id := h.Get("X-XBin-Checkpoint-To")
	if id == "" {
		var st struct{ To string }
		_ = json.Unmarshal(b, &st)
		id = st.To
	}
	if !dcCheckpointRe.MatchString(id) {
		return "", &dcError{code: exitFailed, msg: "xbind's diff named no checkpoint for " + to + ": can't name the reviewed code"}
	}
	return id, nil
}

// previousCheckpoint is what a roll back of x goes back to: the newest ok
// entry of its deploy log with other code than x runs now.
func previousCheckpoint(st *deployState, x string) (string, error) {
	q := url.Values{"tile": {st.Tile}, "deployment": {x}, "limit": {"200"}}
	b, err := dcCall("GET", "/api/xbin/deployments/log?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	var log struct{ Entries []deployEntry }
	if err := decodeAnswer(b, &log); err != nil {
		return "", err
	}
	cur := ""
	if d := st.deployment(x); d != nil && d.Checkpoint != nil {
		cur = d.Checkpoint.ID
	}
	for _, e := range log.Entries {
		if e.Result == "ok" && e.Checkpoint != "" && (e.Deployment == "" || e.Deployment == x) && !sameCheckpoint(e.Checkpoint, cur) {
			return e.Checkpoint, nil
		}
	}
	return "", &dcError{code: exitFailed, msg: x + " has no earlier checkpoint in its deploy log"}
}

// sameCheckpoint: two ids name one checkpoint (each is a unique prefix).
func sameCheckpoint(a, b string) bool {
	return a != "" && b != "" && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}

// --- what a code move prints ---

// deployReport is the impact report every changing command prints before it
// asks or acts (10-ux §8.1): a headline, then labelled lines, each present
// only when it says something.
type deployReport struct {
	head     string
	question string
	lines    [][2]string
	why      string // what a guarded op does, said when it isn't confirmed
}

func (r deployReport) print(w io.Writer) {
	fmt.Fprintln(w, r.head)
	for _, l := range r.lines {
		fmt.Fprintf(w, "  %-8s %s\n", l[0], l[1])
	}
}

func (r *deployReport) add(label, text string) {
	if r.head == "" {
		r.head = r.question
	}
	if text != "" {
		r.lines = append(r.lines, [2]string{label, text})
	}
}

func stopsLine(imp *deployImpact) string {
	if len(imp.Stops) == 0 {
		return ""
	}
	return strings.Join(imp.Stops, ", ") + " stop during it and restart"
}

// buildReport renders the dry run's State and Impact in 10-ux §5.2's words;
// nothing in it is computed by bx.
func buildReport(op deployOp, a dcArgs, st *deployState, x string, imp *deployImpact) deployReport {
	var r deployReport
	cp := a.checkpoint
	if cp == "" && imp.Code != nil {
		cp = imp.Code.To
	}
	switch op.how {
	case "pause":
		r.question = "Pause live reload on " + st.Tile
	case "resume":
		r.question = "Resume live reload on " + x
	case "reload-now":
		r.question = "Reload " + x + " now"
	case "attach":
		r.question = "Attach live reload to " + x
	case "deploy":
		if a.checkpoint != "" {
			r.question = "Deploy " + a.checkpoint + " to " + x
		} else {
			r.question = "Deploy the work tree to " + x
		}
	case "rollback":
		if cp == "" {
			cp = "its previous checkpoint"
		}
		r.question = "Roll back " + x + " to " + cp
	case "promote":
		r.question = fmt.Sprintf("Promote %v → %s", op.body["from"], x)
	}
	r.head = r.question
	if op.how != "pause" && x == st.primary() {
		r.head += " (the primary of " + st.Tile + ")"
	}
	add := func(label, text string) {
		if text != "" {
			r.lines = append(r.lines, [2]string{label, text})
		}
	}
	add("Code", codeLine(op.how, x, imp))
	add("Data", dataLine(op.how, x, imp))
	var pauses []string
	if op.how == "pause" && st.LiveReload != "" {
		pauses = append(pauses, "live reload — saves stop reaching "+x+" until bx live-reload now or bx live-reload resume")
	} else if op.how == "pause" {
		pauses = append(pauses, "live reload is already paused — nothing changes")
	} else if imp.PausesLiveReload {
		pauses = append(pauses, "live reload — later saves won't reach "+x+" until you resume (bx live-reload resume)")
	}
	if len(imp.Stops) > 0 {
		pauses = append(pauses, strings.Join(imp.Stops, ", ")+" stop")
	}
	add("Pauses", strings.Join(pauses, "; "))
	add("Affects", affectsLine(st, x, imp))
	return r
}

func codeLine(how, x string, imp *deployImpact) string {
	c := imp.Code
	if c == nil {
		if how == "pause" {
			return x + " keeps running the code it runs now, pinned to a checkpoint of the work tree taken when the request commits"
		}
		return "the work tree as it is when the request commits (no checkpoint yet)"
	}
	dep := c.Deployment
	if dep == "" {
		dep = x
	}
	stats := fmt.Sprintf("%s, +%d −%d", nFiles(c.Files), c.Added, c.Removed)
	against := ""
	if c.From != "" {
		against = " against " + c.From
	}
	at := ""
	if t, err := time.Parse(time.RFC3339, c.WorkTreeAt); err == nil {
		at = " (the work tree at " + t.Local().Format("15:04") + ")"
	}
	same := c.To == c.From
	switch how {
	case "pause":
		if same || c.Files == 0 {
			return dep + " keeps running the code it runs now, pinned to " + c.To
		}
		return fmt.Sprintf("%s is pinned to %s%s · %s%s: the work tree changed since %s's last build, so pausing ships it once, now", dep, c.To, at, stats, against, dep)
	case "resume", "attach":
		if same {
			return fmt.Sprintf("%s follows the work tree again (no changes since %s): every save reaches it", dep, c.To)
		}
		since := ""
		if c.From != "" {
			since = " changed since " + dep + "'s " + c.From
		}
		return fmt.Sprintf("%s switches to the work tree now: %s (+%d −%d)%s ship at once, then every save reaches %s", dep, nFiles(c.Files), c.Added, c.Removed, since, dep)
	}
	if same {
		return dep + " already runs " + c.To
	}
	s := fmt.Sprintf("%s runs %s%s · %s%s", dep, c.To, at, stats, against)
	if how == "reload-now" {
		s += "; " + dep + " stays pinned: later saves wait for the next reload now"
	}
	return s
}

func dataLine(how, x string, imp *deployImpact) string {
	switch imp.Data {
	case "seed":
		return x + "'s data is replaced by a copy of the primary's"
	case "erase":
		return x + "'s data is deleted"
	case "restore":
		return x + "'s data is restored from a backup"
	}
	switch how {
	case "deploy":
		return x + " keeps its data"
	case "rollback":
		return "stays as it is — a roll back moves code, not state"
	case "promote":
		return "only code moves — " + x + " keeps its data, secrets, cron jobs and routing"
	}
	return "nothing moves"
}

func affectsLine(st *deployState, x string, imp *deployImpact) string {
	reload := ""
	if len(imp.Reloads) > 0 {
		reload = ": frames reload once"
		if d := st.deployment(x); d != nil && d.API != "" && imp.Code != nil && imp.Code.To != imp.Code.From {
			reload += "; WebSocket and SSE connections drop at the 30 s drain"
		}
	}
	switch imp.Affects {
	case "nobody":
		return "nobody now"
	case "deployment":
		return "only people using " + st.Tile + "+" + x + reload
	case "everyone":
		return "everyone using " + st.Tile + reload
	}
	return ""
}

func movedText(how string, e deployEntry) string {
	switch how {
	case "deploy", "reload-now":
		return fmt.Sprintf("%s now runs %s.", e.Deployment, e.Checkpoint)
	case "rollback":
		if e.Previous != "" {
			return fmt.Sprintf("%s now runs %s again (rolled back from %s).", e.Deployment, e.Checkpoint, e.Previous)
		}
		return fmt.Sprintf("%s now runs %s again (rolled back).", e.Deployment, e.Checkpoint)
	case "promote":
		return fmt.Sprintf("%s now runs %s (promoted from %s).", e.Deployment, e.Checkpoint, e.From)
	}
	return ""
}

// howWord names a deploy log entry's kind in a sentence.
func howWord(how string) string {
	switch how {
	case "reload-now":
		return "reload now"
	case "":
		return "deploy"
	}
	return how
}

func nFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// --- what shows code: the deploy log and the diff ---

// depLog prints a tile's deploy log (11-contract §1.10), newest first: one
// deployment's, or every one the caller may see. The deployment defaults to
// $XBIN_DEPLOYMENT when the tile is $XBIN_COMPONENT (DR3), and the output
// names it.
func depLog(cmd string, a dcArgs) error {
	env := os.Getenv("XBIN_COMPONENT")
	ref, name := env, ""
	switch pos := a.pos; {
	case len(pos) > 2:
		return usageError(cmd, "too many arguments: %s", strings.Join(pos, " "))
	case len(pos) == 2:
		ref, name = strings.Trim(pos[0], "/"), pos[1]
	case len(pos) == 1 && strings.ContainsAny(pos[0], "/+"):
		ref = strings.Trim(pos[0], "/")
	case len(pos) == 1:
		name = pos[0]
	}
	if ref == "" {
		return usageError(cmd, "which tile? name it, or run bx in the tile's terminal")
	}
	if name == "" && ref == env && !strings.Contains(ref, "+") {
		name = os.Getenv("XBIN_DEPLOYMENT") // a read command's default (DR3)
	}
	if name != "" {
		if err := checkName(cmd, name); err != nil {
			return err
		}
	}
	q := url.Values{"tile": {ref}}
	if name != "" {
		q.Set("deployment", name)
	}
	if n := a.val("--limit"); n != "" {
		q.Set("limit", n)
	}
	b, err := dcCall("GET", "/api/xbin/deployments/log?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if a.json {
		printRaw(dcOut, b)
		return nil
	}
	var log struct {
		Tile    string        `json:"tile"`
		Entries []deployEntry `json:"entries"`
		More    bool          `json:"more"`
	}
	if err := decodeAnswer(b, &log); err != nil {
		return err
	}
	head := firstOf(log.Tile, ref)
	if name != "" {
		head += "+" + name
	}
	fmt.Fprintf(dcOut, "%s: deploy log\n", head)
	if len(log.Entries) == 0 {
		fmt.Fprintln(dcOut, "  no deploys yet")
	}
	for _, e := range log.Entries {
		where := strings.TrimPrefix(e.From+" → "+e.Deployment, " → ")
		result := e.Result
		if e.Phase != "" && (e.Result == "running" || e.Result == "queued") {
			result += " (" + e.Phase + ")"
		}
		by := who(e.By)
		if e.Agent {
			by += " (agent)"
		}
		line := fmt.Sprintf("  %-4d %-10s %-14s %-10s %-10s by %s", e.ID, howWord(e.How), where, firstOf(e.Checkpoint, "-"), result, by)
		if t := ago(e.At); t != "" {
			line += " · " + t
		}
		fmt.Fprintln(dcOut, strings.TrimRight(line, " "))
		if e.Error != "" {
			fmt.Fprintf(dcOut, "       %s\n", e.Error)
		}
	}
	if log.More {
		fmt.Fprintln(dcOut, "  … older entries: bx deployment log --limit <n> (at most 200)")
	}
	return nil
}

// depDiff prints what differs between two codes of a tile (11-contract
// §1.11): a deployment's (by name), a checkpoint (c:<id>) or the work tree;
// by default the primary's and the work tree. --stat (or --json) asks for the
// file list; the patch goes to stdout as git wrote it, the checkpoints it
// resolved to stderr.
func depDiff(cmd string, a dcArgs) error {
	pos, ref := a.pos, os.Getenv("XBIN_COMPONENT")
	if len(pos) == 3 || len(pos) > 0 && strings.ContainsAny(pos[0], "/+") {
		ref, pos = strings.Trim(pos[0], "/"), pos[1:]
	}
	switch {
	case len(pos) > 2:
		return usageError(cmd, "too many arguments: %s", strings.Join(a.pos, " "))
	case ref == "":
		return usageError(cmd, "which tile? name it, or run bx in the tile's terminal")
	}
	q := url.Values{"tile": {ref}}
	for i, side := range []string{"from", "to"} {
		if i >= len(pos) {
			break
		}
		spec := pos[i]
		switch {
		case spec == "work-tree", strings.HasPrefix(spec, "deployment:") && dcNameRe.MatchString(spec[len("deployment:"):]):
		case strings.HasPrefix(spec, "c:"):
			if !dcCheckpointRe.MatchString(spec) {
				return usageError(cmd, "%q: a checkpoint id is c: and at least 7 lowercase hex digits", spec)
			}
		default:
			if err := checkName(cmd, spec); err != nil {
				return usageError(cmd, "%q: a deployment name, c:<id>, or work-tree", spec)
			}
			spec = "deployment:" + spec
		}
		q.Set(side, spec)
	}
	if p := a.val("--path"); p != "" {
		q.Set("path", p)
	}
	stat := a.has("--stat") || a.json
	if stat {
		q.Set("stat", "1")
	}
	b, h, err := dcRequest("GET", "/api/xbin/deployments/diff?"+q.Encode(), nil, diffTimeout)
	if err != nil {
		return err
	}
	from, to := h.Get("X-XBin-Checkpoint-From"), h.Get("X-XBin-Checkpoint-To")
	if !stat {
		fmt.Fprintf(dcErr, "diff %s → %s\n", firstOf(from, q.Get("from"), "the primary"), firstOf(to, q.Get("to"), "work-tree"))
		dcOut.Write(b)
		if h.Get("X-Truncated") == "true" {
			fmt.Fprintln(dcErr, "bx: the patch was cut at 16 MiB — narrow it with --path, or see the file list with --stat")
		}
		return nil
	}
	if a.json {
		printRaw(dcOut, b)
		return nil
	}
	var st struct {
		From, To  string
		Truncated bool
		Files     []struct {
			Path, Status   string
			Added, Removed int
			Binary         bool
		}
	}
	if err := decodeAnswer(b, &st); err != nil {
		return err
	}
	added, removed := 0, 0
	for _, f := range st.Files {
		n := fmt.Sprintf("+%d −%d", f.Added, f.Removed)
		if f.Binary {
			n = "binary"
		}
		fmt.Fprintf(dcOut, "  %-2s %s  %s\n", f.Status, f.Path, n)
		added, removed = added+f.Added, removed+f.Removed
	}
	fmt.Fprintf(dcOut, "%s, +%d −%d (%s → %s)\n", nFiles(len(st.Files)), added, removed, firstOf(st.From, from), firstOf(st.To, to))
	if st.Truncated {
		fmt.Fprintln(dcOut, "  … more files than the list holds (5000): narrow it with --path")
	}
	return nil
}
