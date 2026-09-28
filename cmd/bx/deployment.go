package main

// deployment.go — the `bx deployment` family (11-contract §9.2): a tile's
// deployments (ls), adding and removing them, reassigning and protecting the
// primary, seeding, resetting and copying vault values into a deployment,
// its switches and limits (set), the edge policy, run now, the deploy log and
// the diff. Every changing command goes through deployclient.go's
// runDeployOp: the state and the op's Can, the dry run and its report (the
// words are 10-ux §5.2's, rendered from State and Impact, never computed
// here), the prompt or --yes, the confirm token, the exit codes. The read
// commands take --json, and `log` defaults to $XBIN_DEPLOYMENT when the tile
// is $XBIN_COMPONENT (DR3); no changing command takes a name from the env.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const deploymentUsage = `  bx promote [<tile>] <from> <to>       give <to> exactly <from>'s code; its data stays
  bx deployment ls [<tile>]             a tile's deployments: code, status, data, this terminal's
  bx deployment add|rm [<tile>] <name>  add: [--from work-tree|primary|c:<id>] [--seed] [--attach]
  bx deployment primary [<tile>] --to <name> | protect [<tile>] on|off
  bx deployment seed|reset [<tile>] <name> [--stop|--vault]
  bx deployment vault-copy [<tile>] <name> --keys k1,k2|--all
  bx deployment set [<tile>] <name> [--deliveries on|off] [--always-on on|off]
                    [--mem <MiB>|default] [--pids <n>|default] [--disk <GiB>|default]
  bx deployment edge [<tile>] [<edge> read|block|inherit|default]
  bx deployment run-now [<tile>] <name> <job>
  bx deployment log [<tile>] [<name>] [--limit <n>]
  bx deployment diff [<tile>] [<from> [<to>]] [--stat] [--path <file>]
                                        changing commands take --dry-run, --yes, --json, --no-wait
  bx status|logs … --deployment <name>  a deployment's status or log ($XBIN_DEPLOYMENT by default)
  bx agent run --deployment <name> …    an agent session whose calls reach that deployment
