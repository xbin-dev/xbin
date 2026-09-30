// handoff_fetch.go — a chat file too large for a handoff's mail (08 §5;
// the partitioned-tiles plan, 90 §I11). A mail item carries up to mailFileBudget of
// files inline; a larger one is staged instead — it waits in the global
// instance's storage, the mail names it, and the other side fetches it over
// the person's own-global calls (F5, attributed to them), after which it is
// deleted:
//
//   - in (a DM's file, global → the person's partition): the DM's staged
//     channel file stays at global, held for the handoff (handoff_files,
//     dir "in"); handoff/dm names it in `fetch`; the partition reads it
//     (GET /handoffs/{id}/files/{fid}) before the mail's transaction, keeps
//     it with the DM, and once that committed acknowledges
//     (POST /handoffs/{id}/fetched) — global deletes what it held;
//   - out (a reply's file, the person's partition → global): the partition
//     stages it at global (PUT /handoffs/{id}/reply-files?key=&name=&mime=,
//     idempotent by key) and names it in outbox/add's `staged`; global's
//     reply posts it as a staged reply file, deleted at the adapter's ack as
//     the inline ones are (purgeHandedReply).
//
// Only the handoff's person may fetch or stage (xbind stamps who calls). A
// file nobody fetched, or staged for a reply that never came, goes after
// fetchHeldTTL. Passing through global's storage is what 08 §5 documents.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fetchHeldTTL: how long global keeps a file held for a handoff — past the
// mail's own life in the inbox (7 days).
const fetchHeldTTL = handoffQueueTTL + 86400

// hoHeld names a file a handoff's other side fetches from global.
type hoHeld struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Size int    `json:"size"`
}

// --- the global instance ----------------------------------------------------------

// holdForFetch (global, mailing a DM): its staged file id is too large for
// the mail — it stays, held for the handoff, and the mail names it.
func (ag *Agent) holdForFetch(h *dmHandoff, id, name, mime string, size int) error {
	if _, err := ag.db.q.Exec(`INSERT OR IGNORE INTO handoff_files (file_id, handoff, person, dir, created)
		VALUES (?, ?, COALESCE((SELECT person FROM handoffs WHERE id=?), ''), 'in', ?)`, id, h.Handoff, h.Handoff, now()); err != nil {
		return err
	}
	h.Fetch = append(h.Fetch, hoHeld{ID: id, Name: name, Mime: mime, Size: size})
	return nil
}

// unheld is staged without the ids a DM's mail left held for a fetch: what
// is deleted once it is mailed.
func unheld(staged []string, fetch []hoHeld) []string {
	var out []string
outer:
	for _, id := range staged {
		for _, f := range fetch {
			if f.ID == id {
				continue outer
			}
		}
		out = append(out, id)
	}
	return out
}

// expireHeldFiles (global, each sender pass): files held longer than
// fetchHeldTTL — never fetched, or staged for a reply that never came — go.
func (ag *Agent) expireHeldFiles() {
	ids := scanStrings(ag.db.q.Query(`SELECT file_id FROM handoff_files WHERE created<?`, now()-fetchHeldTTL))
	if len(ids) > 0 {
		ag.dropStaged(ids)
		_, _ = ag.db.q.Exec(`DELETE FROM handoff_files WHERE created<?`, now()-fetchHeldTTL)
	}
}

