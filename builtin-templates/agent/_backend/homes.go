// homes.go — shared conversations in a partitioned agent, and copies between
// a conversation's two homes (API.md "Partitioned instances" → "Shared
// conversations"; docs/partitions.md). Unpartitioned, nothing here changes a
// thing: no route, no check.
//
// A conversation has one home: a person's own partition (theirs alone, ids
// from 2^40 — partition_start.go) or the global instance (shared; ids below
// 2^40). The page reaches global's through xbind's own-global addressing
// (?xbin-partition=global), attributed to the person, so global applies the
// D83 rules to them as ever: sharing, members, the team, join links, each
// person's pins and read state.
//
//   - Creating a shared conversation: POST /ask at global with `share` —
//     the team (to read or to write) and/or people. At global a person's new
//     conversation must say who shares it (409 otherwise): the shared space
//     holds shared conversations; a person's private ones are in their own
//     partition. A person's POST /runs at global is refused the same way.
//   - Publishing: POST /runs/{id}/publish in a person's partition sends a
//     copy of their conversation (its transcript; its session files only
//     when asked) to global as a new shared conversation of theirs, and
//     deletes the original unless asked to keep it.
//   - Copying back: POST /copy {from} in a person's partition makes a
//     private copy of a shared conversation they can see.
//
// The copies travel as a conversation bundle: GET /runs/{id}/export
// (either home), POST /import (global only; the partition's backend is the
// caller, attributed to its person).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// --- sharing a new conversation at once -------------------------------------------

// shareSpec is who a new conversation is shared with (POST /ask's and POST
// /import's `share`): the team — to read or to write — and/or people.
type shareSpec struct {
	Visibility string        `json:"visibility"` // "" | private | team
	TeamRole   string        `json:"teamRole"`   // with team: viewer | participant (default)
	Members    []shareMember `json:"members"`
}

type shareMember struct {
	User string `json:"user"`
	Role string `json:"role"` // viewer | participant (default)
}

const maxShareMembers = 50

// shared: s names anyone besides the owner.
func (s *shareSpec) shared() bool { return s != nil && (s.Visibility == visTeam || len(s.Members) > 0) }

func (s *shareSpec) check(owner string) error {
	if s == nil {
		return nil
	}
	switch s.Visibility {
	case "", visPrivate, visTeam:
	default:
		return fmt.Errorf("share.visibility is private or team")
	}
	switch s.TeamRole {
	case "", roleViewer, roleParticipant:
	default:
		return fmt.Errorf("share.teamRole is viewer or participant")
	}
	if len(s.Members) > maxShareMembers {
		return fmt.Errorf("share.members: at most %d people — add more later, or share it with the team", maxShareMembers)
	}
	for _, m := range s.Members {
		if !userIDRe.MatchString(m.User) {
			return fmt.Errorf("share.members: %q isn't a user id (the login name)", m.User)
		}
		if m.Role != "" && m.Role != roleViewer && m.Role != roleParticipant {
			return fmt.Errorf("share.members: role is viewer or participant")
		}
		if m.User == owner {
			return fmt.Errorf("share.members: %s is the owner", m.User)
		}
	}
	return nil
}

// stamp applies s to a new conversation's stamp: the team's access.
func (s *shareSpec) stamp(st *runStamp) {
	if s == nil || s.Visibility != visTeam {
		return
	}
	st.Visibility, st.TeamRole = visTeam, roleParticipant
	if s.TeamRole == roleViewer {
		st.TeamRole = roleViewer
	}
}

