package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMail is a drop box in memory. page, when set, cuts every page to that
// many items and says more wait (xbind's ~8 MiB cut: a short page isn't the
// end).
type fakeMail struct {
	mu     sync.Mutex
	items  []mailItem
	acked  []string
	page   int
	reads  int
	ackErr error
}

func (f *fakeMail) Page(_ context.Context, after string, limit int) ([]mailItem, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.page > 0 && f.page < limit {
		limit = f.page
	}
	var out []mailItem
	more := false
	for _, it := range f.items {
		if it.ID <= after {
			continue
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, it)
	}
	return out, more, nil
}

func (f *fakeMail) Ack(_ context.Context, ids ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked = append(f.acked, ids...)
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	var keep []mailItem
	for _, it := range f.items {
		if !gone[it.ID] {
			keep = append(keep, it)
		}
	}
	f.items = keep
	return nil
}

// useMail installs fm as the drop box and a test/hello handler recording
// what it applied; it answers the handled ids.
func useMail(t *testing.T, fm mailSource) *[]string {
	t.Helper()
	old := partitionMail
	partitionMail = fm
	var seen []string
	mailHandlers["test/hello"] = func(_ context.Context, tx *DB, it mailItem) error {
		seen = append(seen, it.ID)
		return tx.putSetting("mail_test_"+it.ID, string(it.Data))
	}
	t.Cleanup(func() {
		partitionMail = old
		delete(mailHandlers, "test/hello")
		mailMu.Lock()
		mailUnknown = map[string]time.Time{}
		mailMu.Unlock()
	})
	return &seen
}

// TestMailboxSkeleton: the doorbell pulls, hands each item to its topic's
// handler once (mail_seen), acks it, and leaves an unknown topic while it is
// young; only xbind (or the tile itself) rings it.
func TestMailboxSkeleton(t *testing.T) {
	ag, h := partAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer"}))
	if rec.Code != 404 {
		t.Fatalf("an unpartitioned agent's mailbox: %d", rec.Code)
	}
	setMode(t, modeUser, "alice")
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "global", Topic: "test/hello", Data: json.RawMessage(`{"n":1}`), At: time.Now()},
		{ID: "002", From: "global", Topic: "later/unknown", At: time.Now()},
		{ID: "003", From: "global", Topic: "test/hello", Data: json.RawMessage(`{"n":3}`), At: time.Now()},
	}}
	seen := useMail(t, fm)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{"partition":"user:alice","pending":3}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice"}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"handled":2`) || !strings.Contains(rec.Body.String(), `"left":1`) {
		t.Fatalf("the doorbell: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(*seen, ",") != "001,003" || strings.Join(fm.acked, ",") != "001,003" || ag.db.getSetting("mail_test_003") != `{"n":3}` {
		t.Fatalf("handled %v, acked %v", *seen, fm.acked)
	}
	// delivered again (an ack lost): acked, not applied twice
	fm.items = append(fm.items, mailItem{ID: "001", From: "global", Topic: "test/hello"})
	if _, err := ag.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || len(fm.items) != 1 || fm.items[0].ID != "002" {
		t.Fatalf("a redelivery: handled %v, left %v", *seen, fm.items)
	}
	for name, hdr := range map[string]map[string]string{
		"another tile": {"X-XBin-From": "apps/other", "X-XBin-Role": "writer"},
		"no headers":   {},
		"a person":     alicesFrame("write"),
	} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, hdr))
		if rec.Code != 403 {
			t.Errorf("%s rang the doorbell: %d", name, rec.Code)
		}
	}
	// at global, a person's partition's call without its person is nobody —
	// never the tile itself, here as on every other route
	setMode(t, modeGlobal, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-Partition": "user:bob"}))
	if rec.Code != 403 {
		t.Fatalf("a person's partition without its person rang global's doorbell: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin"}))
	if rec.Code != 200 {
		t.Fatalf("the tile itself rang the doorbell: %d %s", rec.Code, rec.Body)
	}
}

// TestMailboxPages: a pull reads on while more wait — a page cut short
// isn't the end — acks each page in one call, handles every item once, and
// stops at an ack that fails (the items stay for the next pull).
func TestMailboxPages(t *testing.T) {
	ag, _ := partAgent(t)
	setMode(t, modeUser, "alice")
	fm := &fakeMail{page: 3}
	for i := 1; i <= 8; i++ {
		fm.items = append(fm.items, mailItem{ID: "i" + strconv.Itoa(i), From: "global", Topic: "test/hello", Data: json.RawMessage(strconv.Itoa(i)), At: time.Now()})
	}
	seen := useMail(t, fm)
	c, err := ag.pullMail(context.Background())
	if err != nil || c.Handled != 8 || c.Left != 0 || len(fm.items) != 0 || fm.reads != 3 {
		t.Fatalf("paging: %+v %v, left %v, %d reads", c, err, fm.items, fm.reads)
	}
	if len(*seen) != 8 || len(fm.acked) != 8 {
		t.Fatalf("handled %v, acked %v", *seen, fm.acked)
	}
	// an ack that fails stops the pull; the next one only acks (mail_seen)
	fm.items = []mailItem{{ID: "j1", From: "global", Topic: "test/hello", At: time.Now()}, {ID: "j2", From: "global", Topic: "test/hello", At: time.Now()}}
	fm.ackErr = errors.New("xbind is away")
	if _, err := ag.pullMail(context.Background()); err == nil {
		t.Fatal("a failed ack wasn't reported")
	}
	fm.ackErr = nil
	c, err = ag.pullMail(context.Background())
	if err != nil || c.Handled != 0 || len(fm.items) != 0 || len(*seen) != 10 {
		t.Fatalf("after the failed ack: %+v %v, handled %v", c, err, *seen)
	}
}

