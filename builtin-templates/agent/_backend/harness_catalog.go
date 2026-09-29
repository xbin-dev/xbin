// harness_catalog.go — the coding agents (harnesses) the caller could start,
// and why not (API.md §Coding agents): the sdk's catalog (sdk/acp:
// claude, codex, gemini, opencode) joined with what each bound sandbox
// manager says its images have (hello's images[].harnesses), the classes
// the caller may use and the egress the managers offer; what a probe or a
// session last learned per sandbox (harness_seen); and each person's Auto /
// Always approve setting (harness_prefs, §4.3.12).
//
// An advertisement is not a promise — installs are best-effort — so
// `?probe=<ref>` also asks a running sandbox which of the harnesses' commands
// it has (`command -v`, one short run through the contract's /run), cached
// per sandbox for 10 minutes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// harnessIDRe is a harness id as the contract's hello carries it.
var harnessIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// fakeHarness is the scripted test agent's id (hack/fakesandbox advertises
// it): known like a catalog id, offered only where a manager advertises it.
const fakeHarness = "fake"

// validHarness: an advertised entry the agent can use — a catalog id or the
// fake, or any other id with a command.
func validHarness(e sbxHarness) bool {
	if !harnessIDRe.MatchString(e.ID) {
		return false
	}
	if _, ok := acp.Lookup(e.ID); ok {
		return true
	}
	if len(e.Argv) == 0 {
		return false
	}
	for _, a := range e.Argv {
		if a == "" {
			return false
		}
	}
	return true
}

// advertises: the manager's hello says which harnesses its images have (on
// at least one of them). One that says nothing predates the field — its
// images are unknown, not none.
func (h *sbxHello) advertises() bool {
	if h == nil {
		return false
	}
	for _, im := range h.Images {
		if len(im.Harnesses) > 0 {
			return true
		}
	}
	return false
}

// imageHarnessEntries are the usable harnesses the manager says image id
// has (nil: none, or it says nothing).
func (h *sbxHello) imageHarnessEntries(id string) []sbxHarness {
	if h == nil {
		return nil
	}
	for _, im := range h.Images {
		if im.ID != id {
			continue
		}
		var out []sbxHarness
		seen := map[string]bool{}
		for _, e := range im.Harnesses {
			if validHarness(e) && !seen[e.ID] && len(out) < 32 {
				seen[e.ID] = true
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}

// imageHarnesses are the ids of imageHarnessEntries (SandboxBinding.Harnesses).
func (h *sbxHello) imageHarnesses(id string) []string {
	var out []string
	for _, e := range h.imageHarnessEntries(id) {
		out = append(out, e.ID)
	}
	return out
}

// harnessProvider is what the agent knows of harness id: the sdk catalog's
// entry, the fake's, or one made of the manager's advertisement (e; nil:
// none seen).
func harnessProvider(id string, e *sbxHarness) acp.Provider {
	if p, ok := acp.Lookup(id); ok {
		return p
	}
	var argv []string
	title, login := "", ""
	if e != nil {
		argv, title, login = e.Argv, e.Title, e.Login
	}
	p := acp.Provider{ID: id, Name: id, Driver: "acp", Argv: argv}
	if id == fakeHarness {
		p = acp.Fake(argv)
	}
	if title != "" {
		p.Name = title
	}
	if login != "" {
		p.LoginCmd = login
	}
	if len(p.Bins) == 0 && len(argv) > 0 {
		p.Bins = []string{argv[0]}
	}
	return p
}

// harnessBins are the commands a probe looks for: the catalog's, else the
// advertised command.
func harnessBins(id string, e *sbxHarness) []string {
	return harnessProvider(id, e).Bins
}

// --- the catalog ----------------------------------------------------------------------

// hcImage is one (manager, image) that has — or, from a manager that
// predates the field, may have — a harness.
type hcImage struct {
	Provider   string   `json:"provider"`
	Manager    string   `json:"manager"`
	Image      string   `json:"image"`
	Advertised bool     `json:"advertised"`
	Egress     []string `json:"egress"` // the manager's egress other than none
}

// hcEntry is one harness in GET /harnesses.
type hcEntry struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Available   bool                   `json:"available"`
	Reason      string                 `json:"reason,omitempty"` // no-image | manager-error | no-class | no-egress
	Why         string                 `json:"why,omitempty"`
	Classes     []string               `json:"classes"`
	Images      []hcImage              `json:"images"`
	Modes       []acp.Mode             `json:"modes"`
	DefaultMode string                 `json:"defaultMode"`
	AutoMode    string                 `json:"autoMode"`
	ApproveMode string                 `json:"approveMode"`
	PlanMode    string                 `json:"planMode"`
	Setting     string                 `json:"setting"` // the caller's own: auto | approve
	Login       hcLogin                `json:"login"`
	Options     json.RawMessage        `json:"options,omitempty"`
	Sandboxes   map[string]harnessSeen `json:"sandboxes"`
}

type hcLogin struct {
	Command string `json:"command"`
}

// hcManager is one bound manager as the catalog reads it.
type hcManager struct {
	m     sbxManager
	hello *sbxHello
	err   error
}

// harnessManagers asks every bound manager's hello (cached; in parallel).
func harnessManagers(ctx context.Context) []hcManager {
	mgrs := sandboxManagers()
	out := make([]hcManager, len(mgrs))
	var wg sync.WaitGroup
	for i, m := range mgrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := managerHello(ctx, m)
			out[i] = hcManager{m: m, hello: h, err: err}
		}()
	}
	wg.Wait()
	return out
}

