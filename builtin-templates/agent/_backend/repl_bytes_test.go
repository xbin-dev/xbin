package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// mkZip builds an archive in memory: name → content, in order.
func mkZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(e[1]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mkTarGz(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./pkg/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, e := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: e[0], Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(e[1]))})
		_, _ = tw.Write([]byte(e[1]))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// noise is n incompressible, reproducible bytes.
func noise(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

// bytesAgent is an Agent with its tools, a run, and uploads.
func bytesAgent(t *testing.T) (*Agent, *Run, func(name string, args map[string]any) (string, error)) {
	t.Helper()
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	return ag, run, func(name string, args map[string]any) (string, error) {
		return ag.runTool(context.Background(), run, Config{}, name, args)
	}
}

func js(t *testing.T, tool func(string, map[string]any) (string, error), code string) string {
	t.Helper()
	out, err := tool("js_eval", map[string]any{"code": code})
	if err != nil {
		t.Fatalf("js_eval(%s): %v", code, err)
	}
	return out
}

func mustHave(t *testing.T, what, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("%s: want %q in\n%s", what, w, out)
		}
	}
}

// The report that started this: a zip re-uploaded as base64 text over the
// text cap was held as a blob, and both reads refused it.
func TestReplReadsLargeTextUpload(t *testing.T) {
	ag, run, tool := bytesAgent(t)
	archive := mkZip(t, [2]string{"production/010/README.md", "# Runbook\nstep one\n"},
		[2]string{"pad.bin", string(noise(120 << 10))}) // incompressible: the base64 is ~160 KB
	// line-wrapped, as base64(1) writes it: whitespace decodes away
	b64 := base64.StdEncoding.EncodeToString(archive)
	var wrapped strings.Builder
	for i := 0; i < len(b64); i += 76 {
		wrapped.WriteString(b64[i:min(i+76, len(b64))] + "\n")
	}
	f := upload(t, ag, run.ID, "nodes.zip.b64", "text/plain", []byte(wrapped.String()))
	if !f.Binary || f.Bytes <= maxReplFileBytes {
		t.Fatalf("the fixture must be held as a blob: %+v", f)
	}

	out := js(t, tool, `var b = base64.decode(files.read('nodes.zip.b64')); zip.readText(b, 'production/010/README.md')`)
	mustHave(t, "zip.readText over decoded base64", out, "# Runbook\\nstep one")

	// atob is the web's: one character per byte
	out = js(t, tool, fmt.Sprintf(`var s = atob(files.read('nodes.zip.b64')); s.length === b.length && s.length === %d && s.charCodeAt(0) === 0x50 && s.charCodeAt(1) === 0x4b`, len(archive)))
	mustHave(t, "atob", out, "true")

	// file_read pages it: clipped, with a note
	out, err := tool("file_read", map[string]any{"path": "nodes.zip.b64"})
	if err != nil {
		t.Fatal(err)
	}
	mustHave(t, "file_read of large text", out, "clipped at "+humanBytes(maxReplFileBytes))
	if len(out) > maxReplFileBytes+300 {
		t.Fatalf("file_read pasted %d bytes", len(out))
	}
}

