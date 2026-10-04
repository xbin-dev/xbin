// sandbox_routes.go — the sandboxes routes (API.md §Coding sandboxes): the
// catalog as the caller may see it, creating sandboxes at a manager (for a
// conversation, bound to it), and changing, stopping and deleting them.
// A route's {ref} is a sandbox reference, sent as is or percent-encoded.
package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// sandboxRoutes are registered with the route table's (routes.go).
func sandboxRoutes() []routeDef {
	return []routeDef{
		{"GET /sandboxes", needAny, handleSandboxes},
		{"POST /sandboxes", needStart, handleNewSandbox},
		{"GET /sandboxes/{ref...}", needAny, handleSandbox},
		{"PATCH /sandboxes/{ref...}", needAny, handlePatchSandbox},
		{"DELETE /sandboxes/{ref...}", needAny, handleDeleteSandbox},
		// POST /sandboxes/{ref}/{start|stop|archive|thaw}
		{"POST /sandboxes/{ref...}", needAny, handleSandboxAction},
	}
}

// handleSandboxes lists the sandboxes the caller may see across every bound
// manager, and the managers (what they offer, or why they can't be used).
//
//	GET /sandboxes[?fresh=1]
//	→ {sandboxes: [{ref, provider, manager, …the contract's sandbox…,
//	    mine, canUse, canManage, canEdit, boundTo?, homed?, why?}], managers: [{provider,
//	    title, ok, error?, refusal?, caps, egress, images, sizes, limits}]}
func handleSandboxes(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if r.URL.Query().Get("fresh") == "1" {
		invalidateSandboxCatalog()
	}
	cat := sandboxCatalog(r.Context())
	bound := boundConversations(c, "")
	out := []map[string]any{}
	for _, e := range cat.Sandboxes {
		a := sandboxAccess(c, e.Box)
		if !a.seen() && len(bound[e.Ref]) == 0 {
			continue
		}
		out = append(out, sandboxItem(e, a, bound[e.Ref]))
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"sandboxes": out, "managers": cat.Managers})
}

// sandboxItem is one sandbox as the routes show it: the manager's resource
// with the agent's reference, the caller's access and the conversations the
// caller sees it bound to.
func sandboxItem(e sbxCatalogEntry, a sbxAccess, boundTo []int64) map[string]any {
	v := e.Box.view()
	v["ref"], v["provider"], v["manager"] = e.Ref, e.Provider, e.Manager
	v["mine"], v["canUse"], v["canManage"], v["canEdit"] = a.Mine, a.Use, a.Manage, a.Edit
	if len(boundTo) > 0 {
		v["boundTo"] = boundTo
	}
	if userMode() {
		// a person's partition: whether a conversation here may work in it,
		// and why not (sandbox_partition.go) — its terminal opens either way
		v["homed"] = homedHere(e.Box)
		if why := partitionBoxRefusal(e.Box); why != "" {
			v["why"] = why
		}
	}
	return v
}

// boundConversations maps each sandbox ref (only ref, if set) to the
// conversations the caller may see that have it bound or attached.
func boundConversations(c who, ref string) map[string][]int64 {
	out := map[string][]int64{}
	where, args := aclWhere(c)
	like := `(instr(r.config, '"sandbox":') > 0 OR instr(r.config, '"attached":') > 0)`
	if ref != "" {
		key, _ := json.Marshal(ref)
		like = `instr(r.config, ?) > 0`
		args = append([]any{`"ref":` + string(key)}, args...)
	}
	rows, err := agent.db.q.Query(`SELECT r.id, r.config FROM runs r WHERE r.parent_id=0 AND r.origin<>'`+heldOrigin+`' AND `+
		like+` AND `+where+` ORDER BY r.id DESC LIMIT 2000`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		cfg := parseConfig(raw)
		seen := map[string]bool{}
		for _, b := range append(cfg.Attached, derefBinding(cfg.Sandbox)...) {
			if b.Ref != "" && !seen[b.Ref] && (ref == "" || b.Ref == ref) {
				seen[b.Ref] = true
				out[b.Ref] = append(out[b.Ref], id)
			}
		}
	}
	return out
}

