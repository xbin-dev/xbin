// project_team_member.go — a team project's membership, in its member's
// own partition (API.md §Team projects): POST /memberships makes one from
// the definition at the global instance (read through callGlobal as the
// person; the definition's security part shown first, its hash sent back
// as accept), its sandbox the person's own — cloned from the team's seed
// only when it works as the bot — and its tasks,
// coordinator and credentials a personal project's.
//
// The definition is read again when the membership's page opens, when a
// task is created, every 10 min while it has open tasks (teamLoop) and
// before a change is accepted. Its name, its removed repos and its policy's
// other keys follow at once; its security part — setup scripts,
// instructions, checks, identity, reviews, auto-PR… (teamSecurityKeys) —
// only once the member accepts exactly what they were shown: until
// then def_pending keeps it and the membership runs what it accepted. A
// definition the member can no longer see (removed, or deleted), or only
// as a viewer, archives the membership: credentials scrubbed, the pump and
// the fetches stopped, the tasks left the person's own.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// errTeamGone: the definition is deleted or no longer the member's to see.
var errTeamGone = errors.New("the team project is gone, or you are no longer a member of it")

// teamFetchDef reads definition gpid at the global instance as this
// partition's person. 404 or 403, or something that isn't a team
// definition: errTeamGone.
func teamFetchDef(ctx context.Context, gpid int64) (*ProjectView, error) {
	res, err := callGlobal(ctx, http.MethodGet, fmt.Sprintf("/projects/%d", gpid), nil, "")
	if err != nil {
		return nil, perr(502, "the agent's shared instance didn't answer: %v", err)
	}
	switch {
	case res.Status == http.StatusNotFound || res.Status == http.StatusForbidden:
		return nil, errTeamGone
	case res.Status != http.StatusOK:
		return nil, perr(502, "reading the team project at the agent's shared instance: HTTP %d %s", res.Status, clip(string(res.Body), 200))
	}
	var out struct {
		Project *ProjectView `json:"project"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil || out.Project == nil {
		return nil, perr(502, "the agent's shared instance answered no project")
	}
	d := out.Project
	if d.Kind != projTeam || d.ID != gpid {
		return nil, errTeamGone
	}
	if err := teamCheckDef(d); err != nil {
		return nil, err
	}
	return d, nil
}

// teamCheckDef: a definition this space can take (what the global instance
// itself checks, again: a membership never trusts more than it must).
func teamCheckDef(d *ProjectView) error {
	d.Name = strings.TrimSpace(d.Name)
	switch {
	case d.Name == "" || len([]rune(d.Name)) > 80:
		return perr(502, "the team project's name isn't 1 to 80 characters")
	case len(d.Repos) > 20:
		return perr(502, "the team project has more than 20 repos")
	}
	if why := checkPolicy(orRaw(d.Policy)); why != "" {
		return perr(502, "the team project's policy: %s", why)
	}
	for _, r := range d.Repos {
		if !validRepo(r.Repo) || r.URL == "" || len(r.Setup) > 64<<10 {
			return perr(502, "the team project's repo %q isn't one this space can take", r.Repo)
		}
	}
	return nil
}

// teamPerson: the caller is this partition's person (not view-as) — a
// membership is theirs alone.
func teamPerson(w http.ResponseWriter, r *http.Request) (who, bool) {
	c := callerOf(r)
	switch {
	case !userMode():
		xbin.WriteError(w, http.StatusConflict, "a team project's membership is made in your own space: open the team project there")
		return c, false
	case c.kind != whoUser || c.viewedBy != "" || c.user == "" || c.user != runUser:
		xbin.WriteError(w, http.StatusForbidden, "a membership is its person's own")
		return c, false
	}
	return c, true
}

// membershipOf is the membership {pid} names, the caller's own (404 else).
func membershipOf(w http.ResponseWriter, r *http.Request) (*Project, who, bool) {
	c, ok := teamPerson(w, r)
	if !ok {
		return nil, c, false
	}
	pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	m, err := projAg().db.getProject(pid)
	if err != nil || m.Kind != projMembership || m.Owner != c.user || m.State == projDeleting {
		xbin.WriteError(w, http.StatusNotFound, "no such membership")
		return nil, c, false
	}
	return m, c, true
}

// --- the routes -----------------------------------------------------------------------------------

// handleListMemberships: GET /memberships — this person's memberships, each
// re-read from its definition first (at most every 30 s).
func handleListMemberships(w http.ResponseWriter, r *http.Request) {
	c, ok := teamPerson(w, r)
	if !ok {
		return
	}
	d := projAg().db
	ms, err := d.projectsWhere(`WHERE kind=? AND owner=? AND state<>? ORDER BY updated_ms DESC, id DESC LIMIT 200`, projMembership, c.user, projDeleting)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	// re-read concurrently, all within one deadline: a slow global
	// instance costs the page teamListWait once, then the rows as stored
	ctx, cancel := context.WithTimeout(r.Context(), teamListWait)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, m := range ms {
		if m.State != projActive {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(pid int64) {
			defer func() { <-sem; wg.Done() }()
			if _, err := projAg().teamSync(ctx, pid, false); err != nil && err != errTeamGone {
				logf("project %d: re-reading its team project: %v", pid, err)
			}
		}(m.ID)
	}
	wg.Wait()
	cancel()
	items := []ProjectView{}
	for _, m := range ms {
		if cur, err := d.getProject(m.ID); err == nil {
			items = append(items, d.projectView(cur, lvOwner))
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items})
}

type newMembershipBody struct {
	Team    int64 `json:"team"`
	Sandbox *struct {
		Ref string      `json:"ref"`
		New *sandboxNew `json:"new"`
	} `json:"sandbox"`
	Accept string `json:"accept"`
}

// handleNewMembership: POST /memberships {team, sandbox?, accept} — the
// person's membership of definition team: 201 made, 200 the one they have;
// 409 {error, defHash, definition} until accept is the hash of the
// definition's security part as it is now (the member is shown it first).
func handleNewMembership(w http.ResponseWriter, r *http.Request) {
	c, ok := teamPerson(w, r)
	if !ok {
		return
	}
	var body newMembershipBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Team <= 0 || body.Team >= partitionIDBase {
		xbin.WriteError(w, 400, "team: the team project's id at the agent's shared instance")
		return
	}
	if haltBlocks(w, r, 0) {
		return
	}
	ag := projAg()
	have, _ := ag.db.projectsWhere(`WHERE kind=? AND team_ref=? AND owner=? AND state<>? ORDER BY id LIMIT 1`, projMembership, body.Team, c.user, projDeleting)
	if len(have) > 0 && have[0].State == projActive {
		xbin.WriteJSON(w, 200, map[string]any{"project": ag.db.projectView(have[0], lvOwner)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	def, err := teamFetchDef(ctx, body.Team)
	if err == errTeamGone {
		xbin.WriteError(w, 404, "no such team project (or you can't see it)")
		return
	}
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if lv := levelNamed(def.Level); lv < lvParticipant {
		xbin.WriteError(w, 403, "you are a viewer of this team project: working on it takes a participant")
		return
	}
	sec := teamSecurity(def.Policy, def.Repos)
	hash := teamHash(sec)
	if body.Accept != hash {
		xbin.WriteJSON(w, http.StatusConflict, map[string]any{"refusal": "accept",
			"error":   "review the team project's setup scripts, instructions and identity, then send their hash as accept",
			"defHash": hash, "definition": json.RawMessage(sec), "name": def.Name})
		return
	}
	if len(have) > 0 { // an archived one (they left, and are back): it takes the definition as it is now
		ag.teamRejoin(w, have[0], def)
		return
	}
	api, err := scmFor(def.SCM)
	if err != nil {
		xbin.WriteError(w, 409, fmt.Sprintf("the team project's scm provider (%s) isn't bound to this agent in your space", def.SCM))
		return
	}
	pol := policyOf(def.Policy)
	m := &Project{Name: def.Name, Kind: projMembership, TeamRef: body.Team, Owner: c.tag(), Visibility: visPrivate, TeamRole: roleViewer,
		SCM: api.Provider(), Host: def.Host, Policy: orRaw(def.Policy), State: projActive, DefHash: hash, CreatedBy: c.tag()}
	var newReq *sandboxNew
	switch {
	case body.Sandbox != nil && body.Sandbox.Ref != "":
		if err := checkProjectSandbox(ctx, c, body.Sandbox.Ref); err != nil {
			writeProjErr(w, err)
			return
		}
		m.SandboxRef = body.Sandbox.Ref
	case body.Sandbox != nil && body.Sandbox.New != nil:
		if _, ok := boundManager(body.Sandbox.New.Provider); !ok {
			xbin.WriteError(w, 400, fmt.Sprintf("sandbox.new.provider: %q isn't a sandbox manager bound to this agent", body.Sandbox.New.Provider))
			return
		}
		newReq = body.Sandbox.New
	default:
		prov, _, ok := splitSandboxRef(def.SandboxRef)
		if _, bound := boundManager(prov); !ok || !bound {
			xbin.WriteError(w, 400, "need {sandbox: {ref} or {new: {provider}}}: your workspace for the team project")
			return
		}
		newReq = &sandboxNew{Provider: prov}
	}
	prov := ""
	if newReq != nil {
		prov = newReq.Provider
	} else {
		prov, _, _ = splitSandboxRef(m.SandboxRef)
	}
	probe := &Project{SandboxRef: sandboxRef(prov, "x")} // the class's managers are checked against this one
	if _, err := taskClassFor(c, probe, pol.TaskClass); err != nil {
		writeProjErr(w, err)
		return
	}
	if newReq != nil && pol.As == scmAsBot && pol.MembersAsBot {
		if ref := teamSeedClone(ctx, c, api, def, newReq); ref != "" {
			m.SandboxRef, m.SandboxMade, m.FromSeed, newReq = ref, true, true, nil
		}
	}
	err = ag.db.Tx(func(t *DB) error {
		m.Slug = t.projectSlug(m.Name)
		if err := t.insertProject(m); err != nil {
			return err
		}
		if err := t.shareClash(m.ID, m.SandboxRef); err != nil {
			return err
		}
		for _, dr := range def.Repos {
			if err := teamAddRepo(t, m, dr); err != nil {
				return err
			}
		}
		if newReq != nil {
			b, _ := json.Marshal(newReq)
			if err := t.putSetting(sbxNewKey(m.ID), string(b)); err != nil {
				return err
			}
		}
		if _, err := t.queueJob(m.ID, 0, "", pjSandbox, c.tag(), 0); err != nil {
			return err
		}
		addProjectEvent(t, m.ID, 0, pevNote, map[string]any{"text": "joined the team project", "by": c.tag(), "team": body.Team}, false, "")
		emitProject(t, m.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	teamSyncedNow(m.ID)
	m, _ = ag.db.getProject(m.ID)
	xbin.WriteJSON(w, http.StatusCreated, map[string]any{"project": ag.db.projectView(m, lvOwner)})
}

// levelNamed is a level's word as ProjectView.level says it.
func levelNamed(s string) level {
	switch s {
	case "owner":
		return lvOwner
	case "participant":
		return lvParticipant
	case "viewer":
		return lvViewer
	}
	return lvNone
}

// teamAddRepo writes one of the definition's repos into membership m (a
// bare base of its own, its setup as the definition has it) and queues its
// clone once the workspace is laid out.
func teamAddRepo(t *DB, m *Project, dr ProjectRepo) error {
	if dr.Mode != "" && dr.Mode != repoBare {
		return nil // an adopted clone is its project's own: never a definition's
	}
	r := ProjectRepo{ProjectID: m.ID, Slug: t.repoSlug(m.ID, dr.Repo, dr.Slug), Repo: dr.Repo, URL: dr.URL,
		DefaultBranch: dr.DefaultBranch, Mode: repoBare, Checkout: orStr(dr.Checkout, coWorktree), Setup: dr.Setup, Protected: dr.Protected}
	_, _ = t.q.Exec(`DELETE FROM project_repos WHERE project_id=? AND lower(repo)=lower(?) AND state='removing'`, m.ID, r.Repo)
	if err := t.insertRepo(&r); err != nil {
		return err
	}
	if m.Dir != "" {
		if _, err := t.queueJob(m.ID, 0, r.Slug, pjRepo, m.Owner, 0); err != nil {
			return err
		}
	}
	return nil
}

// teamSeedClone makes the membership's sandbox as a clone of the team's
// seed — only where it works as the bot (the caller checked policy.as bot
// and membersAsBot; the provider must offer this person the bot too), the
// seed has a fork-base snapshot, and the person can see the seed, team
// visible, at the manager that would make the sandbox, which can clone.
// "": start fresh (its own repo jobs).
func teamSeedClone(ctx context.Context, c who, api scmAPI, def *ProjectView, req *sandboxNew) string {
	prov, sid, ok := splitSandboxRef(def.SandboxRef)
	if !ok || prov != req.Provider || def.ForkSnap == "" {
		return ""
	}
	if h, err := api.Hello(ctx); err != nil || !hasStr(h.You.Identities, scmAsBot) {
		return ""
	}
	conn, err := sbxDial(prov, sbxUserOf(c))
	if err != nil {
		return ""
	}
	seed, err := conn.Get(ctx, sid)
	if err != nil || seed.Visibility != visTeam || !seed.hasCap("clone") {
		return ""
	}
	start := true
	box, err := conn.Create(ctx, sbxCreate{Name: orStr(slugOf(def.Name, 40), "team"), Size: req.Size, Egress: req.Egress,
		Visibility: visPrivate, Labels: withHomeLabel(map[string]string{}), Start: &start,
		From: &sbxFrom{Sandbox: sid, Snapshot: def.ForkSnap}, ClientID: fmt.Sprintf("agent:team:%d:seed:%s", def.ID, randomUID())})
	if err != nil {
		logf("a membership of team project %d: cloning its seed (starting fresh instead): %v", def.ID, err)
		return ""
	}
	invalidateSandboxCatalog()
	return sandboxRef(prov, box.ID)
}

// teamRejoin makes an archived membership active again with the definition
// as it is now (its member just accepted exactly that).
func (ag *Agent) teamRejoin(w http.ResponseWriter, m *Project, def *ProjectView) {
	var removed bool
	err := ag.db.Tx(func(t *DB) error {
		cur, err := t.getProject(m.ID)
		if err != nil {
			return err
		}
		if removed, err = teamApplyTx(t, cur, def, true); err != nil {
			return err
		}
		if _, err := t.q.Exec(`UPDATE projects SET state=?, version=version+1, updated_ms=? WHERE id=?`, projActive, nowMs(), m.ID); err != nil {
			return err
		}
		_, _ = t.q.Exec(`UPDATE project_jobs SET created_ms=?, next_ms=0 WHERE project_id=? AND state IN ('queued','waiting')`, nowMs(), m.ID)
		addProjectEvent(t, m.ID, 0, pevNote, map[string]any{"text": "joined the team project again", "by": m.Owner}, false, "")
		emitProject(t, m.ID, "project", 0)
		t.AfterCommit(kickProjectWorker)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if removed {
		go scrubProject(m.ID, "", "repo-removed")
	}
	teamSyncedNow(m.ID)
	ag.pokeTasks(m.ID)
	go projectPump(m.ID)
	cur, _ := ag.db.getProject(m.ID)
	xbin.WriteJSON(w, 200, map[string]any{"project": ag.db.projectView(cur, lvOwner)})
}

// pendingRaw is membership pid's def_pending column ("" none).
func (d *DB) pendingRaw(pid int64) string {
	var s string
	_ = d.q.QueryRow(`SELECT def_pending FROM projects WHERE id=?`, pid).Scan(&s)
	return s
}

// handleMembershipPending: GET /memberships/{pid}/pending — the security
// part as the member accepted it and as the definition has it now.
func handleMembershipPending(w http.ResponseWriter, r *http.Request) {
	m, _, ok := membershipOf(w, r)
	if !ok {
		return
	}
	ag := projAg()
	if m.State == projActive {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		if _, err := ag.teamSync(ctx, m.ID, false); err != nil && err != errTeamGone {
			logf("project %d: re-reading its team project: %v", m.ID, err)
		}
		cancel()
	}
	m, err := ag.db.getProject(m.ID)
	if err != nil {
		xbin.WriteError(w, 404, "no such membership")
		return
	}
	repos, _ := ag.db.projectRepos(m.ID)
	out := map[string]any{"hash": m.DefPending, "accepted": json.RawMessage(teamSecurity(m.Policy, repos)), "pending": nil,
		"state": m.State}
	if raw := ag.db.pendingRaw(m.ID); raw != "" {
		out["pending"] = json.RawMessage(raw)
	}
	xbin.WriteJSON(w, 200, out)
}

// handleMembershipAccept: POST /memberships/{pid}/accept {hash} — the
// pending definition becomes the running one, exactly as shown: the
// definition is read again first, and a hash that isn't its pending one's
// is 409.
func handleMembershipAccept(w http.ResponseWriter, r *http.Request) {
	m, c, ok := membershipOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Hash string `json:"hash"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Hash == "" {
		xbin.WriteError(w, 400, "need {hash}: the pending definition's, as GET /memberships/{pid}/pending showed it")
		return
	}
	if m.State != projActive {
		xbin.WriteError(w, 409, "this membership is "+m.State+": join the team project again (POST /memberships)")
		return
	}
	ag := projAg()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	unlock, err := teamLock(ctx, m.ID) // held to the adopt: no older read lands between
	if err != nil {
		xbin.WriteError(w, http.StatusServiceUnavailable, "the team project is being read again: try again")
		return
	}
	defer unlock()
	def, err := ag.teamSyncLocked(ctx, m.ID)
	switch {
	case err == errTeamGone:
		xbin.WriteError(w, 404, "the team project is gone, or you are no longer a member of it: this membership is archived")
		return
	case err != nil:
		writeProjErr(w, err)
		return
	}
	var removed bool
	err = ag.db.Tx(func(t *DB) error {
		cur, err := t.getProject(m.ID)
		if err != nil {
			return err
		}
		switch {
		case cur.DefPending == "" && body.Hash == cur.DefHash:
			return nil // accepted already
		case cur.DefPending == "" || body.Hash != cur.DefPending:
			return &projErr{code: 409, msg: "the team project's definition changed since you read it: review it again",
				extra: map[string]any{"hash": cur.DefPending}}
		case teamHash(teamSecurity(def.Policy, def.Repos)) != body.Hash:
			return &projErr{code: 409, msg: "the team project's definition changed since you read it: review it again",
				extra: map[string]any{"hash": cur.DefPending}}
		}
		if removed, err = teamApplyTx(t, cur, def, true); err != nil {
			return err
		}
		addProjectEvent(t, m.ID, 0, pevNote, map[string]any{"text": "the team project's changes were accepted", "by": c.tag(), "hash": body.Hash}, false, "")
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if removed {
		go scrubProject(m.ID, "", "repo-removed")
	}
	go projectPump(m.ID)
	cur, _ := ag.db.getProject(m.ID)
	xbin.WriteJSON(w, 200, map[string]any{"project": ag.db.projectView(cur, lvOwner)})
}

// --- following the definition -------------------------------------------------------------

// teamSync re-reads membership pid's definition and applies it
// (teamApplyTx; not force: at most every 30 s, a failed read counting). A
// definition gone, or one its member now only views, archives the
// membership (errTeamGone). An archived membership reads, never follows.
// A membership's re-reads run one at a time (teamLock), each read applied
// before the next starts: an older definition never lands after a newer one.
func (ag *Agent) teamSync(ctx context.Context, pid int64, force bool) (*ProjectView, error) {
	if !force && teamSyncFresh(pid, 30*time.Second) {
		return nil, nil
	}
	unlock, err := teamLock(ctx, pid)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if !force && teamSyncFresh(pid, 30*time.Second) { // read while this one waited
		return nil, nil
	}
	return ag.teamSyncLocked(ctx, pid)
}

// teamSyncLocked is teamSync's read and apply; the caller holds teamLock.
func (ag *Agent) teamSyncLocked(ctx context.Context, pid int64) (*ProjectView, error) {
	m, err := ag.db.getProject(pid)
	if err != nil || m.Kind != projMembership || m.State == projDeleting || !userMode() {
		return nil, nil
	}
	def, err := teamFetchDef(ctx, m.TeamRef)
	status := http.StatusNotFound
	if err == nil && levelNamed(def.Level) < lvParticipant {
		err, status = errTeamGone, http.StatusForbidden // a viewer works on nothing (as POST /memberships refuses one)
	}
	if err == errTeamGone {
		ag.teamLeave(pid, status)
		return nil, err
	}
	teamSyncedNow(pid) // an attempt that failed counts too: a page opens on the stored rows
	if err != nil {
		return nil, err
	}
	if m.State != projActive {
		return def, nil
	}
	var removed bool
	err = ag.db.Tx(func(t *DB) error {
		cur, err := t.getProject(pid)
		if err != nil || cur.State != projActive {
			return err
		}
		removed, err = teamApplyTx(t, cur, def, false)
		return err
	})
	if removed {
		go scrubProject(pid, "", "repo-removed")
	}
	return def, err
}

// teamApplyTx applies definition def to membership m in t: its name, its
// policy's other keys and its removed repos at once (running tasks keep
// their checkouts); its security part only when adopt (the member accepted
// exactly def), else it waits in def_pending — with a note event (wake:
// the coordinator tells the person; it can't accept) when it changed.
// Answers whether repos were removed (the caller scrubs: the credential's
// scope narrows).
func teamApplyTx(t *DB, m *Project, def *ProjectView, adopt bool) (removed bool, err error) {
	cols := map[string]any{}
	if def.Name != m.Name {
		cols["name"] = def.Name
	}
	pol := teamPolicyAccepted(m.Policy, def.Policy)
	mine, err := t.projectRepos(m.ID)
	if err != nil {
		return false, err
	}
	in := map[string]ProjectRepo{}
	for _, dr := range def.Repos {
		in[strings.ToLower(dr.Repo)] = dr
	}
	have := map[string]bool{}
	for _, r := range mine {
		if _, ok := in[strings.ToLower(r.Repo)]; ok {
			have[strings.ToLower(r.Repo)] = true
			continue
		}
		if _, err := t.q.Exec(`DELETE FROM project_repos WHERE project_id=? AND slug=?`, m.ID, r.Slug); err != nil {
			return false, err
		}
		_, _ = t.q.Exec(`UPDATE project_jobs SET state='failed', error='the repo was removed from the team project', updated_ms=?
			WHERE project_id=? AND repo_slug=? AND state IN ('queued','waiting')`, nowMs(), m.ID, r.Slug)
		removed = true
	}
	if adopt {
		var sec struct {
			Policy map[string]json.RawMessage `json:"policy"`
		}
		_ = json.Unmarshal(teamSecurity(def.Policy, def.Repos), &sec)
		pol = teamPolicyAdopt(pol, sec.Policy)
		for _, dr := range def.Repos {
			if have[strings.ToLower(dr.Repo)] {
				if _, err := t.q.Exec(`UPDATE project_repos SET setup=? WHERE project_id=? AND lower(repo)=lower(?)`, dr.Setup, m.ID, dr.Repo); err != nil {
					return removed, err
				}
			} else if err := teamAddRepo(t, m, dr); err != nil {
				return removed, err
			}
		}
	}
	if string(pol) != string(policyCanon(m.Policy)) {
		cols["policy"] = string(pol)
	}
	now, err := t.projectRepos(m.ID)
	if err != nil {
		return removed, err
	}
	secD := teamSecurity(def.Policy, def.Repos)
	hashD := teamHash(secD)
	switch {
	case teamHash(teamSecurity(pol, now)) == hashD:
		if m.DefHash != hashD {
			cols["def_hash"] = hashD
		}
		if m.DefPending != "" {
			cols["def_pending"] = ""
		}
	case m.DefPending != hashD:
		cols["def_pending"] = string(secD)
		addProjectEvent(t, m.ID, 0, pevNote, map[string]any{"text": "the team project's definition changed (setup scripts, instructions, identity or other " +
			"security settings): its member reviews and accepts the changes before they apply", "pending": hashD}, true, "team-pending:"+hashD)
	}
	if len(cols) == 0 && !removed && !adopt {
		return removed, nil
	}
	sets, args := []string{"version=version+1", "updated_ms=?"}, []any{nowMs()}
	for k, v := range cols {
		sets, args = append(sets, k+"=?"), append(args, v)
	}
	if _, err := t.q.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id=?`, append(args, m.ID)...); err != nil {
		return removed, err
	}
	emitProject(t, m.ID, "project", 0)
	if removed || adopt {
		emitProject(t, m.ID, "repo", 0)
	}
	return removed, nil
}

// policyCanon is a stored policy as teamPolicyAccepted writes one (keys
// sorted, no spacing): two that say the same compare equal.
func policyCanon(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(orRaw(raw), &m) != nil {
		return raw
	}
	b, _ := json.Marshal(m)
	return b
}

// teamLeave archives membership pid: its definition is
// gone or no longer the member's — credentials scrubbed, the pump and the
// fetches stopped (archived), its board rows to send dropped; its tasks and
// their conversations stay the person's own.
func (ag *Agent) teamLeave(pid int64, status int) {
	left := false
	_ = ag.db.Tx(func(t *DB) error {
		_, _ = t.q.Exec(`DELETE FROM project_board_out WHERE project_id=?`, pid)
		m, err := t.getProject(pid)
		if err != nil || m.Kind != projMembership || m.State != projActive {
			return nil
		}
		if _, err := t.q.Exec(`UPDATE projects SET state=?, version=version+1, updated_ms=? WHERE id=?`, projArchived, nowMs(), pid); err != nil {
			return err
		}
		addProjectEvent(t, pid, 0, pevNote, map[string]any{"text": "you are no longer a member of this team project, or it was deleted " +
			"(HTTP " + strconv.Itoa(status) + "): this membership is archived; its tasks and their conversations stay yours"}, false, "")
		emitProject(t, pid, "project", 0)
		left = true
		return nil
	})
	if left {
		go scrubProject(pid, "", "left")
		ag.pokeTasks(pid)
	}
}

// --- the team board, for the member's coordinator --------------------------------------------

// teamBoardRows is membership p's team board as its person sees it at the
// global instance (GET /projects/{team}/board, up to 500 rows; `run` only
// on their own rows): what the coordinator's task_list {scope: "team"}
// reads. A definition gone archives the membership (errTeamGone).
func teamBoardRows(ctx context.Context, p *Project) ([]BoardRow, error) {
	if p == nil || p.Kind != projMembership || p.TeamRef == 0 || !userMode() {
		return nil, perr(409, "only a team project's membership has a team board")
	}
	out := []BoardRow{}
	cursor := ""
	for len(out) < 500 {
		path := fmt.Sprintf("/projects/%d/board", p.TeamRef)
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		res, err := callGlobal(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			return nil, perr(502, "the agent's shared instance didn't answer: %v", err)
		}
		if res.Status == http.StatusNotFound || res.Status == http.StatusForbidden {
			if ag := projAg(); ag != nil {
				ag.teamLeave(p.ID, res.Status)
			}
			return nil, errTeamGone
		}
		if res.Status != http.StatusOK {
			return nil, perr(502, "reading the team board: HTTP %d", res.Status)
		}
		var page struct {
			Items []BoardRow `json:"items"`
			Next  string     `json:"next"`
		}
		if err := json.Unmarshal(res.Body, &page); err != nil {
			return nil, perr(502, "the team board: %v", err)
		}
		out = append(out, page.Items...)
		if page.Next == "" || len(page.Items) == 0 {
			break
		}
		cursor = page.Next
	}
	return out, nil
}

// teamBoardText is the board as a model reads it: one line per row, every
// member's own words framed as untrusted text (other members wrote them).
func teamBoardText(rows []BoardRow) string {
	var b strings.Builder
	for _, r := range rows {
		if r.Hidden {
			continue
		}
		line := fmt.Sprintf("%s #%d [%s] %s", r.Member, r.N, orStr(r.State, r.Column), r.Title)
		if r.Branch != "" {
			line += " — " + r.Branch
		}
		for _, pr := range r.PRs {
			line += fmt.Sprintf(" — PR #%d (%s)", pr.Number, pr.State)
		}
		if r.Stale {
			line += " (no longer a member)"
		}
		if r.Run != 0 {
			line += " (yours: conversation " + strconv.FormatInt(r.Run, 10) + ")"
		}
		b.WriteString(line + "\n")
	}
	if b.Len() == 0 {
		return "The team board is empty."
	}
	return untrusted("the team project's members", "its board, in their own words", b.String())
}
