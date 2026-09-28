package main

// deployclient.go — the plumbing every tile-deployments command shares
// (11-contract §9.1): the argument grammar, reading the state, the
// operation's Can checked before anything is sent, the dry run and its
// impact report, the prompt or --yes, naming the reviewed code, waiting on a
// deploy, --json, and the exit codes. The commands register into moreCmds
// from their own files: livereload.go (bx live-reload, and the words for where
// saves go), deploy.go (bx deploy, bx promote, bx rollback, and the impact
// report), deployment.go (the bx deployment family), agentdeploy.go (bx agent
// run --deployment); status.go reads a named deployment's status and log.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"
)

const deployUsage = `  bx live-reload [<tile>] [--json]      where saves go: live reload's target or paused,
                                        what each deployment runs
  bx live-reload pause|now|resume [<tile>] [--to <name>]
                                        keep the tile on the code it runs while you edit ·
                                        ship the work tree once · follow every save again
  bx live-reload attach [<tile>] --to <name>
  bx deploy [<tile>] --to <name> [--checkpoint c:<id>]
                                        put a fresh checkpoint of the work tree (or c:<id>) on it
  bx rollback [<tile>] --to <name> [--checkpoint c:<id>]
                                        back to the previous checkpoint in its deploy log
` + deploymentUsage // deployment.go

// Exit codes of the tile-deployments commands (11-contract §9.1). Every
// existing command keeps exiting 1 on any error and 2 on usage.
const (
	exitFailed        = 1 // a deploy that ran and failed, an invalid state, a network error, any other refusal
	exitUsage         = 2
	exitRefused       = 3 // a Can of kind authority or policy, or HTTP 403
	exitNotConfirmed  = 4 // declined, or a guarded command with no terminal and no --yes
	exitStillRunning  = 5 // still running when bx stopped waiting
	exitNoDeployments = 6 // this xbind has no tile deployments
)

const oldXbindMsg = "this xbind has no tile deployments (no /api/xbin/deployments); upgrade xbind"

// protectedHint is added to a refusal a terminal or agent credential can't
// get past: only a tile manager in a person's own session may (P21).
const protectedHint = " — deploy from the terminal window's deployments panel as a tile manager, or with bx on the host"

// The command's surroundings, which the tests replace.
var (
	dcOut        io.Writer = os.Stdout
	dcErr        io.Writer = os.Stderr
	dcIn         io.Reader = os.Stdin
	dcIsTerminal           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	dcNow                  = time.Now
	dcWaitMax              = 20 * time.Minute // how long bx waits on a deploy (11-contract §9.1)
	dcPoll                 = time.Second      // the least time between two polls of a deploy
)

// dcError is a failed tile-deployments command: its exit code, what bx
// prints on stderr, and under --json what goes to stdout (the server's
// {error, docs}, or the Can object bx refused on).
type dcError struct {
	code      int
	msg       string
	json      []byte
	transport bool // the request never got an answer
}

func (e *dcError) Error() string { return e.msg }

func usageError(cmd, format string, a ...any) *dcError {
	msg := fmt.Sprintf(format, a...)
	if u := dcUsage[cmd]; u != "" {
		msg += "\nusage: " + u
	}
	return &dcError{code: exitUsage, msg: msg}
}

// dcUsage is each command's usage line, printed with a usage error.
var dcUsage = map[string]string{}

// dcCommand adapts a tile-deployments command to moreCmds: it prints its
// error as every bx error prints and exits with the command's own code,
// never main's 1.
func dcCommand(f func([]string) error) func([]string) error {
	return func(args []string) error {
		if code := dcRun(f, args); code != 0 {
			os.Exit(code)
		}
		return nil
	}
}

// dcRun runs a command and reports its failure: the message on stderr, the
// refusal's JSON on stdout under --json. It returns the exit code.
func dcRun(f func([]string) error, args []string) int {
	err := f(args)
	if err == nil {
		return 0
	}
	var e *dcError
	if errors.As(err, &e) && e.json != nil && dcWantsJSON(args) {
		printRaw(dcOut, e.json)
	}
	fmt.Fprintln(dcErr, "bx:", err)
	if e != nil {
		return e.code
	}
	return exitFailed
}

