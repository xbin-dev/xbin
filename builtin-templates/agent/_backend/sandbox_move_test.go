package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sbxPair is a conversation on sandbox a with b attached too.
func sbxPair(t *testing.T, ag *Agent) (*Run, Config, *sbxSandbox, *sbxSandbox) {
	t.Helper()
	r, cfg, a := sbxRun(t, ag, "a", "none")
	b := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "b"})
	cfg.Attached = append(cfg.Attached, sbxBindingOf(b))
	if err := storeBinding(ag.db, r.ID, func(c *Config) error { c.Attached = cfg.Attached; return nil }); err != nil {
		t.Fatal(err)
	}
	return r, cfg, a, b
}

func TestSandboxUploadDownload(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "files", "none")
	if _, err := ag.db.replPutFile(r.ID, "notes.txt", "hello\n", 0); err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0, 1, 2}, 100)...)
	if _, err := ag.acceptUpload(context.Background(), r.ID, "shot.png", "image/png", bytes.NewReader(png)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"file": "notes.txt"}, "notes.txt"},
		{map[string]any{"file": "notes.txt", "path": "docs/"}, "docs/notes.txt"},
		{map[string]any{"file": "notes.txt", "path": "docs/renamed.txt"}, "docs/renamed.txt"},
		{map[string]any{"file": "shot.png", "path": "docs"}, "docs/shot.png"}, // an existing directory
	} {
		out := mustTool(t, ag, r, cfg, "u", "sandbox_upload", c.args)
		p := filepath.Join(box.Workdir, c.want)
		if !strings.HasSuffix(out, " to "+p) {
			t.Fatalf("upload %v: %q", c.args, out)
		}
		got, err := os.ReadFile(p)
		want := []byte("hello\n")
		if strings.HasSuffix(p, ".png") {
			want = png
		}
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("uploaded %s: %q %v", p, got, err)
		}
	}
	if _, err := tool(t, ag, r, cfg, "u", "sandbox_upload", map[string]any{"file": "nope.txt"}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("a missing session file: %v", err)
	}

	put(t, box, "out/report.txt", "the report\n")
	out := mustTool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "out/report.txt"})
	if !strings.HasPrefix(out, "downloaded "+box.Workdir+"/out/report.txt to the session file report.txt (text, ") {
		t.Fatalf("download: %q", out)
	}
	if f, err := ag.db.replFile(r.ID, "report.txt"); err != nil || f.Content != "the report\n" {
		t.Fatalf("the session file: %+v %v", f, err)
	}
	// D136: in place — the same file again writes nothing (keep_both: the old suffix)
	if out := mustTool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "out/report.txt"}); !strings.HasPrefix(out, "unchanged: the session file report.txt already holds ") {
		t.Fatalf("again: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "out/report.txt", "keep_both": true}); !strings.Contains(out, "session file report-2.txt") {
		t.Fatalf("keep_both: %q", out)
	}
	put(t, box, "chart.png", string(png))
	if out := mustTool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "chart.png", "name": "plot.png"}); !strings.Contains(out, "session file plot.png (image/png, ") {
		t.Fatalf("a binary download: %q", out)
	}
	if f, err := ag.db.replFile(r.ID, "plot.png"); err != nil || !f.Binary {
		t.Fatalf("stored as a binary: %+v %v", f, err)
	}
	if _, err := tool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "out"}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("a directory: %v", err)
	}
	huge := put(t, box, "huge.bin", "")
	if err := os.Truncate(huge, maxBinaryFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := tool(t, ag, r, cfg, "d", "sandbox_download", map[string]any{"path": "huge.bin"}); err == nil || !strings.Contains(err.Error(), "a session file holds up to") {
		t.Fatalf("too large: %v", err)
	}
	// both need the files feature; copying needs a second sandbox
	off := cfg
	off.Features = map[string]bool{"files": false}
	if names := specLine(off, 0); strings.Contains(names, " sandbox_upload ") || strings.Contains(names, " sandbox_download ") ||
		!strings.Contains(names, " sandbox_info ") || strings.Contains(names, " sandbox_copy ") {
		t.Fatalf("with files off, one sandbox: %s", names)
	}
}

