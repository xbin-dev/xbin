// harness_spawn.go — the agent delegating to a coding agent (D147
// §4.4, §4.3.13). subagent_spawn's `harness` starts a harness child: the
// task is its first prompt (an hprompt row, the task ledger's as it is
// delivered), its mode the root owner's own setting (§4.3.12) narrowed by
// `harness_mode`, and its turn's end settles the link like any subagent's
// (endHarnessTurnTx) — within maxHarness running per tree and the room its
// sandbox has for one more command. subagent_message to one is an hprompt,
// sent as is; its digest says what it does and whom it waits for. The
// parent model never answers a child's permission: a park goes to people
// (Needs, push, the child card), and a parent's message waits for them
// (harnessPass). A person's direct message to a harness child is told to
// its parent as a notice (an hnote, kept in harness_notes), delivered at
// the parent's next step boundary (actor.go deliverBoundary) and never a
// turn of its own.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// What harness_mode may ask: it only narrows the owner's setting.
const hsmPlan = "plan"

// spawnHarnessIDs are the coding agents subagent_spawn offers: those the
// class allows that a sandbox the spawn may use (the active one, or an
// attached one named by `sandbox`) offers, reaching out (egress ≠ none).
func spawnHarnessIDs(cfg Config) []string {
	cls := classOf(cfg)
	if !cls.has(tsHarness) || !sandboxToolsOn(cfg) {
		return nil
	}
	var out []string
	add := func(b SandboxBinding) {
		if b.Egress == "none" {
			return
		}
		for _, id := range bindingHarnesses(b) {
			if cls.allowsHarness(id) && !hasStr(out, id) {
				out = append(out, id)
			}
		}
	}
	add(*cfg.Sandbox)
	for _, b := range cfg.Attached {
		add(b)
	}
	return out
}

// bindingHarnesses are the coding agents a bound sandbox offers: what its
// manager advertised for its image when it was bound; for a manager that
// says nothing (it predates the field — its sandboxes are built on the
// rootfs that pins them), the sdk catalog's, which the spawn's own check
// and the first start's probe decide.
func bindingHarnesses(b SandboxBinding) []string {
	if b.Harnesses != nil {
		return b.Harnesses
	}
	var out []string
	for _, p := range acp.Providers() {
		out = append(out, p.ID)
	}
	return out
}

// harnessSpawnProps adds `harness` and `harness_mode` to subagent_spawn
// when a coding agent qualifies (§4.4); the description says the limit
// first.
func harnessSpawnProps(cfg Config, props map[string]any) bool {
	ids := spawnHarnessIDs(cfg)
	if len(ids) == 0 {
		return false
	}
	props["harness"] = map[string]any{"type": "string", "enum": ids, "description": fmt.Sprintf(
		"A coding agent sees only the sandbox and the task, not this conversation — so the task must say everything (paths, what to "+
			"change, how to check it). Optional: the coding agent to do the task in the sandbox instead of a subagent. Each one is a full "+
			"coding CLI costing hundreds of MB there: use it for real coding work, at most %d running at once in this conversation, and "+
			"give parallel ones a distinct cwd or a git worktree each. It asks a person, not you, before it acts (you never answer its "+
			"permission requests); its answer is its turn's last text. Not with system or after", cfg.maxHarness())}
	props["harness_mode"] = map[string]any{"type": "string", "enum": []string{hmApprove, hsmPlan}, "description": "optional, with harness: " +
		"approve — it asks a person before every edit and command; plan — it only plans, changing nothing. Absent: the conversation " +
		"owner's own setting for that coding agent. It can only narrow that setting"}
	return true
}

// harnessSpawn is a harness child about to be made: its provider, the
// sandbox it works in and the mode it starts in.
type harnessSpawn struct {
	prov acp.Provider
	b    SandboxBinding
	mode string
}