// dcWantsJSON: the line asks for --json (the last of --json and --no-json).
func dcWantsJSON(args []string) bool {
	on := false
	for _, a := range args {
		switch a {
		case "--json":
			on = true
		case "--no-json":
			on = false
		}
	}
	return on
}

func printRaw(w io.Writer, b []byte) {
	w.Write(append(bytes.TrimSpace(b), '\n'))
}

// --- the argument grammar ---

// dcArgs is one tile-deployments command line (11-contract §9.1): flags
// anywhere between positionals, --flag value or --flag=value, and every
// boolean with its --no- pair, the last one on the line winning (compat
// rule 6).
type dcArgs struct {
	pos        []string
	to         string // --to: the deployment a command acts on
	checkpoint string // --checkpoint c:<id>
	yes        bool
	dryRun     bool
	json       bool
	noWait     bool
	vals       map[string][]string // every other value flag, in order (deployment.go checks each)
	bools      map[string]bool     // every other boolean
}

// dcValueFlags are the flags that take a value; the rest are booleans.
var dcValueFlags = map[string]bool{"--to": true, "--checkpoint": true, "--from": true, "--keys": true,
	"--deliveries": true, "--always-on": true, "--mem": true, "--pids": true, "--disk": true, "--limit": true, "--path": true}

var (
	dcNameRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	dcCheckpointRe = regexp.MustCompile(`^c:[0-9a-f]{7,64}$`)
)

// parseDeploymentArgs parses cmd's arguments; allowed lists the flags cmd
// takes (a boolean's --no- pair comes with it). Every error is a usage error.
func parseDeploymentArgs(cmd string, args []string, allowed ...string) (dcArgs, error) {
	var a dcArgs
	ok := map[string]bool{}
	for _, f := range allowed {
		ok[f] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			a.pos = append(a.pos, args[i+1:]...)
			break
		}
		if !isFlag(arg) {
			a.pos = append(a.pos, arg)
			continue
		}
		name, val, inline := strings.Cut(arg, "=")
		base, on := name, true
		if rest, neg := strings.CutPrefix(name, "--no-"); neg && !dcValueFlags["--"+rest] {
			base, on = "--"+rest, false
		}
		if !ok[base] {
			return a, usageError(cmd, "%v", unknownFlag(cmd, name, false))
		}
		if dcValueFlags[base] {
			if !inline {
				v, err := nextArg(args, &i)
				if err != nil {
					return a, usageError(cmd, "%v", err)
				}
				val = v
			}
			switch base {
			case "--to":
				if !dcNameRe.MatchString(val) {
					return a, usageError(cmd, "--to %q: deployment names are lowercase letters, digits and \"-\", start with a letter, at most 24 characters", val)
				}
				a.to = val
			case "--checkpoint":
				if !dcCheckpointRe.MatchString(val) {
					return a, usageError(cmd, "--checkpoint %q: a checkpoint id is c: and at least 7 lowercase hex digits", val)
				}
				a.checkpoint = val
			default:
				if err := dcCheckFlag(base, val); err != nil {
					return a, usageError(cmd, "%s %q: %v", base, val, err)
				}
				if a.vals == nil {
					a.vals = map[string][]string{}
				}
				a.vals[base] = append(a.vals[base], val)
			}
			continue
		}
		if inline {
			return a, usageError(cmd, "%s takes no value", name)
		}
		switch base {
		case "--yes":
			a.yes = on
		case "--dry-run":
			a.dryRun = on
		case "--json":
			a.json = on
		case "--wait":
			a.noWait = !on
		default:
			if a.bools == nil {
				a.bools = map[string]bool{}
			}
			a.bools[base] = on
		}
	}
	return a, nil
}

// split takes the tile from the positionals, right-aligned (11-contract
// §9.1): the first one is the tile only when there are arity of them (the
// tile and the command's names), and $XBIN_COMPONENT otherwise. A positional
// holding / or + is always a tile ref: deployment names can't contain either.
func (a dcArgs) split(cmd string, arity int) (tile string, rest []string, err error) {
	switch {
	case len(a.pos) > arity:
		return "", nil, usageError(cmd, "too many arguments: %s", strings.Join(a.pos, " "))
	case len(a.pos) == arity && arity > 0:
		return strings.Trim(a.pos[0], "/"), a.pos[1:], nil
	case len(a.pos) > 0 && strings.ContainsAny(a.pos[0], "/+"):
		return "", nil, usageError(cmd, "missing argument after the tile %s", a.pos[0])
	}
	if c := os.Getenv("XBIN_COMPONENT"); c != "" {
		return c, a.pos, nil
	}
	return "", nil, usageError(cmd, "which tile? name it, or run bx in the tile's terminal")
}

