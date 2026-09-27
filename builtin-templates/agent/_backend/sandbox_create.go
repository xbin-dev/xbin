// sandbox_create.go — the agent makes a coding sandbox for its conversation
// (D115, plan item 12). sandbox_create parks for the conversation owner's
// grant (grants.go, capSandboxes: once, or for an hour), creates the sandbox
// at a manager FOR the owner — Sbx-User is the owner, who approved it — and
// binds it into the conversation: the active sandbox when none is, else
// attached. The coding tools see it from the next step of the same turn
// (sbxTurn, sandbox_turn.go).
//
// Only the top-level conversation creates, never in a chat channel's
// conversation or one no person owns (nobody there could approve it), at
// most maxAgentSandboxes per conversation. A create is numbered per
// conversation (sandbox_creates) and its clientId is
// agent:<root>:name:<n>: a call a restart cut off leaves its number pending,
// and the next call with the same name reuses it — the manager answers with
// the sandbox it already made instead of making another.
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxAgentSandboxes bounds the sandboxes the agent creates in one
// conversation.
const maxAgentSandboxes = 4

const sandboxCreateSchemaSQL = `
CREATE TABLE IF NOT EXISTS sandbox_creates (
	root_id    INTEGER NOT NULL,
	n          INTEGER NOT NULL,
	name       TEXT NOT NULL,
	call_id    TEXT NOT NULL DEFAULT '',
	ref        TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT 'pending',
	created_ms INTEGER NOT NULL,
	PRIMARY KEY (root_id, n)
);
`

func (d *DB) addSandboxCreateSchema() error {
	_, err := d.q.Exec(sandboxCreateSchemaSQL)
	return err
}

// sbxCreateRow is one sandbox_create of a conversation: pending (sent, or
// about to be), made (created and bound) or failed (refused, or given up).
type sbxCreateRow struct {
	Root  int64
	N     int
	Name  string
	Ref   string
	State string
}

// clientID names the create at the manager: the same number, the same
// sandbox.
func (r *sbxCreateRow) clientID() string { return fmt.Sprintf("agent:%d:name:%d", r.Root, r.N) }

