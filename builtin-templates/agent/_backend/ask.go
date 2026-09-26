// ask.go — quick asks: the low-friction way in. A question becomes a fresh run
// (kind=quick) that is driven immediately; follow-ups continue in that run like
// any other conversation. The tile's home view is built on this.
//
// A new ask with attachments needs a run to upload into before there is one.
// Two ways, both additive:
//
//   - POST /ask {hold:true} creates the run held (no message, no drive), the
//     tile uploads into it and sends with POST /runs/{id}/message — the web
//     view, which holds the files until Send;
//   - a DRAFT (the native view, whose app uploads a picked file at once, to a
//     path the tile names in advance): PUT /ask/upload?draft=<key>&name= puts
//     the file into a held run for that key — the first upload creates it,
//     hidden from every list (origin "held") — and POST /ask {text, draft,
//     files} releases it: titled, configured and sent as a new ask. A draft
//     never released is deleted when its owner starts another a day later.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// heldOrigin marks a draft's run until it is released: not a conversation
// (conversations.go chatOrigins), not an automation — listed nowhere.
const heldOrigin = "held"

// heldTTL is how long a draft nobody sent is kept.
const heldTTL = 24 * time.Hour

var draftKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text, Toolset string
		// Hold creates the run WITHOUT the user message or a drive, so the tile
		// can upload attachments into it first and then send the message with
		// POST /runs/{id}/message — a quick ask has no run to attach to until
		// this call makes one.
		Hold bool
		// Title and System come from the "New chat with options" dialog.
		Title, System string
		// Draft releases the held run PUT /ask/upload made for this key;
		// Files names the uploads the message carries.
		Draft string
		Files []string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Text = strings.TrimSpace(body.Text)
	if body.Draft != "" {
		if body.Hold || !draftKeyRe.MatchString(body.Draft) {
			xbin.WriteError(w, 400, "draft: the key its uploads used (8–64 of A–Z a–z 0–9 _ -), without hold")
			return
		}
		if id := heldDraft(agent.db, callerOf(r), body.Draft); id != 0 || len(body.Files) > 0 {
			releaseDraft(w, r, id, body.Draft, body.Text, body.Toolset, body.Title, body.System, body.Files)
			return
		}
		// nothing was uploaded for it: an ordinary ask
	}
	if body.Text == "" {
		xbin.WriteError(w, 400, "need {text}")
		return
	}
	cfg := parseConfig(agent.db.getSetting("config"))
	cfg.Toolset = normalizeToolset(body.Toolset)
	if body.System != "" {
		cfg.System = body.System
	}
	// No journal note: a new conversation needs no caption (D83).
	note := ""
	if !body.Hold && haltBlocks(w, r, 0) {
		return
	}
	w0 := callerOf(r)
	st := w0.stamp("chat")
	title := strings.TrimSpace(body.Title)
	if st.TitleSrc = "user"; title == "" {
		title, st.TitleSrc = clip(body.Text, 60), "clip"
	}
	run, err := agent.startRunOpts(runOpts{Title: title, Kind: "quick", Cfg: cfg, Text: body.Text, Hold: body.Hold,
		Note: note, Stamp: st, Sender: w0.user})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, run)
}

// heldDraft is the caller's unreleased draft run for key (0: none).
func heldDraft(d *DB, c who, key string) int64 {
	var id int64
	_ = d.q.QueryRow(`SELECT id FROM runs WHERE session_key=? AND owner=? AND origin=? AND parent_id=0 ORDER BY id LIMIT 1`,
		"held:"+key, c.tag(), heldOrigin).Scan(&id)
	return id
}