// --- the wire ---

// deployState is the answer of GET /deployments, as bx reads it
// (11-contract §1.1, §1.3); the Can objects stay raw so a refusal under
// --json prints the server's bytes.
type deployState struct {
	Tile            string `json:"tile"`
	Selected        string `json:"selected"`
	Record          bool   `json:"record"`
	Seq             int64  `json:"seq"`
	View            string `json:"view"`
	Primary         string `json:"primary"`
	LiveReload      string `json:"liveReload"`
	LastLiveReload  string `json:"lastLiveReload"`
	LiveReloadSince *struct {
		At string `json:"at"`
		By string `json:"by"`
	} `json:"liveReloadSince"`
	WorkTree *struct {
		Changed int    `json:"changed"`
		Since   string `json:"since"`
	} `json:"workTree"`
	ProtectedPrimary bool           `json:"protectedPrimary"`
	Deployments      []deploymentSt `json:"deployments"`
	Caller           *struct {
		Can map[string]json.RawMessage `json:"can"`
	} `json:"caller"`
}

type deploymentSt struct {
	Name       string `json:"name"`
	Primary    bool   `json:"primary"`
	LiveReload bool   `json:"liveReload"`
	Checkpoint *struct {
		ID string `json:"id"`
	} `json:"checkpoint"`
	Status struct {
		State     string       `json:"state"`
		Gen       int          `json:"gen"`
		Deploying *deployEntry `json:"deploying"`
	} `json:"status"`
	API        string                     `json:"api"`
	URL        string                     `json:"url"`
	Data       *dataState                 `json:"data"`
	LastDeploy *deployEntry               `json:"lastDeploy"`
	Can        map[string]json.RawMessage `json:"can"`
}

// deployEntry is one deploy attempt (11-contract §1.1 DeployEntry).
type deployEntry struct {
	ID         int64  `json:"id"`
	Deployment string `json:"deployment"`
	How        string `json:"how"`
	From       string `json:"from"`
	Checkpoint string `json:"checkpoint"`
	Previous   string `json:"previous"`
	Result     string `json:"result"`
	Phase      string `json:"phase"`
	Error      string `json:"error"`
	By         string `json:"by"`
	Agent      bool   `json:"agent"`
	At         string `json:"requestedAt"`
}

// deployImpact is a dry run's report (11-contract §1.1 Impact).
type deployImpact struct {
	Code *struct {
		Deployment string `json:"deployment"`
		From       string `json:"from"`
		To         string `json:"to"`
		Files      int    `json:"files"`
		Added      int    `json:"added"`
		Removed    int    `json:"removed"`
		WorkTreeAt string `json:"workTreeAt"`
	} `json:"code"`
	Data             string   `json:"data"`
	Joins            *joins   `json:"joins"`
	Placeholders     []string `json:"placeholders"`
	PausesLiveReload bool     `json:"pausesLiveReload"`
	Stops            []string `json:"stops"`
	Affects          string   `json:"affects"`
	Reloads          []string `json:"reloads"`
}

// deployAnswer is what every POST of the family answers.
type deployAnswer struct {
	State     *deployState  `json:"state"`
	Deploy    *deployEntry  `json:"deploy"`
	Unchanged bool          `json:"unchanged"`
	Impact    *deployImpact `json:"impact"`
}

type dcCan struct {
	OK   bool   `json:"ok"`
	Why  string `json:"why"`
	Kind string `json:"kind"`
	raw  []byte
}

func (st *deployState) primary() string {
	if st.Primary == "" {
		return "main"
	}
	return st.Primary
}

func (st *deployState) deployment(name string) *deploymentSt {
	for i := range st.Deployments {
		if st.Deployments[i].Name == name {
			return &st.Deployments[i]
		}
	}
	return nil
}