func TestSandboxCopy(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	m := bindSbx(t, "apps/cs")["apps/cs"]
	r, cfg, a, b := sbxPair(t, ag)
	if !strings.Contains(specLine(cfg, 0), " sandbox_copy ") {
		t.Fatal("two attached: sandbox_copy is offered")
	}
	put(t, a, "proj/x.txt", "x\n")
	put(t, a, "proj/sub/y.txt", "y\n")
	if err := os.Chmod(put(t, a, "proj/run.sh", "#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	refB := sandboxRef("apps/cs", b.ID)
	out := mustTool(t, ag, r, cfg, "c", "sandbox_copy", map[string]any{"from": map[string]any{"path": "proj"}, "to": map[string]any{"sandbox": refB, "path": "dst"}})
	if !strings.HasPrefix(out, `copied the contents of "a":`+a.Workdir+`/proj into "b":`+b.Workdir+"/dst (") {
		t.Fatalf("a directory: %q", out)
	}
	for f, want := range map[string]string{"dst/x.txt": "x\n", "dst/sub/y.txt": "y\n"} {
		if got, err := os.ReadFile(filepath.Join(b.Workdir, f)); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", f, got, err)
		}
	}
	// a file, into a directory, by the sandbox's name; its mode kept
	out = mustTool(t, ag, r, cfg, "c", "sandbox_copy", map[string]any{"from": map[string]any{"path": "proj/run.sh"}, "to": map[string]any{"sandbox": "b", "path": "bin/"}})
	if !strings.HasSuffix(out, `"b":`+b.Workdir+"/bin/run.sh (10 B)") {
		t.Fatalf("a file: %q", out)
	}
	if fi, err := os.Stat(filepath.Join(b.Workdir, "bin/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("the mode: %v %v", fi, err)
	}
	// and back, within one sandbox
	mustTool(t, ag, r, cfg, "c", "sandbox_copy", map[string]any{"from": map[string]any{"sandbox": refB, "path": "dst/x.txt"}, "to": map[string]any{"sandbox": refB, "path": "dst/x2.txt"}})
	if got, _ := os.ReadFile(filepath.Join(b.Workdir, "dst/x2.txt")); string(got) != "x\n" {
		t.Fatalf("within b: %q", got)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"from": map[string]any{"path": "proj"}, "to": map[string]any{"path": "proj/inner"}}, "into itself"},
		{map[string]any{"from": map[string]any{"path": "proj"}, "to": map[string]any{"sandbox": "apps/cs|sb-99", "path": "x"}}, "isn't attached"},
		{map[string]any{"from": map[string]any{"path": "nope"}, "to": map[string]any{"sandbox": refB, "path": "x"}}, "nope doesn't exist in the sandbox"},
		{map[string]any{"from": map[string]any{"path": "proj"}}, "needs from"},
	} {
		if _, err := tool(t, ag, r, cfg, "c", "sandbox_copy", c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("copy %v: %v, want %q", c.args, err, c.want)
		}
	}
	// a manager without tar: files still go, a directory is refused
	m.Caps = []string{"exec", "files"}
	forgetHellos()
	r2, cfg2, c, d := sbxPair(t, ag)
	put(t, c, "tree/f.txt", "f\n")
	if _, err := tool(t, ag, r2, cfg2, "c", "sandbox_copy", map[string]any{"from": map[string]any{"path": "tree"}, "to": map[string]any{"sandbox": "b", "path": "t"}}); err == nil ||
		!strings.Contains(err.Error(), "takes the tar capability") {
		t.Fatalf("no tar: %v", err)
	}
	mustTool(t, ag, r2, cfg2, "c", "sandbox_copy", map[string]any{"from": map[string]any{"path": "tree/f.txt"}, "to": map[string]any{"sandbox": "b", "path": "t/f.txt"}})
	if got, _ := os.ReadFile(filepath.Join(d.Workdir, "t/f.txt")); string(got) != "f\n" {
		t.Fatalf("a file without tar: %q", got)
	}
	// copying into a sandbox with egress parks in Approve mode
	net := cfg
	net.Attached = append([]SandboxBinding(nil), cfg.Attached...)
	net.Attached[1].Egress = "internet"
	if sideEffect("sandbox_copy", cfg) || !sideEffect("sandbox_copy", net) || !sideEffect("sandbox_upload", Config{Class: "coding", Sandbox: &net.Attached[1]}) ||
		sideEffect("sandbox_download", net) || sideEffect("sandbox_info", net) {
		t.Fatal("which move tools park")
	}
}