func TestReplZipFromUpload(t *testing.T) {
	ag, run, tool := bytesAgent(t)
	big := strings.Repeat("lock line\n", 10000) // 100 KB: extracted as a blob
	upload(t, ag, run.ID, "nodes.zip", "application/zip", mkZip(t,
		[2]string{"production/010/README.md", "# Runbook\n"},
		[2]string{"production/010/flake.nix", "{ }\n"},
		[2]string{"production/010/a/flake.lock", big},
		[2]string{"production/010/logo.png", string(pngBytes(4))},
		[2]string{"other/skip.txt", "no"},
		[2]string{"production/010/name with spaces.md", "spaced"}))

	out := js(t, tool, `JSON.stringify(zip.list('nodes.zip').map(function (e) { return e.name + ':' + e.size }))`)
	mustHave(t, "zip.list", out, "production/010/README.md:10", "other/skip.txt:2")

	o, _ := tool("js_eval", map[string]any{"code": "files.read('nodes.zip')"})
	mustHave(t, "files.read on a zip", o, "files.readBytes", "zip.")

	out = js(t, tool, `var b = files.readBytes('nodes.zip'); [b instanceof Uint8Array, b[0], b[1]].join()`)
	mustHave(t, "readBytes", out, "true,80,75")

	out = js(t, tool, `console.log(JSON.stringify(zip.extract('nodes.zip', {to: 'x', only: ['production/']})))`)
	mustHave(t, "zip.extract", out, `"path":"x/production/010/README.md"`, `"path":"x/production/010/name_with_spaces.md"`,
		`"path":"x/production/010/logo.png"`, `"binary":true`)
	if strings.Contains(out, "skip.txt") {
		t.Fatalf("extract ignored only: %s", out)
	}

	readme, err := ag.db.replFile(run.ID, "x/production/010/README.md")
	if err != nil || readme.Binary || readme.Content != "# Runbook\n" {
		t.Fatalf("README should be a text row: %+v %v", readme, err)
	}
	lock, err := ag.db.replFile(run.ID, "x/production/010/a/flake.lock")
	if err != nil || !lock.Binary || lock.Bytes != len(big) {
		t.Fatalf("a large entry should be a blob: %+v %v", lock, err)
	}
	out = js(t, tool, `files.read('x/production/010/a/flake.lock').length`)
	mustHave(t, "files.read of an extracted blob", out, fmt.Sprint(len(big)))

	// globs: a bare one matches base names anywhere
	out = js(t, tool, `zip.extract('nodes.zip', {to: 'y', only: ['*.nix']}).map(function (e) { return e.path }).join()`)
	mustHave(t, "glob extract", out, "y/production/010/flake.nix")

	// a tool's own blob file can be removed; an upload can't
	js(t, tool, `files.remove('x/production/010/a/flake.lock')`)
	if _, err := ag.db.replFile(run.ID, "x/production/010/a/flake.lock"); err == nil {
		t.Fatal("files.remove left the extracted blob file")
	}
	o, _ = tool("js_eval", map[string]any{"code": "files.remove('nodes.zip')"})
	mustHave(t, "remove an upload", o, "Files tab")
}

func TestReplZipSlipAndBomb(t *testing.T) {
	ag, run, tool := bytesAgent(t)
	upload(t, ag, run.ID, "evil.zip", "application/zip", mkZip(t, [2]string{"../../etc/passwd", "root"}))
	o, _ := tool("js_eval", map[string]any{"code": "zip.extract('evil.zip')"})
	mustHave(t, "zip slip", o, "climbs out")

	// 80 MiB of zeros deflates to ~80 KB: over the in-VM entry cap
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("zeros")
	chunk := make([]byte, 1<<20)
	for i := 0; i < 80; i++ {
		_, _ = f.Write(chunk)
	}
	_ = w.Close()
	upload(t, ag, run.ID, "bomb.zip", "application/zip", buf.Bytes())
	o, _ = tool("js_eval", map[string]any{"code": "zip.read('bomb.zip', 'zeros').length"})
	mustHave(t, "zip bomb", o, "archive entry too large")
	o, _ = tool("js_eval", map[string]any{"code": "zip.extract('bomb.zip')"})
	mustHave(t, "zip bomb extract", o, "archive entry too large")
}

func TestReplTarGz(t *testing.T) {
	ag, run, tool := bytesAgent(t)
	upload(t, ag, run.ID, "src.tar.gz", "application/gzip", mkTarGz(t,
		[2]string{"./pkg/a.txt", "alpha"}, [2]string{"./pkg/b.json", `{"b":1}`}))
	out := js(t, tool, `JSON.stringify(tar.list('src.tar.gz').map(function (e) { return e.name }))`)
	mustHave(t, "tar.list", out, "./pkg/a.txt", "./pkg/")
	out = js(t, tool, `tar.readText('src.tar.gz', 'pkg/a.txt')`)
	mustHave(t, "tar.readText", out, "alpha")
	out = js(t, tool, `tar.extract('src.tar.gz', {to: 't'}).map(function (e) { return e.path }).join()`)
	mustHave(t, "tar.extract", out, "t/pkg/a.txt,t/pkg/b.json")
	out = js(t, tool, `new TextDecoder().decode(gzip.decompress(gzip.compress('zażółć')))`)
	mustHave(t, "gzip round trip", out, "zażółć")
}