`

// diffTimeout outlasts the diff route's own 30 s (its 504).
const diffTimeout = 45 * time.Second

// runNowTimeout outlasts run now's wait on the handler (2 minutes).
const runNowTimeout = 150 * time.Second

var changingFlags = []string{"--yes", "--dry-run", "--json", "--wait"}

// deploymentSubs are the family's subcommands: their flags and what runs.
var deploymentSubs = map[string]struct {
	flags []string
	run   func(cmd string, a dcArgs) error
}{
	"ls":         {[]string{"--json"}, depList},
	"add":        {append([]string{"--from", "--seed", "--attach"}, changingFlags...), depAdd},
	"rm":         {changingFlags, depRemove},
	"primary":    {append([]string{"--to"}, changingFlags...), depPrimary},
	"protect":    {changingFlags, depProtect},
	"seed":       {append([]string{"--stop"}, changingFlags...), depSeed},
	"reset":      {append([]string{"--vault"}, changingFlags...), depReset},
	"vault-copy": {append([]string{"--keys", "--all"}, changingFlags...), depVaultCopy},
	"set":        {append([]string{"--deliveries", "--always-on", "--mem", "--pids", "--disk"}, changingFlags...), depSet},
	"edge":       {changingFlags, depEdge},
	"run-now":    {changingFlags, depRunNow},
	"log":        {[]string{"--limit", "--json"}, depLog},
	"diff":       {[]string{"--stat", "--path", "--json"}, depDiff},
}

func init() {
	moreCmds["deployment"] = dcCommand(cmdDeployment)
	for sub, u := range map[string]string{
		"":           "bx deployment ls|add|rm|primary|protect|seed|reset|vault-copy|set|edge|run-now|log|diff …",
		"ls":         "bx deployment ls [<tile>] [--json]",
		"add":        "bx deployment add [<tile>] <name> [--from work-tree|primary|c:<id>] [--seed] [--attach] [--dry-run] [--yes] [--json]",
		"rm":         "bx deployment rm [<tile>] <name> [--dry-run] [--yes] [--json]",
		"primary":    "bx deployment primary [<tile>] --to <name> (or: primary <tile> <name>) [--dry-run] [--yes] [--json]",
		"protect":    "bx deployment protect [<tile>] on|off [--dry-run] [--yes] [--json]",
		"seed":       "bx deployment seed [<tile>] <name> [--stop] [--dry-run] [--yes] [--json]",
		"reset":      "bx deployment reset [<tile>] <name> [--vault] [--dry-run] [--yes] [--json]",
		"vault-copy": "bx deployment vault-copy [<tile>] <name> (--keys <k1,k2> | --all) [--dry-run] [--yes] [--json]",
		"set":        "bx deployment set [<tile>] <name> [--deliveries on|off] [--always-on on|off] [--mem <MiB>|default] [--pids <n>|default] [--disk <GiB>|default]",
		"edge":       "bx deployment edge [<tile>] [<edge> read|block|inherit|default] [--dry-run] [--yes] [--json]",
		"run-now":    "bx deployment run-now [<tile>] <name> <job> [--dry-run] [--yes] [--json]",
		"log":        "bx deployment log [<tile>] [<name>] [--limit <n>] [--json]",
		"diff":       "bx deployment diff [<tile>] [<from> [<to>]] [--stat] [--path <file>] [--json]",
	} {
		dcUsage[strings.TrimSpace("deployment "+sub)] = u
	}
}

func cmdDeployment(args []string) error {
	sub, rest := deploymentSub(args)
	s, ok := deploymentSubs[sub]
	if !ok {
		if sub == "" {
			return usageError("deployment", "which subcommand?")
		}
		return usageError("deployment", "unknown subcommand %q", sub)
	}
	cmd := "deployment " + sub
	a, err := parseDeploymentArgs(cmd, rest, s.flags...)
	if err != nil {
		return err
	}
	return s.run(cmd, a)
}

// deploymentSub finds the subcommand: the first positional, wherever flags
// put it; a value flag's value is never taken for it.
func deploymentSub(args []string) (string, []string) {
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
		return a, append(append([]string{}, args[:i]...), args[i+1:]...)
	}
	return "", args
}

// --- the grammar beyond deployclient.go's ---

// dcCheckFlag checks the value of a flag the family adds (the usage error
// names the flag and the value).
func dcCheckFlag(flag, v string) error {
	switch flag {
	case "--from":
		if v != "work-tree" && v != "primary" && !dcCheckpointRe.MatchString(v) {
			return errors.New("work-tree, primary, or a checkpoint id c:<hex>")
		}
	case "--deliveries", "--always-on":
		if v != "on" && v != "off" {
			return errors.New("on or off")
		}
	case "--mem", "--pids", "--disk", "--limit":
		if n, err := strconv.ParseInt(v, 10, 64); (err != nil || n <= 0) && (v != "default" || flag == "--limit") {
			return errors.New("a positive whole number" + map[bool]string{true: "", false: `, or "default"`}[flag == "--limit"])
		}
	case "--keys":
		for _, k := range strings.Split(v, ",") {
			if strings.TrimSpace(k) == "" {
				return errors.New("key names, separated by commas")
			}
		}
	case "--path":
		if strings.TrimSpace(v) == "" {
			return errors.New("a file of the tile")
		}
	}
	return nil
}

func (a dcArgs) val(flag string) string {
	if v := a.vals[flag]; len(v) > 0 {
		return v[len(v)-1] // the last one wins (compat rule 6)
	}
	return ""
}

func (a dcArgs) has(flag string) bool { return a.bools[flag] }

// named takes the tile ref and the names a command acts on from the
// positionals, right-aligned (11-contract §9.1); arity counts the tile and
// the names. A qualified ref (apps/crm+dev) stands for the tile and the
// first name, unresolved: the server resolves it, and bx reads the name back
// from the state's selected.
func (a dcArgs) named(cmd string, arity int) (ref string, names []string, qualified bool, err error) {
	if len(a.pos) == arity-1 && len(a.pos) > 0 && strings.Contains(a.pos[0], "+") {
		return strings.Trim(a.pos[0], "/"), a.pos[1:], true, nil
	}
	if ref, names, err = a.split(cmd, arity); err == nil && len(names) < arity-1 {
		err = usageError(cmd, "which deployment? name it (a changing command never takes it from the env)")
	}
	return ref, names, false, err
}

func checkName(cmd, n string) error {
	if !dcNameRe.MatchString(n) {
		return usageError(cmd, "%q: deployment names are lowercase letters, digits and \"-\", start with a letter, at most 24 characters", n)
	}
	return nil
}

// depOp is the op of a command acting on one deployment: the tile and the
// name from the positionals or the qualifier, the target read back from the
// state; the rest of the positionals are returned.
func depOp(cmd, route, can string, a dcArgs, arity int) (deployOp, []string, error) {
	ref, names, q, err := a.named(cmd, arity)
	if err != nil {
		return deployOp{}, nil, err
	}
	op := deployOp{cmd: cmd, route: route, how: route, can: can, perDep: true, body: map[string]any{"tile": ref}}
	name := ""
	if !q {
		name, names = names[0], names[1:]
		if err := checkName(cmd, name); err != nil {
			return op, nil, err
		}
		op.body["deployment"] = name
	}
	op.target = func(st *deployState) string { return firstOf(name, st.Selected) }
	return op, names, nil
}

// --- the wire beyond deployclient.go's ---

// dataState is Deployment.data (11-contract §1.1).
type dataState struct {
	State string `json:"state"`
	From  string `json:"from"`
	At    string `json:"at"`
	By    string `json:"by"`
	Reset bool   `json:"reset"`
	Busy  string `json:"busy"`
}

// joins is the (scope, name) namespace an added deployment joins.
type joins struct {
	Scope string `json:"scope"`
	State string `json:"state"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// dcRequest sends one request of the family and answers its body and
