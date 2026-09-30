package broker

// partitionmail_api.go — the partition mail routes (plans/partitions/04 §3;
// PD-15, S4): who may send to whom, who reads and acks an inbox.
//
//	POST /api/xbin/partitions/mail      {to, topic, data, ttl?, source?} → {ok, id}
//	GET  /api/xbin/partitions/mail      ?after=<id>&limit=<n> → {items, more}
//	POST /api/xbin/partitions/mail/ack  {ids} → {ok}
//
// The caller's own inbox is the only one it ever reads or acks, taken from
// its credential — no parameter names another:
//   - a principal of the tile acting in a person's partition (its backend,
//     that person's frames, terminals and agent sessions; the partition gate
//     stamped the partition) owns that person's inbox, and mails "global"
//     only: no person-to-person channel runs through xbind;
//   - the tile's global instance's backend (its instance token, on the
//     primary) owns the global inbox, and mails any person who is live on the
//     tile — "404 no such person here" otherwise, which says nothing more —
//     or "global";
//   - everyone else — people outside the tile's own credentials (admins
//     included), the root token, frames and terminals that act in no
//     person's partition (the owner token's), other tiles and every
//     delivery principal — 403. Admins see counts only (GET /partitions).
//
// xbind stamps from: "global" or "user:<id>", never taken from the body.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vault"
)

func (b *Broker) registerPartitionMail(srv *server.Server) {
	srv.RegisterAPI("POST /partitions/mail", b.apiMailSend)
	srv.RegisterAPI("GET /partitions/mail", b.apiMailList)
	srv.RegisterAPI("POST /partitions/mail/ack", b.apiMailAck)
}

// errMailOutsider refuses a principal that is neither a person's partition
// of a partitioned tile nor its global instance's backend.
var errMailOutsider = statusErr{http.StatusForbidden, "partition mail is sent and read only by a partitioned tile's " +
	"global instance (its backend) and by its people's partitions (their backends, frames, terminals and agent sessions)"}

// mailSelf is the inbox p owns and mails from, or why it has none.
func (b *Broker) mailSelf(p auth.Principal) (mailBox, error) {
	tile := b.publisherTile(p)
	if tile == "" || p.Impersonator != "" {
		return mailBox{}, errMailOutsider
	}
	if _, partitioned, err := b.tilePartitioning(tile); err != nil {
		return mailBox{}, statusErr{http.StatusForbidden, err.Error()}
	} else if !partitioned {
		return mailBox{}, statusErr{http.StatusForbidden, tile + " keeps no person's data apart: partition mail runs " +
			"between a partitioned tile's global instance and its people's partitions"}
	}
	if p.Partition.IsUser() { // the partition gate stamped it
		own := p
		own.Component = tile
		t, err := b.partTargetOf(own, tile)
		if err != nil {
			return mailBox{}, statusErr{http.StatusForbidden, err.Error()}
		}
		return mailBox{tile: tile, dep: t.dep, bucket: t.pkey, part: t.part, user: t.user, uid: t.uid}, nil
	}
	if p.Via != "instance" || p.Component != tile {
		return mailBox{}, errMailOutsider
	}
	part, err := b.addressedPartition(p, tile)
	if err != nil {
		return mailBox{}, statusErr{http.StatusForbidden, err.Error()}
	}
	dep, err := b.addressed(p, tile)
	switch {
	case err != nil:
		return mailBox{}, statusErr{http.StatusForbidden, err.Error()}
	case part != util.PartitionGlobal:
		return mailBox{}, errMailOutsider
	case !b.isPrimary(tile, dep):
		return mailBox{}, statusErr{http.StatusForbidden, tile + ": partition mail is the primary's; a deployment beyond it has none"}
	}
	return mailBox{tile: tile, dep: dep, bucket: mailGlobalBox, part: util.PartitionGlobal}, nil
}

// mailTo is the inbox from's mail to to reaches.
func (b *Broker) mailTo(from mailBox, to string) (mailBox, error) {
	tile := from.tile
	if to == string(util.PartitionGlobal) {
		if spec, _, _ := b.tilePartitioning(tile); !spec.Global {
			return mailBox{}, statusErr{http.StatusForbidden, tile + " has no global instance to mail"}
		}
		return mailBox{tile: tile, dep: from.dep, bucket: mailGlobalBox, part: util.PartitionGlobal}, nil
	}
	part, err := util.ParsePartition(to)
	id, isUser := part.User()
	switch {
	case err != nil || !isUser:
		return mailBox{}, statusErr{http.StatusBadRequest, `to must be "global" or "user:<id>"`}
	case from.part.IsUser():
		return mailBox{}, statusErr{http.StatusForbidden, "a person's partition mails only \"global\": no person-to-person channel runs through xbind"}
	}
	noSuch := statusErr{http.StatusNotFound, "no such person here: " + to}
	if b.personLive(id, tile) != nil {
		return mailBox{}, noSuch
	}
	uid, err := b.mintPartitionUID(id)
	if err != nil {
		return mailBox{}, noSuch
	}
	return mailBox{tile: tile, dep: from.dep, bucket: util.PartitionKey(id, uid), part: part, user: id, uid: uid}, nil
}