func TestReplLargeWritesAndBytes(t *testing.T) {
	ag, run, tool := bytesAgent(t)
	out := js(t, tool, `var big = Array(200001).join('ab\n'); var w = files.write('big.csv', big); [w.bytes, w.mime].join()`)
	mustHave(t, "large write", out, "600000,text/csv")
	out = js(t, tool, `files.read('big.csv') === big`)
	mustHave(t, "large read back", out, "true")
	f, _ := ag.db.replFile(run.ID, "big.csv")
	if !f.Binary {
		t.Fatalf("a 600 KB write should be a blob: %+v", f)
	}
	// overwrite it with small text: a text row again, the next version
	out = js(t, tool, `files.write('big.csv', 'a,b\n').version`)
	mustHave(t, "shrink", out, "2")
	if f, _ = ag.db.replFile(run.ID, "big.csv"); f.Binary || f.Content != "a,b\n" {
		t.Fatalf("shrunk file: %+v", f)
	}

	out = js(t, tool, `var u = new Uint8Array([0, 255, 1, 2, 3]); files.writeBytes('raw.bin', u.subarray(1, 4)).bytes`)
	mustHave(t, "writeBytes of a subarray", out, "3")
	out = js(t, tool, `Array.prototype.join.call(files.readBytes('raw.bin'))`)
	mustHave(t, "readBytes", out, "255,1,2")
	out = js(t, tool, `files.read('raw.bin', {encoding: 'base64'})`)
	mustHave(t, "read base64", out, base64.StdEncoding.EncodeToString([]byte{255, 1, 2}))

	out = js(t, tool, `var e = new TextEncoder().encode('€'); [e.length, new TextDecoder('utf-8').decode(e), btoa('hi'), atob('aGk=')].join()`)
	mustHave(t, "encoders", out, "3,€,aGk=,hi")
	o, _ := tool("js_eval", map[string]any{"code": "new TextDecoder('utf-8', {fatal: true}).decode(new Uint8Array([0xff]))"})
	mustHave(t, "fatal decode", o, "TypeError")
	o, _ = tool("js_eval", map[string]any{"code": "btoa('€')"})
	mustHave(t, "btoa outside latin1", o, "Latin1")

	// a megabyte array prints a head and a length
	out = js(t, tool, `new Uint8Array(1 << 20)`)
	mustHave(t, "inspect bytes", out, "Uint8Array(1048576) [ 0, 0", "more")
	if len(out) > 1000 {
		t.Fatalf("inspect printed %d bytes", len(out))
	}

	// zip.create → writeBytes → zip.readText
	out = js(t, tool, `files.writeBytes('made.zip', zip.create({'a.txt': 'A', 'b.bin': new Uint8Array([1])})); zip.readText('made.zip', 'a.txt')`)
	mustHave(t, "zip.create", out, `"A"`)
	if f, _ := ag.db.replFile(run.ID, "made.zip"); f.Mime != "application/zip" {
		t.Fatalf("made.zip mime = %q", f.Mime)
	}
}

// A session that read blobs and extracted an archive rebuilds from its log.
func TestReplReplayWithBlobs(t *testing.T) {
	s, db := newTestSession(t, 1)
	data := mkZip(t, [2]string{"r.txt", "replayed"})
	io := testIO(db)
	if _, _, _, err := io.putFileData(context.Background(), 1, "in.zip", data, "application/zip", &fileSource{Kind: "upload"}); err != nil {
		t.Fatal(err)
	}
	if r := s.logged(t, `var got = zip.readText('in.zip', 'r.txt'); zip.extract('in.zip', {to: 'o'}).length`); r.err != nil {
		t.Fatal(r.err)
	}
	s2, banner := replay(t, db, 1)
	if strings.Contains(banner, "STOPPED") {
		t.Fatalf("replay stopped: %s", banner)
	}
	if r := s2.testRun(t, "got"); r.err != nil || r.value.String() != "replayed" {
		t.Fatalf("after replay got = %v (%v)", r.value, r.err)
	}
}