// pinnedTo names what deployment name runs: "pinned to c:…", or "the work
// tree" while it follows it.
func (st *deployState) pinnedTo(name string) string {
	if d := st.deployment(name); d != nil && d.Checkpoint != nil && d.Checkpoint.ID != "" {
		return "pinned to " + d.Checkpoint.ID
	} else if d != nil && d.LiveReload {
		return "following the work tree"
	}
	return "pinned to its current code"
}

// can finds a permission of the state: the target deployment's (perDep) or
// the caller's. nil when the state doesn't carry it: the server judges.
func (st *deployState) can(key, dep string, perDep bool) *dcCan {
	var m map[string]json.RawMessage
	if perDep {
		if d := st.deployment(dep); d != nil {
			m = d.Can
		}
	} else if st.Caller != nil {
		m = st.Caller.Can
	}
	raw, ok := m[key]
	if !ok {
		return nil
	}
	c := &dcCan{raw: raw}
	if json.Unmarshal(raw, c) != nil {
		return nil
	}
	return c
}

// refusal is the error for a Can bx won't send past: exit 3 for authority
// and policy, 1 for state (11-contract §9.1).
func (c *dcCan) refusal() *dcError {
	msg := c.Why
	if msg == "" {
		msg = "not allowed"
	}
	code := exitFailed
	if c.Kind == "authority" || c.Kind == "policy" {
		code = exitRefused
		msg += refusalHint(msg)
	}
	return &dcError{code: code, msg: msg, json: c.raw}
}

// refusalHint answers the refusals a tile credential can't get past with
// where it can be done (11-contract §9.1 "Hints").
func refusalHint(msg string) string {
	if (strings.HasPrefix(msg, "the primary of ") && strings.Contains(msg, " is protected")) ||
		strings.Contains(msg, "is a tile manager's act, done in a person's own session") {
		return protectedHint
	}
	return ""
}

// apiHint is apiJSON's hint for a refusal (main.go): on a protected primary,
// or a manager act refused to a tile credential, where it can be done, in
// place of the scoped-terminal hint (10-ux §8.4).
func apiHint(status int, msg, hint string) string {
	if h := refusalHint(msg); status == http.StatusForbidden && h != "" {
		return h
	}
	return hint
}

// dcCall sends one request of the family; an answer of 400 or more is a
// *dcError carrying its exit code.
func dcCall(method, path string, body any) ([]byte, error) {
	resp, err := api(method, path, body)
	if err != nil {
		return nil, &dcError{code: exitFailed, msg: err.Error(), transport: true}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &dcError{code: exitFailed, msg: err.Error(), transport: true}
	}
	if resp.StatusCode < 400 {
		return b, nil
	}
	return nil, httpRefusal(resp.StatusCode, b)
}

// httpRefusal classifies an error answer. A 404 or 405 whose body is not a
// JSON object with "error" is Go's mux: the route is missing, this xbind
// predates tile deployments (12-compat §4.2).
func httpRefusal(status int, b []byte) *dcError {
	var e struct {
		Error *string `json:"error"`
	}
	isErr := json.Unmarshal(b, &e) == nil && e.Error != nil
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && !isErr {
		return &dcError{code: exitNoDeployments, msg: oldXbindMsg}
	}
	d := &dcError{code: exitFailed, msg: strings.TrimSpace(string(b))}
	if isErr {
		d.msg, d.json = *e.Error, b
	}
	if d.msg == "" {
		d.msg = http.StatusText(status)
	}
	if status == http.StatusForbidden {
		d.code = exitRefused
		d.msg += refusalHint(d.msg)
	}
	return d
}

func decodeAnswer(b []byte, out any) error {
	if err := json.Unmarshal(b, out); err != nil {
		return &dcError{code: exitFailed, msg: "unexpected answer from xbind: " + err.Error()}
	}
	return nil
}

// getDeployState reads GET /deployments for a tile ref. A query string never
// carries the qualifier (P17: a '+' there reads as a space), so a ref ending
// in "+<name>" asks for its tile with deployment=<name>. When that names no
// deployment of a tile with a record, the ref is a tile's own name holding
// '+' (an exact match wins: no new name may hold one, but older directories
// keep resolving), asked for as it is; failing that, the first answer
// stands.
func getDeployState(ref string) (*deployState, []byte, error) {
	tile, dep := splitRef(ref)
	st, b, err := readDeployState(tile, dep)
	var old *dcError
	if dep == "" || err == nil && st.Record || errors.As(err, &old) && old.code == exitNoDeployments {
		return st, b, err
	}
	if st2, b2, err2 := readDeployState(ref, ""); err2 == nil {
		return st2, b2, nil
	}
	return st, b, err
}

