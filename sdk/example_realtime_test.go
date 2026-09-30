package xbin_test

// Realtime between partitions (docs/partitions.md §Realtime between
// partitions): the worked example of its three patterns, as one tile —
// apps/rooms, whose xbin.json declares
//
//	"partition": ["user", "global"], "partitionMail": "/mailbox"
//
// and whose scope.json declares
//
//	"board": {"type": "kv", "shared": true},  // 1: the status board, one copy
//	"live":  {"type": "bus", "shared": true}, // 1: its changes, to every reader
//	"mine":  {"type": "kv"}                   // 3: each person's own mentions
//
// docs/partitions.md quotes these functions — internal/docscheck keeps the
// quotes and this file the same — and realtime_example_test.go runs them
// against a stand-in for xbind.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Example_realtime is apps/rooms' main: the same code runs in every
// instance — each person's partition and the global instance.
func Example_realtime() {
	xbin.RequirePartition() // never one instance for everybody (§Older xbinds)
	xbin.Serve(roomsRoutes())
}

// roomsRoutes are apps/rooms' routes in this instance.
func roomsRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /board", board)       // 1: every instance
	mux.HandleFunc("POST /status", setStatus) // 1: a person's partition
	if xbin.PartitionUser() == "" {           // 2: the global instance is the hub
		h := newRooms()
		mux.HandleFunc("POST /rooms/{room}/members", h.invite)
		mux.HandleFunc("GET /rooms/{room}/follow", h.follow)
		mux.HandleFunc("POST /rooms/{room}/posts", h.post)
	}
	mux.HandleFunc("POST /mailbox", mailbox)  // 3: rung by xbind
	mux.HandleFunc("GET /mentions", mentions) // 3: a person's own
	return mux
}

// ---- 1. Tile-wide live state: a shared resource and a shared bus ----

type status struct {
	Text string `json:"text"`
}

