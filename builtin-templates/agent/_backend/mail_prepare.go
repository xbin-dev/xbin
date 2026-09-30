// mail_prepare.go — a mail item's files go to the blob store before its
// handler's transaction (mailbox.go). The agent's database has one
// connection, so an upload inside the transaction would hold every other
// writer — channel intake, the adapter's acks — for as long as it takes.
//
// The objects are named after the item (handed/<item>/<i>), so an item
// whose transaction failed, pulled again, writes the same objects rather
// than new ones; what the handler didn't keep, and everything of a
// transaction that failed, is deleted after it. Only the two topics that
// carry files are prepared: handoff/dm in a person's partition — whose
// files too large for the mail are fetched from the global instance here
// too (handoff_fetch.go) — and outbox/add at the global instance.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// preparedFiles are an item's files stored ahead: index → object (binary
// files only), which of them the handler kept, and a store failure (the
// handler returns it: the item stays for the next pull).
type preparedFiles struct {
	blobs      map[int]string
	kept       map[int]bool
	err        error
	fetched    fetchedFiles // a DM's files fetched from global (handoff_fetch.go)
	ackHandoff string       // …whose handoff global may drop them for, once committed
}

type preparedKey struct{}

// prepareMail stores the binary files of an item a handler will take, and
// returns the handler's context and the step that settles them once the
// transaction committed (or didn't).
func prepareMail(ctx context.Context, it mailItem) (context.Context, func(committed bool)) {
	var files []hoFile
	var h dmHandoff
	switch {
	case it.Topic == topicDM && userMode() && it.From == "global":
		if json.Unmarshal(it.Data, &h) == nil {
			files = h.Files
		}
	case it.Topic == topicOutbox && globalMode() && strings.HasPrefix(it.From, "user:"):
		var o outboxAddItem
		if json.Unmarshal(it.Data, &o) == nil {
			files = o.Files
		}
	}
	if len(files) == 0 && len(h.Fetch) == 0 || agent == nil {
		return ctx, func(bool) {}
	}
	p := &preparedFiles{blobs: map[int]string{}, kept: map[int]bool{}}
	sum := sha256.Sum256([]byte(it.ID))
	for i, f := range files {
		mime, text := mailFileKind(f)
		if text {
			continue
		}
		path := fmt.Sprintf("handed/%s/%d", hex.EncodeToString(sum[:12]), i)
		if err := agent.blobs.Put(ctx, path, f.Data, mime); err != nil {
			p.err = err
			break
		}
		p.blobs[i] = path
	}
	if p.err == nil && len(h.Fetch) > 0 {
		fetchHeld(ctx, p, h.Handoff, h.Fetch, len(files), hex.EncodeToString(sum[:12])) // handoff_fetch.go
	}
	return context.WithValue(ctx, preparedKey{}, p), func(committed bool) {
		if committed && p.ackHandoff != "" {
			ackFetched(p.ackHandoff)
		}
		var drop []string
		for i, path := range p.blobs {
			if !committed || !p.kept[i] {
				drop = append(drop, path)
			}
		}
		agent.dropBlobs(drop)
	}
}

// preparedBlob is the object prepareMail stored for file i, now kept by
// the handler — "" when there is none (its caller stores the file itself);
// an error when storing failed.
func preparedBlob(ctx context.Context, i int) (string, error) {
	p, _ := ctx.Value(preparedKey{}).(*preparedFiles)
	if p == nil {
		return "", nil
	}
	if p.err != nil {
		return "", p.err
	}
	path := p.blobs[i]
	if path != "" {
		p.kept[i] = true
	}
	return path, nil
}

// mailFileKind is a mailed file's type, and whether it is kept as text in
// the database (anything else goes to the blob store).
func mailFileKind(f hoFile) (mime string, text bool) {
	mime = normalizeMime(f.Mime, f.Data[:min(len(f.Data), 512)])
	return mime, isTextMime(mime) && len(f.Data) <= maxReplFileBytes && utf8.Valid(f.Data)
}