func readDeployState(tile, dep string) (*deployState, []byte, error) {
	q := url.Values{"tile": {tile}}
	if dep != "" {
		q.Set("deployment", dep)
	}
	b, err := dcCall("GET", "/api/xbin/deployments?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	st := &deployState{}
	if err := decodeAnswer(b, st); err != nil {
		return nil, nil, err
	}
	return st, b, nil
}

// splitRef splits a tile ref "<tile>+<name>" at its last segment's last '+'
// when a deployment name follows; any other ref is a tile with no name.
func splitRef(ref string) (tile, dep string) {
	i := strings.LastIndexByte(ref, '+')
	if i < 1 || ref[i-1] == '/' || strings.Contains(ref[i:], "/") || !dcNameRe.MatchString(ref[i+1:]) {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}

// queryTile is the tile and the deployment a query names for ref (P17):
// splitRef's, resolved through the state when the ref has a qualifier, since
// a '+' may also be part of a tile's own name.
func queryTile(ref string) (tile, dep string, err error) {
	if _, d := splitRef(ref); d == "" {
		return ref, "", nil
	}
	st, _, err := getDeployState(ref)
	if err != nil {
		return "", "", err
	}
	return st.Tile, st.Selected, nil
}

// readRef is a read command's positional tile ref (bx status, bx logs):
// "<tile>+<name>" names that deployment of its tile, as a path would, and
// goes out as the tile and deployment= (P17) — unless something in the
// workspace sits at the whole ref (a tile named with '+' before the rule:
// the exact match wins, with no request), or xbind knows no such
// deployment. dep "": the ref is a tile's own path.
func readRef(ref string) (tile, dep string) {
	if _, d := splitRef(ref); d == "" {
		return ref, ""
	}
	if ws := workspaceRoot(); ws != "" {
		if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(ref))); err == nil {
			return ref, ""
		}
	}
	if st, _, err := getDeployState(ref); err == nil && st.Selected != "" {
		return st.Tile, st.Selected
	}
	return ref, ""
}

// --- running a changing command ---

// deployOp is one changing command.
type deployOp struct {
	cmd    string         // the command, for usage and messages: "live-reload pause"
	route  string         // under /api/xbin/deployments/
	how    string         // pause | resume | reload-now | attach | deploy | rollback | promote | …
	can    string         // the permission it needs (11-contract §1.1)
	perDep bool           // can is the target deployment's, not the caller's
	body   map[string]any // the request, without dryRun, expect, checkpoint and seq
	// target is the deployment acted on, read from the state.
	target func(st *deployState) string
	// deployment.go's ops: guard always asks; confirm, the route's token, is
	// sent in the dry run and once confirmed; report and result word the op;
	// jsonOut keeps the answer (bx deployment set); timeout outlasts 30 s.
	guard   bool
	confirm string
	report  func(st *deployState, x string, imp *deployImpact) deployReport
	result  func(b []byte, before, after *deployState, x string) string
	jsonOut *[]byte
	timeout time.Duration
}