// TestMailboxUnknownTopics: an item no handler knows stays in the inbox
// while it is young (a newer version mid-deploy may be the one to read it)
// and is acked unhandled once it is past mailUnknownGrace — never left to
// wake the partition for its whole ttl.
func TestMailboxUnknownTopics(t *testing.T) {
	ag, _ := partAgent(t)
	setMode(t, modeUser, "alice")
	fm := &fakeMail{items: []mailItem{
		{ID: "k1", From: "global", Topic: "newer/topic", At: time.Now().Add(-mailUnknownGrace - time.Minute)},
		{ID: "k2", From: "global", Topic: "newer/topic", At: time.Now()},
		{ID: "k3", From: "global", Topic: "newer/topic"}, // no time: counted from its first sight here
	}}
	useMail(t, fm)
	c, err := ag.pullMail(context.Background())
	if err != nil || c.Dropped != 1 || c.Left != 2 || strings.Join(fm.acked, ",") != "k1" {
		t.Fatalf("first pull: %+v %v, acked %v", c, err, fm.acked)
	}
	// a pull later: still young
	if c, _ := ag.pullMail(context.Background()); c.Left != 2 || len(fm.items) != 2 {
		t.Fatalf("second pull: %+v, left %v", c, fm.items)
	}
	// past the grace (k3 from when it was first seen)
	mailMu.Lock()
	mailUnknown["k3"] = time.Now().Add(-mailUnknownGrace)
	mailMu.Unlock()
	fm.items[0].At = time.Now().Add(-mailUnknownGrace)
	if c, _ := ag.pullMail(context.Background()); c.Dropped != 2 || len(fm.items) != 0 {
		t.Fatalf("past the grace: %+v, left %v", c, fm.items)
	}
}

// TestMailboxSDK: the drop box is xbind's partition mail through the SDK —
// GET /api/xbin/partitions/mail with after and limit, read on while more,
// then POST …/mail/ack with the page's ids; an older xbind (404) is an error
// the doorbell reports.
func TestMailboxSDK(t *testing.T) {
	ag, h := partAgent(t)
	setMode(t, modeUser, "alice")
	var mu sync.Mutex
	inbox := []map[string]any{
		{"id": "0001", "from": "global", "topic": "test/hello", "data": map[string]int{"n": 1}, "at": time.Now(), "expires": time.Now().Add(time.Hour)},
		{"id": "0002", "from": "global", "topic": "test/hello", "data": map[string]int{"n": 2}, "at": time.Now(), "expires": time.Now().Add(time.Hour)},
	}
	var gets []string
	var acks [][]string
	gone := false
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case gone:
			http.NotFound(w, r)
		case r.Method == "GET" && r.URL.Path == "/api/xbin/partitions/mail":
			gets = append(gets, r.URL.RawQuery)
			var page []map[string]any
			for _, it := range inbox {
				if it["id"].(string) > r.URL.Query().Get("after") {
					page = append(page, it)
				}
			}
			more := len(page) > 1
			if more { // one item a page: the ~8 MiB cut
				page = page[:1]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": page, "more": more})
		case r.Method == "POST" && r.URL.Path == "/api/xbin/partitions/mail/ack":
			var body struct{ IDs []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			acks = append(acks, body.IDs)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 500)
		}
	}))
	seen := useMail(t, sdkMail{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{"partition":"user:alice","pending":2}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice"}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"handled":2`) {
		t.Fatalf("the doorbell: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if strings.Join(*seen, ",") != "0001,0002" || len(gets) != 2 || gets[0] != "limit=50" || gets[1] != "after=0001&limit=50" ||
		len(acks) != 2 || acks[0][0] != "0001" || acks[1][0] != "0002" || ag.db.getSetting("mail_test_0002") != `{"n":2}` {
		t.Errorf("handled %v, reads %q, acks %v", *seen, gets, acks)
	}
	gone = true
	mu.Unlock()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer"}))
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "partition-mail/1") {
		t.Errorf("an xbind without partition mail: %d %s", rec.Code, rec.Body)
	}
}
