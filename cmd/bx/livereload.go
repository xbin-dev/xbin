package main

// livereload.go — `bx live-reload` (11-contract §9.2): where a tile's saves
// go, and pausing, reloading now, resuming and attaching live reload. The
// plumbing (the state, the Can, the dry run, the prompt, waiting, exit codes)
// is deployclient.go's; the words for where saves go are here (10-ux §12.1).

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func init() {
	moreCmds["live-reload"] = dcCommand(cmdLiveReload)
	dcUsage["live-reload"] = "bx live-reload [<tile>] [--json]"
	dcUsage["live-reload pause"] = "bx live-reload pause [<tile>] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["live-reload now"] = "bx live-reload now [<tile>] [--other-branch] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["live-reload resume"] = "bx live-reload resume [<tile>] [--to <name>] [--other-branch] [--dry-run] [--yes] [--json] [--no-wait]"
	dcUsage["live-reload attach"] = "bx live-reload attach [<tile>] --to <name> [--other-branch] [--dry-run] [--yes] [--json] [--no-wait]"
}

// liveReloadFlags are the flags of each subcommand ("" = the state).
// --other-branch feeds a deployment assigned a branch from a work tree on
// another one this time (D131).
var liveReloadFlags = map[string][]string{
	"":       {"--json"},
	"pause":  {"--yes", "--dry-run", "--json", "--wait"},
	"now":    {"--other-branch", "--yes", "--dry-run", "--json", "--wait"},
	"resume": {"--to", "--other-branch", "--yes", "--dry-run", "--json", "--wait"},
	"attach": {"--to", "--other-branch", "--yes", "--dry-run", "--json", "--wait"},
}

func cmdLiveReload(args []string) error {
	sub, rest := liveReloadSub(args)
	cmd := strings.TrimSpace("live-reload " + sub)
	a, err := parseDeploymentArgs(cmd, rest, liveReloadFlags[sub]...)
	if err != nil {
		return err
	}
	ref, _, err := a.split(cmd, 1)
	if err != nil {
		return err
	}
	if sub == "" {
		return showLiveReload(a, ref)
	}
	op := deployOp{cmd: cmd, route: "live-reload/" + sub, body: map[string]any{"tile": ref}, otherBranch: a.has("--other-branch")}
	switch sub {
	case "pause": // pins live reload's target where it stands (05-model §5)
		op.how, op.can = "pause", "pause"
		op.target = func(st *deployState) string {
			return firstOf(st.LiveReload, st.LastLiveReload, st.primary())
		}
	case "now": // once, to where live reload last was, which stays pinned
		op.how, op.can = "reload-now", "reloadNow"
		op.target = func(st *deployState) string { return firstOf(st.LastLiveReload, st.primary()) }
	case "resume": // default: where live reload last was
		op.how, op.can = "resume", "resume"
		if a.to != "" {
			op.body["deployment"] = a.to
		}
		op.target = func(st *deployState) string {
			return firstOf(a.to, st.Selected, st.LastLiveReload, st.primary())
		}
	case "attach":
		op.how, op.can, op.perDep = "attach", "attach", true
		if a.to != "" {
			op.body["deployment"] = a.to
		}
		op.target = func(st *deployState) string { return firstOf(a.to, st.Selected) }
	}
	return runDeployOp(op, a)
}

// liveReloadSub finds the subcommand: the first positional, when it is one.
// Flags may come before it, and a value flag's value is never taken for it.
func liveReloadSub(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if isFlag(a) {
			if !strings.Contains(a, "=") && dcValueFlags[a] {
				i++
			}
			continue
		}
		if _, ok := liveReloadFlags[a]; ok && a != "" {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
		break
	}
	return "", args
}

// showLiveReload prints where a tile's saves go, in 10-ux §12.1's words, and
// what each deployment runs; --json prints the state verbatim.
func showLiveReload(a dcArgs, ref string) error {
	st, raw, err := getDeployState(ref)
	if err != nil {
		return err
	}
	if a.json {
		printRaw(dcOut, raw)
		return nil
	}
	fmt.Fprintf(dcOut, "%s: %s\n", st.Tile, liveReloadLine(st))
	if st.Record {
		width := 4
		for _, d := range st.Deployments {
			width = max(width, len(d.Name))
		}
		for _, d := range st.Deployments {
			fmt.Fprintf(dcOut, "  %-*s  %s\n", width, d.Name, deploymentLine(st, d))
		}
	}
	if st.View == "reader" {
		return nil
	}
	if st.branchAware() {
		fmt.Fprintf(dcOut, "  the work tree is on %s\n", firstOf(st.workTreeBranch(), "no branch"))
	}
	switch last := firstOf(st.LastLiveReload, st.primary()); {
	case !st.Record:
		fmt.Fprintf(dcOut, "  bx live-reload pause keeps %s on the code it runs now while you edit\n", st.primary())
	case st.LiveReload == "":
		fmt.Fprintf(dcOut, "  bx live-reload now ships the work tree to %s once; bx live-reload resume follows every save again\n", last)
	default:
		fmt.Fprintf(dcOut, "  bx live-reload pause stops saves reaching %s\n", st.LiveReload)
	}
	return nil
}

