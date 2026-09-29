// sandbox_move.go — moving files between the session files and the bound
// sandbox (sandbox_upload, sandbox_download) and between the conversation's
// attached sandboxes (sandbox_copy: a directory as a tar stream, a file
// through the file routes); sandbox_info says what is attached and which jobs
// run; and the sandbox a subagent is spawned onto (spawnSandbox).
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

var sandboxMoveTools = map[string]bool{"sandbox_upload": true, "sandbox_download": true, "sandbox_copy": true, "sandbox_info": true}

func sandboxMoveSpecs(cfg Config) []toolSpec {
	var specs []toolSpec
	if cfg.feature("files") {
		specs = append(specs,
			toolSpec{Type: "function", Function: funcDef{
				Name:        "sandbox_upload",
				Description: "Copy a session file (file_list lists them; attachments too) into the coding sandbox.",
				Parameters: obj([]string{"file"}, map[string]any{
					"file": strProp("the session file's name"),
					"path": strProp("where in the sandbox: a file path, or a directory (ending in /, or existing) to put it in; default: the working directory"),
				}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "sandbox_download",
				Description: fmt.Sprintf("Copy a file from the coding sandbox into the session files (up to %d MB), to keep it with the conversation or hand it to people. "+
					"It overwrites the session file of that name in place: the version it replaces is kept (file_diff shows what changed), "+
					"and a file that hasn't changed (same etag or sha256) writes nothing. "+
					"render_html and file_view take a sandbox path directly — no download needed first. "+
					"A directory: pack it with bash (tar czf) first.", maxBinaryFileBytes>>20),
				Parameters: obj([]string{"path"}, map[string]any{
					"path":      strProp("the file in the sandbox"),
					"name":      strProp("the session file's name (default: the file's own)"),
					"keep_both": boolProp("keep an existing session file of that name and save this one beside it (-2, -3…) instead of overwriting it"),
				}),
			}})
	}
	if len(cfg.Attached) > 1 {
		end := func(what string) map[string]any {
			return map[string]any{"type": "object", "description": what, "required": []string{"path"}, "properties": map[string]any{
				"sandbox": strProp("an attached sandbox's ref (sandbox_info lists them); default: the active one"),
				"path":    strProp("the path in it"),
			}}
		}
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "sandbox_copy",
			Description: "Copy a file or a directory between this conversation's attached sandboxes (or within one). " +
				"A directory's contents land in to.path (created if missing); a file goes to to.path, or into it when it is a directory.",
			Parameters: obj([]string{"from", "to"}, map[string]any{"from": end("what to copy"), "to": end("where to")}),
		}})
	}
	return append(specs, toolSpec{Type: "function", Function: funcDef{
		Name:        "sandbox_info",
		Description: "The sandboxes this conversation has attached — which is active, their state, egress, image and directories — and its latest jobs.",
		Parameters:  obj(nil, map[string]any{}),
	}})
}

// copyTouches: sandbox_copy writes into a sandbox the call names, so it
// counts as a side effect when any attached one has egress.
func copyTouches(cfg Config) bool {
	for _, a := range cfg.Attached {
		if a.Egress != "none" {
			return true
		}
	}
	return cfg.Sandbox != nil && cfg.Sandbox.Egress != "none"
}

func (ag *Agent) runSandboxMoveTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	switch name {
	case "sandbox_upload":
		return ag.toolSbxUpload(ctx, run, cfg, args)
	case "sandbox_download":
		return ag.toolSbxDownload(ctx, run, cfg, args)
	case "sandbox_copy":
		return ag.toolSbxCopy(ctx, run, cfg, args)
	case "sandbox_info":
		return ag.toolSbxInfo(ctx, run, cfg)
	}
	return "", fmt.Errorf("unknown sandbox tool %q", name)
}

// into resolves a destination: a path ending in / or naming a directory
// takes the source's base name.
func into(ctx context.Context, use *sbxUse, dest, base string) (string, error) {
	p, err := use.path(dest)
	if err != nil {
		return "", err
	}
	if dest == "" || strings.HasSuffix(dest, "/") {
		return path.Join(p, base), nil
	}
	if st, err := use.Conn.Stat(ctx, use.ID, p); err == nil && st.Type == "dir" {
		return path.Join(p, base), nil
	}
	return p, nil
}