// headers, within timeout (0: bx's 30 s); an answer of 400 or more is a
// *dcError carrying its exit code, as dcCall's.
func dcRequest(method, path string, body any, timeout time.Duration) ([]byte, http.Header, error) {
	base, client := transport()
	c := *client
	if timeout > 0 {
		c.Timeout = timeout
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return nil, nil, &dcError{code: exitFailed, msg: err.Error()}
	}
	if tok := ownerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, &dcError{code: exitFailed, msg: err.Error(), transport: true}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, &dcError{code: exitFailed, msg: err.Error(), transport: true}
	}
	if resp.StatusCode >= 400 {
		return nil, resp.Header, httpRefusal(resp.StatusCode, b)
	}
	return b, resp.Header, nil
}

// whereSavesGo says where saves go when a command moved live reload
// (SC-AGENT-BX): livereload.go's words, plus the two moves only this family
// makes — removing the deployment live reload was on, and making it the
// primary.
func whereSavesGo(before, after *deployState) string {
	was, now := before.LiveReload, after.LiveReload
	switch p := after.primary(); {
	case was != "" && now == "" && before.deployment(was) != nil && after.deployment(was) == nil:
		return fmt.Sprintf("Live reload paused — it was on %s. bx live-reload now and bx live-reload resume go to %s (the primary), which is %s.", was, p, after.pinnedTo(p))
	case was != "" && was == now && now == p && before.primary() != p:
		return fmt.Sprintf("Live reload: %s — %s is the primary now: every save reaches everyone using %s.", now, now, after.Tile)
	}
	return savesLine(before, after)
}

// --- the changing commands ---