// liveReloadLine is the state, as 10-ux §12.1's `bx live-reload` column
// words it.
func liveReloadLine(st *deployState) string {
	p := st.primary()
	if !st.Record {
		return p + " — every save reaches everyone (no deployments)"
	}
	var s string
	switch {
	case st.LiveReload == p:
		s = p + " (primary) — every save reaches everyone"
	case st.View == "reader":
		s = p + " " + st.pinnedTo(p)
	case st.LiveReload == "":
		last := firstOf(st.LastLiveReload, p)
		s = "paused"
		if since := st.LiveReloadSince; since != nil && since.By != "" {
			s += " by " + who(since.By)
			if t := ago(since.At); t != "" {
				s += " " + t
			}
		}
		switch wt := st.WorkTree; {
		case wt != nil && wt.Since != "" && wt.Changed > 0:
			s += fmt.Sprintf(" — %s changed since %s (%s)", nFiles(wt.Changed), wt.Since, last)
		case wt != nil && wt.Since != "":
			s += fmt.Sprintf(" — no changes since %s (%s)", wt.Since, last)
		default:
			s += " — " + last + " " + st.pinnedTo(last)
		}
	default:
		s = fmt.Sprintf("%s — saves reach %s+%s; %s %s", st.LiveReload, st.Tile, st.LiveReload, p, st.pinnedTo(p))
	}
	for _, d := range st.Deployments {
		if d.LastDeploy != nil && d.LastDeploy.Result == "failed" {
			runs := "its previous code"
			if d.Checkpoint != nil && d.Checkpoint.ID != "" {
				runs = d.Checkpoint.ID
			}
			s += fmt.Sprintf(" — the last deploy to %s failed; %s keeps running %s", d.Name, d.Name, runs)
			break
		}
	}
	return s
}

// deploymentLine is one deployment: its role, its code, its status, and
// whether this terminal's calls reach it.
func deploymentLine(st *deployState, d deploymentSt) string {
	role := "-"
	if d.Primary {
		role = "primary"
	}
	code := "work tree"
	if d.Checkpoint != nil && d.Checkpoint.ID != "" {
		code = "pinned to " + d.Checkpoint.ID
	}
	status := d.Status.State
	if status != "" && d.Status.Gen > 0 {
		status += fmt.Sprintf(" g%d", d.Status.Gen)
	}
	if e := d.Status.Deploying; e != nil {
		status += " · deploying"
		if e.Checkpoint != "" {
			status += " " + e.Checkpoint
		}
		if e.Phase != "" {
			status += " (" + e.Phase + ")"
		}
	}
	line := fmt.Sprintf("%-7s  %-20s  %s", role, code, status)
	if d.Branch != "" { // D131
		line += "   branch " + d.Branch
		if d.BranchOverride != "" {
			line += " (" + d.BranchOverride + " this time)"
		}
	}
	if os.Getenv("XBIN_COMPONENT") == st.Tile {
		if dep := os.Getenv("XBIN_DEPLOYMENT"); dep == d.Name || (dep == "" && d.Primary) {
			line += "   ← this terminal"
		}
	}
	return strings.TrimRight(line, " ")
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// savesLine says where saves go, when the command moved live reload
// (SC-AGENT-BX): pausing it, attaching it, resuming it, or a code move onto
// the live reload target, which pauses it.
func savesLine(before, after *deployState) string {
	was, now := before.LiveReload, after.LiveReload
	if was == now {
		return ""
	}
	p := after.primary()
	if now == "" {
		x := was
		if x == "" {
			x = after.LastLiveReload
		}
		return fmt.Sprintf("Live reload paused — %s is %s. Saves stop reaching %s until bx live-reload now or bx live-reload resume.", x, after.pinnedTo(x), x)
	}
	if now == p {
		return fmt.Sprintf("Live reload: %s — saves reach %s again, and everyone using %s.", now, now, after.Tile)
	}
	return fmt.Sprintf("Live reload: %s — saves reach %s+%s; %s is %s.", now, after.Tile, now, p, after.pinnedTo(p))
}

func unchangedText(how string, st *deployState, x string) string {
	switch how {
	case "pause":
		if st.LiveReload == "" {
			return "Live reload is paused — " + x + " is " + st.pinnedTo(x) + "."
		}
	case "resume", "attach":
		if st.LiveReload == x {
			return "Live reload is on " + x + " — every save reaches it."
		}
	case "reload-now":
		return "No changes: " + x + " already runs the work tree's code."
	}
	return x + " already runs this code."
}

// who names a stamp's author: "user:ana" → "ana".
func who(by string) string {
	if u, ok := strings.CutPrefix(by, "user:"); ok {
		return u
	}
	return by
}

// ago renders an RFC 3339 time as "12m ago".
func ago(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	switch d := dcNow().Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}
