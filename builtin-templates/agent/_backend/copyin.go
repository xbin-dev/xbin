// copyin.go — "Add a copy of my …" (PD-32): the non-hosting way to bring
// something of a person's own into a shared conversation (API.md "Non-secure
// conversations"). The person's partition reads the items from its own
// store and posts copies to the global instance, attributed to them (F5);
// the global instance keeps them in the conversation as its session files.
// The conversation stays an ordinary shared one, run by the global
// instance: no host, no pause, no non-secure chip — and the originals stay
// private, where they were.
//
//	POST /copyin {conversation, files: [{run, path}]}   a person's partition
//	POST /runs/{id}/copyin {files: [bundleFile]}        the global instance
//
// v1 copies session files of the person's own conversations. A memory
// item, a skill, a document from another tile or a file from a sandbox are
// future items of the same route.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// maxCopyInItems bounds one copy-in.
const maxCopyInItems = 20

// copyInItem names one of a person's own session files.
type copyInItem struct {
	Run  int64  `json:"run"`
	Path string `json:"path"`
}

// handleCopyIn: POST /copyin {conversation, files} in a person's partition —
// copies of their own session files go to a shared conversation.
func handleCopyIn(w http.ResponseWriter, r *http.Request) {
	if !userMode() {
		xbin.WriteError(w, 404, "copies come from a person's own space (their partition of a partitioned agent)")
		return
	}
	c := callerOf(r)
	if c.kind != whoUser || c.user != runUser || c.viewedBy != "" {
		xbin.WriteError(w, 403, "only "+runUser+" adds copies of their own things")
		return
	}
	var body struct {
		Conversation int64        `json:"conversation"`
		Files        []copyInItem `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Conversation <= 0 || len(body.Files) == 0 {
		xbin.WriteError(w, 400, "need {conversation, files: [{run, path}]}")
		return
	}
	if body.Conversation >= teamIDBase {
		xbin.WriteError(w, 409, "copies go to a shared conversation of the shared space (not a non-secure or a private one)")
		return
	}
	if len(body.Files) > maxCopyInItems {
		xbin.WriteError(w, 400, fmt.Sprintf("at most %d files at a time", maxCopyInItems))
		return
	}
	var files []bundleFile
	total := 0
	for _, it := range body.Files {
		f, err := ownFile(r.Context(), it)
		if err != nil {
			xbin.WriteError(w, 404, err.Error())
			return
		}
		total += len(f.Content) + len(f.Data)
		if total > maxBundleFiles {
			writeBundleErr(w, tooLargeToCopy{})
			return
		}
		files = append(files, f)
	}
	req, err := bundleJSON(map[string]any{"files": files}, true)
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	res, err := callGlobal(r.Context(), http.MethodPost, fmt.Sprintf("/runs/%d/copyin", body.Conversation), req, "application/json")
	if err != nil {
		xbin.WriteError(w, 502, "the shared instance didn't answer: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", orStr(res.Type, "application/json"))
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// ownFile reads one of this person's session files (their own conversation's).
func ownFile(ctx context.Context, it copyInItem) (bundleFile, error) {
	missing := fmt.Errorf("%s: no such file in your conversation #%d", it.Path, it.Run)
	if it.Run < partitionIDBase {
		return bundleFile{}, missing
	}
	p, err := normReplPath(it.Path)
	if err != nil {
		return bundleFile{}, missing
	}
	run, err := agent.db.getRun(it.Run)
	if err != nil {
		return bundleFile{}, missing
	}
	f, err := agent.db.replFile(rootOf(run), p)
	if err != nil {
		return bundleFile{}, missing
	}
	out := bundleFile{Path: f.Path, Mime: f.Mime, Binary: f.Binary}
	if !f.Binary {
		out.Content = f.Content
		return out, nil
	}
	if out.Data, err = agent.readBlob(ctx, f.Blob); err != nil {
		return bundleFile{}, fmt.Errorf("%s: %v", f.Path, err)
	}
	return out, nil
}

// handleCopyInAt: POST /runs/{id}/copyin {files} at the global instance — a
// participant adds copies of their own things to a shared conversation.
// They land under from-<person>/ in its session files, and a note in it
// says who added what.
func handleCopyInAt(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, 404, "copies go to the agent's shared instance")
		return
	}
	c := callerOf(r)
	if c.kind != whoUser {
		xbin.WriteError(w, 403, "a person adds copies of their own things")
		return
	}
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	var body struct {
		Files []bundleFile `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxBundleBytes))).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeBundleErr(w, tooLargeToCopy{})
			return
		}
		xbin.WriteError(w, 400, "need {files: [{path, content | data}]}")
		return
	}
	if len(body.Files) == 0 || len(body.Files) > maxCopyInItems {
		xbin.WriteError(w, 400, fmt.Sprintf("between 1 and %d files", maxCopyInItems))
		return
	}
	var added []string
	for _, f := range body.Files {
		name := path.Base(strings.TrimSpace(f.Path))
		p, err := normReplPath("from-" + c.user + "/" + name)
		if err != nil || name == "." || name == "/" {
			xbin.WriteError(w, 400, "a file's path: "+f.Path)
			return
		}
		src := &fileSource{Kind: "copy", Path: f.Path}
		var got *ReplFile
		if f.Binary {
			got, err = agent.acceptUploadSrc(r.Context(), root, p, f.Mime, io.Reader(bytes.NewReader(f.Data)), src)
		} else {
			got, err = agent.db.replPutFileSrc(root, p, f.Content, 0, src)
			if err == nil && f.Mime != "" {
				_, _ = agent.db.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, f.Mime, root, got.Path)
			}
		}
		if err != nil {
			xbin.WriteError(w, 400, fmt.Sprintf("copying %s: %v", f.Path, err))
			return
		}
		added = append(added, got.Path)
	}
	_ = agent.db.Tx(func(t *DB) error {
		t.journal(root, "note", map[string]string{"text": fmt.Sprintf("%s added a copy of %s from their own space (the original stays private). "+
			"Everyone who can read this conversation can read the copy.", c.user, strings.Join(added, ", "))})
		if agent.eng != nil {
			agent.eng.emitRun(t, root)
		}
		return nil
	})
	agent.db.bumpActivity(root)
	xbin.WriteJSON(w, 200, map[string]any{"conversation": root, "files": added})
}
