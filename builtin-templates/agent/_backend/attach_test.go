package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// memBlobs is the in-memory blob store the tests use in place of the gateway.
type memBlobs struct {
	mu      sync.Mutex
	objs    map[string][]byte
	gets    int
	failPut error
}

func newMemBlobs() *memBlobs { return &memBlobs{objs: map[string][]byte{}} }

func (m *memBlobs) Put(_ context.Context, p string, data []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut != nil {
		return m.failPut
	}
	m.objs[p] = append([]byte(nil), data...)
	return nil
}

func (m *memBlobs) Get(_ context.Context, p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	b, ok := m.objs[p]
	if !ok {
		return nil, fmt.Errorf("blob get: 404")
	}
	return b, nil
}

func (m *memBlobs) Delete(_ context.Context, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, p)
	return nil
}

func (m *memBlobs) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs)
}

// pngBytes is a real PNG header followed by filler, so sniffing agrees.
func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, n)...)
	return b
}

func upload(t *testing.T, ag *Agent, runID int64, name, mime string, data []byte) *ReplFile {
	t.Helper()
	f, err := ag.acceptUpload(context.Background(), runID, name, mime, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return f
}

// attachTo writes a user message carrying these files, the way handleMessage does.
func attachTo(t *testing.T, ag *Agent, runID int64, text string, paths ...string) int64 {
	t.Helper()
	files, err := ag.checkAttachments(runID, paths)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ag.db.addMessage(&Message{RunID: runID, Role: "user", Content: text + attachmentNote(files)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.linkMessageFiles(runID, id, files); err != nil {
		t.Fatal(err)
	}
	return id
}

// useGlobalAgent points the handlers' package-level agent at ag for one test.
func useGlobalAgent(t *testing.T, ag *Agent) {
	prev := agent
	agent = ag
	t.Cleanup(func() { agent = prev })
}

func serve(h http.HandlerFunc, method, target string, runID int64, body []byte, ctype string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.SetPathValue("id", fmt.Sprint(runID))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// --- uploads ------------------------------------------------------------

// Text stays in sqlite, where file_read, the REPL and the render pane use it;
// nothing goes to the blob store.
func TestUploadTextStaysText(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	blobs := ag.blobs.(*memBlobs)
	id, _ := db.createRun("t", "", 0)

	f := upload(t, ag, id, "sales.csv", "text/csv; charset=utf-8", []byte("a,b\n1,2\n"))
	if f.Binary || f.Mime != "text/csv" {
		t.Fatalf("a CSV should be stored as text/csv, got binary=%v mime=%q", f.Binary, f.Mime)
	}
	got, err := db.replFile(id, "sales.csv")
	if err != nil || got.Content != "a,b\n1,2\n" || got.Mime != "text/csv" {
		t.Fatalf("stored row = %+v, %v", got, err)
	}
	if blobs.count() != 0 {
		t.Fatal("text must not go to the blob store")
	}

	// Invalid UTF-8 claiming to be text is not text.
	f = upload(t, ag, id, "weird.txt", "text/plain", []byte{0xff, 0xfe, 0x00, 0x01})
	if !f.Binary {
		t.Fatal("bytes that are not UTF-8 must be stored as binary, whatever the browser claims")
	}
}

// Binary bytes go to the blob store; the row keeps only metadata, and the raw
// route serves the bytes back with their type and nosniff.
func TestUploadBinaryGoesToBlob(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	blobs := ag.blobs.(*memBlobs)
	id, _ := db.createRun("t", "", 0)

	img := pngBytes(100)
	w := serve(handleUpload, "PUT", "/runs/x/upload?name=Screen%20Shot%20(1).png", id, img, "") // no type: sniffed
	if w.Code != 200 {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	var res struct {
		Path   string
		Mime   string
		Bytes  int
		Binary bool
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Path != "Screen-Shot-1.png" || res.Mime != "image/png" || !res.Binary || res.Bytes != len(img) {
		t.Fatalf("upload result = %+v", res)
	}
	row, _ := db.replFile(id, res.Path)
	if row.Content != "" || row.Blob == "" || !strings.HasPrefix(row.Blob, fmt.Sprintf("runs/%d/", id)) {
		t.Fatalf("a binary row must hold only metadata, got %+v", row)
	}
	if blobs.count() != 1 {
		t.Fatalf("blob store has %d objects, want 1", blobs.count())
	}

	w = serve(handleRaw, "GET", "/runs/x/raw?path="+res.Path, id, nil, "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), img) {
		t.Fatalf("raw = %d, %d bytes", w.Code, w.Body.Len())
	}
	if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("raw headers = %v", w.Header())
	}
}

func TestSanitizeUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"photo.png":              "photo.png",
		"../../etc/passwd":       "passwd",
		`C:\Users\me\shot.png`:   "shot.png",
		"my photo (1).png":       "my-photo-1.png",
		"...hidden":              "hidden",
		"":                       "upload",
		"ünïcødé.txt":            "n-c-d.txt",
		strings.Repeat("a", 300): strings.Repeat("a", 100),
	} {
		got := sanitizeUploadName(in)
		if got != want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
		if _, err := normReplPath(got); err != nil {
			t.Errorf("sanitized %q is still not a valid path: %v", got, err)
		}
	}
}

// Uploads never overwrite: the model may already have referred to the file,
// and blob objects are immutable.
func TestUploadCollisionGetsASuffix(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	a := upload(t, ag, id, "a.png", "image/png", pngBytes(10))
	b := upload(t, ag, id, "a.png", "image/png", pngBytes(20))
	c := upload(t, ag, id, "a.png", "image/png", pngBytes(30))
	if a.Path != "a.png" || b.Path != "a-2.png" || c.Path != "a-3.png" {
		t.Fatalf("paths = %s %s %s", a.Path, b.Path, c.Path)
	}
	// A text upload onto a model-written file is a new file too.
	_, _ = db.replPutFile(id, "notes.md", "mine", 0)
	n := upload(t, ag, id, "notes.md", "text/markdown", []byte("theirs"))
	if n.Path != "notes-2.md" {
		t.Fatalf("text collision path = %s", n.Path)
	}
	if f, _ := db.replFile(id, "notes.md"); f.Content != "mine" {
		t.Fatal("an upload overwrote the model's file")
	}
}

func TestUploadTooLargeIs413(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	blobs := ag.blobs.(*memBlobs)
	id, _ := db.createRun("t", "", 0)

	w := serve(handleUpload, "PUT", "/runs/x/upload?name=big.bin", id, make([]byte, maxBinaryFileBytes+1), "application/octet-stream")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload = %d %s, want 413", w.Code, w.Body)
	}
	if files, _ := db.replFiles(id); len(files) != 0 || blobs.count() != 0 {
		t.Fatalf("a refused upload left %d rows and %d objects", len(files), blobs.count())
	}
	if w := serve(handleUpload, "PUT", "/runs/x/upload?name=a.png", 9999, pngBytes(1), "image/png"); w.Code != 404 {
		t.Fatalf("upload to a missing run = %d, want 404", w.Code)
	}
	if w := serve(handleUpload, "PUT", "/runs/x/upload", id, pngBytes(1), "image/png"); w.Code != 400 {
		t.Fatalf("upload without a name = %d, want 400", w.Code)
	}
}

// A failure on either side of an upload leaves nothing behind: no row when the
// blob write fails, no object when the row is refused.
func TestUploadFailureLeavesNothing(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	blobs := ag.blobs.(*memBlobs)
	id, _ := db.createRun("t", "", 0)

	blobs.failPut = errors.New("gateway down")
	if _, err := ag.acceptUpload(context.Background(), id, "a.png", "image/png", bytes.NewReader(pngBytes(10))); err == nil ||
		!strings.Contains(err.Error(), "storing the file failed") {
		t.Fatalf("a failed blob put should fail the upload, got %v", err)
	}
	if files, _ := db.replFiles(id); len(files) != 0 {
		t.Fatal("a failed blob put left a row pointing at nothing")
	}
	blobs.failPut = nil

	// Fill the run's attachment budget, then overflow it.
	for i := 0; i < maxBinaryRunBytes/maxBinaryFileBytes; i++ {
		size := maxBinaryFileBytes
		if i == 0 {
			size -= 100
		}
		if _, err := db.replPutBinary(id, fmt.Sprintf("huge%d.bin", i), "application/octet-stream", size, "runs/x/fake"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ag.acceptUpload(context.Background(), id, "b.png", "image/png", bytes.NewReader(pngBytes(200))); err == nil {
		t.Fatal("an upload over the per-run budget should be refused")
	}
	if blobs.count() != 0 {
		t.Fatalf("the refused row's object was left behind (%d objects)", blobs.count())
	}
}

// Deleting a run deletes its objects and its children's; deleting one file
// deletes its object.
func TestDeletesDropBlobs(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	blobs := ag.blobs.(*memBlobs)

	root, _ := db.createRun("root", "", 0)
	kid, _ := db.createRun("kid", "", root)
	other, _ := db.createRun("other", "", 0)
	upload(t, ag, root, "a.png", "image/png", pngBytes(10))
	upload(t, ag, kid, "b.png", "image/png", pngBytes(10))
	keep := upload(t, ag, other, "c.png", "image/png", pngBytes(10))
	gone := upload(t, ag, other, "d.png", "image/png", pngBytes(10))
	attachTo(t, ag, other, "look", "d.png")

	if err := ag.deleteRunTree(root); err != nil {
		t.Fatal(err)
	}
	if blobs.count() != 2 {
		t.Fatalf("after deleting the tree %d objects remain, want 2 (the other run's)", blobs.count())
	}

	w := serve(handleFileDelete, "DELETE", "/runs/x/file?path=d.png", other, nil, "")
	if w.Code != 200 {
		t.Fatalf("file delete = %d %s", w.Code, w.Body)
	}
	rest, _ := db.replFile(other, keep.Path)
	if blobs.count() != 1 || blobs.objs[rest.Blob] == nil || blobs.objs[gone.Blob] != nil {
		t.Fatal("deleting one file should drop exactly its object")
	}
	if len(db.messageFiles(other)) != 0 {
		t.Fatal("deleting a file should unlink it from its message")
	}
	if _, ok := ag.blobCache.get(gone.Blob); ok {
		t.Fatal("a deleted object is still served from the cache")
	}
}

// --- attaching to a message -------------------------------------------------

// A bad attachment fails the request before anything is written; a good one
// is linked and noted in the text the model reads.
func TestMessageAttachments(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)
	// Occupy the only slot, so the message queues the run instead of driving
	// it against an LLM that isn't there.
	ag.setLimit(1)
	if !ag.tryAcquire() {
		t.Fatal("slot")
	}
	defer ag.releaseSlot()

	w := serve(handleMessage, "POST", "/runs/x/message", id, []byte(`{"text":"hi","files":["nope.png"]}`), "application/json")
	if w.Code != 400 {
		t.Fatalf("a missing attachment = %d, want 400", w.Code)
	}
	if msgs, _ := db.messages(id, true); len(msgs) != 0 {
		t.Fatal("a refused message was written anyway")
	}

	upload(t, ag, id, "a.png", "image/png", pngBytes(2000))
	w = serve(handleMessage, "POST", "/runs/x/message", id, []byte(`{"files":["a.png"]}`), "application/json")
	if w.Code != 200 {
		t.Fatalf("message = %d %s", w.Code, w.Body)
	}
	msgs, _ := db.messages(id, true)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if !strings.HasPrefix(msgs[0].Content, "(see attached)") || !strings.Contains(msgs[0].Content, "[attached: a.png (image/png, ") {
		t.Fatalf("stored text = %q", msgs[0].Content)
	}
	if got := db.messageFiles(id)[msgs[0].ID]; len(got) != 1 || got[0] != "a.png" {
		t.Fatalf("messageFiles = %v", db.messageFiles(id))
	}
}

// --- the model sees images ------------------------------------------------

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func partsIn(t *testing.T, c any) []wirePart {
	t.Helper()
	raw, ok := c.(json.RawMessage)
	if !ok {
		return nil
	}
	var ps []wirePart
	if err := json.Unmarshal(raw, &ps); err != nil {
		t.Fatalf("content is not a parts array: %s", raw)
	}
	return ps
}

func images(t *testing.T, out []wireMsg) (n int) {
	for _, m := range out {
		for _, p := range partsIn(t, m.Content) {
			if p.Type == "image_url" {
				n++
			}
		}
	}
	return n
}

// assertWireValid checks what the provider enforces: each assistant tool_calls
// block is followed by exactly its tool results before anything else, and no
// two user messages are adjacent.
func assertWireValid(t *testing.T, out []wireMsg) {
	t.Helper()
	for i := 0; i < len(out); i++ {
		if i > 0 && out[i].Role == "user" && out[i-1].Role == "user" {
			t.Fatalf("wire[%d]: two user messages in a row", i)
		}
		if out[i].Role != "assistant" || len(out[i].ToolCalls) == 0 {
			if out[i].Role == "tool" {
				t.Fatalf("wire[%d]: a tool result not inside a tool block", i)
			}
			continue
		}
		want := map[string]bool{}
		for _, c := range out[i].ToolCalls {
			want[c.ID] = true
		}
		j := i + 1
		for ; j < len(out) && out[j].Role == "tool"; j++ {
			if !want[out[j].ToolCallID] {
				t.Fatalf("wire[%d]: unexpected tool result %s", j, out[j].ToolCallID)
			}
			delete(want, out[j].ToolCallID)
		}
		if len(want) > 0 {
			t.Fatalf("wire[%d]: tool block interrupted before answering %v", i, want)
		}
		i = j - 1
	}
}

func TestAttachedImageRidesItsUserMessage(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)

	img := pngBytes(50)
	upload(t, ag, id, "a.png", "image/png", img)
	upload(t, ag, id, "data.csv", "text/csv", []byte("x\n1\n"))
	upload(t, ag, id, "doc.zip", "application/zip", []byte("PK\x03\x04zipzip"))
	attachTo(t, ag, id, "what is this?", "a.png", "data.csv", "doc.zip")
	stored, _ := db.messages(id, true)
	ag.blobCache = newBlobCache(8 << 20) // as after a restart: nothing cached yet

	out, err := ag.assembleContext(context.Background(), run, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[1].Role != "user" {
		t.Fatalf("wire = %+v", out)
	}
	ps := partsIn(t, out[1].Content)
	if len(ps) != 2 || ps[0].Type != "text" || ps[1].Type != "image_url" {
		t.Fatalf("parts = %+v — want the text then exactly one image (not the csv, not the zip)", ps)
	}
	if !strings.Contains(ps[0].Text, "what is this?") || !strings.Contains(ps[0].Text, "doc.zip") {
		t.Fatalf("text part lost the message or its attachment note: %q", ps[0].Text)
	}
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img); ps[1].ImageURL.URL != want {
		t.Fatalf("image url = %.60s…", ps[1].ImageURL.URL)
	}
	if !hasVisionContent(out) {
		t.Fatal("an image in the context must route the call to a vision model")
	}
	// Never stored: the transcript row is unchanged text.
	if again, _ := db.messages(id, true); again[0].Content != stored[0].Content || strings.Contains(again[0].Content, "base64") {
		t.Fatal("assembling the context changed the stored message")
	}
	// Every loop step re-assembles; only the first may go to the gateway.
	_, _ = ag.assembleContext(context.Background(), run, Config{})
	if gets := ag.blobs.(*memBlobs).gets; gets != 1 {
		t.Fatalf("two assemblies fetched the image %d times, want 1", gets)
	}
}

// Only the newest images are sent; older ones become a note pointing at
// file_view, so a long run does not grow its prompt with every image.
func TestImageBudgetKeepsTheNewest(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)

	for i := 1; i <= maxInlineImages+2; i++ {
		name := fmt.Sprintf("p%d.png", i)
		upload(t, ag, id, name, "image/png", pngBytes(i))
		attachTo(t, ag, id, "img "+name, name)
		_, _ = db.addMessage(&Message{RunID: id, Role: "assistant", Content: "ok"})
	}
	out, _ := ag.assembleContext(context.Background(), run, Config{})
	if n := images(t, out); n != maxInlineImages {
		t.Fatalf("%d images inlined, want %d", n, maxInlineImages)
	}
	var notes []string
	for _, m := range out {
		for _, p := range partsIn(t, m.Content) {
			if p.Type == "text" && strings.HasPrefix(p.Text, "[image ") {
				notes = append(notes, p.Text)
			}
		}
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "p1.png") || !strings.Contains(notes[1], "p2.png") ||
		!strings.Contains(notes[0], "file_view") {
		t.Fatalf("the two oldest should become notes, got %q", notes)
	}
	assertWireValid(t, out)
}

// file_view's image goes after the turn's WHOLE tool block — splitting the
// block would be rejected by the provider — and rides the owner's next message
// when one follows, rather than making two user turns in a row.
func TestFileViewImageFollowsTheToolBlock(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	upload(t, ag, id, "chart.png", "image/png", pngBytes(40))
	_, _ = db.addMessage(&Message{RunID: id, Role: "user", Content: "check the chart"})

	view := call("v1", "file_view")
	view.Function.Arguments = `{"path":"chart.png"}`
	bad := call("v2", "file_view")
	bad.Function.Arguments = `{"path":"missing.png"}`
	note := call("n1", "note")
	addAssistantCalls(t, db, id, view, bad, note)
	ok, err := ag.runTool(context.Background(), run, Config{}, "file_view", map[string]any{"path": "chart.png"})
	if err != nil {
		t.Fatal(err)
	}
	ag.addToolResult(id, view, ok)
	ag.addToolResult(id, bad, "error: no such file")
	ag.addToolResult(id, note, "noted")

	out, _ := ag.assembleContext(context.Background(), run, Config{})
	assertWireValid(t, out)
	// system, user, assistant, tool×3, then the image turn.
	if len(out) != 7 || out[6].Role != "user" {
		t.Fatalf("wire roles = %v", roles(out))
	}
	ps := partsIn(t, out[6].Content)
	if len(ps) != 2 || !strings.Contains(ps[0].Text, "not the owner") || ps[1].Type != "image_url" {
		t.Fatalf("image turn = %+v — want the tool-layer label and exactly one image (the failed view adds none)", ps)
	}

	// The owner replies: the image now rides that message instead.
	_, _ = db.addMessage(&Message{RunID: id, Role: "user", Content: "and?"})
	out, _ = ag.assembleContext(context.Background(), run, Config{})
	assertWireValid(t, out)
	if len(out) != 7 {
		t.Fatalf("wire roles = %v", roles(out))
	}
	ps = partsIn(t, out[6].Content)
	if len(ps) != 3 || !strings.Contains(ps[0].Text, "not the owner") || ps[1].Type != "image_url" || ps[2].Text != "and?" {
		t.Fatalf("merged turn = %+v", ps)
	}
}

func roles(out []wireMsg) []string {
	var r []string
	for _, m := range out {
		r = append(r, m.Role)
	}
	return r
}

func TestVisionOff(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	off := Config{Features: map[string]bool{"vision": false}}

	upload(t, ag, id, "a.png", "image/png", pngBytes(10))
	attachTo(t, ag, id, "see", "a.png")
	out, _ := ag.assembleContext(context.Background(), run, off)
	if images(t, out) != 0 || hasVisionContent(out) {
		t.Fatal("vision off must inline nothing")
	}
	if ps := partsIn(t, out[1].Content); len(ps) != 2 || !strings.Contains(ps[1].Text, "vision is off") {
		t.Fatalf("parts = %+v — the model should be told why it can't see the image", ps)
	}
	if _, err := ag.runTool(context.Background(), run, off, "file_view", map[string]any{"path": "a.png"}); err == nil {
		t.Fatal("file_view must refuse with vision off")
	}
	for _, s := range toolSpecs(off, 0, nil) {
		if s.Function.Name == "file_view" {
			t.Fatal("file_view should not be offered with vision off")
		}
	}
	var offered bool
	for _, s := range toolSpecs(Config{}, 0, nil) {
		offered = offered || s.Function.Name == "file_view"
	}
	if !offered {
		t.Fatal("file_view should be offered with vision on")
	}
}

func TestFileViewRefusesNonImages(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	upload(t, ag, id, "a.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	upload(t, ag, id, "doc.pdf", "application/pdf", []byte("%PDF-1.4 ..."))
	if _, err := db.replPutBinary(id, "huge.png", "image/png", maxInlineImageBytes+1, "runs/x/huge"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.svg", "doc.pdf", "huge.png", "missing.png"} {
		if _, err := ag.runTool(context.Background(), run, Config{}, "file_view", map[string]any{"path": p}); err == nil {
			t.Errorf("file_view(%s) should fail", p)
		}
	}
}

// Binary files never leak into the transcript as text, and the text tools
// point at the right one instead.
func TestBinaryFilesInTextTools(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	upload(t, ag, id, "a.png", "image/png", pngBytes(10))
	upload(t, ag, id, "doc.pdf", "application/pdf", []byte("%PDF-1.4 ..."))
	tool := func(name string, args map[string]any) (string, error) {
		return ag.runTool(context.Background(), run, Config{}, name, args)
	}

	if out, err := tool("file_read", map[string]any{"path": "a.png"}); err != nil || !strings.Contains(out, "use file_view") {
		t.Fatalf("file_read(image) = %q, %v", out, err)
	}
	if out, _ := tool("file_read", map[string]any{"path": "doc.pdf"}); !strings.Contains(out, "cannot be read as text") {
		t.Fatalf("file_read(pdf) = %q", out)
	}
	if _, err := tool("file_write", map[string]any{"path": "a.png", "content": "oops"}); err == nil {
		t.Fatal("file_write must not overwrite an attachment")
	}
	if _, err := tool("file_edit", map[string]any{"path": "a.png", "old": "x", "new": "y"}); err == nil {
		t.Fatal("file_edit must refuse an attachment")
	}
	out, err := tool("js_eval", map[string]any{"code": "files.read('a.png')"})
	if !strings.Contains(out+fmt.Sprint(err), "binary file (image/png)") {
		t.Fatalf("files.read on an image should throw, got %q, %v", out, err)
	}
	out, err = tool("js_eval", map[string]any{"code": "JSON.stringify(files.list().filter(function (f) { return f.binary }).map(function (f) { return f.mime }))"})
	if err != nil || !strings.Contains(out, "image/png") {
		t.Fatalf("files.list should show attachments' types, got %q, %v", out, err)
	}
	out, err = tool("js_eval", map[string]any{"code": "files.remove('a.png')"})
	if !strings.Contains(out+fmt.Sprint(err), "Files tab") {
		t.Fatalf("files.remove on an attachment should refuse, got %q, %v", out, err)
	}
	if _, err := db.replFile(id, "a.png"); err != nil {
		t.Fatal("the attachment was removed")
	}
}