func depAdd(cmd string, a dcArgs) error {
	ref, names, _, err := a.named(cmd, 2)
	if len(a.pos) == 1 && strings.Contains(a.pos[0], "+") {
		// The deployment doesn't exist yet, so its qualified ref can't
		// resolve: add apps/crm+dev means dev on apps/crm.
		i := strings.LastIndexByte(a.pos[0], '+')
		ref, names, err = strings.Trim(a.pos[0][:i], "/"), []string{a.pos[0][i+1:]}, nil
	}
	if err != nil {
		return err
	}
	name := names[0]
	if err := checkName(cmd, name); err != nil {
		return err
	}
	op := deployOp{cmd: cmd, route: "add", how: "add", can: "add", body: map[string]any{"tile": ref, "deployment": name},
		target: func(*deployState) string { return name }}
	if f := a.val("--from"); f != "" {
		op.body["from"] = f
	}
	if a.has("--seed") {
		op.body["data"], op.confirm, op.guard = "seed", "copy-data", true
	}
	if a.has("--attach") {
		op.body["attach"] = true
	}
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		r := deployReport{question: "Add deployment " + x + " to " + st.Tile}
		code := x + " runs a fresh checkpoint of the work tree"
		switch f := a.val("--from"); {
		case f == "primary":
			code = x + " runs " + st.primary() + "'s code"
		case strings.HasPrefix(f, "c:"):
			code = x + " runs " + f
		case imp.Code == nil:
			code = x + " runs the work tree as it is when the request commits (no checkpoint yet)"
		}
		if c := imp.Code; c != nil && c.To != "" && !strings.HasPrefix(a.val("--from"), "c:") {
			code += ", " + c.To
		}
		if a.has("--attach") {
			code += "; live reload moves to " + x
			if st.LiveReload != "" {
				code += " and " + st.LiveReload + " is pinned where it stands"
			}
		}
		data := x + " starts empty"
		if j := imp.Joins; j != nil {
			data = fmt.Sprintf("joins %s+%s's data (%s%s)", j.Scope, x, j.State, stamp(j.By, j.At))
		}
		if a.has("--seed") {
			data = "seeded from " + st.primary() + ": its data, which may be personal"
			r.why = "this copies " + st.primary() + "'s data, which may be personal, into " + x
		}
		r.add("Code", code)
		r.add("Data", data+"; secrets start as names only")
		r.add("Edges", "it uses "+st.Tile+"'s grants and bindings, reading other tiles' primaries as reader")
		r.add("Pauses", stopsLine(imp))
		r.add("Affects", fmt.Sprintf("nobody now. It is reachable at /c/%s+%s/ by people with write on %s and by its terminals; its cron jobs and bus subscriptions fire for it, with its data (anything they send is real); its ingress hosts and interface instances stay with the primary; alwaysOn stays off", st.Tile, x, st.Tile))
		return r
	}
	op.result = func(b []byte, _, after *deployState, x string) string {
		u := "/c/" + after.Tile + "+" + x + "/"
		if d := after.deployment(x); d != nil && d.URL != "" {
			u = d.URL
		}
		return "Added " + x + " at " + u + "."
	}
	return runDeployOp(op, a)
}

func depRemove(cmd string, a dcArgs) error {
	op, _, err := depOp(cmd, "remove", "remove", a, 2)
	if err != nil {
		return err
	}
	op.confirm, op.guard = "erase", true
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		r := deployReport{question: "Remove deployment " + x, why: "this deletes " + x + "'s data, secrets and logs"}
		r.add("Data", x+" stops. Its data, secrets, logs, cron jobs and subscriptions are deleted, and this can't be undone. Its checkpoints stay until cleanup")
		if st.LiveReload == x {
			p := st.primary()
			r.add("Pauses", fmt.Sprintf("live reload was on %s; bx live-reload now and resume then go to %s, which is %s", x, p, st.pinnedTo(p)))
		}
		r.add("Affects", "people using /c/"+st.Tile+"+"+x+"/ lose it")
		return r
	}
	op.result = func(_ []byte, _, _ *deployState, x string) string { return "Removed " + x + "." }
	return runDeployOp(op, a)
}