// addMembers shares the new conversation root with s's people.
func (s *shareSpec) addMembers(t *DB, root int64, by who) error {
	if s == nil {
		return nil
	}
	for _, m := range s.Members {
		role := m.Role
		if role == "" {
			role = roleParticipant
		}
		if _, err := t.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, ?, 'invite', ?)
			ON CONFLICT(run_id, user) DO UPDATE SET role=excluded.role`, root, m.User, role, by.tag(), now()); err != nil {
			return err
		}
	}
	return nil
}

const (
	noPrivateAtGlobal = "the agent's shared space keeps shared conversations: say who shares this one (share: the team, or people) — " +
		"your private conversations are in your own space"
	shareFromOwnSpace = "a conversation made in your own space is yours alone: create a shared one in the shared space (?xbin-partition=global), " +
		"or publish a copy of this one (POST /runs/{id}/publish)"
)

// askShareOK checks POST /ask's share for the instance's mode (handleAsk):
// in a person's partition a new conversation can't be shared (409); at
// global a person's must be (409 unless it names someone), and isn't made
// from a draft (the native view's uploads go to the person's own space).
func askShareOK(w http.ResponseWriter, r *http.Request, s *shareSpec, draft string) bool {
	c := callerOf(r)
	if err := s.check(c.user); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return false
	}
	switch {
	case userMode() && s.shared():
		xbin.WriteError(w, http.StatusConflict, shareFromOwnSpace)
		return false
	case globalMode() && personFromPartition(r) && !s.shared():
		xbin.WriteError(w, http.StatusConflict, noPrivateAtGlobal)
		return false
	case s.shared() && draft != "":
		xbin.WriteError(w, http.StatusBadRequest, "share a new conversation without a draft: create it held (hold: true), upload into it, then send")
		return false
	}
	return true
}

// shareNew shares a conversation POST /ask just made with s's people (its
// team access came with its stamp). Should that fail, the conversation goes
// again: at global it would otherwise be a person's private one.
func shareNew(w http.ResponseWriter, run *Run, s *shareSpec, by who) bool {
	if s == nil || len(s.Members) == 0 {
		return true
	}
	if err := agent.db.Tx(func(t *DB) error { return s.addMembers(t, run.ID, by) }); err != nil {
		_ = agent.db.Tx(func(t *DB) error { agent.cancelRuns(t, run.ID, true, "sharing it failed"); return nil })
		_ = agent.deleteRunTree(run.ID)
		xbin.WriteError(w, http.StatusInternalServerError, "sharing the new conversation: "+err.Error())
		return false
	}
	agent.membersChanged(run.ID)
	return true
}

// globalRoute is h as the global instance serves pattern: a person's
// attributed call may not start a conversation of theirs there without
// sharing it (POST /ask checks its body itself: askShareOK) — neither a run
// (POST /runs) nor a new ask's draft (PUT /ask/upload: a held private run
// whose file would sit in the shared space, and which no shared ask could
// ever send — a shared chat is made held and uploaded into instead). The
// same rule for automations — a person's private trigger or schedule is made
// in their own partition — needs the body and the saved row (a switch vs an
// edit), so their handlers check it: refusePrivateAtGlobal
// (trigger_registry.go).
func globalRoute(pattern string, h http.HandlerFunc) http.HandlerFunc {
	var why string
	switch pattern {
	case "POST /runs":
		why = noPrivateAtGlobal + " (POST /ask with share)"
	case "PUT /ask/upload":
		why = "a new chat's draft is made in your own space; a shared chat is created held (POST /ask {share, hold: true}), " +
			"then files are uploaded into it (PUT /runs/{id}/upload)"
	default:
		return refuseWhileMoving(pattern, h) // homes_move.go: a conversation moving out takes no more changes
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if personFromPartition(r) {
			xbin.WriteError(w, http.StatusConflict, why)
			return
		}
		h(w, r)
	}
}

// --- the routes -------------------------------------------------------------------

// homeRoutes mounts the copies' routes in a partitioned agent (none
// unpartitioned): export in both modes, import at global, publish and copy
// in a person's partition.
func homeRoutes(mux *http.ServeMux) {
	if !partitioned() {
		return
	}
	for _, rt := range []routeDef{
		{"GET /runs/{id}/export", needViewer, handleExport},
		{"POST /import", needStart, handleImport},
		{"POST /runs/{id}/publish", needOwner, handlePublish},
		{"POST /copy", needStart, handleCopy},
	} {
		// hostedRoute: a hosted conversation's export is team's (hosted_serve.go), its publish 409
		mux.Handle(rt.pattern, agentRole(hostedRoute(rt.pattern, rt.need, guard(rt.need, rt.h))))
	}
}

// handleExport: GET /runs/{id}/export[?files=1] — the conversation (a
// subagent's: its root's) as a bundle.
func handleExport(w http.ResponseWriter, r *http.Request) {
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	files := r.URL.Query().Get("files") == "1"
	b, err := agent.exportConv(r.Context(), rootOf(run), files)
	var out []byte
	if err == nil {
		out, err = bundleJSON(b, files)
	}
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// importBody is POST /import's body.
type importBody struct {
	Conversation *convBundle `json:"conversation"`
	Share        *shareSpec  `json:"share"`
}

// handleImport: POST /import {conversation, share} at the global instance —
// a new shared conversation of the caller's from a bundle (a person's
// partition publishing one). A person's must name who shares it.
func handleImport(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, http.StatusConflict, "copies into your own space are made with POST /copy {from}")
		return
	}
	var body importBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxBundleBytes))).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeBundleErr(w, tooLargeToCopy{})
			return
		}
		xbin.WriteError(w, 400, "bad body: "+err.Error())
		return
	}
	c := callerOf(r)
	if err := body.Share.check(c.user); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if c.kind == whoUser && !body.Share.shared() {
		xbin.WriteError(w, http.StatusConflict, noPrivateAtGlobal)
		return
	}
	cls, err := importClass(c, body.Conversation)
	if err != nil {
		writeClassErr(w, err)
		return
	}
	st := c.stamp("chat")
	body.Share.stamp(&st)
	note := "Published from " + orStr(c.user, "another space") + "’s own space: a copy of a conversation of theirs."
	run, err := agent.importConv(r.Context(), body.Conversation, c, st, cls, body.Share, note, true)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	xbin.WriteJSON(w, 200, run)
}

// importClass is the class a copy runs in here: the bundle's when the caller
// may use it, else the caller's default.
func importClass(c who, b *convBundle) (agentClass, error) {
	if b != nil && b.Class != "" {
		if cls, err := requestedClass(c, b.Class, ""); err == nil {
			return cls, nil
		}
	}
	return requestedClass(c, "", "")
}

func writeImportErr(w http.ResponseWriter, err error) {
	code := 500
	if _, ok := err.(badRequest); ok {
		code = 400
	}
	if writeHarnessMoveErr(w, err) { // harness_partition.go
		return
	}
	xbin.WriteError(w, code, err.Error())
}

// publishBody is POST /runs/{id}/publish's body.
type publishBody struct {
	Share *shareSpec `json:"share"`
	Files bool       `json:"files"` // carry its session files too
	Keep  bool       `json:"keep"`  // keep the private original (else it is deleted once the copy is made)
}

// handlePublish: POST /runs/{id}/publish {share, files?, keep?} in a
// person's partition — a copy of their conversation goes to the shared
// space (the global instance, as a new conversation of theirs shared as
// share says); the original is deleted unless keep. Answers {run: the
// shared copy (global's id), deleted}.
func handlePublish(w http.ResponseWriter, r *http.Request) {
	if !userMode() {
		xbin.WriteError(w, http.StatusConflict, "this conversation is in the shared space already: share it (POST /runs/{id}/members, PATCH visibility)")
		return
	}
	var body publishBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if err := body.Share.check(runUser); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if !body.Share.shared() {
		xbin.WriteError(w, 400, "share: the team, or people — a copy in the shared space is shared")
		return
	}
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	b, err := agent.exportConv(r.Context(), root, body.Files)
	var payload []byte
	if err == nil {
		payload, err = bundleJSON(importBody{Conversation: b, Share: body.Share}, body.Files)
	}
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, http.MethodPost, "/import", payload, "application/json")
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "the agent's shared instance didn't answer: "+err.Error())
		return
	}
	if res.Status/100 != 2 {
		w.Header().Set("Content-Type", orStr(res.Type, "application/json"))
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	var copied Run
	if err := json.Unmarshal(res.Body, &copied); err != nil || copied.ID <= 0 || copied.ID >= partitionIDBase {
		xbin.WriteError(w, http.StatusBadGateway, "the shared instance's answer names no shared conversation: "+clip(string(res.Body), 200))
		return
	}
	deleted := false
	if !body.Keep {
		deleted = deleteConversation(root) == nil
	}
	xbin.WriteJSON(w, 200, map[string]any{"run": copied, "deleted": deleted, "left": b.Left})
}

// copyBody is POST /copy's body.
type copyBody struct {
	From  int64 `json:"from"`  // a shared conversation's id (global's)
	Files bool  `json:"files"` // carry its session files too
}

// handleCopy: POST /copy {from, files?} in a person's partition — a private
// copy of a shared conversation they can see (read through the global
// instance, as them). Answers the new run.
func handleCopy(w http.ResponseWriter, r *http.Request) {
	if !userMode() {
		xbin.WriteError(w, http.StatusConflict, "copies are made into a person's own space")
		return
	}
	var body copyBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if body.From <= 0 || body.From >= partitionIDBase {
		xbin.WriteError(w, 400, "from: a shared conversation's id (below 2^40)")
		return
	}
	path := "/runs/" + strconv.FormatInt(body.From, 10) + "/export"
	if body.Files {
		path += "?files=1"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := exportAtGlobal(ctx, path)
	if tl := (tooLargeToCopy{}); errors.As(err, &tl) {
		writeBundleErr(w, err)
		return
	}
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "the agent's shared instance didn't answer: "+err.Error())
		return
	}
	if res.Status/100 != 2 {
		w.Header().Set("Content-Type", orStr(res.Type, "application/json"))
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	var b convBundle
	if err := json.Unmarshal(res.Body, &b); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "the shared instance's export: "+err.Error())
		return
	}
	c := callerOf(r)
	cls, err := importClass(c, &b)
	if err != nil {
		writeClassErr(w, err)
		return
	}
	note := "A private copy of a shared conversation" + map[bool]string{true: " of " + b.Owner, false: ""}[b.Owner != "" && b.Owner != c.user] +
		": only you can open it; the shared one goes on without it."
	run, err := agent.importConv(r.Context(), &b, c, c.stamp("chat"), cls, nil, note, false)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	xbin.WriteJSON(w, 200, run)
}

// exportAtGlobal reads a bundle from the global instance as this
// partition's person (callGlobal's gateway call, with room for a bundle:
// gwDo keeps 4 MiB of an answer). A var so tests stand in.
var exportAtGlobal = func(ctx context.Context, path string) (gwResp, error) {
	if !userMode() {
		return gwResp{}, errors.New("only a person's partition calls the global instance")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xbin.GlobalURL(path), nil)
	if err != nil {
		return gwResp{}, err
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return gwResp{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBundleBytes)+1))
	if err == nil && len(b) > maxBundleBytes { // an export says so itself (413); this is a backend that didn't
		return gwResp{}, tooLargeToCopy{}
	}
	return gwResp{Status: resp.StatusCode, Type: resp.Header.Get("Content-Type"), Body: b}, err
}

// deleteConversation deletes a conversation as DELETE /runs/{id} does.
func deleteConversation(root int64) error {
	acl, _ := agent.db.loadACL(root)
	_ = agent.db.Tx(func(t *DB) error {
		agent.cancelRuns(t, root, true, "published: a copy went to the shared space")
		return nil
	})
	if err := agent.deleteRunTree(root); err != nil {
		return err
	}
	agent.acl.flush(root)
	_, _ = agent.db.q.Exec(`DELETE FROM run_user_state WHERE run_id=?`, root)
	if agent.eng != nil {
		agent.eng.hub.publish(&Event{Type: evRun, Run: root, Root: root, Data: map[string]any{"id": root, "deleted": true}, acl: acl})
	}
	return nil
}