// knownHarness: an id the agent knows — the sdk catalog's, the fake, or
// one a bound manager advertises (asked only when the others miss).
func knownHarness(ctx context.Context, id string) (acp.Provider, bool) {
	if p, ok := acp.Lookup(id); ok {
		return p, true
	}
	if id == fakeHarness {
		return harnessProvider(id, nil), true
	}
	for _, hm := range harnessManagers(ctx) {
		if hm.hello == nil {
			continue
		}
		for _, im := range hm.hello.Images {
			for _, e := range hm.hello.imageHarnessEntries(im.ID) {
				if e.ID == id {
					return harnessProvider(id, &e), true
				}
			}
		}
	}
	return acp.Provider{}, false
}

// unknownHarnesses checks the harnesses the classes being saved name: each
// must be known (knownHarness) or already named by the class as saved
// before (an agent a manager stopped advertising doesn't hold up an edit).
// "" = fine.
func unknownHarnesses(ctx context.Context, cur *classState, classes []agentClass) string {
	for _, c := range classes {
		was, _ := cur.find(c.ID)
		for _, id := range c.Harnesses.Names {
			if hasStr(was.Harnesses.Names, id) {
				continue
			}
			if _, ok := knownHarness(ctx, id); !ok {
				return fmt.Sprintf("class %s: no coding agent %q (GET /harnesses lists them)", c.ID, id)
			}
		}
	}
	return ""
}

