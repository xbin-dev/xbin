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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
// sharing it (POST /ask checks its body itself: askShareOK).
func globalRoute(pattern string, h http.HandlerFunc) http.HandlerFunc {
	if pattern != "POST /runs" {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if personFromPartition(r) {
			xbin.WriteError(w, http.StatusConflict, noPrivateAtGlobal+" (POST /ask with share)")
			return
		}
		h(w, r)
	}
}

// --- the conversation bundle ------------------------------------------------------

const (
	bundleVersion  = 1
	maxBundleBytes = 24 << 20 // a bundle as a request body
	maxBundleBlobs = 16 << 20 // binary files a copy carries, together
)

// convBundle is one conversation as a copy carries it: its root's
// transcript as the model reads it (every message, with its tool calls and
// results, folded ones marked), and — when asked — its session files.
// Subagents' own transcripts, memory, grants, sandboxes and the task ledger
// stay behind.
type convBundle struct {
	Version  int          `json:"version"`
	Title    string       `json:"title"`
	Class    string       `json:"class,omitempty"`
	Model    string       `json:"model,omitempty"` // the conversation's pick
	Summary  string       `json:"summary,omitempty"`
	Owner    string       `json:"owner,omitempty"`
	Messages []bundleMsg  `json:"messages"`
	Files    []bundleFile `json:"files,omitempty"`
	Left     []string     `json:"left,omitempty"` // files not carried (over the size cap)
}

type bundleMsg struct {
	Role       string   `json:"role"`
	Content    string   `json:"content"`
	Name       string   `json:"name,omitempty"`
	ToolCallID string   `json:"toolCallId,omitempty"`
	ToolCalls  string   `json:"toolCalls,omitempty"`
	Compacted  bool     `json:"compacted,omitempty"`
	Created    int64    `json:"created"`
	Sender     string   `json:"sender,omitempty"`
	Model      string   `json:"model,omitempty"`
	Files      []string `json:"files,omitempty"` // the session files it carried
}

type bundleFile struct {
	Path    string `json:"path"`
	Mime    string `json:"mime,omitempty"`
	Content string `json:"content,omitempty"` // a text file
	Data    []byte `json:"data,omitempty"`    // a binary one (base64 on the wire)
	Binary  bool   `json:"binary,omitempty"`
}

// exportConv reads conversation root into a bundle.
func (ag *Agent) exportConv(ctx context.Context, root int64, withFiles bool) (*convBundle, error) {
	run, err := ag.db.getRun(root)
	if err != nil {
		return nil, err
	}
	cfg, _ := ag.db.runConfig(root)
	b := &convBundle{Version: bundleVersion, Title: run.Title, Class: cfg.Class, Model: cfg.Pick, Summary: run.Summary, Owner: run.Owner}
	msgs, err := ag.db.messages(root, false)
	if err != nil {
		return nil, err
	}
	carried := ag.db.messageFiles(root)
	for _, m := range msgs {
		if m.Role == "system" {
			continue // the new home's own system prompt is written at import
		}
		bm := bundleMsg{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls,
			Compacted: m.Compacted, Created: m.Created}
		var meta msgMeta
		if len(m.Meta) > 0 && json.Unmarshal(m.Meta, &meta) == nil {
			bm.Sender, bm.Model = meta.Sender, meta.Model
		}
		if withFiles {
			bm.Files = carried[m.ID]
		}
		b.Messages = append(b.Messages, bm)
	}
	if !withFiles {
		return b, nil
	}
	files, err := ag.db.replFiles(root)
	if err != nil {
		return nil, err
	}
	blobBytes := 0
	for _, f := range files {
		if !f.Binary {
			full, err := ag.db.replFile(root, f.Path)
			if err != nil {
				return nil, err
			}
			b.Files = append(b.Files, bundleFile{Path: f.Path, Mime: f.Mime, Content: full.Content})
			continue
		}
		if blobBytes+f.Bytes > maxBundleBlobs {
			b.Left = append(b.Left, f.Path)
			continue
		}
		data, err := ag.readBlob(ctx, f.Blob)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Path, err)
		}
		blobBytes += len(data)
		b.Files = append(b.Files, bundleFile{Path: f.Path, Mime: f.Mime, Data: data, Binary: true})
	}
	return b, nil
}