// harnessSpawnOf checks subagent_spawn's `harness` (nil, nil: none named):
// the class allows it, the spawn sandbox offers it and reaches out, it may
// still be used, no more coding agents run in the tree than maxHarness,
// and the sandbox has room for one more command.
func (e *Engine) harnessSpawnOf(ctx context.Context, ts *turnState, args map[string]any, onSandbox *SandboxBinding) (*harnessSpawn, error) {
	id, want := strings.TrimSpace(str(args["harness"])), strings.TrimSpace(str(args["harness_mode"]))
	if id == "" {
		if want != "" {
			return nil, fmt.Errorf("harness_mode goes with harness")
		}
		return nil, nil
	}
	cfg := ts.cfg
	cls := classOf(cfg)
	switch {
	case strings.TrimSpace(str(args["system"])) != "":
		return nil, fmt.Errorf("system: a coding agent keeps its own instructions — leave system out with harness")
	case !cls.has(tsHarness) || !cls.has(tsSandbox):
		return nil, fmt.Errorf("coding agents are not available in this conversation's class (%s)", cls.Name)
	case !sandboxToolsOn(cfg):
		return nil, fmt.Errorf("a coding agent works in a sandbox, and this conversation has none")
	case len(toInt64Slice(args["after"])) > 0:
		return nil, fmt.Errorf("after: a coding agent starts at once — wait for those first (subagent_wait), then start it")
	case want != "" && want != hmApprove && want != hsmPlan:
		return nil, fmt.Errorf("harness_mode: approve or plan")
	}
	b := *cfg.Sandbox
	if onSandbox != nil {
		b = *onSandbox
	}
	name := orStr(b.Name, b.Ref)
	prov := harnessProvider(id, nil)
	switch {
	case !cls.allowsHarness(id):
		return nil, fmt.Errorf("harness: the %s class doesn't allow %s", cls.Name, harnessName(id))
	case !hasStr(bindingHarnesses(b), id):
		return nil, fmt.Errorf("harness: %s doesn't offer %s (it offers %s)", name, harnessName(id), orStr(strings.Join(bindingHarnesses(b), ", "), "none"))
	case b.Egress == "none":
		return nil, fmt.Errorf("%s must reach its provider — %s's egress is none", prov.Name, name)
	}
	if seen := e.db.seenHarnesses(id)[b.Ref]; seen.Installed != nil && !*seen.Installed {
		return nil, fmt.Errorf("%s doesn't have %s (%s not found)", name, prov.Name, strings.Join(prov.Bins, ", "))
	}
	if n := e.db.runningHarnesses(ts.root); n >= cfg.maxHarness() {
		return nil, errMaxHarness(cfg)
	}
	u, err := e.ag.sandboxUse(withSbxCall(ctx, sbxCall{run: ts.run.ID, name: "subagent_spawn", approve: cfg.Approve}), ts.root, cfg, b.Ref)
	if err != nil {
		return nil, err
	}
	name = orStr(u.Box.Name, name)
	if u.Box.effectiveEgress() == "none" {
		return nil, fmt.Errorf("%s must reach its provider — %s's egress is none", prov.Name, name)
	}
	if u.Hello.advertises() && !hasStr(u.Hello.imageHarnesses(u.Box.Image.ID), id) {
		// a binding without harnesses offers the catalog's; a manager that
		// does advertise says what this image has (as POST /ask checks)
		return nil, fmt.Errorf("%s's image doesn't have %s", name, prov.Name)
	}
	if max := u.Hello.Limits.ExecsRunning; max > 0 {
		execs, err := u.Conn.ExecList(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		running := 0
		for _, x := range execs {
			if x.State == "running" {
				running++
			}
		}
		if running+1 > max-4 {
			return nil, fmt.Errorf("%s runs %d command%s (its limit is %d) — a coding agent needs room", name, running, plural(running), max)
		}
	}
	return &harnessSpawn{prov: prov, b: b, mode: e.harnessChildMode(ts.root, prov, want)}, nil
}

func errMaxHarness(cfg Config) error {
	return fmt.Errorf("%d coding agents already run in this conversation (the limit) — wait for one or cancel one", cfg.maxHarness())
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// harnessChildMode is the mode a harness child starts in: harness_mode's
// (approve, plan) when given, else the root conversation owner's own
// setting (§4.3.12) — never an explicit mode, and never wider than theirs.
func (e *Engine) harnessChildMode(root int64, prov acp.Provider, want string) string {
	m := e.harnessChildModeOf(root, prov, want)
	if !prov.Safe(m) {
		return "" // (never: the catalog's settings are safe) the adapter's own
	}
	return m
}

func (e *Engine) harnessChildModeOf(root int64, prov acp.Provider, want string) string {
	switch want {
	case hsmPlan:
		return orStr(prov.PlanMode, prov.ApproveMode)
	case hmApprove:
		return prov.ApproveMode
	}
	if r, err := e.db.getRun(root); err == nil && person(r.Owner) && e.db.harnessMode(r.Owner, prov.ID) == hmAuto && prov.AutoMode != "" {
		return prov.AutoMode
	}
	return prov.ApproveMode
}

// runningHarnesses counts the coding agents at work below root: a turn
// running, or parked on a person.
func (d *DB) runningHarnesses(root int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM runs WHERE root_id=? AND parent_id<>0 AND engine=? AND status IN (?, ?, ?, ?, ?, ?)`,
		root, engineHarness, statusRunning, statusWaiting, statusAwait, statusSleep, statusQueued, statusBlocked).Scan(&n)
	return n
}

// apply makes child a harness child's config: the coding agent in the
// spawn sandbox (its active one), no subagent contract (a coding agent
// keeps its own instructions).
func (h *harnessSpawn) apply(child *Config, parent Config) {
	b := h.b
	child.Sandbox = &b
	child.System = strings.TrimSuffix(parent.System, subagentContract) // a subagent parent's carries it
	child.Engine = engineHarness
	child.Harness = &HarnessConfig{Provider: h.prov.ID, Mode: h.mode, Ref: b.Ref, Cwd: b.Cwd}
}

// --- subagent_message -------------------------------------------------------------

// harnessMessageReply says where a parent's message to harness child n
// goes (§4.4), as things stand before it is queued: into its running
// turn, after it, or as its next prompt.
func (e *Engine) harnessMessageReply(t *DB, n *Run) string {
	hs, _ := t.harnessSession(n.ID)
	steers := hs != nil && hs.Steering
	if s := e.harnessOf(n.ID); s != nil && s.steers() {
		steers = true
	}
	switch {
	case n.Status == statusWaiting:
		if w := harnessWaitWords(t, n); w != "" {
			return fmt.Sprintf("queued: #%d is %s — it gets your message once they have", n.ID, w)
		}
		return fmt.Sprintf("queued until #%d's current turn ends", n.ID)
	case n.Status == statusRunning || (hs != nil && hs.PromptState != ""):
		if steers {
			return fmt.Sprintf("steered into #%d's running turn", n.ID)
		}
		return fmt.Sprintf("queued until #%d's current turn ends", n.ID)
	}
	return fmt.Sprintf("sent as #%d's next prompt", n.ID)
}

// --- the digest -------------------------------------------------------------------

// harnessDigestHead is a harness child's digest line (§4.4): its phase,
// "harness ‹provider› · N tool calls · $cost", and — parked on a person —
// what it waits for; doing is what it is doing now.
func harnessDigestHead(t *DB, r *Run, l *Link, age string) (head, wait, doing string) {
	sum := harnessSummary(t, r)
	if sum == nil {
		return fmt.Sprintf("#%d %s — %s for %s", r.ID, r.Title, phaseWord(r, l), age), "", ""
	}
	state, _ := sum["state"].(string)
	phase := phaseWord(r, l)
	switch {
	case r.Status == statusWaiting:
		phase = "waiting for a person"
	case !active(r.Status) && (state == "failed" || state == "lost"):
		phase = state
		if e, _ := sum["error"].(string); e != "" {
			phase += ": " + clip(oneLine(e), 160)
		}
	}
	c, _ := sum["counts"].(hCounts)
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s — %s for %s, harness %s · %d tool call%s", r.ID, r.Title, phase, age, sum["provider"], c.Tools, plural(c.Tools))
	if cost := usageCost(sum["usage"]); cost != "" {
		b.WriteString(" · " + cost)
	}
	if a, ok := sum["activity"].(hActivity); ok && active(r.Status) && a.Kind != "idle" && a.Kind != "waiting" {
		doing = a.Kind
		if a.Title != "" {
			doing += ": " + clip(oneLine(a.Title), 120)
		}
	}
	return b.String(), harnessWaitWords(t, r), doing
}

// harnessWaitWords is what a parked harness run waits for, in a person's
// terms ("" when it doesn't).
func harnessWaitWords(t *DB, r *Run) string {
	p := parsePending(r.Pending)
	if r.Status != statusWaiting || p.Harness == nil {
		return ""
	}
	switch p.Kind {
	case "approval":
		title := ""
		if p.Harness.Tool != nil {
			title = orStr(p.Harness.Tool.Title, p.Harness.Tool.Label)
		}
		return "waiting for a person to approve: " + clip(oneLine(orStr(title, "a tool call")), 120)
	case "question":
		return "waiting for a person to answer: " + clip(oneLine(p.Harness.Message), 120)
	case "login":
		return "waiting for a person to sign in"
	}
	return ""
}

// usageCost is a usage's cumulative cost ("$0.40"; "" when not said).
func usageCost(v any) string {
	if v == nil {
		return ""
	}
	raw, _ := json.Marshal(v)
	var u struct {
		Cost *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Cost == nil {
		return ""
	}
	if u.Cost.Currency == "" || strings.EqualFold(u.Cost.Currency, "USD") {
		return fmt.Sprintf("$%.2f", u.Cost.Amount)
	}
	return fmt.Sprintf("%.2f %s", u.Cost.Amount, u.Cost.Currency)
}

// --- a person's direct message (§4.3.13) -------------------------------------------

// noteParentTx tells child's parent that a person (who) messaged child
// directly: an hnote, delivered as a notice at the parent's next step
// boundary, or before its next turn's first message — it never starts a
// turn. It waits in harness_notes, not the inbox: an idle parent may keep
// one for good, and a build from before coding agents, rolled back to,
// counts every undelivered inbox row as work it never does (its resume job
// would wake the tile every minute).
func (ag *Agent) noteParentTx(t *DB, child *Run, who, text string, files []string) error {
	cfg, err := t.runConfig(child.ID)
	if err != nil || cfg.Harness == nil || child.ParentID == 0 {
		return err
	}
	if text == "" && len(files) > 0 {
		text = "(files: " + strings.Join(files, ", ") + ")"
	}
	note := fmt.Sprintf("[direct message to #%d (%s) from %s]\n%s", child.ID, t.harnessRunName(child.ID, cfg.Harness.Provider), who, text)
	return t.addHarnessNote(child.ParentID, inboxBody{Text: note, Source: "harness", From: child.ID, Sender: who})
}

// addHarnessNote queues a notice for run, after the inbox rows it has now.
func (d *DB) addHarnessNote(runID int64, body inboxBody) error {
	raw, _ := json.Marshal(body)
	_, err := d.q.Exec(`INSERT INTO harness_notes (run_id, after, body, created)
		VALUES (?, (SELECT COALESCE(max(id), 0) FROM inbox), ?, ?)`, runID, string(raw), now())
	return err
}

// harnessNotesAfter are run's notices (only the undelivered ones: pending)
// as inbox rows of kind hnote — their ids are harness_notes' — and each
// one's place: the inbox rows up to that id came before it.
func (d *DB) harnessNotesAfter(runID int64, pending bool) ([]*InboxRow, []int64) {
	q := `SELECT id, after, body, created, delivered_at, msg_id FROM harness_notes WHERE run_id=?`
	if pending {
		q += ` AND delivered_at=0`
	}
	rows, err := d.q.Query(q+` ORDER BY after, id`, runID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var out []*InboxRow
	var after []int64
	for rows.Next() {
		r := &InboxRow{RunID: runID, Kind: inboxHNote}
		var body string
		var a int64
		if rows.Scan(&r.ID, &a, &body, &r.Created, &r.DeliveredAt, &r.MsgID) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(body), &r.Body)
		out, after = append(out, r), append(after, a)
	}
	return out, after
}

// moveInboxNotes moves the notices an earlier build of this program left
// in the inbox (run's; 0: every run's) to harness_notes, each where it
// was. At the start (addHarnessSchema), and as a run's input is read: a
// process of that build still serves through a blue/green swap, after the
// start's move. d: a transaction.
func (d *DB) moveInboxNotes(runID int64) error {
	where, args := `kind='`+inboxHNote+`' AND delivered_at=0`, []any{}
	if runID != 0 {
		where, args = where+` AND run_id=?`, append(args, runID)
	}
	var n int
	if err := d.q.QueryRow(`SELECT count(*) FROM inbox WHERE `+where, args...).Scan(&n); err != nil || n == 0 {
		return err
	}
	if _, err := d.q.Exec(`INSERT INTO harness_notes (run_id, after, body, created)
		SELECT run_id, id - 1, body, created FROM inbox WHERE `+where+` ORDER BY id`, args...); err != nil {
		return err
	}
	_, err := d.q.Exec(`DELETE FROM inbox WHERE `+where, args...)
	return err
}

// undeliveredWithNotes is run's pending input with its notices among it,
// each after the inbox rows that were there when it was written.
func (d *DB) undeliveredWithNotes(runID int64) []*InboxRow {
	_ = d.moveInboxNotes(runID)
	rows := d.undelivered(runID)
	notes, after := d.harnessNotesAfter(runID, true)
	if len(notes) == 0 {
		return rows
	}
	out := make([]*InboxRow, 0, len(rows)+len(notes))
	i := 0
	for k, n := range notes {
		for i < len(rows) && rows[i].ID <= after[k] {
			out = append(out, rows[i])
			i++
		}
		out = append(out, n)
	}
	return append(out, rows[i:]...)
}

// consumeHarnessNote marks a notice delivered; false if something else
// already did.
func (d *DB) consumeHarnessNote(id, msgID int64) bool {
	res, err := d.q.Exec(`UPDATE harness_notes SET delivered_at=?, msg_id=? WHERE id=? AND delivered_at=0`, now(), msgID, id)
	return err == nil && rowsAffected(res) == 1
}