func derefBinding(b *SandboxBinding) []SandboxBinding {
	if b == nil {
		return nil
	}
	return []SandboxBinding{*b}
}

// --- one sandbox ----------------------------------------------------------------

// routeSandbox is the route's sandbox as the caller finds it: fresh from its
// manager (asked as the caller), with their access and where they see it
// bound. A sandbox the caller may neither see nor find bound to a
// conversation of theirs is not-found.
type routeSandbox struct {
	conn    *sbxConn
	id      string
	entry   sbxCatalogEntry
	access  sbxAccess
	boundTo []int64
}

func loadRouteSandbox(r *http.Request, ref string) (*routeSandbox, error) {
	c := callerOf(r)
	conn, id, err := sbxDialRef(ref, sbxUserOf(c))
	if err != nil {
		return nil, err
	}
	hello, err := managerHello(r.Context(), conn.M)
	if err != nil {
		return nil, err
	}
	box, err := conn.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}
	rs := &routeSandbox{conn: conn, id: id, access: sandboxAccess(c, box), boundTo: boundConversations(c, ref)[ref],
		entry: sbxCatalogEntry{Ref: ref, Provider: conn.M.Provider, Manager: hello.title(conn.M.Provider), Box: box}}
	if !rs.access.seen() && len(rs.boundTo) == 0 {
		return nil, refuse(404, "no such sandbox")
	}
	return rs, nil
}

func (rs *routeSandbox) item() map[string]any { return sandboxItem(rs.entry, rs.access, rs.boundTo) }

// handleSandbox is GET /sandboxes/{ref}: one sandbox, fresh — or, for a
// path ending in /terminal (a sandbox id never holds "/"), a terminal in it
// relayed to the caller (terminal_relay.go).
func handleSandbox(w http.ResponseWriter, r *http.Request) {
	if ref, ok := strings.CutSuffix(r.PathValue("ref"), "/terminal"); ok {
		handleSandboxTerminal(w, r, ref)
		return
	}
	rs, err := loadRouteSandbox(r, r.PathValue("ref"))
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	xbin.WriteJSON(w, http.StatusOK, rs.item())
}

// handlePatchSandbox changes a sandbox at its manager — its owner's call.
// New labels keep sbxInternalLabel when the sandbox has it: they go with
// the version they were merged against (unless the caller sent one), so a
// mark set meanwhile (markInternal) fails the PATCH rather than being
// overwritten — read again and merged, once.
//
//	PATCH /sandboxes/{ref} {name?, visibility?, members?, shares?, labels?,
//	egress?, size?, autoStopMin?, version?}
func handlePatchSandbox(w http.ResponseWriter, r *http.Request) {
	var p sbxPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		xbin.WriteError(w, 400, "bad body")
		return
	}
	if p.Visibility != nil && *p.Visibility != visPrivate && *p.Visibility != visTeam {
		xbin.WriteError(w, 400, "visibility is private or team")
		return
	}
	if p.Members != nil {
		for _, m := range *p.Members {
			if !userIDRe.MatchString(m) {
				xbin.WriteError(w, 400, "members: user ids (login names)")
				return
			}
		}
	}
	rs, err := loadRouteSandbox(r, r.PathValue("ref"))
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	if !rs.access.Edit {
		xbin.WriteError(w, 403, "only the sandbox's owner can change it")
		return
	}
	box := rs.entry.Box
	if p.Visibility != nil || p.Members != nil || len(p.Shares) > 0 {
		nb := *box // as the PATCH leaves it
		if p.Visibility != nil {
			nb.Visibility = *p.Visibility
		}
		if p.Members != nil {
			nb.Members = *p.Members
		}
		if len(p.Shares) > 0 {
			nb.Shares = p.Shares
		}
		// before the share is live: no saved sign-in left there (D179, the review's L8)
		if sandboxShared(&nb) {
			if err := readyForShare(r.Context(), rs.conn, rs.id, sandboxRef(rs.conn.M.Provider, rs.id), sbxLabel(box)+" is shared now"); err != nil {
				xbin.WriteError(w, http.StatusBadGateway, sbxLabel(box)+" isn't shared: "+err.Error()+" — try again")
				return
			}
			if !scmScrubForShare(w, r.Context(), sandboxRef(rs.conn.M.Provider, rs.id), sbxLabel(box)) { // nor a project's scm credential (scm_scrub.go)
				return
			}
		}
	}
	for attempt := 0; ; attempt++ {
		q := p
		if p.Labels != nil {
			labels := keepMark(box, *p.Labels)
			q.Labels = &labels
			if v := box.Version; p.Version == nil && v > 0 {
				q.Version = &v
			}
		}
		nb, err := rs.conn.Patch(r.Context(), rs.id, q)
		if err == nil {
			box = nb
			break
		}
		if p.Labels == nil || p.Version != nil || attempt > 0 || sbxRefusal(err) != "precondition" {
			writeSbxErr(w, err)
			return
		}
		if box, err = rs.conn.Get(r.Context(), rs.id); err != nil {
			writeSbxErr(w, err)
			return
		}
		if !sandboxAccess(callerOf(r), box).Edit {
			xbin.WriteError(w, 403, "only the sandbox's owner can change it")
			return
		}
	}
	invalidateSandboxCatalog()
	if sandboxShared(box) { // shared now: no saved sign-in stays in a coding agent there (D179, harness_creds.go)
		stopCredsIn(sandboxRef(rs.conn.M.Provider, rs.id), sbxLabel(box)+" is shared now")
		_ = scmScrubSandbox(r.Context(), sandboxRef(rs.conn.M.Provider, rs.id), scrubShare) // shared in the moment between (scm_scrub.go)
	}
	rs.entry.Box, rs.access = box, sandboxAccess(callerOf(r), box)
	xbin.WriteJSON(w, http.StatusOK, rs.item())
}

