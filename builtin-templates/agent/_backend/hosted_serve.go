// hosted_serve.go — the global instance serving the members of a hosted
// (non-secure) conversation (hosted.go). A person's page reaches every
// conversation below 2^40 at the global instance (model/homes.js), a hosted
// one's too — its id is in team's range (team_runs.go) — so the members use
// the same routes as for any shared conversation. hostedRoute sends a route
// on such an id to the handlers below, which read and write team with the
// agent's own code (the team view, hosted_global.go) under team's ACL:
//
//   - the view and the stream: the conversation as it is in team, and the
//     host's run as its engine posts it (hosted_global.go);
//   - a member's message, answer, interrupt or stop is written into team's
//     inbox and rings the host's partition (hosted/input): the host's engine
//     takes it up — never the global instance's;
//   - adding a member, letting the team in, making a viewer a participant is
//     done here and rings the host, whose engine pauses the conversation
//     until the host confirms the wider audience (hosted.go);
//   - share links are refused: they would let in people the host never saw;
//   - anything else on a hosted conversation is 409 (not built for it).
//
// The conversation list at the global instance adds a person's hosted
// conversations to its first page (hostedList).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	noLinksHosted = "a non-secure conversation has no join links: they would let in people its host never confirmed — add people by name"
	notForHosted  = "not available in a non-secure (hosted) conversation"
)

// hostedHandlers are the routes a hosted conversation has at the global
// instance, by the route table's pattern.
var hostedHandlers = map[string]http.HandlerFunc{
	"GET /runs/{id}/view":                handleHostedView,
	"GET /runs/{id}/stream":              handleHostedStream,
	"GET /stream":                        handleHostedStream,
	"GET /runs/{id}":                     handleHostedGetRun,
	"POST /runs/{id}/message":            handleHostedMessage,
	"POST /runs/{id}/answer":             handleHostedMessage,
	"POST /runs/{id}/interrupt":          handleHostedControl,
	"POST /runs/{id}/cancel":             handleHostedControl,
	"DELETE /runs/{id}/inbox/{iid}":      handleHostedRemoveQueued,
	"GET /runs/{id}/asks":                handleHostedAsks,
	"GET /runs/{id}/members":             handleHostedMembers,
	"POST /runs/{id}/members":            handleHostedAddMember,
	"DELETE /runs/{id}/members/{user}":   handleHostedRemoveMember,
	"PATCH /runs/{id}":                   handleHostedPatch,
	"POST /runs/{id}/read":               handleHostedRead,
	"POST /runs/{id}/links":              func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 409, noLinksHosted) },
	"DELETE /runs/{id}":                  handleHostedDelete,
	"GET /runs/{id}/files":               handleHostedFiles,
	"GET /runs/{id}/file":                handleHostedFile,
	"GET /runs/{id}/export":              handleHostedExport,
	"POST /runs/{id}/copyin":             func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 409, notForHosted) },
	"DELETE /runs/{id}/links/{lid}":      func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 404, "no such link") },
	"DELETE /runs/{id}/grants/{cap}":     func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 409, notForHosted) },
	"GET /runs/{id}/tree":                handleHostedTree,
	"GET /runs/{id}/raw":                 handleHostedRaw,
	"GET /runs/{id}/thumb":               handleHostedRaw,
	"GET /runs/{id}/sandboxes/{rest...}": func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 409, notForHosted) },
}

// hostedRoute is next, except at the global instance for a route on a
// hosted conversation (its id in team's range): that is served from team
// (hostedHandlers, 409 for a route it doesn't have). The conversation list
// gains the caller's hosted conversations. Unpartitioned and in a person's
// partition: next, untouched.
func hostedRoute(pattern string, n need, next http.HandlerFunc) http.HandlerFunc {
	if pattern == "GET /conversations" {
		return func(w http.ResponseWriter, r *http.Request) {
			if !globalMode() {
				next(w, r)
				return
			}
			hostedList(w, r, next)
		}
	}
	if !strings.Contains(pattern, "{id}") && pattern != "GET /stream" {
		return next
	}
	h, ok := hostedHandlers[pattern]
	if !ok {
		h = func(w http.ResponseWriter, _ *http.Request) { xbin.WriteError(w, 409, notForHosted) }
	}
	hg := hostedGuard(n, h)
	return func(w http.ResponseWriter, r *http.Request) {
		if !globalMode() || !hostedID(hostedTarget(r)) {
			next(w, r)
			return
		}
		hg(w, r)
	}
}