func depPrimary(cmd string, a dcArgs) error {
	var op deployOp
	if a.to != "" { // --to names it; a qualified ref may too (both given and different: the server's 400)
		ref, _, err := a.split(cmd, 1)
		if err != nil {
			return err
		}
		op = deployOp{cmd: cmd, route: "primary", how: "primary", can: "primary", perDep: true,
			body: map[string]any{"tile": ref, "deployment": a.to}, target: func(*deployState) string { return a.to }}
	} else {
		var err error
		if op, _, err = depOp(cmd, "primary", "primary", a, 2); err != nil {
			return err
		}
	}
	op.confirm, op.guard = "data-stays", true
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		p := st.primary()
		r := deployReport{question: "Make " + x + " the primary of " + st.Tile,
			why: "this sends everything that reaches " + st.Tile + " to " + x + ", which serves " + x + "'s data, not " + p + "'s"}
		if c := imp.Code; c != nil && c.To != "" && st.ProtectedPrimary {
			r.add("Code", fmt.Sprintf("the primary is protected: %s becomes protected, live reload leaves it, and it is pinned to %s", x, c.To))
		}
		served := x + "'s data"
		if d := st.deployment(x); d != nil && d.Data != nil {
			served += " (" + dataWords(d.Data, false) + ")"
		}
		r.add("Data", fmt.Sprintf("the primary will serve %s. %s's data does not move: %s keeps it, keeps running its code, and its cron jobs and subscriptions keep firing for it; its ingress hosts and interface instances become dormant", served, p, p))
		if n := len(imp.Placeholders); n > 0 {
			r.add("Secrets", fmt.Sprintf("%s has no value for %d secret(s) %s uses (%s): copy or set them first", x, n, p, strings.Join(imp.Placeholders, ", ")))
		}
		pauses := p + "'s and " + x + "'s backends restart now; WebSocket and SSE connections drop"
		if st.LiveReload == x && !st.ProtectedPrimary {
			pauses += "; live reload is attached to " + x + ": from now on every save reaches everyone using " + st.Tile
		}
		r.add("Pauses", pauses)
		r.add("Affects", "everyone using "+st.Tile+": its URL, other tiles' bindings and grants, ingress, interface instances, notifications and the app move to "+x+" at once; cron jobs and bus subscriptions stay with the deployment that registered them")
		return r
	}
	op.result = func(b []byte, _, _ *deployState, x string) string {
		var ans struct {
			InactiveHosts []string `json:"inactiveHosts"`
		}
		_ = json.Unmarshal(b, &ans)
		s := x + " is now the primary — it serves " + x + "'s data."
		if len(ans.InactiveHosts) > 0 {
			s += "\n  inactive: " + strings.Join(ans.InactiveHosts, ", ") + " (another tile holds the host; it stays off)"
		}
		return s
	}
	return runDeployOp(op, a)
}

func depProtect(cmd string, a dcArgs) error {
	ref, rest, err := a.split(cmd, 2)
	if err != nil {
		return err
	}
	if len(rest) != 1 || rest[0] != "on" && rest[0] != "off" {
		return usageError(cmd, "protect on, or protect off")
	}
	on := rest[0] == "on"
	op := deployOp{cmd: cmd, route: "protect", how: "protect", can: "protect", guard: true,
		body: map[string]any{"tile": ref, "on": on}, target: func(st *deployState) string { return st.primary() }}
	op.report = func(st *deployState, p string, imp *deployImpact) deployReport {
		if !on {
			r := deployReport{question: "Unprotect " + p, why: "this lets terminals and agents change " + p + "'s code again"}
			r.add("Code", "terminal users and their agents can deploy to "+p+" again, as saving did before protection")
			r.add("Terminals", "new sessions call "+p+" by default again; running ones keep their target")
			return r
		}
		r := deployReport{question: "Protect " + p, why: "this stops terminals and agents from changing " + p + "'s code"}
		r.add("Code", "only tile managers (the tile's owner, its org's admins, or a workspace admin) change "+p+"'s code — deploy, promote, roll back, reload now — from their own session, naming the checkpoint they reviewed, never from a terminal or an agent")
		if st.LiveReload == p {
			pin := "a checkpoint of the work tree taken when the request commits"
			if c := imp.Code; c != nil && c.To != "" {
				pin = c.To
			}
			r.add("Pauses", "live reload leaves "+p+", which is pinned to "+pin)
		}
		r.add("Terminals", "terminal and agent sessions that call "+p+" restart now, calling another deployment or with the tile API off")
		return r
	}
	op.result = func(_ []byte, _, _ *deployState, p string) string {
		if on {
			return p + " is protected."
		}
		return p + " is no longer protected."
	}
	return runDeployOp(op, a)
}