// handleAskUpload is PUT /ask/upload?draft=<key>&name=<name>: one attachment
// for a new ask that has no run yet. The first upload for a key creates the
// held run; later ones (in parallel too) go into the same one. It answers
// what PUT /runs/{id}/upload does, plus the run.
func handleAskUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	key, name := q.Get("draft"), q.Get("name")
	switch {
	case !draftKeyRe.MatchString(key):
		xbin.WriteError(w, 400, "need ?draft=<key> (8–64 of A–Z a–z 0–9 _ -)")
		return
	case name == "":
		xbin.WriteError(w, 400, "need ?name=")
		return
	}
	c := callerOf(r)
	var id int64
	var stale []int64
	err := agent.db.Tx(func(t *DB) error {
		if id = heldDraft(t, c, key); id != 0 {
			return nil
		}
		stale = scanIDs(t.q.Query(`SELECT id FROM runs WHERE origin=? AND owner=? AND parent_id=0 AND created<?`,
			heldOrigin, c.tag(), time.Now().Add(-heldTTL).Unix()))
		st := c.stamp("chat")
		st.Origin, st.SessionKey, st.TitleSrc = heldOrigin, "held:"+key, "clip"
		var err error
		id, err = agent.startRunTx(t, runOpts{Title: "New chat", Kind: "quick", Cfg: parseConfig(t.getSetting("config")),
			Hold: true, Stamp: st, Sender: c.user})
		return err
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	for _, old := range stale { // drafts their owner never sent
		if old != id {
			_ = agent.deleteRunTree(old)
			agent.acl.flush(old)
		}
	}
	body := http.MaxBytesReader(w, r.Body, maxBinaryFileBytes+1)
	f, err := agent.acceptUpload(r.Context(), id, name, r.Header.Get("Content-Type"), body)
	if err != nil {
		code := 400
		var mbe *http.MaxBytesError
		switch {
		case err == errTooLarge || errors.As(err, &mbe):
			code, err = http.StatusRequestEntityTooLarge, errTooLarge
		case strings.Contains(err.Error(), "storing the file failed"):
			code = 502
		}
		xbin.WriteError(w, code, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"path": f.Path, "mime": f.Mime, "bytes": f.Bytes, "binary": f.Binary, "run": id})
}

var errDraftGone = errors.New("those attachments are gone (the draft was sent or expired) — remove them and attach them again")

// releaseDraft turns the held run into the new ask: titled from the text (or
// the files' names), the caller's tool mode and instructions, the first
// message queued with its files, and driven. id 0: the draft is gone.
func releaseDraft(w http.ResponseWriter, r *http.Request, id int64, key, text, toolset, title, system string, files []string) {
	if id == 0 {
		xbin.WriteError(w, 409, errDraftGone.Error())
		return
	}
	if text == "" && len(files) == 0 {
		xbin.WriteError(w, 400, "need {text} or {files}")
		return
	}
	if _, err := agent.checkAttachments(id, files); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if haltBlocks(w, r, 0) {
		return
	}
	c := callerOf(r)
	cfg := parseConfig(agent.db.getSetting("config"))
	cfg.Toolset = normalizeToolset(toolset)
	if system != "" {
		cfg.System = system
	}
	cfgJSON, _ := json.Marshal(cfg)
	titleSrc := "user"
	if title = strings.TrimSpace(title); title == "" {
		titleSrc = "clip"
		if title = text; title == "" {
			var names []string
			for _, f := range files {
				names = append(names, f[strings.LastIndex(f, "/")+1:])
			}
			title = strings.Join(names, ", ")
		}
		title = clip(title, 60)
	}
	err := agent.db.Tx(func(t *DB) error {
		if heldDraft(t, c, key) != id { // sent meanwhile (a retried Send)
			return errDraftGone
		}
		if _, err := t.q.Exec(`UPDATE runs SET title=?, title_src=?, origin=?, session_key='', config=?, activity_ms=? WHERE id=?`,
			clip(title, 120), titleSrc, c.stamp("chat").Origin, string(cfgJSON), time.Now().UnixMilli(), id); err != nil {
			return err
		}
		if system != "" { // the stored prompt, as a new ask with instructions has it
			_, _ = t.q.Exec(`UPDATE messages SET content=? WHERE run_id=? AND role='system'`, system, id)
			_, _ = t.q.Exec(`UPDATE messages_fts SET content=? WHERE msg_id IN (SELECT id FROM messages WHERE run_id=? AND role='system')`, system, id)
		}
		if _, _, err := t.enqueue(id, inboxUser, inboxBody{Text: text, Files: files, Source: "human", Sender: c.user}, ""); err != nil {
			return err
		}
		if agent.eng != nil {
			agent.eng.emitInbox(t, id, id)
			agent.eng.emitRun(t, id)
		}
		return nil
	})
	if errors.Is(err, errDraftGone) {
		xbin.WriteError(w, 409, err.Error())
		return
	}
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if agent.eng != nil {
		agent.eng.Poke(id)
	}
	agent.markRead(id, c.user) // you have seen what you just wrote
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 500, fmt.Sprintf("released run %d: %v", id, err))
		return
	}
	xbin.WriteJSON(w, 200, run)
}