// keepMark is labels, with box's sbxInternalLabel kept: what it has held
// stays marked.
func keepMark(box *sbxSandbox, labels map[string]string) map[string]string {
	mark := box.Labels[sbxInternalLabel]
	if mark == "" || labels[sbxInternalLabel] != "" {
		return labels
	}
	out := map[string]string{sbxInternalLabel: mark}
	for k, v := range labels {
		if k != sbxInternalLabel {
			out[k] = v
		}
	}
	return out
}

// handleDeleteSandbox deletes a sandbox at its manager — its owner's or a
// tile manager's call — and detaches it from every conversation.
func handleDeleteSandbox(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	rs, err := loadRouteSandbox(r, ref)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	if !rs.access.Manage {
		xbin.WriteError(w, 403, "only the sandbox's owner or the agent's managers can delete it")
		return
	}
	if err := rs.conn.Delete(r.Context(), rs.id); err != nil && sbxRefusal(err) != "not-found" {
		writeSbxErr(w, err)
		return
	}
	invalidateSandboxCatalog()
	projectSandboxGone(ref) // what a project handed out for it is revoked (scm_scrub.go)
	n := detachEverywhere(ref)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "detached": n})
}

// detachEverywhere takes a (deleted) sandbox off every conversation that has
// it; how many there were. Subagents' copies are left to fail their check.
func detachEverywhere(ref string) int {
	key, _ := json.Marshal(ref)
	roots := scanIDs(agent.db.q.Query(`SELECT id FROM runs WHERE parent_id=0 AND instr(config, ?) > 0`, `"ref":`+string(key)))
	n := 0
	for _, root := range roots {
		changed := false
		err := agent.db.Tx(func(t *DB) error {
			changed = false
			err := storeBinding(t, root, func(cfg *Config) error {
				changed = detachSandbox(cfg, ref)
				return nil
			})
			if err == nil && changed && agent.eng != nil {
				agent.eng.emitRun(t, root)
			}
			return err
		})
		if err == nil && changed {
			n++
		}
	}
	return n
}