// setStatus is POST /status in a person's partition: their line on the
// board every reader of the tile sees. The board is a "shared": true kv, one
// copy every partition writes; the event goes out on a shared bus, so it
// reaches every reader's page.
func setStatus(w http.ResponseWriter, r *http.Request) {
	me := xbin.PartitionUser()
	if me == "" { // the global instance: nobody's line
		xbin.WriteError(w, http.StatusForbidden, "set your status from your own partition")
		return
	}
	var st status
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&st); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := xbin.KV(xbin.Resource("board")).PutJSON("status/"+me, st); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := xbin.Publish(xbin.Resource("live"), "status/"+me, st); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// board is GET /board in every instance: the whole board, which a page reads
// once before it follows the bus.
func board(w http.ResponseWriter, r *http.Request) {
	kv := xbin.KV(xbin.Resource("board"))
	keys, err := kv.List("status/")
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := map[string]status{}
	for _, k := range keys {
		var st status
		if kv.GetJSON(k, &st) == nil {
			out[strings.TrimPrefix(k, "status/")] = st
		}
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// ---- 2. Member-scoped live state: the global instance as hub ----

// roomPost is one post, as the hub passes it on and mails it.
type roomPost struct {
	Room string `json:"room"`
	From string `json:"from"` // stamped by the hub from the call
	Text string `json:"text"`
}

// rooms is the global instance's hub: who is in each room — global's own
// record (in memory here; a real tile keeps it in global's data) — and the
// pages following each room.
type rooms struct {
	mu      sync.Mutex
	members map[string]map[string]bool        // room → person → a member
	follows map[string]map[chan []byte]string // room → a page's stream → its person
}

func newRooms() *rooms {
	return &rooms{members: map[string]map[string]bool{}, follows: map[string]map[chan []byte]string{}}
}

// person is who a call to the global instance acts for: a person's page,
// terminal or partition calling it through their own partition
// ({partition: 'global'}, xbin.GlobalURL). From is this tile then, but the
// caller is that person — never the tile itself. Everyone else — another
// tile, the root token, a public request, an admin viewing as someone — is
// nobody here.
func person(r *http.Request) string {
	c := xbin.Caller(r)
	if c.From != xbin.Self() || c.User == "" || c.Partition != "user:"+c.User || c.ViewedBy != "" {
		return ""
	}
	return c.User
}

// invite is POST /rooms/{room}/members at global: a member adds someone who
// can read the tile; the first call makes the room, its caller in it.
func (h *rooms) invite(w http.ResponseWriter, r *http.Request) {
	room, who := r.PathValue("room"), person(r)
	if who == "" {
		xbin.WriteError(w, http.StatusForbidden, "rooms are for people")
		return
	}
	var in struct {
		User string `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := xbin.AccessOf(r.Context(), in.User)
	if err != nil || !a.CanRead() {
		xbin.WriteError(w, http.StatusNotFound, "no such person here")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.members[room] == nil {
		h.members[room] = map[string]bool{who: true}
	}
	if !h.members[room][who] {
		xbin.WriteError(w, http.StatusForbidden, "not a member of "+room)
		return
	}
	h.members[room][a.User] = true
	w.WriteHeader(http.StatusNoContent)
}

// follow is GET /rooms/{room}/follow at global: a member's page follows the
// room here and gets each post as a line of JSON until either side closes.
func (h *rooms) follow(w http.ResponseWriter, r *http.Request) {
	room, who := r.PathValue("room"), person(r)
	ch := make(chan []byte, 64)
	h.mu.Lock()
	if !h.members[room][who] {
		h.mu.Unlock()
		xbin.WriteError(w, http.StatusForbidden, "not a member of "+room)
		return
	}
	if h.follows[room] == nil {
		h.follows[room] = map[chan []byte]string{}
	}
	h.follows[room][ch] = who
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.follows[room], ch); h.mu.Unlock() }()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	for {
		select {
		case line := <-ch:
			if _, err := w.Write(line); err != nil {
				return
			}
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// post is POST /rooms/{room}/posts at global, from a member's partition
// (xbin.GlobalURL) or page ({partition: 'global'}): the hub stamps who
// posted — from the call, never the body — passes the post to every member
// following the room, and mails each member it mentions (pattern 3).
func (h *rooms) post(w http.ResponseWriter, r *http.Request) {
	room, who := r.PathValue("room"), person(r)
	var in struct {
		Text     string   `json:"text"`
		Mentions []string `json:"mentions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := roomPost{Room: room, From: who, Text: in.Text}
	line, _ := json.Marshal(p)
	line = append(line, '\n')
	mentioned := map[string]bool{}
	h.mu.Lock()
	if !h.members[room][who] {
		h.mu.Unlock()
		xbin.WriteError(w, http.StatusForbidden, "not a member of "+room)
		return
	}
	for ch, member := range h.follows[room] {
		if h.members[room][member] { // still a member: checked at every post
			select {
			case ch <- line:
			default: // a page that can't keep up misses a post; it never stalls the room
			}
		}
	}
	for _, m := range in.Mentions {
		if m != who && h.members[room][m] {
			mentioned[m] = true
		}
	}
	h.mu.Unlock()
	for m := range mentioned {
		if _, err := xbin.MailContext(r.Context(), "user:"+m, "room/mention", p); err != nil {
			log.Printf("mention of %s in %s: %v", m, room, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// say is how a person's partition posts to a room from its own work — a long
// job reporting its progress, say: its backend calls the global instance
// (xbin.GlobalURL), and the call arrives there as this partition's person.
func say(ctx context.Context, room, text string, mentions ...string) error {
	body, err := json.Marshal(map[string]any{"text": text, "mentions": mentions})
	if err != nil {
		return err
	}
	u := xbin.GlobalURL("rooms/" + url.PathEscape(room) + "/posts")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("posting to %s: %s", room, resp.Status)
	}
	return nil
}

// ---- 3. Waking a partition: partition mail ----

// mailbox is POST /mailbox ("partitionMail": "/mailbox"): xbind rings it
// while this instance's inbox holds items, starting a person's stopped
// partition for it — one that has run before. Bob's partition keeps a
// mention in his own data and tells his phone, whether or not a page of his
// is open.
func mailbox(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	after := ""
	for {
		pg, err := xbin.InboxPageContext(ctx, after, 100)
		if err != nil { // the doorbell rings again later
			xbin.WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		var done []string
		for _, it := range pg.Items {
			after = it.ID
			if it.Topic == "room/mention" && it.From == "global" {
				if err := keepMention(ctx, it); err != nil {
					log.Printf("mention %s: %v", it.ID, err)
					continue // not acknowledged: it comes again
				}
			}
			done = append(done, it.ID) // handled, or a topic this code doesn't know
		}
		if err := xbin.AckContext(ctx, done...); err != nil {
			xbin.WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if !pg.More {
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// keepMention stores a mention in the person's own data — once: the item's
// id dedupes a delivery that comes again — and tells their phone.
func keepMention(ctx context.Context, it xbin.MailItem) error {
	mine := xbin.KV(xbin.Resource("mine"))
	if _, err := mine.Get("mention/" + it.ID); err == nil {
		return nil // an earlier ring kept it
	} else if !errors.Is(err, xbin.ErrNotFound) {
		return err
	}
	var p roomPost
	if err := json.Unmarshal(it.Data, &p); err != nil {
		return nil // nothing to keep
	}
	if err := mine.Put("mention/"+it.ID, it.Data); err != nil {
		return err
	}
	return xbin.NotifyUser(ctx, xbin.PartitionUser(), p.From+" in "+p.Room, p.Text, "#room="+url.QueryEscape(p.Room))
}

// mentions is GET /mentions in a person's partition: their own, for their
// page.
func mentions(w http.ResponseWriter, r *http.Request) {
	mine := xbin.KV(xbin.Resource("mine"))
	keys, err := mine.List("mention/")
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := []json.RawMessage{}
	for _, k := range keys {
		if b, err := mine.Get(k); err == nil {
			out = append(out, b)
		}
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}
