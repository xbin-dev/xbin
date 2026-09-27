package main

// deploy.go — `bx deploy` and `bx rollback` (11-contract §9.2): put a
// checkpoint on a deployment, or put back the one before. A code move never
// takes its target from the env (DR3): --to names it, or the tile ref's
// qualifier (apps/crm+dev). The plumbing is deployclient.go's; this file
// also holds what every changing command prints before it acts (the impact
// report, 10-ux §8.1) and the line a finished code move ends with.

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func init() {
	moreCmds["deploy"] = dcCommand(cmdDeploy)
	moreCmds["rollback"] = dcCommand(cmdRollback)
	dcUsage["deploy"] = "bx deploy [<tile>] --to <name> [--checkpoint c:<id>] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["rollback"] = "bx rollback [<tile>] --to <name> [--checkpoint c:<id>] [--dry-run] [--yes] [--json] [--no-wait]"
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

// --- what a code move prints ---

// deployReport is the impact report every changing command prints before it
// asks or acts (10-ux §8.1): a headline, then labelled lines, each present
// only when it says something.
type deployReport struct {
	head     string
	question string
	lines    [][2]string
}

func (r deployReport) print(w io.Writer) {
	fmt.Fprintln(w, r.head)
	for _, l := range r.lines {
		fmt.Fprintf(w, "  %-8s %s\n", l[0], l[1])
	}
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