func (ag *Agent) toolSbxUpload(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	name, err := normReplPath(str(args["file"]))
	if err != nil {
		return "", err
	}
	f, err := ag.db.replFile(run.ID, name)
	if err != nil {
		return "", err
	}
	data := []byte(f.Content)
	if f.Binary {
		if data, err = ag.readBlob(ctx, f.Blob); err != nil {
			return "", fmt.Errorf("reading %s: %w", name, err)
		}
	}
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return "", err
	}
	p, err := into(ctx, use, str(args["path"]), path.Base(name))
	if err != nil {
		return "", err
	}
	if _, err := use.Conn.WriteFile(ctx, use.ID, p, bytes.NewReader(data), sbxWrite{Mkdirs: true}); err != nil {
		return "", err
	}
	return fmt.Sprintf("uploaded the session file %s (%s) to %s", name, humanBytes(len(data)), p), nil
}

func (ag *Agent) toolSbxDownload(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return "", err
	}
	p, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	// in place, versioned (D136); keep_both is the old way: a free name
	f, prev, unchanged, err := ag.fetchSandboxFile(ctx, run, use, p, str(args["name"]), "sandbox_download", args["keep_both"] == true)
	if err != nil {
		return "", err
	}
	if unchanged {
		return fetched(p, f, prev, true), nil
	}
	return fetched(p, f, prev, false) + "\n\n" + ag.db.replFileIndex(run.ID), nil
}