// runDeployOp runs a changing command (11-contract §9.1): the state and the
// operation's Can, the dry run and its report, the prompt or --yes, the
// request naming the code the report showed, and the wait.
func runDeployOp(op deployOp, a dcArgs) error {
	ref, _ := op.body["tile"].(string)
	st, _, err := getDeployState(ref)
	if err != nil {
		return err
	}
	target := op.target(st)
	if target == "" {
		return usageError(op.cmd, "which deployment? name it with --to (a code move never defaults its target)")
	}
	if op.how == "attach" && st.Record && st.View != "reader" && st.LiveReload == "" {
		return &dcError{code: exitFailed, msg: fmt.Sprintf("live reload is paused: resume it onto %s instead — bx live-reload resume %s --to %s", target, st.Tile, target)}
	}
	if c := st.can(op.can, target, op.perDep); c != nil && !c.OK {
		return c.refusal()
	}
	named, err := reviewedCode(op, a, st, target) // deploy.go: onto a protected primary
	if err != nil {
		return err
	}
	for k, v := range named {
		op.body[k] = v
	}

	dry := map[string]any{"dryRun": true}
	for k, v := range op.body {
		dry[k] = v
	}
	if op.confirm != "" {
		dry["confirm"] = op.confirm // judged as for real (11-contract §1.2); a dry run changes nothing
	}
	b, err := dcCall("POST", "/api/xbin/deployments/"+op.route, dry)
	if err != nil {
		return err
	}
	var da deployAnswer
	if err := decodeAnswer(b, &da); err != nil {
		return err
	}
	if da.State != nil {
		st = da.State
	}
	imp := da.Impact
	if imp == nil {
		imp = &deployImpact{}
	}
	rep := buildReport(op, a, st, target, imp)
	if op.report != nil {
		rep = op.report(st, target, imp)
	}
	info := dcOut // what a person reads; stdout carries only JSON under --json
	if a.json {
		info = dcErr
	}
	rep.print(info)
	if a.dryRun {
		if op.jsonOut != nil {
			*op.jsonOut = b
		} else if a.json {
			printRaw(dcOut, b)
		} else {
			fmt.Fprintln(dcOut, "dry run: nothing changed")
		}
		return nil
	}

	onPrimary := target == st.primary()
	guarded := op.guard || onPrimary && (op.how == "deploy" || op.how == "rollback" || op.how == "promote" ||
		((op.how == "reload-now" || op.how == "resume" || op.how == "attach") && movesCode(imp)))
	if guarded && !a.yes {
		if !dcIsTerminal() {
			why := firstOf(rep.why, fmt.Sprintf("this moves code onto the primary of %s, which everyone using it runs", st.Tile))
			return &dcError{code: exitNotConfirmed, msg: "not confirmed: " + why + " — without a terminal, add --yes when the user asked for it"}
		}
		fmt.Fprintf(dcErr, "%s? [y/N] ", rep.question)
		line, _ := bufio.NewReader(dcIn).ReadString('\n')
		if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
			return &dcError{code: exitNotConfirmed, msg: "not confirmed: nothing changed"}
		}
	}

	// Name the code the report showed, so it is what ships (11-contract §9.1).
	body := map[string]any{}
	for k, v := range op.body {
		body[k] = v
	}
	if op.confirm != "" {
		body["confirm"] = op.confirm // --yes, or the person said yes
	}
	name := func(key, id string) { // a code named before the dry run stays named
		if _, ok := body[key]; !ok {
			body[key] = id
		}
	}
	if c := imp.Code; c != nil && c.To != "" {
		switch {
		case op.how == "reload-now", op.how == "promote", op.how == "primary" && st.ProtectedPrimary,
			op.how == "protect" && body["on"] == true:
			name("expect", c.To)
		case op.how == "deploy" && a.checkpoint == "" && st.ProtectedPrimary && onPrimary:
			name("checkpoint", c.To)
		case op.how == "deploy" && a.checkpoint == "" && body["restart"] == nil:
			name("expect", c.To)
		case op.how == "rollback" && a.checkpoint == "":
			name("checkpoint", c.To)
		}
	}
	if st.Record && (guarded || (st.ProtectedPrimary && onPrimary)) {
		body["seq"] = st.Seq
	}
	before := st
	if op.timeout > 0 {
		b, _, err = dcRequest("POST", "/api/xbin/deployments/"+op.route, body, op.timeout)
	} else {
		b, err = dcCall("POST", "/api/xbin/deployments/"+op.route, body)
	}
	if err != nil {
		return err
	}
	var ans deployAnswer
	if err := decodeAnswer(b, &ans); err != nil {
		return err
	}
	if op.jsonOut != nil {
		*op.jsonOut = b
	} else if a.json {
		printRaw(dcOut, b)
	}
	after := ans.State
	if after == nil {
		after = before
	}
	// Where saves go moved when the request was committed, whatever the
	// deploy's fate: say so on every path from here (SC-AGENT-BX).
	saves := func() {
		if s := whereSavesGo(before, after); s != "" { // deployment.go
			fmt.Fprintln(info, s)
		}
	}
	if ans.Deploy == nil || ans.Unchanged {
		if op.result != nil && !ans.Unchanged {
			fmt.Fprintln(info, op.result(b, before, after, target))
		} else {
			fmt.Fprintln(info, unchangedText(op.how, after, target))
		}
		saves()
		return nil
	}
	e := *ans.Deploy
	tile := firstOf(after.Tile, before.Tile)
	if a.noWait && (e.Result == "queued" || e.Result == "running") {
		fmt.Fprintf(info, "%s: %s %d on %s accepted (%s); it goes on without bx\n", tile, howWord(e.How), e.ID, e.Deployment, e.Result)
		saves()
		return nil
	}
	if e.Result == "queued" || e.Result == "running" {
		if e, err = waitDeploy(tile, e); err != nil {
			saves()
			return err
		}
	}
	switch e.Result {
	case "failed":
		keeps := e.Previous
		if keeps == "" {
			keeps = "its previous code"
		}
		msg := fmt.Sprintf("Deploy to %s failed — %s keeps running %s", e.Deployment, e.Deployment, keeps)
		if e.Error != "" {
			msg += "\n  " + e.Error
		}
		saves()
		return &dcError{code: exitFailed, msg: msg}
	case "cancelled":
		saves()
		return &dcError{code: exitFailed, msg: fmt.Sprintf("cancelled: %s was removed or %s was disabled", e.Deployment, tile)}
	}
	if t := movedText(op.how, e); t != "" {
		fmt.Fprintln(info, t)
	}
	if op.result != nil {
		fmt.Fprintln(info, op.result(b, before, after, target))
	}
	saves()
	if !after.Record && before.Record {
		fmt.Fprintf(info, "%s is back to plain live reload, with no deployments.\n", tile)
	}
	return nil
}