// importConv makes a new conversation of c's from a bundle, stamped st, in
// class cls (the class's settings are this home's), with a note saying where
// it came from. Its files first, then the transcript, then the rest in one
// transaction; a failure leaves nothing behind.
func (ag *Agent) importConv(ctx context.Context, b *convBundle, c who, st runStamp, cls agentClass, s *shareSpec, note string) (*Run, error) {
	if b == nil || b.Version != bundleVersion {
		return nil, errBadRequest(fmt.Sprintf("conversation: a bundle of version %d (GET /runs/{id}/export)", bundleVersion))
	}
	if len(b.Messages) == 0 {
		return nil, errBadRequest("conversation: no messages to copy")
	}
	cfg := parseConfig(ag.db.getSetting("config"))
	cfg.setClass(cls, false)
	if validPick(b.Model) {
		cfg.Pick = b.Model
	}
	cfgJSON, _ := json.Marshal(cfg)
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "a copy"
	}
	st.Origin, st.TitleSrc = "chat", "user"
	var id int64
	err := ag.db.Tx(func(t *DB) error {
		var err error
		if id, err = t.createRunStamped(clip(title, 120), string(cfgJSON), 0, statusIdle, st); err != nil {
			return err
		}
		t.setRunKind(id, "quick")
		if _, err := t.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System}); err != nil {
			return err
		}
		return s.addMembers(t, id, c)
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Run, error) {
		_ = ag.deleteRunTree(id)
		return nil, err
	}
	paths := map[string]string{} // the bundle's path → where it landed
	for _, f := range b.Files {
		var got *ReplFile
		var err error
		if f.Binary {
			got, err = ag.acceptUploadSrc(ctx, id, f.Path, f.Mime, bytes.NewReader(f.Data), &fileSource{Kind: "copy"})
		} else {
			var p string
			if p, err = normReplPath(f.Path); err == nil {
				got, err = ag.db.replPutFileSrc(id, p, f.Content, 0, &fileSource{Kind: "copy"})
				if err == nil && f.Mime != "" {
					_, _ = ag.db.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, f.Mime, id, got.Path)
				}
			}
		}
		if err != nil {
			return fail(fmt.Errorf("copying %s: %w", f.Path, err))
		}
		paths[f.Path] = got.Path
	}
	err = ag.db.Tx(func(t *DB) error {
		for _, bm := range b.Messages {
			switch bm.Role {
			case "user", "assistant", "tool":
			default:
				return errBadRequest("conversation: a message's role is user, assistant or tool")
			}
			m := &Message{RunID: id, Role: bm.Role, Content: bm.Content, Name: bm.Name, ToolCallID: bm.ToolCallID,
				ToolCalls: bm.ToolCalls, Compacted: bm.Compacted}
			if bm.Sender != "" || bm.Model != "" {
				m.Meta, _ = json.Marshal(msgMeta{Sender: bm.Sender, Model: bm.Model})
			}
			if _, err := t.addMessage(m); err != nil {
				return err
			}
			for _, p := range bm.Files {
				if at, ok := paths[p]; ok {
					if _, err := t.q.Exec(`INSERT OR IGNORE INTO message_files (msg_id, run_id, path) VALUES (?, ?, ?)`, m.ID, id, at); err != nil {
						return err
					}
				}
			}
			if bm.Role == "user" && !bm.Compacted {
				if err := t.recordAsk(m, "human", bm.Sender); err != nil {
					return err
				}
			}
		}
		if b.Summary != "" {
			if err := t.setSummary(id, b.Summary); err != nil {
				return err
			}
		}
		if note != "" {
			t.journal(id, "note", map[string]string{"text": note})
		}
		if ag.eng != nil {
			ag.eng.emitRun(t, id)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(s.members()) > 0 {
		ag.membersChanged(id)
	}
	return ag.db.getRun(id)
}

func (s *shareSpec) members() []shareMember {
	if s == nil {
		return nil
	}
	return s.Members
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
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
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
	b, err := agent.exportConv(r.Context(), rootOf(run), r.URL.Query().Get("files") == "1")
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, b)
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
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBundleBytes)).Decode(&body); err != nil {
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
	note := "Published from " + orStr(c.user, "another space") + "’s own space: a copy — the original stayed there."
	run, err := agent.importConv(r.Context(), body.Conversation, c, st, cls, body.Share, note)
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
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	payload, _ := json.Marshal(importBody{Conversation: b, Share: body.Share})
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
	res, err := callGlobal(ctx, http.MethodGet, path, nil, "")
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
	run, err := agent.importConv(r.Context(), &b, c, c.stamp("chat"), cls, nil, note)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	xbin.WriteJSON(w, 200, run)
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