func (ag *Agent) toolSbxCopy(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	from, _ := args["from"].(map[string]any)
	to, _ := args["to"].(map[string]any)
	if str(from["path"]) == "" || str(to["path"]) == "" {
		return "", fmt.Errorf("sandbox_copy needs from {sandbox?, path} and to {sandbox?, path}")
	}
	root := rootOf(run)
	src, err := ag.sandboxUse(ctx, root, cfg, refOf(cfg, str(from["sandbox"])))
	if err != nil {
		return "", err
	}
	dst, err := ag.sandboxUse(ctx, root, cfg, refOf(cfg, str(to["sandbox"])))
	if err != nil {
		return "", err
	}
	sp, err := src.path(str(from["path"]))
	if err != nil {
		return "", err
	}
	st, err := src.Conn.Stat(ctx, src.ID, sp)
	if err != nil {
		return "", noPath(sp, err)
	}
	same := src.Binding.Ref == dst.Binding.Ref
	where := func(u *sbxUse, p string) string { return fmt.Sprintf("%q:%s", orStr(u.Box.Name, u.Binding.Ref), p) }
	switch st.Type {
	case "dir":
		dp, err := dst.path(str(to["path"]))
		if err != nil {
			return "", err
		}
		if same && (dp == sp || strings.HasPrefix(dp, sp+"/")) {
			return "", fmt.Errorf("can't copy %s into itself", sp)
		}
		for _, u := range []*sbxUse{src, dst} {
			if !u.Hello.has("tar") || !u.Box.hasCap("tar") {
				return "", fmt.Errorf("copying a directory takes the tar capability, and the sandbox %q lacks it — copy single files, or pack it with bash (tar czf) and copy the archive",
					orStr(u.Box.Name, u.Binding.Ref))
			}
		}
		tr, err := src.Conn.TarGet(ctx, src.ID, sp, nil)
		if err != nil {
			return "", err
		}
		defer tr.Close()
		cr := &countingReader{r: tr}
		if err := dst.Conn.TarPut(ctx, dst.ID, dp, cr, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("copied the contents of %s into %s (%s as tar)", where(src, sp), where(dst, dp), humanBytes(int(cr.n))), nil
	case "file", "symlink":
		dp, err := into(ctx, dst, str(to["path"]), path.Base(sp))
		if err != nil {
			return "", err
		}
		if same && dp == sp {
			return "", fmt.Errorf("%s is already there", sp)
		}
		body, _, err := src.Conn.OpenFile(ctx, src.ID, sp, 0, 0)
		if err != nil {
			return "", err
		}
		defer body.Close()
		out, err := dst.Conn.WriteFile(ctx, dst.ID, dp, body, sbxWrite{Mkdirs: true, Mode: string(st.Mode)})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("copied %s to %s (%s)", where(src, sp), where(dst, dp), humanBytes(int(out.Size))), nil
	}
	return "", fmt.Errorf("%s is neither a file nor a directory (%s)", sp, st.Type)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (ag *Agent) toolSbxInfo(ctx context.Context, run *Run, cfg Config) (string, error) {
	root := rootOf(run)
	bindings := cfg.Attached
	if cfg.Sandbox != nil {
		if _, ok := attachedRef(cfg, cfg.Sandbox.Ref); !ok {
			bindings = append([]SandboxBinding{*cfg.Sandbox}, bindings...)
		}
	}
	var b strings.Builder
	names := map[string]string{}
	for _, bd := range bindings {
		names[bd.Ref] = orStr(bd.Name, bd.Ref)
		mark := "attached"
		if cfg.Sandbox != nil && bd.Ref == cfg.Sandbox.Ref {
			mark = "active"
		}
		use, err := ag.sandboxUse(ctx, root, cfg, bd.Ref)
		if err != nil {
			fmt.Fprintf(&b, "%s %q (%s): unavailable — %v\n", mark, orStr(bd.Name, bd.Ref), bd.Ref, err)
			continue
		}
		x := use.Box
		names[bd.Ref] = orStr(x.Name, bd.Ref)
		egress := orStr(x.Egress, "unknown")
		if x.EgressNext != "" && x.EgressNext != x.Egress {
			egress += " (" + x.EgressNext + " from its next start)"
		}
		fmt.Fprintf(&b, "%s %q (%s) — %s · egress %s · image %s · %s\n", mark, x.Name, bd.Ref, x.State, egress,
			orStr(x.Image.ID, "?"), use.Hello.title(bd.Ref))
		fmt.Fprintf(&b, "  cwd %s · workdir %s · home %s · user %s · caps %s\n", use.cwd(), x.Workdir, orStr(x.Home, "?"),
			orStr(x.User, "?"), strings.Join(x.Caps, ","))
	}
	if len(bindings) == 0 {
		b.WriteString("no sandbox is attached to this conversation\n")
	}
	jobs := ag.db.jobList(root, false, 15)
	ag.refreshJobs(ctx, root, cfg, jobs)
	if len(jobs) > 0 {
		b.WriteString("jobs (newest first):\n")
	}
	for _, j := range jobs {
		state := endWords(j.State, j.Exit, "")
		took := time.Duration(j.Ended-j.Created) * time.Millisecond
		if j.running() {
			state, took = j.State, time.Since(time.UnixMilli(j.Created))
		}
		fmt.Fprintf(&b, "  job %d · %s · %s · %s (in %q, %s)\n", j.Job, state, fmtDur(took), clip(j.Command, 120),
			orStr(names[j.Ref], j.Ref), j.Cwd)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// refOf is the ref a model named a sandbox by (its ref or its name; "" is
// the active one).
func refOf(cfg Config, s string) string {
	if b, ok := attachedRef(cfg, strings.TrimSpace(s)); ok {
		return b.Ref
	}
	return strings.TrimSpace(s)
}

// attachedRef finds an attached sandbox by its ref, or by a name no other
// attached one shares.
func attachedRef(cfg Config, ref string) (SandboxBinding, bool) {
	for _, a := range cfg.Attached {
		if a.Ref == ref {
			return a, true
		}
	}
	var hit []SandboxBinding
	for _, a := range cfg.Attached {
		if a.Name == ref {
			hit = append(hit, a)
		}
	}
	if len(hit) == 1 {
		return hit[0], true
	}
	return SandboxBinding{}, false
}

// --- subagents on another sandbox -------------------------------------------------------

// sandboxSpawnProps adds subagent_spawn's {sandbox, cwd} where a sandbox is
// bound.
func sandboxSpawnProps(cfg Config, props map[string]any) {
	if !sandboxToolsOn(cfg) {
		return
	}
	props["sandbox"] = strProp("optional: an attached sandbox's ref (sandbox_info lists them) for the subagent to work in instead of yours")
	props["cwd"] = strProp("optional: the subagent's working directory in its sandbox (absolute, or relative to that sandbox's)")
}

// spawnSandbox is the sandbox a subagent is spawned onto: nil keeps the
// parent's (childConfig copies it). Only an attached one — the subagent
// works under the binding the conversation already holds.
func spawnSandbox(cfg Config, args map[string]any) (*SandboxBinding, error) {
	ref, cwd := strings.TrimSpace(str(args["sandbox"])), strings.TrimSpace(str(args["cwd"]))
	if ref == "" && cwd == "" {
		return nil, nil
	}
	if !sandboxToolsOn(cfg) {
		return nil, fmt.Errorf("this conversation has no sandbox to put a subagent in")
	}
	b := *cfg.Sandbox
	if ref != "" {
		var ok bool
		if b, ok = attachedRef(cfg, ref); !ok {
			return nil, fmt.Errorf("sandbox %s isn't attached to this conversation — sandbox_info lists the attached ones", ref)
		}
	}
	if cwd != "" {
		if !strings.HasPrefix(cwd, "/") {
			cwd = path.Join(orStr(b.Cwd, "/"), cwd)
		}
		c, ok := cleanCwd(cwd)
		if !ok {
			return nil, fmt.Errorf("cwd: an absolute path in the sandbox, or one relative to its working directory")
		}
		b.Cwd = c
	}
	return &b, nil
}