// movesCode: the dry run's code differs from what the target runs, or bx
// can't tell (no capture was taken).
func movesCode(imp *deployImpact) bool {
	return imp.Code == nil || imp.Code.To != imp.Code.From
}

// waitDeploy polls GET /deployments/log?id=&wait=25 until the attempt has a
// final result, printing each phase to stderr, for at most dcWaitMax.
func waitDeploy(tile string, e deployEntry) (deployEntry, error) {
	start := dcNow()
	fmt.Fprintf(dcErr, "%s: %s %d %s", tile, e.How, e.ID, strings.TrimPrefix(e.From+" → "+e.Deployment, " → "))
	if e.Checkpoint != "" {
		fmt.Fprintf(dcErr, " %s", e.Checkpoint)
	}
	phase := ""
	show := func(e deployEntry) {
		if e.Phase != "" && e.Phase != phase {
			phase = e.Phase
			fmt.Fprintf(dcErr, " … %s", phase)
		}
	}
	show(e)
	misses := 0
	for e.Result == "queued" || e.Result == "running" {
		left := dcWaitMax - dcNow().Sub(start)
		if left <= 0 {
			fmt.Fprintln(dcErr, " …")
			return e, &dcError{code: exitStillRunning, msg: fmt.Sprintf("%s %d on %s is still %s — bx stopped waiting after %s; bx deployment log %s shows where it stands", howWord(e.How), e.ID, e.Deployment, e.Result, dcWaitMax.Round(time.Second), tile)}
		}
		wait := min(25, max(1, int(left/time.Second)))
		asked := dcNow()
		b, err := dcCall("GET", fmt.Sprintf("/api/xbin/deployments/log?tile=%s&id=%d&wait=%d", url.QueryEscape(tile), e.ID, wait), nil)
		var de *dcError
		if errors.As(err, &de) && de.transport && misses < 3 {
			misses++ // a blip while xbind is busy: ask again
			time.Sleep(dcPoll)
			continue
		}
		if err != nil {
			fmt.Fprintln(dcErr)
			return e, err
		}
		misses = 0
		var ans struct {
			Entry *deployEntry `json:"entry"`
		}
		if err := decodeAnswer(b, &ans); err != nil || ans.Entry == nil {
			fmt.Fprintln(dcErr)
			return e, &dcError{code: exitFailed, msg: "unexpected answer from xbind's deploy log"}
		}
		e = *ans.Entry
		show(e)
		if (e.Result == "queued" || e.Result == "running") && dcNow().Sub(asked) < dcPoll {
			time.Sleep(dcPoll) // an xbind that doesn't hold the answer is polled, not hammered
		}
	}
	fmt.Fprintf(dcErr, " … %s (%s)\n", e.Result, dcNow().Sub(start).Round(time.Second))
	return e, nil
}