func depSeed(cmd string, a dcArgs) error {
	op, _, err := depOp(cmd, "seed", "seed", a, 2)
	if err != nil {
		return err
	}
	op.confirm, op.guard = "copy-data", true
	if a.has("--stop") {
		op.body["stop"] = true
	}
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		p := st.primary()
		r := deployReport{question: "Seed " + x + " with " + p + "'s data", why: "this copies " + p + "'s data, which may be personal, into " + x + ", replacing " + x + "'s"}
		data := "copies " + p + "'s data as of now (kv, sqlite, files, blobs) into " + x + ", replacing " + x + "'s data"
		if a.has("--stop") {
			data += "; " + p + " stops for a point-in-time copy"
		}
		r.add("Data", data)
		r.add("Pauses", firstOf(stopsLine(imp), x+" stops during the copy and restarts"))
		r.add("Affects", "the copy may contain personal data: everyone with write on "+st.Tile+", and their agents, can open "+x+" and run any code there; protecting the primary doesn't cover this copy")
		return r
	}
	op.result = func(_ []byte, before, after *deployState, x string) string {
		if d := after.deployment(x); d != nil && d.Data != nil && d.Data.Busy != "" {
			return x + " is being seeded from " + after.primary() + "; bx deployment ls shows when it is done."
		}
		return x + " seeded from " + after.primary() + "."
	}
	return runDeployOp(op, a)
}

func depReset(cmd string, a dcArgs) error {
	op, _, err := depOp(cmd, "reset", "reset", a, 2)
	if err != nil {
		return err
	}
	op.confirm, op.guard = "erase-data", true
	if a.has("--vault") {
		op.body["vault"] = true
	}
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		r := deployReport{question: "Reset " + x + "'s data", why: "this deletes everything in " + x + "'s data"}
		data := "deletes everything in " + x + "'s data (kv, sqlite, files, blobs); " + x + " restarts empty; " + st.primary() + "'s data is not touched; this can't be undone"
		if a.has("--vault") {
			data += "; " + x + "'s secrets are cleared too, every key a name only"
		}
		r.add("Data", data)
		r.add("Pauses", stopsLine(imp))
		return r
	}
	op.result = func(_ []byte, _, after *deployState, x string) string {
		if d := after.deployment(x); d != nil && d.Data != nil && d.Data.Busy != "" {
			return x + "'s data is being reset; bx deployment ls shows when it is done."
		}
		return x + "'s data was reset."
	}
	return runDeployOp(op, a)
}

func depVaultCopy(cmd string, a dcArgs) error {
	op, _, err := depOp(cmd, "vault-copy", "vaultCopy", a, 2)
	if err != nil {
		return err
	}
	var keys []string
	for _, v := range a.vals["--keys"] { // --keys may repeat
		for _, k := range strings.Split(v, ",") {
			keys = append(keys, strings.TrimSpace(k))
		}
	}
	switch {
	case (len(keys) > 0) == a.has("--all"):
		return usageError(cmd, "name the secrets with --keys k1,k2, or copy them all with --all")
	case a.has("--all"):
		op.body["all"] = true
	default:
		op.body["keys"] = keys
	}
	op.guard = true
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		p := st.primary()
		what := "every secret"
		if len(keys) > 0 {
			what = strings.Join(keys, ", ")
		}
		r := deployReport{question: "Copy secrets to " + x, why: "this copies " + p + "'s secret values into " + x}
		r.add("Data", fmt.Sprintf("copies the values of %s from %s's vault into %s's. Any code running on %s can read them, and any terminal user can deploy code to %s, even while %s is protected", what, p, x, x, x, p))
		return r
	}
	op.result = func(b []byte, _, after *deployState, x string) string {
		var ans struct{ Copied, Missing []string }
		_ = json.Unmarshal(b, &ans)
		s := fmt.Sprintf("Copied %d secret(s) to %s.", len(ans.Copied), x)
		if len(ans.Missing) > 0 {
			s += fmt.Sprintf("\n  %s has no value for %s", after.primary(), strings.Join(ans.Missing, ", "))
		}
		return s
	}
	return runDeployOp(op, a)
}