// writeMailErr answers err: a statusErr as it says, a sealed vault 503.
func writeMailErr(w http.ResponseWriter, err error) {
	var se statusErr
	switch {
	case errors.As(err, &se):
		server.WriteError(w, se.code, se.msg, mailDocs)
	case errors.Is(err, vault.ErrSealed):
		server.WriteError(w, http.StatusServiceUnavailable, "the vault is sealed: partition mail is sealed with it (bx vault unseal)", mailDocs)
	default:
		server.WriteError(w, http.StatusInternalServerError, "partition mail: "+err.Error(), mailDocs)
	}
}

// mailText checks a short string field: printable, valid UTF-8, at most max bytes.
func mailText(name, s string, max int) error {
	if len(s) > max || !utf8.ValidString(s) {
		return statusErr{http.StatusBadRequest, fmt.Sprintf("%s: at most %d bytes of UTF-8", name, max)}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return statusErr{http.StatusBadRequest, name + ": no control characters"}
		}
	}
	return nil
}

// apiMailSend — POST /partitions/mail.
func (b *Broker) apiMailSend(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	r.Body = http.MaxBytesReader(w, r.Body, mailItemMax+64<<10)
	var body struct {
		To     string          `json:"to"`
		Topic  string          `json:"topic"`
		Data   json.RawMessage `json:"data"`
		TTL    *int64          `json:"ttl"`
		Source string          `json:"source"`
		From   string          `json:"from"` // never read: xbind stamps from
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			server.WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a mail item is at most %d bytes (topic and data)", mailItemMax), mailDocs)
			return
		}
		server.WriteError(w, http.StatusBadRequest, err.Error(), mailDocs)
		return
	}
	from, err := b.mailSelf(p)
	if err != nil {
		writeMailErr(w, err)
		return
	}
	ttl := mailTTLDefault
	if body.TTL != nil {
		if *body.TTL < 1 || *body.TTL > int64(mailTTLMax/time.Second) {
			server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("ttl is in seconds, 1 to %d (30 days); the default is 7 days", int64(mailTTLMax/time.Second)), mailDocs)
			return
		}
		ttl = time.Duration(*body.TTL) * time.Second
	}
	switch {
	case len(body.Topic)+len(body.Data) > mailItemMax:
		server.WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a mail item is at most %d bytes (topic and data)", mailItemMax), mailDocs)
		return
	case mailText("topic", body.Topic, mailTopicMax) != nil:
		writeMailErr(w, mailText("topic", body.Topic, mailTopicMax))
		return
	case mailText("source", body.Source, mailSourceMax) != nil:
		writeMailErr(w, mailText("source", body.Source, mailSourceMax))
		return
	}
	to, err := b.mailTo(from, body.To)
	if err != nil {
		writeMailErr(w, err)
		return
	}
	id, err := b.mailPut(to, from, body.Topic, body.Data, ttl)
	if err != nil {
		writeMailErr(w, err)
		return
	}
	if body.Source != "" && from.part == util.PartitionGlobal && to.user != "" {
		// a private trigger's event handed to its person: its source counts
		// in their egress ledger (06 §6.1), never the content
		b.ledgerCount(to.tile, to.user, LedgerTrigger, body.Source)
	}
	b.ringMail(mailBellKey{to.tile, to.dep, to.bucket}, ringNew, time.Time{})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// apiMailList — GET /partitions/mail[?after=<id>&limit=<n>].
func (b *Broker) apiMailList(w http.ResponseWriter, r *http.Request) {
	box, err := b.mailSelf(auth.PrincipalOf(r))
	if err != nil {
		writeMailErr(w, err)
		return
	}
	limit := mailPageDefault
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > mailPageMax {
			server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("limit: 1 to %d", mailPageMax), mailDocs)
			return
		}
		limit = n
	}
	items, more, err := b.mailList(box, r.URL.Query().Get("after"), limit)
	if err != nil {
		writeMailErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "more": more})
}

// apiMailAck — POST /partitions/mail/ack {ids}.
func (b *Broker) apiMailAck(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), mailDocs)
		return
	}
	box, err := b.mailSelf(auth.PrincipalOf(r))
	if err != nil {
		writeMailErr(w, err)
		return
	}
	if len(body.IDs) > mailAckMax {
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("at most %d ids at once", mailAckMax), mailDocs)
		return
	}
	keys := make([][]byte, 0, len(body.IDs))
	for _, id := range body.IDs {
		k, ok := parseMailID(id)
		if !ok {
			server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a mail id", id), mailDocs)
			return
		}
		keys = append(keys, k)
	}
	left, err := b.mailAck(box, keys)
	if err != nil {
		writeMailErr(w, err)
		return
	}
	if left == 0 {
		b.forgetBell(mailBellKey{box.tile, box.dep, box.bucket})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