// harnessCatalog builds GET /harnesses for caller c. visible says whether
// the caller may see a sandbox ref (for what was learned about it; nil: no
// one's).
func harnessCatalog(c who, mgrs []hcManager, visible func(ref string) bool) []hcEntry {
	var order []string
	byID := map[string]*hcEntry{}
	adv := map[string]*sbxHarness{} // the first advertisement of each id
	add := func(id string) *hcEntry {
		if e, ok := byID[id]; ok {
			return e
		}
		order = append(order, id)
		e := &hcEntry{ID: id, Classes: []string{}, Images: []hcImage{}, Sandboxes: map[string]harnessSeen{}}
		byID[id] = e
		return e
	}
	for _, p := range acp.Providers() {
		add(p.ID)
	}
	var failed []hcManager
	for _, hm := range mgrs {
		if hm.err != nil || hm.hello == nil {
			failed = append(failed, hm)
			continue
		}
		h := hm.hello
		egress := []string{}
		for _, e := range h.Egress {
			if e != "none" && !hasStr(egress, e) {
				egress = append(egress, e)
			}
		}
		img := func(im sbxImage, advertised bool) hcImage {
			return hcImage{Provider: hm.m.Provider, Manager: h.title(hm.m.Provider), Image: im.ID, Advertised: advertised, Egress: egress}
		}
		if !h.advertises() {
			for _, im := range h.Images {
				for _, p := range acp.Providers() {
					e := add(p.ID)
					e.Images = append(e.Images, img(im, false))
				}
			}
			continue
		}
		for _, im := range h.Images {
			for _, he := range h.imageHarnessEntries(im.ID) {
				e := add(he.ID)
				e.Images = append(e.Images, img(im, true))
				if adv[he.ID] == nil {
					adv[he.ID] = &he
				}
			}
		}
	}
	st := currentClasses()
	def := st.defaultFor(c)
	person := c.kind == whoUser && c.viewedBy == ""
	var modes map[string]string
	if person {
		modes = agent.db.harnessModes(c.user)
	}
	out := make([]hcEntry, 0, len(order))
	for _, id := range order {
		e := byID[id]
		p := harnessProvider(id, adv[id])
		e.Name, e.Modes, e.DefaultMode = p.Name, p.Modes, p.DefaultMode
		e.AutoMode, e.ApproveMode, e.PlanMode = p.AutoMode, p.ApproveMode, p.PlanMode
		if e.Modes == nil {
			e.Modes = []acp.Mode{}
		}
		e.Login.Command = p.LoginCmd
		if e.Login.Command == "" && adv[id] != nil {
			e.Login.Command = adv[id].Login
		}
		e.Setting = hmApprove
		if m := modes[id]; m != "" {
			e.Setting = m
		}
		var classes []agentClass
		for _, cls := range st.list {
			if cls.usableBy(c) && cls.allowsHarness(id) {
				classes = append(classes, cls)
			}
		}
		for _, cls := range classes { // the caller's default first
			if cls.ID == def.ID {
				e.Classes = append(e.Classes, cls.ID)
			}
		}
		for _, cls := range classes {
			if cls.ID != def.ID {
				e.Classes = append(e.Classes, cls.ID)
			}
		}
		e.Reason, e.Why = harnessUnavailable(e, classes, failed)
		e.Available = e.Reason == ""
		e.Options = agent.db.harnessOptions(id)
		if visible != nil {
			for ref, s := range agent.db.seenHarnesses(id) {
				if visible(ref) {
					e.Sandboxes[ref] = s
				}
			}
		}
		out = append(out, *e)
	}
	return out
}

// harnessUnavailable is why a harness can't be started (reason, why for
// people), the first that holds: no image has it (or a manager that didn't
// answer might), no class the caller may use allows it, no manager offering
// it offers an egress other than none that such a class allows.
func harnessUnavailable(e *hcEntry, classes []agentClass, failed []hcManager) (string, string) {
	if len(e.Images) == 0 {
		if len(failed) > 0 {
			return "manager-error", fmt.Sprintf("%s didn't answer: %v", failed[0].m.Provider, failed[0].err)
		}
		return "no-image", "no bound sandbox manager's image has it"
	}
	if len(classes) == 0 {
		return "no-class", "no class you may use allows coding agents"
	}
	for _, im := range e.Images {
		for _, eg := range im.Egress {
			for _, cls := range classes {
				if cls.allowsManager(im.Provider) && cls.allowsEgress(eg) {
					return "", ""
				}
			}
		}
	}
	im := e.Images[0]
	if len(im.Egress) == 0 {
		return "no-egress", fmt.Sprintf("needs internet access — %s offers none (bind its internet class)", im.Manager)
	}
	return "no-egress", fmt.Sprintf("needs internet access — no class you may use allows it from %s (it offers %s)",
		im.Manager, strings.Join(im.Egress, ", "))
}

// --- the probe ------------------------------------------------------------------------