// hostedTarget is the conversation a request names: the path's id, or
// GET /stream's run.
func hostedTarget(r *http.Request) int64 {
	if id := pathID(r); id != 0 {
		return id
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	return id
}

// hostedGuard is guard (routes.go) for a hosted conversation: the caller's
// access from team's ACL.
func hostedGuard(n need, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := principal(r)
		switch {
		case c.kind == whoNone:
			xbin.WriteError(w, 403, "no caller identity")
			return
		case c.kind == whoCron, n == needCron, n == needSelf, n == needManager:
			xbin.WriteError(w, 409, notForHosted)
			return
		}
		tv := teamView()
		if tv == nil {
			xbin.WriteError(w, 503, "the shared space is being upgraded — try again in a minute")
			return
		}
		_, lv, err := hostedLevel(tv.db, c, hostedTarget(r))
		if err != nil || lv == lvNone {
			xbin.WriteError(w, 404, "no such run")
			return
		}
		want := needLevel(n)
		if want < lvViewer {
			want = lvViewer
		}
		if lv < want {
			xbin.WriteError(w, 403, "you are a "+lv.String()+" of this conversation; that needs "+want.String())
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), whoKey, c), levelKey, lv)
		h(w, r.WithContext(ctx))
	}
}

// hostedRootOf is the route's conversation (its root) in team, with its hosting.
func hostedRootOf(w http.ResponseWriter, r *http.Request) (*Agent, *Run, *teamHost, bool) {
	tv := teamView()
	run, err := tv.db.getRun(pathID(r))
	if err == nil {
		run, err = tv.db.getRun(rootOf(run))
	}
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return nil, nil, nil, false
	}
	h, err := tv.db.teamHost(run.ID)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return nil, nil, nil, false
	}
	return tv, run, h, true
}

// talkable: members may send it work (409 otherwise, saying why).
func talkable(w http.ResponseWriter, h *teamHost) bool {
	switch hostedInfo(h)["state"] {
	case hostActive:
		return true
	case hostPaused:
		xbin.WriteError(w, 409, "paused: waiting for "+hostOf(h)+" to confirm who is in it now")
	default:
		xbin.WriteError(w, 409, hostOf(h)+" no longer hosts it: continue it without their resources (POST /hosted/{id}/continue)")
	}
	return false
}

// --- reading -------------------------------------------------------------------------

func handleHostedView(w http.ResponseWriter, r *http.Request) {
	tv, _, h, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	viewWith(w, r, tv, func(v map[string]any) {
		info := hostedInfo(h)
		v["hosted"] = info
		if run, ok := v["run"].(map[string]any); ok {
			run["hosted"] = info // as the stream's run summaries carry it (teamView's decorate)
		}
	})
}

func handleHostedStream(w http.ResponseWriter, r *http.Request) { streamWith(w, r, teamView()) }

func handleHostedGetRun(w http.ResponseWriter, r *http.Request) {
	tv, _, h, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	run, _ := tv.db.getRun(id)
	msgs, _ := tv.db.messages(id, false)
	steps, _ := tv.db.steps(id)
	mem, _ := tv.db.memory(id)
	files, _ := tv.db.replFiles(id)
	xbin.WriteJSON(w, 200, map[string]any{"run": run, "messages": legacyMessages(msgs), "steps": steps, "memory": mem,
		"files": files, "queued": tv.db.queuedView(id), "hosted": hostedInfo(h)})
}

func handleHostedAsks(w http.ResponseWriter, r *http.Request) {
	asks, err := teamView().db.asks(pathID(r))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if asks == nil {
		asks = []*Ask{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"asks": asks})
}

func handleHostedTree(w http.ResponseWriter, r *http.Request) {
	tv := teamView()
	run, err := tv.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	runs, _ := tv.db.treeRuns(rootOf(run))
	var out []map[string]any
	for _, x := range runs {
		out = append(out, runSummary(x))
	}
	xbin.WriteJSON(w, 200, map[string]any{"root": rootOf(run), "runs": out})
}

func handleHostedMembers(w http.ResponseWriter, r *http.Request) {
	tv, root, h, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	members := []map[string]any{}
	if rows, err := tv.db.q.Query(`SELECT user, role, added_by, via, created FROM run_members WHERE run_id=? ORDER BY created, user`, root.ID); err == nil {
		for rows.Next() {
			var u, role, by, via string
			var created int64
			if rows.Scan(&u, &role, &by, &via, &created) == nil {
				members = append(members, map[string]any{"user": u, "role": role, "addedBy": by, "via": via, "created": created})
			}
		}
		rows.Close()
	}
	xbin.WriteJSON(w, 200, map[string]any{"owner": root.Owner, "visibility": root.Visibility, "teamRole": root.TeamRole,
		"members": members, "links": []any{}, "hosted": hostedInfo(h)})
}