// handleSandboxAction is POST /sandboxes/{ref}/{start|stop|archive|thaw}
// [?wait=<s>] [{start?} on thaw]. Starting, thawing and stopping are for who
// may use it; archiving for who may manage it. With ?conversation=<id>, a
// participant of a conversation acts on a sandbox bound there, under the
// binder's right (sandboxUse).
func handleSandboxAction(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("ref")
	i := strings.LastIndexByte(rest, '/')
	if i < 0 {
		xbin.WriteError(w, 404, "POST /sandboxes/{ref}/{start|stop|archive|thaw}")
		return
	}
	ref, action := rest[:i], rest[i+1:]
	switch action {
	case "start", "stop", "archive", "thaw":
	default:
		xbin.WriteError(w, 404, "no action "+action+" (start, stop, archive, thaw)")
		return
	}
	var body struct {
		Start bool `json:"start"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait = max(0, min(wait, 120))
	conn, id, err := actionConn(r, ref, action)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	if action == "stop" || action == "archive" { // no project's scm credential left in it (scm_scrub.go)
		_ = scmScrubSandbox(r.Context(), ref, action)
	}
	box, err := conn.Lifecycle(r.Context(), id, action, wait, body.Start)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	invalidateSandboxCatalog()
	c := callerOf(r)
	hello, _ := managerHello(r.Context(), conn.M)
	xbin.WriteJSON(w, http.StatusOK, sandboxItem(sbxCatalogEntry{Ref: ref, Provider: conn.M.Provider,
		Manager: hello.title(conn.M.Provider), Box: box}, sandboxAccess(c, box), boundConversations(c, ref)[ref]))
}

// actionConn finds who may run a lifecycle action on ref, and as whom.
func actionConn(r *http.Request, ref, action string) (*sbxConn, string, error) {
	if conv := r.URL.Query().Get("conversation"); conv != "" && action != "archive" {
		id, _ := strconv.ParseInt(conv, 10, 64)
		run, lv, err := agent.runAccess(callerOf(r), id)
		if err != nil || lv == lvNone {
			return nil, "", refuse(404, "no such run")
		}
		if lv < lvParticipant {
			return nil, "", refuse(403, "only someone who may talk in the conversation can do that")
		}
		root := rootOf(run)
		cfg, err := agent.db.runConfig(root)
		if err != nil {
			return nil, "", refuse(404, "no such run")
		}
		use, err := agent.sandboxUse(r.Context(), root, cfg, ref)
		if err != nil {
			return nil, "", err
		}
		return use.Conn, use.ID, nil
	}
	rs, err := loadRouteSandbox(r, ref)
	if err != nil {
		return nil, "", err
	}
	if !(rs.access.Use || rs.access.Manage) || (action == "archive" && !rs.access.Manage) {
		return nil, "", refuse(403, "you may not %s this sandbox", action)
	}
	return rs.conn, rs.id, nil
}

// --- creating ---------------------------------------------------------------------

// newSandboxReq is POST /sandboxes.
type newSandboxReq struct {
	Provider   string   `json:"provider"` // which manager ("" = the only one bound)
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Size       string   `json:"size"`
	Egress     string   `json:"egress"`
	Visibility string   `json:"visibility"`
	Members    []string `json:"members"`
	// Conversation creates it for a conversation (the caller takes part in
	// it): a team conversation's sandbox is a team one, its participants are
	// members, and it is bound there (unless bind is false) at cwd.
	Conversation int64  `json:"conversation"`
	Bind         *bool  `json:"bind"`
	Cwd          string `json:"cwd"`
	ClientID     string `json:"clientId"`
	Start        *bool  `json:"start"`
}

// handleNewSandbox creates a sandbox at a manager, owned by the caller (the
// person in Sbx-User), and binds it to a conversation when it is made for
// one. → 201 {…the sandbox as GET /sandboxes/{ref} shows it…, binding?}
func handleNewSandbox(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var q newSandboxReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		xbin.WriteError(w, 400, "bad body")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	switch {
	case q.Name == "" || len(q.Name) > 64:
		xbin.WriteError(w, 400, "name: 1–64 characters")
		return
	case q.Visibility != "" && q.Visibility != visPrivate && q.Visibility != visTeam:
		xbin.WriteError(w, 400, "visibility is private or team")
		return
	case len(q.ClientID) > 64:
		xbin.WriteError(w, 400, "clientId: up to 64 characters")
		return
	}
	for _, m := range q.Members {
		if !userIDRe.MatchString(m) {
			xbin.WriteError(w, 400, "members: user ids (login names)")
			return
		}
	}
	if q.Provider == "" {
		if ms := sandboxManagers(); len(ms) == 1 {
			q.Provider = ms[0].Provider
		} else {
			xbin.WriteError(w, 400, "provider: which manager (GET /sandboxes lists them)")
			return
		}
	}
	cwd, ok := cleanCwd(q.Cwd)
	if !ok {
		xbin.WriteError(w, 400, "cwd: an absolute path in the sandbox")
		return
	}
	req := sbxCreate{Name: q.Name, Image: q.Image, Size: q.Size, Egress: q.Egress, Visibility: q.Visibility,
		Members: q.Members, Start: q.Start}
	if q.ClientID != "" {
		req.ClientID = c.tag() + ":" + q.ClientID // one person's retry, never another's sandbox
	}
	var root int64
	var cfg Config
	bind := false
	if q.Conversation != 0 {
		run, lv, err := agent.runAccess(c, q.Conversation)
		if err != nil || lv == lvNone {
			xbin.WriteError(w, 404, "no such run")
			return
		}
		if lv < lvParticipant {
			xbin.WriteError(w, 403, "only someone who may talk in the conversation can make it a sandbox")
			return
		}
		root = rootOf(run)
		if cfg, err = agent.db.runConfig(root); err != nil {
			xbin.WriteError(w, 404, "no such run")
			return
		}
		bind = q.Bind == nil || *q.Bind
		if bind {
			if why := sandboxClassAllows(cfg, q.Provider, q.Egress); why != "" {
				xbin.WriteError(w, 403, why)
				return
			}
		}
		forConversation(&req, c, root)
	}
	conn, err := sbxDial(q.Provider, sbxUserOf(c))
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	hello, err := managerHello(r.Context(), conn.M)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	box, err := conn.Create(r.Context(), req)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	invalidateSandboxCatalog()
	ref := sandboxRef(conn.M.Provider, box.ID)
	out := sandboxItem(sbxCatalogEntry{Ref: ref, Provider: conn.M.Provider, Manager: hello.title(conn.M.Provider), Box: box},
		sandboxAccess(c, box), nil)
	if bind {
		b, err := prepareBinding(r.Context(), c, cfg, sandboxPick{Ref: ref, Cwd: cwd})
		if err == nil {
			err = agent.db.Tx(func(t *DB) error {
				if err := storeBinding(t, root, func(cfg *Config) error { return attachSandbox(cfg, b) }); err != nil {
					return err
				}
				if agent.eng != nil {
					agent.eng.emitRun(t, root)
				}
				return nil
			})
		}
		if err != nil {
			_ = conn.Delete(r.Context(), box.ID) // made for a binding that can't be: don't leave it behind
			invalidateSandboxCatalog()
			writeSbxErr(w, err)
			return
		}
		out["binding"], out["boundTo"] = b, []int64{root}
	}
	xbin.WriteJSON(w, http.StatusCreated, out)
}

// forConversation shapes a sandbox made for a conversation (the owner's
// rule: team-shared conversations have shared sandboxes): a team
// conversation's is team; its owner and participants are members; it is
// labeled with the conversation.
func forConversation(req *sbxCreate, c who, root int64) {
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	req.Labels["xbin.agent/conversation"] = strconv.FormatInt(root, 10)
	req.Labels = withHomeLabel(req.Labels) // a partitioned agent: whose db the conversation is in (sandbox_partition.go)
	a, err := agent.aclOf(root)
	if err != nil {
		return
	}
	if a.visibility == visTeam {
		req.Visibility = visTeam
	}
	members := map[string]bool{}
	for _, m := range req.Members {
		members[m] = true
	}
	add := func(u string) {
		if u != "" && u != sbxUserOf(c) && !strings.HasPrefix(u, "el:") && !members[u] {
			members[u] = true
			req.Members = append(req.Members, u)
		}
	}
	add(a.owner)
	var parts []string
	for u, role := range a.members {
		if role == roleParticipant {
			parts = append(parts, u)
		}
	}
	sort.Strings(parts)
	for _, u := range parts {
		add(u)
	}
}