// harnessProbeTTL is how long a sandbox's probe stands.
var harnessProbeTTL = 10 * time.Minute

var harnessProbes = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func forgetHarnessProbes() {
	harnessProbes.Lock()
	harnessProbes.at = map[string]time.Time{}
	harnessProbes.Unlock()
}

// hcProbe is what ?probe=<ref> did.
type hcProbe struct {
	Ref    string `json:"ref"`
	Ran    bool   `json:"ran"`              // the sandbox was asked just now
	Cached bool   `json:"cached,omitempty"` // a probe of the last 10 minutes stands
	Error  string `json:"error,omitempty"`  // why it wasn't asked
}

// probeScript prints each of its arguments that is a command in the
// sandbox.
const probeScript = `for b in "$@"; do command -v "$b" >/dev/null 2>&1 && echo "$b"; done; exit 0`

// probeHarnesses asks a running sandbox the caller may use which of its
// harnesses' commands it has, and records what it learned (harness_seen). A
// stopped sandbox isn't started. mgrs are the managers' hellos.
func probeHarnesses(ctx context.Context, c who, ref string, mgrs []hcManager) hcProbe {
	pr := hcProbe{Ref: ref}
	conn, id, err := sbxDialRef(ref, sbxUserOf(c))
	if err != nil {
		pr.Error = err.Error()
		return pr
	}
	var hello *sbxHello
	for _, hm := range mgrs {
		if hm.m.Provider == conn.M.Provider {
			hello = hm.hello
			if hm.err != nil {
				pr.Error = hm.err.Error()
				return pr
			}
		}
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		if sbxRefusal(err) == "not-found" {
			err = fmt.Errorf("no such sandbox")
		}
		pr.Error = err.Error()
		return pr
	}
	switch a := sandboxAccess(c, box); {
	case !a.seen():
		pr.Error = "no such sandbox"
		return pr
	case !a.Use:
		pr.Error = "you may not use this sandbox (" + box.Name + ")"
		return pr
	case box.State != "running":
		pr.Error = fmt.Sprintf("the sandbox is %s — a probe doesn't start it", orStr(box.State, "not running"))
		return pr
	case !box.hasCap("exec"):
		pr.Error = "this sandbox offers no commands"
		return pr
	}
	harnessProbes.Lock()
	at, ok := harnessProbes.at[ref]
	harnessProbes.Unlock()
	if ok && time.Since(at) < harnessProbeTTL {
		pr.Cached = true
		return pr
	}
	type probed struct {
		id   string
		bins []string
	}
	var list []probed
	if hello.advertises() {
		for _, e := range hello.imageHarnessEntries(box.Image.ID) {
			list = append(list, probed{e.ID, harnessBins(e.ID, &e)})
		}
	} else {
		for _, p := range acp.Providers() {
			list = append(list, probed{p.ID, p.Bins})
		}
	}
	if len(list) == 0 {
		pr.Error = "its image has no coding agents"
		return pr
	}
	argv := []string{"sh", "-c", probeScript, "probe"}
	for _, p := range list {
		for _, b := range p.bins {
			if !hasStr(argv[4:], b) {
				argv = append(argv, b)
			}
		}
	}
	budget := 8 * time.Second
	if dl, ok := ctx.Deadline(); ok && time.Until(dl)-time.Second < budget {
		budget = time.Until(dl) - time.Second
	}
	if budget < time.Second {
		pr.Error = "no time left to ask the sandbox"
		return pr
	}
	res, err := conn.Run(ctx, id, sbxRunReq{Argv: argv, TimeoutMs: int(budget / time.Millisecond), MaxOutput: 4096})
	switch {
	case err != nil:
		pr.Error = err.Error()
		return pr
	case res.TimedOut:
		pr.Error = "the sandbox didn't answer in time"
		return pr
	}
	found := map[string]bool{}
	if res.Stdout != nil {
		for _, l := range strings.Split(res.Stdout.Head+"\n"+res.Stdout.Tail, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				found[l] = true
			}
		}
	}
	for _, p := range list {
		inst := len(p.bins) > 0
		for _, b := range p.bins {
			inst = inst && found[b]
		}
		if err := agent.db.noteHarnessSeen(ref, p.id, &inst, nil); err != nil {
			pr.Error = err.Error()
			return pr
		}
	}
	harnessProbes.Lock()
	harnessProbes.at[ref] = time.Now()
	harnessProbes.Unlock()
	pr.Ran = true
	return pr
}

