package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// uploadAs PUTs raw bytes through the route table as c.
func uploadAs(t *testing.T, mux *http.ServeMux, c caller, target string, body []byte, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", target, bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("X-XBin-From", c.from)
	r.Header.Set("X-XBin-Role", "admin")
	if c.user != "" {
		r.Header.Set("X-XBin-User", c.user)
		r.Header.Set("X-XBin-User-Level", c.level)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

type draftUpload struct {
	Path string `json:"path"`
	Mime string `json:"mime"`
	Run  int64  `json:"run"`
}

func decodeInto[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return v
}

// listed says whether run id shows anywhere a person looks: their
// conversation list, GET /runs, and a title search for q (a draft is "New chat").
func listed(t *testing.T, mux *http.ServeMux, c caller, id int64, q string) []string {
	t.Helper()
	var where []string
	has := func(body string) bool { return strings.Contains(body, fmt.Sprintf(`"id":%d,`, id)) }
	if has(callAs(t, mux, c, "GET", "/conversations?limit=100", nil).Body.String()) {
		where = append(where, "conversations")
	}
	if has(callAs(t, mux, c, "GET", "/runs?roots=1", nil).Body.String()) {
		where = append(where, "runs")
	}
	if has(callAs(t, mux, c, "GET", "/conversations?q="+q, nil).Body.String()) {
		where = append(where, "search")
	}
	return where
}

// The native view's held ask: files picked at home go into a run held for
// the draft key — the first upload makes it, parallel ones share it, nobody
// sees it — and POST /ask {draft, files} sends it as the new conversation.
func TestDraftAskFromHome(t *testing.T) {
	ag, mux := accessFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("what are these?"), say("two images"))
	const key = "k3y-home-0001"
	var wg sync.WaitGroup
	ups := make([]draftUpload, 3)
	for i := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := uploadAs(t, mux, asAlice, fmt.Sprintf("/ask/upload?draft=%s&name=p%d.png", key, i), pngBytes(4+i), "image/png")
			if w.Code != 200 {
				t.Errorf("upload %d: %d %s", i, w.Code, w.Body)
				return
			}
			ups[i] = decodeInto[draftUpload](t, w)
		}()
	}
	wg.Wait()
	id := ups[0].Run
	if id == 0 || ups[1].Run != id || ups[2].Run != id || ups[0].Mime != "image/png" {
		t.Fatalf("parallel uploads for one draft share one run: %+v", ups)
	}
	if got := listed(t, mux, asAlice, id, "chat"); len(got) != 0 {
		t.Fatalf("an unsent draft shows in %v", got)
	}
	// the key is the caller's: another person's upload makes their own draft
	bob := decodeInto[draftUpload](t, uploadAs(t, mux, asBob, "/ask/upload?draft="+key+"&name=x.txt", []byte("hi"), "text/plain"))
	if bob.Run == id || bob.Run == 0 {
		t.Fatalf("bob's upload went into run %d (alice's is %d)", bob.Run, id)
	}

	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "what are these?", "toolset": "web", "draft": key,
		"files": []string{ups[0].Path, ups[2].Path}}) // p1 was removed from the composer
	if w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	run := decodeInto[Run](t, w)
	if run.ID != id || run.Title != "what are these?" || run.TitleSrc != "clip" || run.Origin != "chat" || run.SessionKey != "" ||
		run.Owner != "alice" || run.Kind != "quick" {
		t.Fatalf("released run: %+v", run)
	}
	waitFor(t, "the answer", func() bool { return strings.Contains(transcript(ag.db, id), "two images") })
	if cfg, _ := ag.db.runConfig(id); cfg.Toolset != "web" {
		t.Fatalf("the tool mode chosen at Send: %q", cfg.Toolset)
	}
	msgs, _ := ag.db.messages(id, false)
	var first *Message
	for _, m := range msgs {
		if m.Role == "user" {
			first = m
			break
		}
	}
	if first == nil || !strings.Contains(first.Content, "p0.png") || !strings.Contains(first.Content, "p2.png") || strings.Contains(first.Content, "p1.png") {
		t.Fatalf("the first message carries the files sent: %+v", first)
	}
	if got := listed(t, mux, asAlice, id, "these"); len(got) != 3 {
		t.Fatalf("a sent draft is a conversation like any other: listed in %v", got)
	}

	// sent: the key is spent — files for it are gone, text alone is a new ask
	if w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "again", "draft": key, "files": []string{ups[0].Path}}); w.Code != 409 {
		t.Fatalf("a spent draft with files: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "just text", "draft": key}); w.Code != 200 || decodeInto[Run](t, w).ID == id {
		t.Fatalf("a draft with nothing uploaded is an ordinary ask: %d %s", w.Code, w.Body)
	}
}