func handleHostedFiles(w http.ResponseWriter, r *http.Request) {
	files, err := teamView().db.replFiles(pathID(r))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if files == nil {
		files = []*ReplFile{}
	}
	xbin.WriteJSON(w, 200, files)
}

func handleHostedFile(w http.ResponseWriter, r *http.Request) {
	path, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := teamView().db.replFile(pathID(r), path)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	if f.Binary {
		xbin.WriteError(w, 404, "its bytes are in its host's own space")
		return
	}
	xbin.WriteJSON(w, 200, f)
}

func handleHostedRaw(w http.ResponseWriter, r *http.Request) {
	path, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := teamView().db.replFile(pathID(r), path)
	if err != nil || f.Binary {
		xbin.WriteError(w, 404, "no such file here (a binary one's bytes are in its host's own space)")
		return
	}
	w.Header().Set("Content-Type", orStr(f.Mime, "text/plain; charset=utf-8"))
	_, _ = w.Write([]byte(f.Content))
}

func handleHostedExport(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	b, err := tv.exportConv(r.Context(), root.ID, false) // the transcript: its binary files are the host's
	var out []byte
	if err == nil {
		out, err = bundleJSON(b, false)
	}
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// --- members' work: into team, then ring the host -----------------------------------------

func handleHostedMessage(w http.ResponseWriter, r *http.Request) {
	tv, root, h, ok := hostedRootOf(w, r)
	if !ok || !talkable(w, h) {
		return
	}
	id := pathID(r)
	var body struct {
		Text     string   `json:"text"`
		Files    []string `json:"files"`
		ClientID string   `json:"clientId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" && len(body.Files) == 0 {
		xbin.WriteError(w, 400, "need {text} or {files}")
		return
	}
	if _, err := tv.checkAttachments(id, body.Files); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	run, _ := tv.db.getRun(id)
	sender := callerOf(r).user
	iid, _, err := tv.queue(id, inboxUser, inboxBody{Text: body.Text, Files: body.Files, Source: "human", Sender: sender}, body.ClientID)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	tv.db.bumpActivity(id)
	tv.markRead(root.ID, sender)
	wakeHost(tv, root.ID, hostedInput{Run: id})
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "inboxId": iid, "queued": run != nil && active(run.Status)})
}

// handleHostedControl: interrupt or stop — the row that says so goes into
// team; the host's engine aborts the step in flight when rung.
func handleHostedControl(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	run, _ := tv.db.getRun(id)
	signal := "cancel"
	var stopped []int64
	if strings.HasSuffix(r.URL.Path, "/interrupt") {
		signal = "interrupt"
		_ = tv.db.Tx(func(t *DB) error {
			if run != nil && active(run.Status) {
				if _, _, err := t.enqueue(id, inboxInterrupt, inboxBody{Reason: "interrupted by " + orStr(callerOf(r).user, "a member")}, ""); err != nil {
					return err
				}
			}
			tv.cancelBelow(t, id, "parent interrupted")
			return nil
		})
	} else {
		var body struct{ Scope, Reason string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = tv.db.Tx(func(t *DB) error {
			stopped = tv.cancelRuns(t, id, body.Scope != "node", body.Reason)
			return nil
		})
	}
	wakeHost(tv, root.ID, hostedInput{Run: id, Signal: signal})
	if stopped == nil {
		stopped = []int64{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "cancelled": stopped, "returned": []any{}})
}

func handleHostedRemoveQueued(w http.ResponseWriter, r *http.Request) {
	tv := teamView()
	id := pathID(r)
	iid, _ := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if c, lv := callerOf(r), levelOf(r); lv < lvOwner {
		rows := tv.db.inboxRows(`WHERE id=? AND run_id=?`, iid, id)
		if len(rows) == 1 && rows[0].Body.Sender != c.user {
			xbin.WriteError(w, 403, "only the conversation's owner can take back someone else's message")
			return
		}
	}
	var removed, exists bool
	_ = tv.db.Tx(func(t *DB) error {
		removed, exists = t.removeQueued(id, iid)
		if removed {
			if run, err := t.getRun(id); err == nil {
				tv.eng.emitInbox(t, rootOf(run), id)
			}
		}
		return nil
	})
	switch {
	case removed:
		xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
	case exists:
		xbin.WriteError(w, 409, "already delivered to the agent")
	default:
		xbin.WriteError(w, 404, "no such queued message")
	}
}

func handleHostedRead(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	tv.markRead(root.ID, callerOf(r).user)
	xbin.WriteJSON(w, 200, map[string]any{"readMs": time.Now().UnixMilli()})
}

// --- the audience ----------------------------------------------------------------------

// audienceChanged applies a changed ACL to the streams and rings the host,
// whose engine pauses the conversation if it is now wider than confirmed.
func audienceChanged(tv *Agent, root int64) {
	tv.membersChanged(root)
	wakeHost(tv, root, hostedInput{Signal: "audience"})
}

func handleHostedAddMember(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	var body struct{ User, Role string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Role == "" {
		body.Role = roleParticipant
	}
	switch {
	case !userIDRe.MatchString(body.User):
		xbin.WriteError(w, 400, "need {user: a user id (the login name)}")
		return
	case body.Role != roleViewer && body.Role != roleParticipant:
		xbin.WriteError(w, 400, "role is viewer or participant")
		return
	case body.User == root.Owner:
		xbin.WriteError(w, 400, "that is the owner")
		return
	}
	if _, err := tv.db.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, ?, 'invite', ?)
		ON CONFLICT(run_id, user) DO UPDATE SET role=excluded.role`, root.ID, body.User, body.Role, callerOf(r).tag(), now()); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	audienceChanged(tv, root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleHostedRemoveMember(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	user := r.PathValue("user")
	if c := callerOf(r); levelOf(r) < lvOwner && !(c.kind == whoUser && c.user == user) {
		xbin.WriteError(w, 403, "only the owner can remove someone else")
		return
	}
	_, _ = tv.db.q.Exec(`DELETE FROM run_members WHERE run_id=? AND user=?`, root.ID, user)
	_, _ = tv.db.q.Exec(`DELETE FROM run_user_state WHERE run_id=? AND user=?`, root.ID, user)
	audienceChanged(tv, root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleHostedPatch: a person's pin and archive; the owner's title and who
// may see it (a wider audience pauses it for the host); its model.
func handleHostedPatch(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	c, lv := callerOf(r), levelOf(r)
	var body struct {
		Title      *string         `json:"title"`
		Pinned     *bool           `json:"pinned"`
		Archived   *bool           `json:"archived"`
		Visibility *string         `json:"visibility"`
		TeamRole   *string         `json:"teamRole"`
		Model      *string         `json:"model"`
		Class      *string         `json:"class"`
		Sandbox    json.RawMessage `json:"sandbox"`
		Detach     *string         `json:"detach"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		xbin.WriteError(w, 400, "bad body")
		return
	}
	switch {
	case body.Class != nil || len(body.Sandbox) > 0 || body.Detach != nil:
		xbin.WriteError(w, 409, "a non-secure conversation's class and sandbox are its host's to set, in their own space")
		return
	case (body.Title != nil || body.Visibility != nil || body.TeamRole != nil) && lv < lvOwner:
		xbin.WriteError(w, 403, "only the conversation's owner can rename or share it")
		return
	case body.Model != nil && (lv < lvParticipant || !validPick(*body.Model)):
		xbin.WriteError(w, 400, "model: a model id from GET /models, by someone who may talk in it")
		return
	}
	if body.Pinned != nil || body.Archived != nil {
		if c.kind != whoUser {
			xbin.WriteError(w, 400, "pin and archive are a person's own")
			return
		}
		ms := time.Now().UnixMilli()
		s := tv.db.setUserState(root.ID, c.user, func(s *userState) {
			if body.Pinned != nil {
				s.PinnedAt = map[bool]int64{true: ms, false: 0}[*body.Pinned]
			}
			if body.Archived != nil {
				s.ArchivedAt = map[bool]int64{true: ms, false: 0}[*body.Archived]
			}
		})
		tv.emitUserState(root.ID, c.user, s)
	}
	changedACL := false
	err := tv.db.Tx(func(t *DB) error {
		if body.Title != nil {
			title := strings.TrimSpace(*body.Title)
			if title == "" {
				return errBadRequest("the title can't be empty")
			}
			if _, err := t.q.Exec(`UPDATE runs SET title=?, title_src='user' WHERE id=?`, clip(title, 120), root.ID); err != nil {
				return err
			}
		}
		if body.Visibility != nil || body.TeamRole != nil {
			vis, role := root.Visibility, root.TeamRole
			if body.Visibility != nil {
				vis = *body.Visibility
			}
			if body.TeamRole != nil {
				role = *body.TeamRole
			}
			if (vis != visPrivate && vis != visTeam) || (role != roleViewer && role != roleParticipant) {
				return errBadRequest("visibility is private|team, teamRole viewer|participant")
			}
			if _, err := t.q.Exec(`UPDATE runs SET visibility=?, team_role=? WHERE root_id=? OR id=?`, vis, role, root.ID, root.ID); err != nil {
				return err
			}
			changedACL = true
		}
		if body.Model != nil {
			cfg, err := t.runConfig(root.ID)
			if err != nil {
				return err
			}
			cfg.Pick = *body.Model
			raw, _ := json.Marshal(cfg)
			if _, err := t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), root.ID); err != nil {
				return err
			}
		}
		tv.eng.emitRun(t, root.ID)
		return nil
	})
	if err != nil {
		writeTxErr(w, err)
		return
	}
	if changedACL {
		audienceChanged(tv, root.ID)
	}
	run, _ := tv.db.getRun(root.ID)
	st := tv.db.userStates(c.user, []int64{root.ID})[root.ID]
	xbin.WriteJSON(w, 200, tv.convItem(run, c, st))
}

// handleHostedDelete: its owner deletes it (the host's engine finds nothing
// left to drive).
func handleHostedDelete(w http.ResponseWriter, r *http.Request) {
	tv, root, _, ok := hostedRootOf(w, r)
	if !ok {
		return
	}
	if pathID(r) != root.ID {
		xbin.WriteError(w, 409, notForHosted)
		return
	}
	if levelOf(r) < lvOwner {
		xbin.WriteError(w, 403, "only the conversation's owner can delete it")
		return
	}
	dropConversation(tv, root.ID, "deleted")
	_, _ = tv.db.q.Exec(`DELETE FROM team_hosts WHERE run_id=?`, root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// --- the list ---------------------------------------------------------------------------

// bufWriter keeps a handler's answer to add to it.
type bufWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (b *bufWriter) Header() http.Header         { return b.h }
func (b *bufWriter) WriteHeader(code int)        { b.code = code }
func (b *bufWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }

// hostedList is GET /conversations at the global instance with the
// caller's hosted conversations (those they may see: in Mine, the ones they
// own or joined; in Shared, all) added to its first page, each with its
// hosting — what the page shows its ⚠ chip from.
func hostedList(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	qs := r.URL.Query()
	c := principal(r)
	tv := teamView()
	scope := qs.Get("scope")
	if tv == nil || c.kind != whoUser || qs.Get("cursor") != "" || strings.TrimSpace(qs.Get("q")) != "" || scope == "team" {
		next(w, r)
		return
	}
	bw := &bufWriter{h: http.Header{}, code: 200}
	next(bw, r)
	var out map[string]json.RawMessage
	if bw.code != 200 || json.Unmarshal(bw.buf.Bytes(), &out) != nil {
		for k, v := range bw.h {
			w.Header()[k] = v
		}
		w.WriteHeader(bw.code)
		_, _ = w.Write(bw.buf.Bytes())
		return
	}
	where, args := aclWhere(c)
	runs, _ := tv.db.queryRuns(`r WHERE r.parent_id=0 AND `+where+` ORDER BY r.activity_ms DESC LIMIT 100`, args...)
	var ids []int64
	for _, x := range runs {
		ids = append(ids, x.ID)
	}
	states := tv.db.userStates(c.user, ids)
	var pinned, items []map[string]any
	_ = json.Unmarshal(out["pinned"], &pinned)
	_ = json.Unmarshal(out["items"], &items)
	archived := qs.Get("archived") == "1"
	for _, x := range runs {
		h, err := tv.db.teamHost(x.ID)
		if err != nil {
			continue
		}
		it := tv.convItem(x, c, states[x.ID])
		if scope != "shared" && it["mine"] != true {
			continue
		}
		st := states[x.ID]
		if (st.ArchivedAt != 0) != archived {
			continue
		}
		it["hosted"] = hostedInfo(h)
		if st.PinnedAt != 0 && !archived {
			pinned = append(pinned, it)
		} else {
			items = append(items, it)
		}
	}
	if pinned == nil {
		pinned = []map[string]any{}
	}
	if items == nil {
		items = []map[string]any{}
	}
	var nextCur string
	_ = json.Unmarshal(out["next"], &nextCur)
	xbin.WriteJSON(w, 200, map[string]any{"pinned": pinned, "items": items, "next": nextCur})
}