func scanStrings(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}, err error) []string {
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// fetchRoutes mounts the fetch and stage routes at a partitioned agent's
// global instance.
func fetchRoutes(mux *http.ServeMux) {
	if !globalMode() {
		return
	}
	for _, rt := range []routeDef{
		{"GET /handoffs/{id}/files/{fid}", needUser, handleHandoffFile},
		{"POST /handoffs/{id}/fetched", needUser, handleHandoffFetched},
		{"PUT /handoffs/{id}/reply-files", needUser, handleReplyFilePut},
	} {
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
	}
}

// handoffOf is the route's handoff when the caller — their own partition —
// is its person (404 otherwise).
func handoffOf(w http.ResponseWriter, r *http.Request) (id string, chID int64, ok bool) {
	id = r.PathValue("id")
	var person, kind string
	err := agent.db.q.QueryRow(`SELECT person, kind, channel_id FROM handoffs WHERE id=?`, id).Scan(&person, &kind, &chID)
	if err != nil || kind != "dm" || !personFromPartition(r) || person != callerOf(r).user {
		xbin.WriteError(w, http.StatusNotFound, "no such handoff")
		return "", 0, false
	}
	return id, chID, true
}

// handleHandoffFile: GET /handoffs/{id}/files/{fid} — a file held for the
// caller's handoff.
func handleHandoffFile(w http.ResponseWriter, r *http.Request) {
	id, _, ok := handoffOf(w, r)
	if !ok {
		return
	}
	var mime, content, blob string
	if err := agent.db.q.QueryRow(`SELECT c.mime, c.content, c.blob FROM handoff_files h JOIN channel_files c ON c.id=h.file_id
		WHERE h.file_id=? AND h.handoff=? AND h.dir='in'`, r.PathValue("fid"), id).Scan(&mime, &content, &blob); err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such file: fetched already, or gone")
		return
	}
	data := []byte(content)
	if blob != "" {
		var err error
		if data, err = agent.readBlob(r.Context(), blob); err != nil {
			xbin.WriteError(w, http.StatusBadGateway, "reading the file: "+err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", orStr(mime, "application/octet-stream"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// handleHandoffFetched: POST /handoffs/{id}/fetched — the caller's
// partition has the handoff's files: what global held goes.
func handleHandoffFetched(w http.ResponseWriter, r *http.Request) {
	id, _, ok := handoffOf(w, r)
	if !ok {
		return
	}
	ids := scanStrings(agent.db.q.Query(`SELECT file_id FROM handoff_files WHERE handoff=? AND dir='in'`, id))
	agent.dropStaged(ids)
	_, _ = agent.db.q.Exec(`DELETE FROM handoff_files WHERE handoff=? AND dir='in'`, id)
	xbin.WriteJSON(w, http.StatusOK, map[string]int{"deleted": len(ids)})
}

// replyFileID is the staged id of a reply's file: the same for the same
// person and key, so a retry stages it once.
func replyFileID(person, key string) string {
	s := sha256.Sum256([]byte(person + "\x00" + key))
	return "r" + hex.EncodeToString(s[:9])
}

// handleReplyFilePut: PUT /handoffs/{id}/reply-files?key=&name=&mime= (body:
// the bytes, ≤ 16 MiB) — the caller's partition stages a reply's file too
// large for its mail; outbox/add names it in `staged`. → {id}.
func handleReplyFilePut(w http.ResponseWriter, r *http.Request) {
	id, chID, ok := handoffOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	key := q.Get("key")
	if key == "" || len(key) > 200 {
		xbin.WriteError(w, http.StatusBadRequest, "need ?key= (the reply's, unique to it)")
		return
	}
	fid := replyFileID(callerOf(r).user, key)
	var have int
	_ = agent.db.q.QueryRow(`SELECT count(*) FROM handoff_files WHERE file_id=? AND handoff=?`, fid, id).Scan(&have)
	if have > 0 {
		xbin.WriteJSON(w, http.StatusOK, map[string]string{"id": fid}) // staged before (a retry)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBinaryFileBytes+1))
	if err != nil || len(data) > maxBinaryFileBytes {
		xbin.WriteError(w, http.StatusRequestEntityTooLarge, errTooLarge.Error())
		return
	}
	of, err := stageReplyFileAs(r.Context(), fid, chID, hoFile{Name: q.Get("name"), Mime: q.Get("mime"), Data: data})
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "storing the file: "+err.Error())
		return
	}
	if _, err := agent.db.q.Exec(`INSERT OR IGNORE INTO handoff_files (file_id, handoff, person, dir, created) VALUES (?, ?, ?, 'out', ?)`,
		fid, id, callerOf(r).user, now()); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"id": fid, "name": of.Name, "bytes": of.Bytes})
}

// stageReplyFileAs stores a staged reply file under id (channel_files, as
// stageReplyFile does for an inline one).
func stageReplyFileAs(ctx context.Context, id string, chID int64, f hoFile) (outFile, error) {
	name := sanitizeUploadName(orStr(f.Name, "file"))
	mime, text := mailFileKind(f)
	content, blob := "", ""
	if text {
		content = string(f.Data)
	} else {
		blob = fmt.Sprintf("chanfiles/%d/%s", chID, id)
		if err := agent.blobs.Put(ctx, blob, f.Data, mime); err != nil {
			return outFile{}, err
		}
	}
	if _, err := agent.db.q.Exec(`INSERT OR IGNORE INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, chID, name, mime, len(f.Data), content, blob, now()); err != nil {
		return outFile{}, err
	}
	return outFile{Name: name, Mime: mime, Bytes: len(f.Data), Path: stagedPrefix + id}, nil
}

// takeStagedReplies (global, handleOutboxAdd): the files a person's reply
// staged (their own, for this handoff) as the outbox row's; they are the
// row's now (deleted at its ack).
func takeStagedReplies(t *DB, handoff, person string, ids []string) []outFile {
	var out []outFile
	for _, id := range ids {
		var name, mime string
		var size int
		if err := t.q.QueryRow(`SELECT c.name, c.mime, c.size FROM handoff_files h JOIN channel_files c ON c.id=h.file_id
			WHERE h.file_id=? AND h.handoff=? AND h.person=? AND h.dir='out'`, id, handoff, person).Scan(&name, &mime, &size); err != nil {
			logf("outbox/add for handoff %s: staged file %s isn't %s's for it — left out", handoff, id, person)
			continue
		}
		_, _ = t.q.Exec(`DELETE FROM handoff_files WHERE file_id=?`, id)
		out = append(out, outFile{Name: name, Mime: mime, Bytes: size, Path: stagedPrefix + id})
	}
	return out
}

// --- a person's partition -------------------------------------------------------------

// fetchedFiles are the files prepareMail fetched for a DM's `fetch`, and
// what the DM's text says about any that were gone.
type fetchedFiles struct {
	files []hoFile
	note  string
}

// fetchHeld reads a DM's held files from global (prepareMail, before the
// transaction; stored ahead like inline ones from index base). An error is
// the network's or the blob store's: the item stays for the next pull.
func fetchHeld(ctx context.Context, p *preparedFiles, handoff string, fetch []hoHeld, base int, sum string) {
	var gone []string
	for j, f := range fetch {
		res, err := exportAtGlobal(ctx, "/handoffs/"+url.PathEscape(handoff)+"/files/"+url.PathEscape(f.ID))
		if err == nil && res.Status == http.StatusNotFound {
			gone = append(gone, fmt.Sprintf("%s (%s)", f.Name, humanBytes(f.Size)))
			continue
		}
		if err == nil && res.Status != http.StatusOK {
			err = fmt.Errorf("HTTP %d %s", res.Status, clip(string(res.Body), 200))
		}
		if err != nil {
			p.err = fmt.Errorf("fetching %s from the global instance: %w", f.Name, err)
			return
		}
		hf := hoFile{Name: f.Name, Mime: orStr(f.Mime, res.Type), Data: res.Body}
		if mime, text := mailFileKind(hf); !text {
			path := fmt.Sprintf("handed/%s/%d", sum, base+j)
			if err := agent.blobs.Put(ctx, path, hf.Data, mime); err != nil {
				p.err = err
				return
			}
			p.blobs[base+len(p.fetched.files)] = path
		}
		p.fetched.files = append(p.fetched.files, hf)
	}
	if len(gone) > 0 {
		p.fetched.note = "\n\n[not passed on — gone before your space could fetch it: " + strings.Join(gone, ", ") + "]"
	}
	p.ackHandoff = handoff
}

// takeFetched (a person's partition, handling a handoff/dm): the DM's
// fetched files join its inline ones — in the order prepareMail stored
// them — and a note on any that were gone joins its text.
func takeFetched(ctx context.Context, h *dmHandoff) error {
	p, _ := ctx.Value(preparedKey{}).(*preparedFiles)
	if p == nil || len(h.Fetch) == 0 {
		return nil
	}
	if p.err != nil {
		return p.err
	}
	h.Files = append(h.Files, p.fetched.files...)
	h.Text += p.fetched.note
	return nil
}

// ackFetched (a person's partition, once the DM's transaction committed):
// global may delete what it held for the handoff. A failure leaves them to
// global's fetchHeldTTL.
func ackFetched(handoff string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if res, err := callGlobal(ctx, http.MethodPost, "/handoffs/"+url.PathEscape(handoff)+"/fetched", nil, ""); err != nil || res.Status/100 != 2 {
			logf("handoff %s: acknowledging its fetched files: %v (HTTP %d) — the global instance deletes them later", handoff, err, res.Status)
		}
	}()
}

// stageReply (a person's partition, mailing a reply): a file too large for
// the mail is staged at global for the reply (by the row's key and the
// file's place, so a retry stages it once) → its staged id. errStageRefused:
// global won't take it (the handoff is unknown there, the file too large) —
// the reply goes without it.
func stageReply(ctx context.Context, handoff, key string, f hoFile) (string, error) {
	q := url.Values{"key": {key}, "name": {f.Name}, "mime": {f.Mime}}
	res, err := callGlobal(ctx, http.MethodPut, "/handoffs/"+url.PathEscape(handoff)+"/reply-files?"+q.Encode(), f.Data, "application/octet-stream")
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		err := fmt.Errorf("staging %s at the global instance: HTTP %d %s", f.Name, res.Status, clip(string(res.Body), 200))
		if res.Status/100 == 4 && res.Status != http.StatusConflict && res.Status != http.StatusTooManyRequests {
			err = fmt.Errorf("%w: %v", errStageRefused, err)
		}
		return "", err
	}
	var out struct{ ID string }
	if json.Unmarshal(res.Body, &out) != nil || out.ID == "" {
		return "", fmt.Errorf("staging %s: no id in %s", f.Name, clip(string(res.Body), 200))
	}
	return out.ID, nil
}

// errStageRefused: global's final "no" to staging a reply's file.
var errStageRefused = errors.New("refused")

// replyFileKey is the stage key of a reply row's file i.
func replyFileKey(rowKey string, i int) string { return rowKey + "/" + strconv.Itoa(i) }