// Files only: titled from their names. Bad requests change nothing.
func TestDraftAskRules(t *testing.T) {
	ag, mux := accessFixture(t)
	const key = "files-only-01"
	up := decodeInto[draftUpload](t, uploadAs(t, mux, asAlice, "/ask/upload?draft="+key+"&name=report.pdf", []byte("%PDF-1.4 x"), "application/pdf"))
	for _, c := range []struct {
		body any
		code int
	}{
		{map[string]any{"text": "x", "draft": "short"}, 400},
		{map[string]any{"text": "x", "draft": key, "hold": true}, 400},
		{map[string]any{"text": "x", "draft": key, "files": []string{"nope.png"}}, 400},
		{map[string]any{"draft": key}, 400},
	} {
		if w := callAs(t, mux, asAlice, "POST", "/ask", c.body); w.Code != c.code {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body)
		}
	}
	for target, code := range map[string]int{"/ask/upload?name=a.txt": 400, "/ask/upload?draft=" + key: 400, "/ask/upload?draft=a%20b%20c%20d%20e&name=a": 400} {
		if w := uploadAs(t, mux, asAlice, target, []byte("x"), "text/plain"); w.Code != code {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	if w := uploadAs(t, mux, asCron, "/ask/upload?draft="+key+"&name=a", []byte("x"), "text/plain"); w.Code != 403 {
		t.Fatalf("the scheduler: %d", w.Code)
	}
	if r, _ := ag.db.getRun(up.Run); r.Origin != heldOrigin {
		t.Fatalf("a refused release left the draft alone: %+v", r)
	}
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"draft": key, "files": []string{up.Path}, "title": ""})
	if w.Code != 200 {
		t.Fatalf("files only: %d %s", w.Code, w.Body)
	}
	if r := decodeInto[Run](t, w); r.Title != "report.pdf" || r.TitleSrc != "clip" {
		t.Fatalf("titled from its files: %+v", r)
	}
}

// A draft nobody sent is deleted, files and all, when its owner starts
// another a day later — and never someone else's.
func TestDraftAskExpires(t *testing.T) {
	ag, mux := accessFixture(t)
	old := decodeInto[draftUpload](t, uploadAs(t, mux, asAlice, "/ask/upload?draft=old-draft-1&name=a.png", pngBytes(3), "image/png"))
	bobs := decodeInto[draftUpload](t, uploadAs(t, mux, asBob, "/ask/upload?draft=old-draft-2&name=b.png", pngBytes(3), "image/png"))
	blobs := ag.blobs.(*memBlobs)
	before := blobs.count()
	for _, id := range []int64{old.Run, bobs.Run} {
		if _, err := ag.db.q.Exec(`UPDATE runs SET created=created-? WHERE id=?`, int64(heldTTL.Seconds())+60, id); err != nil {
			t.Fatal(err)
		}
	}
	fresh := decodeInto[draftUpload](t, uploadAs(t, mux, asAlice, "/ask/upload?draft=new-draft-1&name=c.txt", []byte("c"), "text/plain"))
	if fresh.Run == old.Run {
		t.Fatal("a new key is a new draft")
	}
	if _, err := ag.db.getRun(old.Run); err == nil {
		t.Fatal("alice's day-old draft is still there")
	}
	if _, err := ag.db.getRun(bobs.Run); err != nil {
		t.Fatal("bob's draft went with alice's")
	}
	if blobs.count() != before-1 {
		t.Fatalf("its attachment's blob: %d → %d", before, blobs.count())
	}
	if w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "x", "draft": "old-draft-1", "files": []string{old.Path}}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "attach them again") {
		t.Fatalf("sending an expired draft: %d %s", w.Code, w.Body)
	}
}