// madeSandboxes counts the sandboxes the agent made in a conversation.
func (d *DB) madeSandboxes(root int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM sandbox_creates WHERE root_id=? AND state='made'`, root).Scan(&n)
	return n
}

// createRow is the latest create of name in a conversation in state (nil:
// none).
func (d *DB) createRow(root int64, name, state string) *sbxCreateRow {
	r := &sbxCreateRow{Root: root}
	err := d.q.QueryRow(`SELECT n, name, ref, state FROM sandbox_creates WHERE root_id=? AND name=? AND state=? ORDER BY n DESC LIMIT 1`,
		root, name, state).Scan(&r.N, &r.Name, &r.Ref, &r.State)
	if err != nil {
		return nil
	}
	return r
}

// beginCreate numbers a create: a pending one of the same name (a call a
// restart cut off) keeps its number, anything else takes the next.
func (d *DB) beginCreate(root int64, name, call string) (*sbxCreateRow, error) {
	if r := d.createRow(root, name, "pending"); r != nil {
		_, _ = d.q.Exec(`UPDATE sandbox_creates SET call_id=? WHERE root_id=? AND n=?`, call, root, r.N)
		return r, nil
	}
	r := &sbxCreateRow{Root: root, Name: name, State: "pending"}
	err := d.q.QueryRow(`INSERT INTO sandbox_creates (root_id, n, name, call_id, created_ms)
		SELECT ?1, COALESCE(MAX(n), 0) + 1, ?2, ?3, ?4 FROM sandbox_creates WHERE root_id=?1 RETURNING n`,
		root, name, call, nowMs()).Scan(&r.N)
	return r, err
}

func (d *DB) endCreate(r *sbxCreateRow, state, ref string) error {
	r.State, r.Ref = state, ref
	_, err := d.q.Exec(`UPDATE sandbox_creates SET state=?, ref=? WHERE root_id=? AND n=?`, state, ref, r.Root, r.N)
	return err
}

// --- the call, planned -----------------------------------------------------------

// sbxCreatePlan is a sandbox_create call resolved against the class and the
// manager: what will be made, and where.
type sbxCreatePlan struct {
	Provider string // the manager
	Title    string // its title (the provider until it is asked)
	Name     string
	Image    string // "" = the manager's default
	Size     string
	Egress   string
	Cwd      string // absolute, or relative to the sandbox's workdir ("" = the workdir)

	egressSet bool // the call named an egress
	cls       agentClass
}

// classManagers are the bound managers a class allows, in binding order.
func classManagers(cls agentClass) []string {
	var out []string
	for _, m := range sandboxManagers() {
		if cls.allowsManager(m.Provider) {
			out = append(out, m.Provider)
		}
	}
	return out
}

// planSandboxCreate reads a call against the class and the bound managers,
// asking none of them: why it can't be, in words the model can act on.
func planSandboxCreate(cfg Config, args map[string]any) (*sbxCreatePlan, error) {
	cls := classOf(cfg)
	if !cls.has(tsSandbox) {
		return nil, fmt.Errorf("sandbox_create is not available: this conversation's class has no sandbox toolset")
	}
	p := &sbxCreatePlan{Name: strings.TrimSpace(str(args["name"])), Image: strings.TrimSpace(str(args["image"])),
		Size: strings.TrimSpace(str(args["size"])), Egress: strings.TrimSpace(str(args["egress"])), cls: cls}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 64 || strings.IndexFunc(p.Name, notPlainRune) >= 0 {
		return nil, fmt.Errorf("sandbox_create needs a name: 1–64 characters on one line, no control characters")
	}
	ms := classManagers(cls)
	if len(ms) == 0 {
		return nil, fmt.Errorf("no sandbox manager this conversation's class (%s) allows is bound to the agent — ask the user to bind one", orStr(cls.Name, cls.ID))
	}
	switch want := strings.TrimSpace(str(args["manager"])); {
	case want == "":
		p.Provider = ms[0]
	case hasStr(ms, want):
		p.Provider = want
	default:
		return nil, fmt.Errorf("manager %q isn't one this conversation can use — it can use %s", want, strings.Join(ms, ", "))
	}
	p.Title = p.Provider
	if p.Egress != "" {
		if !cls.allowsEgress(p.Egress) {
			return nil, fmt.Errorf("this conversation's class (%s) doesn't allow a sandbox with egress %q — it allows %s",
				orStr(cls.Name, cls.ID), p.Egress, strings.Join(cls.SandboxEgress, ", "))
		}
		p.egressSet = true
	} else if p.Egress = "none"; !cls.allowsEgress("none") && len(cls.SandboxEgress) > 0 {
		p.Egress = cls.SandboxEgress[0]
	}
	if cwd := strings.TrimSpace(str(args["cwd"])); cwd != "" {
		if len(cwd) > 4096 || strings.ContainsRune(cwd, 0) || cwd == "~" || strings.HasPrefix(cwd, "~/") {
			return nil, fmt.Errorf("cwd: an absolute path in the sandbox, or one relative to its workdir")
		}
		p.Cwd = cwd
	}
	return p, nil
}

// resolve asks the manager (its hello, cached) and fills in its defaults:
// the image and size it marks default, and the first egress the class allows
// that it offers ("none" first).
func (p *sbxCreatePlan) resolve(ctx context.Context) error {
	m, ok := boundManager(p.Provider)
	if !ok {
		return &sbxError{Provider: p.Provider, Refusal: "unbound", Msg: "this sandbox manager is no longer bound to the agent"}
	}
	h, err := managerHello(ctx, m)
	if err != nil {
		return err
	}
	p.Title = h.title(p.Provider)
	if len(h.Egress) > 0 {
		switch {
		case p.egressSet && !hasStr(h.Egress, p.Egress):
			return fmt.Errorf("%s doesn't offer egress %q — it offers %s", p.Title, p.Egress, strings.Join(h.Egress, ", "))
		case !p.egressSet:
			p.Egress = ""
			for _, e := range append([]string{"none"}, p.cls.SandboxEgress...) {
				if p.cls.allowsEgress(e) && hasStr(h.Egress, e) {
					p.Egress = e
					break
				}
			}
			if p.Egress == "" {
				return fmt.Errorf("%s offers no egress this conversation's class allows (it offers %s; the class allows %s)",
					p.Title, strings.Join(h.Egress, ", "), strings.Join(p.cls.SandboxEgress, ", "))
			}
		}
	}
	var images, sizes []string
	defImage, defSize := "", ""
	for _, im := range h.Images {
		images = append(images, im.ID)
		if im.Default && defImage == "" {
			defImage = im.ID
		}
	}
	for _, sz := range h.Sizes {
		sizes = append(sizes, sz.ID)
		if sz.Default && defSize == "" {
			defSize = sz.ID
		}
	}
	if defImage == "" && len(images) > 0 {
		defImage = images[0]
	}
	if defSize == "" && len(sizes) > 0 {
		defSize = sizes[0]
	}
	switch {
	case p.Image == "":
		p.Image = defImage
	case len(images) > 0 && !hasStr(images, p.Image):
		return fmt.Errorf("%s has no image %q — it has %s", p.Title, p.Image, strings.Join(images, ", "))
	}
	switch {
	case p.Size == "":
		p.Size = defSize
	case len(sizes) > 0 && !hasStr(sizes, p.Size):
		return fmt.Errorf("%s has no size %q — it has %s", p.Title, p.Size, strings.Join(sizes, ", "))
	}
	return nil
}

// words is the plan for the owner's approval card, resolved: what will be
// made. The model's words in it are quoted (the name) or plain ids, so they
// can't pass for the card's own.
func (p *sbxCreatePlan) words() string {
	return fmt.Sprintf("the coding sandbox %q at %s — image %s, size %s, egress %s",
		clipRunes(p.Name, 64), p.Title, askToken(p.Image), askToken(p.Size), askToken(p.Egress))
}

// notPlainRune: a character a name shown to the owner may not carry — a
// control or format character (bidi overrides among them), or a line break.
func notPlainRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

// askToken is a model-supplied id in the ask: as is when it is a plain id,
// else quoted (and clipped).
func askToken(s string) string {
	if s == "" {
		return "default"
	}
	if len(s) <= 64 && strings.IndexFunc(s, func(r rune) bool {
		return !(r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/@+-", r)))
	}) < 0 {
		return s
	}
	return fmt.Sprintf("%q", clipRunes(s, 64))
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// askedIn: the owner's ask (sandboxesGrantAsk) names exactly this plan.
func (p *sbxCreatePlan) askedIn(ask string) bool {
	w := p.words()
	return ask == "create "+w || strings.HasPrefix(ask, "create "+w+"; and ") ||
		strings.Contains(ask, "; and "+w+"; and ") || strings.HasSuffix(ask, "; and "+w)
}

// resolved is a call planned and resolved against its manager (its hello,
// cached) within the hello's timeout; nil when it can't be — the call runs
// and says why instead of parking on a guess.
func resolved(cfg Config, args map[string]any) *sbxCreatePlan {
	p, err := planSandboxCreate(cfg, args)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sbxHelloTimeout)
	defer cancel()
	if p.resolve(ctx) != nil {
		return nil
	}
	return p
}

// --- the grant -------------------------------------------------------------------

// sandboxCreateRefusal says why a conversation may not create a sandbox at
// all ("" = it may ask): such a call is refused, never parked.
func (ag *Agent) sandboxCreateRefusal(run *Run, cfg Config) string {
	switch {
	case run.Depth > 0:
		return "only the conversation itself can create a sandbox, not a subagent — tell your parent what you need"
	case cfg.Channel:
		return "a chat channel's conversation can't create sandboxes (nobody there can approve one) — ask for one to be bound instead"
	case !person(run.Owner):
		return "no person owns this conversation to approve a new sandbox — ask for one to be bound instead"
	case ag.db.madeSandboxes(rootOf(run)) >= maxAgentSandboxes:
		return fmt.Sprintf("this conversation has already created %d sandboxes, the most it may — work in one of them, or ask the user", maxAgentSandboxes)
	}
	return ""
}

// sandboxesGrantNeeded (grantDefs): a sandbox_create call that could run —
// resolved against its manager, so the ask can say what it makes — with no
// live grant. One that would be refused, or that the manager can't resolve
// now, runs and says why.
func sandboxesGrantNeeded(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) bool {
	for _, c := range calls {
		if c.Function.Name != "sandbox_create" || cfg.denied("sandbox_create") {
			continue
		}
		args, _ := callArgs(c, own)
		if _, err := planSandboxCreate(cfg, args); err != nil || ag.sandboxCreateRefusal(run, cfg) != "" ||
			ag.db.liveGrant(rootOf(run), capSandboxes) {
			continue
		}
		if resolved(cfg, args) != nil {
			return true
		}
	}
	return false
}

// sandboxesGrantAsk (grantDefs): exactly what the step's creates will make —
// the manager, image, size and egress, defaults resolved, and that a team
// conversation's is the team's (forConversation). A call its manager can't
// resolve now isn't in it (allowed with the rest, it refuses: its words
// aren't what the owner allowed).
func sandboxesGrantAsk(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) string {
	var parts []string
	for _, c := range calls {
		if c.Function.Name != "sandbox_create" {
			continue
		}
		args, _ := callArgs(c, own)
		if p := resolved(cfg, args); p != nil {
			parts = append(parts, p.words())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	ask := "create " + strings.Join(parts, "; and ")
	if a, err := ag.aclOf(rootOf(run)); err == nil && a.visibility == visTeam {
		ask += " — shared with the team, as this conversation is: anyone on the team may use it"
	}
	return ask
}

// --- the tool --------------------------------------------------------------------

func sandboxCreateSpec(cls agentClass) toolSpec {
	ms := classManagers(cls)
	manager := strProp("which sandbox manager (default " + ms[0] + ")")
	if len(ms) > 1 {
		manager["enum"] = ms
	}
	egress := strProp("its network: none (default where allowed), internet (the public internet), open (as the manager gives it)")
	if len(cls.SandboxEgress) > 0 {
		egress["enum"] = cls.SandboxEgress
	}
	return toolSpec{Type: "function", Function: funcDef{
		Name: "sandbox_create",
		Description: "Create a new coding sandbox (a separate machine with a shell and a filesystem) for this conversation, and bind it here. " +
			"It asks the conversation's owner first — they allow it once or for an hour, or deny it. " +
			"When no sandbox is active it becomes the active one, and bash, read, write, edit and the rest work in it from your next step; " +
			"otherwise it is attached beside the active one (sandbox_copy and subagent_spawn {sandbox} reach it). " +
			fmt.Sprintf("Create one only when the work needs a machine and none is bound; at most %d per conversation.", maxAgentSandboxes),
		Parameters: obj([]string{"name"}, map[string]any{
			"name":    strProp("a short name people will recognise, e.g. api-dev"),
			"manager": manager,
			"image":   strProp("the manager's image id (default: its default)"),
			"size":    strProp("the manager's size id (default: its default)"),
			"egress":  egress,
			"cwd":     strProp("where the tools will work: absolute, or relative to the sandbox's workdir (default: the workdir; created if missing)"),
		}),
	}}
}

// sandboxCreateOffered: a top-level conversation of a class with the sandbox
// toolset, not a chat channel's, with a manager its class allows bound.
func sandboxCreateOffered(cfg Config, depth int) bool {
	return depth == 0 && !cfg.Channel && classOf(cfg).has(tsSandbox) && len(classManagers(classOf(cfg))) > 0
}

// toolSandboxCreate makes the sandbox for the conversation's owner and binds
// it.
func (ag *Agent) toolSandboxCreate(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	if why := ag.sandboxCreateRefusal(run, cfg); why != "" {
		return "", errors.New(why)
	}
	p, err := planSandboxCreate(cfg, args)
	if err != nil {
		return "", err
	}
	root := rootOf(run)
	if err := p.resolve(ctx); err != nil {
		return "", err
	}
	if !ag.db.liveGrant(root, capSandboxes) && !grantedOnce(ctx, capSandboxes) {
		// execTools parks for the grant first; this is the backstop
		return "", errors.New(grantOf(capSandboxes).Forbid)
	}
	if ask, ok := grantAskOf(ctx, capSandboxes); ok && !p.askedIn(ask) {
		// the owner allowed what the ask said; the manager's offer changed since
		return "", fmt.Errorf("what this call would make has changed since the owner allowed it (they allowed: %s; it would now be %s) — call sandbox_create again to ask them",
			strings.TrimPrefix(ask, "create "), p.words())
	}
	stored, err := ag.db.runConfig(root)
	if err != nil {
		return "", err
	}
	cfg.HeldInternal = cfg.HeldInternal || stored.HeldInternal // an earlier call of this turn may have set it
	// the same name made here before and still attached: a repeated call
	if prev := ag.db.createRow(root, p.Name, "made"); prev != nil {
		if b, ok := stored.sandboxBinding(prev.Ref); ok {
			active := stored.Sandbox != nil && stored.Sandbox.Ref == b.Ref
			noteSandbox(ctx, b, active)
			return "Already created here: " + createdText(b, "", active, stored), nil
		}
	}
	if len(stored.Attached) >= maxAttached {
		return "", fmt.Errorf("this conversation already has %d sandboxes attached, the most it may — ask the user to detach one", maxAttached)
	}
	owner := who{kind: whoUser, user: run.Owner, level: "read"}
	conn, err := sbxDial(p.Provider, run.Owner)
	if err != nil {
		return "", err
	}
	row, err := ag.db.beginCreate(root, p.Name, toolCallOf(ctx))
	if err != nil {
		return "", err
	}
	box, row, err := ag.createAt(ctx, conn, p, owner, root, row)
	if err != nil {
		return "", err
	}
	ref := sandboxRef(p.Provider, box.ID)
	// from here the sandbox exists: a binding that can't be leaves nothing behind
	undo := func(err error) (string, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sbxCallTimeout)
		defer cancel()
		if derr := conn.Delete(dctx, box.ID); derr != nil && sbxRefusal(derr) != "not-found" {
			logf("sandbox_create #%d: deleting %s after a failed binding: %v", root, ref, derr)
		}
		_ = ag.db.endCreate(row, "failed", ref)
		invalidateSandboxCatalog()
		return "", err
	}
	cwd, err := ag.createCwd(ctx, conn, box, p.Cwd)
	if err != nil {
		return undo(err)
	}
	b, err := prepareBinding(ctx, owner, cfg, sandboxPick{Ref: ref, Cwd: cwd})
	if err != nil {
		return undo(err)
	}
	active := false
	err = ag.db.Tx(func(t *DB) error {
		if err := storeBinding(t, root, func(c *Config) error {
			var err error
			active, err = addSandbox(c, b)
			stored = *c
			return err
		}); err != nil {
			return err
		}
		if err := t.endCreate(row, "made", ref); err != nil {
			return err
		}
		if ag.eng != nil {
			ag.eng.emitRun(t, root)
		}
		return nil
	})
	if err != nil {
		return undo(err)
	}
	invalidateSandboxCatalog()
	noteSandbox(ctx, b, active)
	return "Created " + createdText(b, box.Workdir, active, stored), nil
}

// createAt sends the create. A number the manager already used for another
// request (a cut-off call made something else under it) is given up for the
// next one; a refusal from the manager fails the number; no answer at all
// leaves it pending for the next call to reuse.
func (ag *Agent) createAt(ctx context.Context, conn *sbxConn, p *sbxCreatePlan, owner who, root int64, row *sbxCreateRow) (*sbxSandbox, *sbxCreateRow, error) {
	for attempt := 0; ; attempt++ {
		req := sbxCreate{Name: p.Name, Image: p.Image, Size: p.Size, Egress: p.Egress, ClientID: row.clientID()}
		forConversation(&req, owner, root)
		box, err := conn.Create(ctx, req)
		if err == nil {
			return box, row, nil
		}
		var se *sbxError
		answered := errors.As(err, &se) && se.Status != 0
		if answered {
			_ = ag.db.endCreate(row, "failed", "")
		}
		if !answered || se.Refusal != "exists" || attempt > 0 {
			return nil, row, err
		}
		if row, err = ag.db.beginCreate(root, p.Name, toolCallOf(ctx)); err != nil {
			return nil, row, err
		}
	}
}

// createCwd is where the tools will work in a new sandbox: "" (its workdir),
// or the asked directory, made when it is missing — always: a sandbox the
// manager answered for before it runs is waited for, and a stopped one starts
// on the file operation (the contract).
func (ag *Agent) createCwd(ctx context.Context, conn *sbxConn, box *sbxSandbox, cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	if !strings.HasPrefix(cwd, "/") {
		cwd = path.Join(orStr(box.Workdir, "/"), cwd)
	}
	c, ok := cleanCwd(cwd)
	if !ok {
		return "", fmt.Errorf("cwd: an absolute path in the sandbox, or one relative to its workdir")
	}
	if err := awaitStarted(ctx, conn, box); err != nil {
		return "", err
	}
	st, err := conn.Stat(ctx, box.ID, c)
	switch {
	case sbxRefusal(err) == "not-found":
		if err := conn.Mkdir(ctx, box.ID, c, true); err != nil {
			return "", fmt.Errorf("making the working directory %s: %w", c, err)
		}
	case err != nil:
		return "", err
	case st.Type != "dir":
		return "", fmt.Errorf("cwd: %s isn't a directory in the new sandbox", c)
	}
	return c, nil
}

// createStartWait bounds waiting for a new sandbox that is still being made
// or started.
var createStartWait = 2 * time.Minute

// awaitStarted waits while a new sandbox is creating or starting (asked
// again with a growing pause, within createStartWait).
func awaitStarted(ctx context.Context, conn *sbxConn, box *sbxSandbox) error {
	until := time.Now().Add(createStartWait)
	pause := 100 * time.Millisecond
	for state := box.State; state == "creating" || state == "starting"; {
		if !time.Now().Before(until) {
			return fmt.Errorf("the new sandbox is still %s after %s — its working directory can't be made yet", state, fmtDur(createStartWait))
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(pause):
		}
		pause = min(2*pause, 2*time.Second)
		b, err := conn.Get(ctx, box.ID)
		if err != nil {
			return err
		}
		state = b.State
		if state == "error" || state == "deleting" {
			return fmt.Errorf("the new sandbox is %s (%s)", state, orStr(b.StateDetail, "its manager says no more"))
		}
	}
	return nil
}

// addSandbox binds b into cfg: the active sandbox when none is active (or it
// already was), else attached beside it. Says whether it is the active one.
func addSandbox(cfg *Config, b SandboxBinding) (bool, error) {
	if b.held {
		cfg.HeldInternal = true
	}
	if cfg.Sandbox == nil || cfg.Sandbox.Ref == b.Ref {
		return true, attachSandbox(cfg, b)
	}
	for i := range cfg.Attached {
		if cfg.Attached[i].Ref == b.Ref {
			cfg.Attached[i] = b
			return false, nil
		}
	}
	if len(cfg.Attached) >= maxAttached {
		return false, errBadRequest(fmt.Sprintf("a conversation attaches at most %d sandboxes — detach one first", maxAttached))
	}
	cfg.Attached = append(cfg.Attached, b)
	return false, nil
}

// createdText is sandbox_create's result.
func createdText(b SandboxBinding, workdir string, active bool, cfg Config) string {
	var s strings.Builder
	fmt.Fprintf(&s, "the sandbox %q (ref %s) at %s — image %s; %s.\n", b.Name, b.Ref, orStr(b.Manager, "its manager"),
		orStr(b.Image, "default"), egressWords(b.Egress)+" (egress "+orStr(b.Egress, "unknown")+")")
	if workdir != "" && workdir != b.Cwd {
		fmt.Fprintf(&s, "Its workdir is %s; ", workdir)
	}
	fmt.Fprintf(&s, "the tools work in %s.\n", orStr(b.Cwd, orStr(workdir, "its workdir")))
	if active {
		s.WriteString("It is now this conversation's active sandbox: bash, read, write, edit, ls, glob and grep work in it from your next step.")
	} else {
		name := ""
		if cfg.Sandbox != nil {
			name = orStr(cfg.Sandbox.Name, cfg.Sandbox.Ref)
		}
		fmt.Fprintf(&s, "It is attached; the active sandbox is still %q. sandbox_copy moves files to it, and subagent_spawn {\"sandbox\": %q} puts a subagent in it.", name, b.Ref)
	}
	return s.String()
}