// --- routes ---------------------------------------------------------------------------

// harnessRoutes are registered with the route table's (routes.go).
func harnessRoutes() []routeDef {
	return []routeDef{
		{"GET /harnesses", needAny, handleHarnesses},
		{"GET /prefs/harness-mode", needAny, handleHarnessModes},
		{"PUT /prefs/harness-mode/{provider}", needAny, handlePutHarnessMode},
	}
}

// handleHarnesses is the catalog: the coding agents the caller could start,
// and why not.
//
//	GET /harnesses[?probe=<ref>] → {harnesses: [hcEntry…], probe?: hcProbe}
func handleHarnesses(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ref := r.URL.Query().Get("probe")
	if ref != "" {
		if _, _, ok := splitSandboxRef(ref); !ok {
			xbin.WriteError(w, 400, "probe: a sandbox reference from GET /sandboxes (<manager>|<id>)")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	loadClasses(agent.db) // fresh: another process may have saved
	mgrs := harnessManagers(ctx)
	out := map[string]any{}
	if ref != "" {
		out["probe"] = probeHarnesses(ctx, c, ref, mgrs)
	}
	var visible func(string) bool
	if agent.db.anyHarnessSeen() {
		cat := sandboxCatalog(ctx)
		refs := map[string]bool{}
		for _, e := range cat.Sandboxes {
			if sandboxAccess(c, e.Box).seen() {
				refs[e.Ref] = true
			}
		}
		if p, ok := out["probe"].(hcProbe); ok && (p.Ran || p.Cached) {
			refs[ref] = true // the caller may use it: just checked
		}
		visible = func(ref string) bool { return refs[ref] }
	}
	out["harnesses"] = harnessCatalog(c, mgrs, visible)
	xbin.WriteJSON(w, http.StatusOK, out)
}

// errNotPerson answers a route that is a person's own.
func errNotPerson(w http.ResponseWriter, c who) bool {
	if c.kind == whoUser && c.viewedBy == "" {
		return false
	}
	xbin.WriteError(w, http.StatusForbidden, "the setting is a person's own")
	return true
}

// handleHarnessModes is the caller's own Auto / Always approve settings.
//
//	GET /prefs/harness-mode → {modes: {"<provider>": "auto"|"approve"}} (set ones only)
func handleHarnessModes(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if errNotPerson(w, c) {
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"modes": agent.db.harnessModes(c.user)})
}

// handlePutHarnessMode sets the caller's own setting for a harness. It is
// applied when a harness conversation or child is created; existing ones
// keep their mode.
//
//	PUT /prefs/harness-mode/{provider} {mode: "auto"|"approve"} → {provider, mode}
func handlePutHarnessMode(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if errNotPerson(w, c) {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Mode != hmAuto && body.Mode != hmApprove) {
		xbin.WriteError(w, 400, `mode is "auto" or "approve"`)
		return
	}
	id := r.PathValue("provider")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, ok := acp.Provider{}, harnessIDRe.MatchString(id)
	if ok {
		p, ok = knownHarness(ctx, id)
	}
	if !ok {
		xbin.WriteError(w, 400, fmt.Sprintf("no coding agent %q", id))
		return
	}
	if body.Mode == hmAuto && p.AutoMode == "" {
		xbin.WriteError(w, 400, p.Name+" has no auto mode — it asks as its own settings say")
		return
	}
	if err := agent.db.setHarnessMode(c.user, id, body.Mode); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]string{"provider": id, "mode": body.Mode})
}