// depSet calls the deliveries, always-on and limits routes, in that order,
// for the flags given, and stops at the first refusal; under --json it prints
// the last answer, which carries the state after all of them.
func depSet(cmd string, a dcArgs) error {
	base, _, err := depOp(cmd, "", "", a, 2)
	if err != nil {
		return err
	}
	var ops []deployOp
	sw := func(flag, route, can, what string) {
		v := a.val(flag)
		if v == "" {
			return
		}
		op := base
		op.route, op.how, op.can = route, route, can
		op.body = map[string]any{"on": v == "on"}
		for k, bv := range base.body {
			op.body[k] = bv
		}
		op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
			return switchReport(route, v == "on", st, x)
		}
		op.result = func(_ []byte, _, _ *deployState, x string) string { return what + " " + v + " for " + x + "." }
		ops = append(ops, op)
	}
	sw("--deliveries", "deliveries", "deliveries", "Deliveries")
	sw("--always-on", "always-on", "alwaysOn", "alwaysOn")
	limits := map[string]any{}
	var said []string
	for _, l := range [][3]string{{"--mem", "memMiB", "memory %s MiB"}, {"--pids", "pids", "pids %s"}, {"--disk", "diskGiB", "disk %s GiB"}} {
		switch v := a.val(l[0]); v {
		case "":
		case "default":
			limits[l[1]] = nil
			said = append(said, strings.Fields(l[2])[0]+" the tile's default")
		default:
			n, _ := strconv.ParseInt(v, 10, 64)
			limits[l[1]] = n
			said = append(said, fmt.Sprintf(l[2], v))
		}
	}
	if len(limits) > 0 {
		op := base
		op.route, op.how, op.can = "limits", "limits", "limits"
		op.body = map[string]any{"limits": limits}
		for k, bv := range base.body {
			op.body[k] = bv
		}
		op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
			r := deployReport{question: "Set " + x + "'s limits"}
			r.add("Data", strings.Join(said, ", ")+"; "+st.Tile+"'s own limits are the ceiling, and the primary keeps first call on them")
			return r
		}
		op.result = func(_ []byte, _, _ *deployState, x string) string {
			return x + "'s limits set: " + strings.Join(said, ", ") + "."
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return usageError(cmd, "nothing to set: give --deliveries, --always-on, --mem, --pids or --disk")
	}
	var last []byte
	for i := range ops {
		ops[i].jsonOut = &last
		if err := runDeployOp(ops[i], a); err != nil {
			return err
		}
	}
	switch {
	case a.json:
		printRaw(dcOut, last)
	case a.dryRun:
		fmt.Fprintln(dcOut, "dry run: nothing changed")
	}
	return nil
}

func switchReport(route string, on bool, st *deployState, x string) deployReport {
	var r deployReport
	switch {
	case route == "deliveries" && on:
		r.question = "Turn " + x + "'s deliveries back on"
		r.add("Data", fmt.Sprintf("%s's cron jobs and bus subscriptions fire for %s again, alongside %s's. Their side effects are real: they run with %s's data, %s's grants (reading other tiles' primaries) and %s's network, so anything they send (email, webhooks) is real", x, x, st.primary(), x, st.Tile, st.Tile))
	case route == "deliveries":
		r.question = "Turn off deliveries for " + x
		r.add("Data", x+"'s cron jobs and bus subscriptions stop firing and are listed dormant until a tile manager turns them back on; run now still delivers a job once")
	case on:
		r.question = "Keep " + x + " running"
		r.add("Data", x+" starts now, is never idle-stopped and restarts after exits, like "+st.primary()+"; it uses memory while it runs")
	default:
		r.question = "Stop keeping " + x + " running"
		r.add("Data", x+" is idle-stopped again, like any deployment nobody uses")
	}
	return r
}