func TestSandboxInfo(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, a, b := sbxPair(t, ag)
	mustTool(t, ag, r, cfg, "j1", "bash", map[string]any{"command": "true"})
	mustTool(t, ag, r, cfg, "j2", "bash", map[string]any{"command": "sleep 30", "background": true})
	out := mustTool(t, ag, r, cfg, "i", "sandbox_info", nil)
	for _, want := range []string{
		`active "a" (apps/cs|` + a.ID + `) — running · egress none · image base · Fake sandboxes (test fixture)`,
		"  cwd " + a.Workdir + " · workdir " + a.Workdir + " · home " + a.Home + " · user dev · caps exec,",
		`attached "b" (apps/cs|` + b.ID + `) — running`,
		"jobs (newest first):\n  job 2 · running · ",
		`sleep 30 (in "a", ` + a.Workdir + ")",
		"  job 1 · exit 0 · ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info lacks %q:\n%s", want, out)
		}
	}
	shortGrace(t)
	mustTool(t, ag, r, cfg, "k", "bash_kill", map[string]any{"job": 2})
}

// A subagent can be spawned onto another attached sandbox (only an attached
// one), with its own working directory.
func TestSubagentOnAnotherSandbox(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, _, b := sbxPair(t, ag)
	if err := os.MkdirAll(filepath.Join(b.Workdir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	refB := sandboxRef("apps/cs", b.ID)
	if !strings.Contains(specLine(cfg, 0), " subagent_spawn ") {
		t.Fatal("no subagents")
	}
	var props map[string]any
	for _, s := range toolSpecs(cfg, 0, nil) {
		if s.Function.Name == "subagent_spawn" {
			props = s.Function.Parameters["properties"].(map[string]any)
		}
	}
	if props["sandbox"] == nil || props["cwd"] == nil {
		t.Fatalf("spawn's props: %v", props)
	}
	f := fakeOf(ag)
	f.on(taskIs("where are you"), func(req LLMRequest) (LLMReply, error) {
		if lastIs("tool", "")(req) {
			return say("reported")(req)
		}
		return callTools(tc("k1", "bash", `{"command":"echo on $SANDBOX_NAME; pwd"}`))(req)
	})
	f.on(forRun(r.ID, lastIs("user", "delegate")), callTools(
		tc("s1", "subagent_spawn", `{"task":"where are you","sandbox":"`+refB+`","cwd":"sub"}`),
		tc("s2", "subagent_spawn", `{"task":"elsewhere","sandbox":"apps/cs|sb-99"}`)))
	f.on(forRun(r.ID, lastIs("tool", "")), say("done"))
	send(t, ag, r.ID, "delegate")
	waitFor(t, "the parent's answer", func() bool { return strings.Contains(transcript(ag.db, r.ID), "A:done") })
	kid := childByTask(ag.db, r.ID, "where are you")
	if kid == nil {
		t.Fatal("no child")
	}
	kcfg, _ := ag.db.runConfig(kid.ID)
	if kcfg.Sandbox == nil || kcfg.Sandbox.Ref != refB || kcfg.Sandbox.Cwd != filepath.Join(b.Workdir, "sub") || len(kcfg.Attached) != 2 {
		t.Fatalf("the child's sandbox: %+v", kcfg.Sandbox)
	}
	if got := fullText(ag.db, kid.ID); !strings.Contains(got, "on b\n"+filepath.Join(b.Workdir, "sub")+"\n[exit 0") {
		t.Fatalf("the child ran on b:\n%s", got)
	}
	if got := fullText(ag.db, r.ID); !strings.Contains(got, "isn't attached to this conversation") {
		t.Fatalf("an unattached sandbox is refused:\n%s", got)
	}
	if parent, _ := ag.db.runConfig(r.ID); parent.Sandbox.Ref != cfg.Sandbox.Ref {
		t.Fatalf("the parent moved: %+v", parent.Sandbox)
	}
	// spawnSandbox itself
	if sb, err := spawnSandbox(cfg, map[string]any{"cwd": "/abs"}); err != nil || sb.Ref != cfg.Sandbox.Ref || sb.Cwd != "/abs" {
		t.Fatalf("a cwd alone: %+v %v", sb, err)
	}
	if sb, err := spawnSandbox(cfg, map[string]any{"sandbox": "b"}); err != nil || sb.Ref != refB {
		t.Fatalf("by name: %+v %v", sb, err)
	}
	if sb, err := spawnSandbox(cfg, map[string]any{}); err != nil || sb != nil {
		t.Fatalf("nothing asked: %+v %v", sb, err)
	}
	if _, err := spawnSandbox(Config{Class: "coding"}, map[string]any{"sandbox": refB}); err == nil {
		t.Fatal("nothing bound")
	}
}