func depEdge(cmd string, a dcArgs) error {
	isEdge := func(s string) bool { return strings.HasPrefix(s, "slot:") || strings.HasPrefix(s, "grant:") }
	pos, ref := a.pos, os.Getenv("XBIN_COMPONENT")
	switch {
	case len(pos) > 3:
		return usageError(cmd, "too many arguments: %s", strings.Join(pos, " "))
	case len(pos) == 3 || len(pos) == 1 && !isEdge(pos[0]):
		ref, pos = strings.Trim(pos[0], "/"), pos[1:]
	}
	switch {
	case ref == "":
		return usageError(cmd, "which tile? name it, or run bx in the tile's terminal")
	case len(pos) == 0:
		return listEdges(a, ref)
	case len(pos) == 1 || !isEdge(pos[0]):
		return usageError(cmd, "name the edge (slot:<name> or grant:<target>) and its policy: read, block, inherit or default")
	}
	edge, policy := pos[0], pos[1]
	if policy != "read" && policy != "block" && policy != "inherit" && policy != "default" {
		return usageError(cmd, "%q: an edge's policy is read, block, inherit or default", policy)
	}
	op := deployOp{cmd: cmd, route: "edge", how: "edge", can: "edges", body: map[string]any{"tile": ref, "edge": edge, "policy": policy},
		target: func(st *deployState) string { return st.primary() }}
	op.report = func(st *deployState, _ string, _ *deployImpact) deployReport {
		var r deployReport
		switch policy {
		case "read":
			r.question = "Let non-primary deployments read " + edge
			r.add("Data", st.Tile+"'s non-primary deployments may call "+edge+"'s primary, as reader. They never write to it")
		case "inherit":
			r.question = "Let non-primary deployments use " + edge + " as " + st.Tile + " does"
			r.add("Data", "they get "+st.Tile+"'s relay policy, never host networking or a provider splice; anything they send is real")
		case "block":
			r.question = "Block " + edge + " for non-primary deployments"
			r.add("Data", st.Tile+"'s non-primary deployments can't use "+edge)
		default:
			r.question = "Reset " + edge + " to its default for non-primary deployments"
		}
		r.add("Affects", "every non-primary deployment of "+st.Tile+", from its next call; the primary uses every edge as today")
		return r
	}
	op.result = func(_ []byte, _, after *deployState, _ string) string {
		return edge + ": " + policy + " for the non-primary deployments of " + after.Tile + "."
	}
	return runDeployOp(op, a)
}

func depRunNow(cmd string, a dcArgs) error {
	op, rest, err := depOp(cmd, "run-now", "runNow", a, 3)
	if err != nil {
		return err
	}
	job := rest[0]
	op.body["job"], op.timeout = job, runNowTimeout
	op.report = func(st *deployState, x string, _ *deployImpact) deployReport {
		r := deployReport{question: "Run " + job + " on " + x + " once"}
		r.add("Data", "runs "+job+" once on "+x+", with "+x+"'s data and "+st.Tile+"'s network: anything it sends (email, webhooks) is real")
		return r
	}
	op.result = func(b []byte, _, _ *deployState, _ string) string {
		var ans struct {
			Delivery struct {
				Status int   `json:"status"`
				MS     int64 `json:"ms"`
			} `json:"delivery"`
		}
		_ = json.Unmarshal(b, &ans)
		return fmt.Sprintf("delivered · %d · %d ms", ans.Delivery.Status, ans.Delivery.MS)
	}
	return runDeployOp(op, a)
}
