// files_paths.go — one view of a conversation's files in two explicit places
// (D136): the session files (this tile's store) and the bound sandbox.
// file_read, file_view, render_html, browser_check, file_info and file_diff
// take either:
//
//	report.html, session:report.html   a session file (a bare key always was)
//	session:report.html@2              an earlier version of one (file_diff, file_read, file_info)
//	/work/report.html, ./out/r.html, ~/r.html, sandbox:r.html
//	                                   a file in the sandbox, when one is bound
//
// A miss says where the file is instead, when the other place has it. The
// sandbox's files are read through the manager's Files route; showing one
// (render_html, file_view) copies it into the session files first, in place
// (fetchSandboxFile — sandbox_download's semantics).
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// fileRef is a file named in either place.
type fileRef struct {
	sandbox bool
	key     string // a session file's key
	ver     int    // a session file's version (0: the current one)
	path    string // a sandbox path, as written (sbxUse.path resolves it)
}

func (r fileRef) label() string {
	switch {
	case r.sandbox:
		return r.path
	case r.ver > 0:
		return fmt.Sprintf("session:%s@%d", r.key, r.ver)
	}
	return "session:" + r.key
}

// looksSandbox: a path only the sandbox can mean.
func looksSandbox(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") ||
		s == "~" || strings.HasPrefix(s, "~/") || s == "." || s == ".."
}

// parseFileRef reads a path a model wrote. sbx: a sandbox is bound (and the
// class has the toolset) — without one, ./x is the session file x, as it
// always was.
func parseFileRef(s string, sbx bool) (fileRef, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "session:"):
		return sessionRef(strings.TrimPrefix(s, "session:"))
	case strings.HasPrefix(s, "sandbox:"), strings.HasPrefix(s, "file://"):
		p := strings.TrimPrefix(strings.TrimPrefix(s, "sandbox:"), "file://")
		if !sbx {
			return fileRef{}, fmt.Errorf("%s names a sandbox file, and no sandbox is bound to this conversation", s)
		}
		if p == "" {
			return fileRef{}, fmt.Errorf("path is required")
		}
		return fileRef{sandbox: true, path: p}, nil
	case sbx && looksSandbox(s):
		return fileRef{sandbox: true, path: s}, nil
	case !sbx && (strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~")):
		return fileRef{}, fmt.Errorf("%q looks like a sandbox path, and no sandbox is bound to this conversation — session files are named by their key (file_list lists them)", s)
	}
	return sessionRef(s)
}

func sessionRef(s string) (fileRef, error) {
	key, ver := s, 0
	if i := strings.LastIndexByte(s, '@'); i > 0 {
		if n, err := strconv.Atoi(s[i+1:]); err == nil && n > 0 {
			key, ver = s[:i], n
		}
	}
	k, err := normReplPath(key)
	if err != nil {
		return fileRef{}, err
	}
	return fileRef{key: k, ver: ver}, nil
}

// fileArg parses a tool's path argument for this conversation.
func fileArg(cfg Config, args map[string]any, name string) (fileRef, error) {
	return parseFileRef(str(args[name]), sandboxToolsOn(cfg))
}

// sbxCtx marks ctx as a sandbox call of run (sandboxUse re-checks the
// binding against the conversation's own), for the file tools that reach
// into the sandbox.
func sbxCtx(ctx context.Context, run *Run, cfg Config, name string) context.Context {
	if sbxCallOf(ctx).run != 0 {
		return ctx
	}
	return withSbxCall(ctx, sbxCall{run: run.ID, name: name, approve: cfg.Approve})
}

// sandboxFile resolves a sandbox path and stats it (following nothing: a
// symlink is described as one — statFollow where content is read).
func (ag *Agent) sandboxFile(ctx context.Context, run *Run, cfg Config, p string) (*sbxUse, string, error) {
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return nil, "", err
	}
	abs, err := use.path(p)
	return use, abs, err
}

// sessionFile reads a session file (a version of one), and on a miss looks
// in the sandbox for the same name.
func (ag *Agent) sessionFile(ctx context.Context, run *Run, cfg Config, r fileRef) (*ReplFile, error) {
	f, err := ag.db.fileVersion(run.ID, r.key, r.ver)
	if err != nil && r.ver == 0 {
		return nil, ag.sessionMiss(ctx, run, cfg, r.key, err)
	}
	return f, err
}

// sessionMiss adds where the file is instead: the sandbox, when it has one
// by that name (relative to its working directory). A cheap stat, only on a
// miss.
func (ag *Agent) sessionMiss(ctx context.Context, run *Run, cfg Config, key string, err error) error {
	if !sandboxToolsOn(cfg) {
		return err
	}
	ctx, cancel := context.WithTimeout(sbxCtx(ctx, run, cfg, "file"), 10*time.Second)
	defer cancel()
	use, p, uerr := ag.sandboxFile(ctx, run, cfg, key)
	if uerr != nil {
		return err
	}
	if st, serr := use.Conn.Stat(ctx, use.ID, p); serr == nil && st.Type != "dir" {
		return fmt.Errorf("%v — not a session file, but the sandbox has %s (pass that path to use it there)", err, p)
	}
	return err
}

// sandboxMiss words a sandbox not-found, adding the session file of that
// name when there is one.
func (ag *Agent) sandboxMiss(run *Run, p string, err error) error {
	if sbxRefusal(err) != "not-found" {
		return err
	}
	for _, k := range []string{strings.TrimPrefix(p, "./"), path.Base(p)} {
		if key, kerr := normReplPath(k); kerr == nil {
			if _, ferr := ag.db.replFile(run.ID, key); ferr == nil {
				return fmt.Errorf("%s doesn't exist in the sandbox — session:%s is a session file", p, key)
			}
		}
	}
	return noPath(p, err)
}

// --- copying a sandbox file in -------------------------------------------------------

// fetchSandboxFile copies the sandbox file p into the session files as name
// ("" = its own name): in place — a new version of the session file, the
// replaced one kept — or, keepBoth, under a free name as uploads are. A
// session file that came from this very file, whose etag the sandbox still
// gives, is up to date: nothing is read or written. So is one whose content
// hashes the same.
func (ag *Agent) fetchSandboxFile(ctx context.Context, run *Run, use *sbxUse, p, name, tool string, keepBoth bool) (f, prev *ReplFile, unchanged bool, err error) {
	st, err := use.Conn.Stat(ctx, use.ID, p)
	switch {
	case err != nil:
		return nil, nil, false, ag.sandboxMiss(run, p, err)
	case st.Type == "dir":
		return nil, nil, false, fmt.Errorf("%s is a directory — pack it with bash (tar czf out.tgz …) and download the archive", p)
	case st.Size > maxBinaryFileBytes:
		return nil, nil, false, fmt.Errorf("%s is %s; a session file holds up to %s", p, humanBytes(int(st.Size)), humanBytes(maxBinaryFileBytes))
	}
	name = sanitizeUploadName(orStr(strings.TrimSpace(name), path.Base(p)))
	src := &fileSource{Kind: "sandbox", Tool: tool, Call: toolCallOf(ctx), Sandbox: use.Binding.Ref,
		Box: orStr(use.Box.Name, use.Binding.Name), Path: p, ETag: st.ETag}
	if !keepBoth && st.ETag != "" {
		if cur, err := ag.db.replFile(run.ID, name); err == nil && cur.Source != nil && cur.Source.Kind == "sandbox" &&
			cur.Source.Sandbox == src.Sandbox && cur.Source.Path == p && cur.Source.ETag == st.ETag {
			return cur, cur, true, nil
		}
	}
	body, etag, err := use.Conn.OpenFile(ctx, use.ID, p, 0, 0)
	if err != nil {
		return nil, nil, false, ag.sandboxMiss(run, p, err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxBinaryFileBytes+1))
	if err != nil {
		return nil, nil, false, fmt.Errorf("reading %s: %w", p, err)
	}
	if etag != "" {
		src.ETag = etag
	}
	if keepBoth {
		f, err := ag.acceptUploadSrc(ctx, run.ID, name, "", bytes.NewReader(data), src)
		return f, nil, false, err
	}
	return ag.putFileData(ctx, run.ID, name, data, "", src)
}

// fetched says what fetchSandboxFile did, in a tool result's words.
func fetched(p string, f, prev *ReplFile, unchanged bool) string {
	kind := "text"
	if f.Binary {
		kind = f.Mime
	}
	switch {
	case unchanged:
		return fmt.Sprintf("unchanged: the session file %s already holds %s (sha %s, v%d) — nothing written",
			f.Path, p, shortSHA(f.SHA256), f.Version)
	case prev != nil:
		was := ""
		if w := prev.Source.words(); w != "" {
			was = ", " + w
		}
		return fmt.Sprintf("downloaded %s to the session file %s (%s, %s) — v%d, replacing v%d%s; file_diff {\"a\": %q} shows what changed",
			p, f.Path, kind, humanBytes(f.Bytes), f.Version, prev.Version, was, f.Path)
	}
	return fmt.Sprintf("downloaded %s to the session file %s (%s, %s)", p, f.Path, kind, humanBytes(f.Bytes))
}

// sessionCopy is the session file a sandbox path shows as: copied in place
// (a render or a view of a sandbox file).
func (ag *Agent) sessionCopy(ctx context.Context, run *Run, cfg Config, r fileRef, tool string) (*ReplFile, string, error) {
	if !r.sandbox {
		f, err := ag.sessionFile(ctx, run, cfg, r)
		return f, "", err
	}
	ctx = sbxCtx(ctx, run, cfg, tool)
	use, p, err := ag.sandboxFile(ctx, run, cfg, r.path)
	if err != nil {
		return nil, "", err
	}
	f, prev, unchanged, err := ag.fetchSandboxFile(ctx, run, use, p, "", tool, false)
	if err != nil {
		return nil, "", err
	}
	return f, fetched(p, f, prev, unchanged), nil
}

// --- file_info, file_diff, file_list ------------------------------------------------

// side is one file of file_info/file_diff, read.
type side struct {
	label  string
	text   string // "" for a binary one
	binary bool
	bytes  int
	sha    string
	f      *ReplFile // a session file
	st     *sbxStat  // a sandbox file
	use    *sbxUse
	p      string
}

const maxDiffSide = 1 << 20

func (ag *Agent) readSide(ctx context.Context, run *Run, cfg Config, r fileRef, withText bool) (*side, error) {
	if !r.sandbox {
		f, err := ag.sessionFile(ctx, run, cfg, r)
		if err != nil {
			return nil, err
		}
		s := &side{label: fileRef{key: f.Path, ver: r.ver}.label(), binary: f.Binary, bytes: f.Bytes, f: f, text: f.Content}
		s.sha = ag.fileSHA(ctx, f)
		return s, nil
	}
	ctx = sbxCtx(ctx, run, cfg, "file")
	use, p, err := ag.sandboxFile(ctx, run, cfg, r.path)
	if err != nil {
		return nil, err
	}
	at, st, err := statFollow(ctx, use, p)
	if err != nil {
		return nil, ag.sandboxMiss(run, p, err)
	}
	if st.Type == "dir" {
		return nil, fmt.Errorf("%s is a directory in the sandbox", p)
	}
	s := &side{label: p, bytes: int(st.Size), st: st, use: use, p: at}
	if !withText && st.Size > maxBinaryFileBytes {
		return s, nil
	}
	lim := int64(maxBinaryFileBytes)
	if withText {
		lim = maxDiffSide
	}
	if st.Size > lim {
		return nil, fmt.Errorf("%s is %s — over the %s file_diff reads; diff it with bash in the sandbox", p, humanBytes(int(st.Size)), humanBytes(int(lim)))
	}
	data, _, err := use.Conn.ReadFile(ctx, use.ID, at, 0, 0, lim)
	if err != nil {
		return nil, ag.sandboxMiss(run, p, err)
	}
	s.sha, s.bytes = shaHex(data), len(data)
	if looksBinary(data) {
		s.binary = true
	} else {
		s.text = string(data)
	}
	return s, nil
}

func (ag *Agent) toolFileDiff(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	sbx := sandboxToolsOn(cfg)
	ra, err := parseFileRef(str(args["a"]), sbx)
	if err != nil {
		return "", fmt.Errorf("a: %w", err)
	}
	var rb fileRef
	switch {
	case strings.TrimSpace(str(args["b"])) != "":
		if rb, err = parseFileRef(str(args["b"]), sbx); err != nil {
			return "", fmt.Errorf("b: %w", err)
		}
	case !ra.sandbox: // one session file: its previous version → it now
		f, err := ag.sessionFile(ctx, run, cfg, fileRef{key: ra.key})
		if err != nil {
			return "", err
		}
		cur := ra.ver
		if cur == 0 {
			cur = f.Version
		}
		prev := 0
		for _, v := range ag.db.fileVersions(run.ID, ra.key) {
			if v.Version < cur {
				prev = v.Version
				break
			}
		}
		if prev == 0 {
			return "", fmt.Errorf("session:%s@%d has no earlier version kept — name b to compare it with another file", ra.key, cur)
		}
		ra, rb = fileRef{key: ra.key, ver: prev}, fileRef{key: ra.key, ver: cur}
	default: // a sandbox file: the session file copied from it → it now
		use, p, err := ag.sandboxFile(sbxCtx(ctx, run, cfg, "file_diff"), run, cfg, ra.path)
		if err != nil {
			return "", err
		}
		files, _ := ag.db.replFiles(run.ID)
		for _, f := range files {
			if f.Source != nil && f.Source.Kind == "sandbox" && f.Source.Sandbox == use.Binding.Ref && f.Source.Path == p {
				rb, ra = ra, fileRef{key: f.Path}
				break
			}
		}
		if rb.path == "" {
			return "", fmt.Errorf("no session file was copied from %s — name b to compare it with one", p)
		}
	}
	a, err := ag.readSide(ctx, run, cfg, ra, true)
	if err != nil {
		return "", err
	}
	b, err := ag.readSide(ctx, run, cfg, rb, true)
	if err != nil {
		return "", err
	}
	desc := func(s *side) string {
		return fmt.Sprintf("%s (%s, sha %s)", s.label, humanBytes(s.bytes), shortSHA(s.sha))
	}
	if a.sha != "" && a.sha == b.sha {
		return fmt.Sprintf("identical: %s and %s have the same content (sha %s)", a.label, b.label, shortSHA(a.sha)), nil
	}
	if a.binary || b.binary {
		return fmt.Sprintf("binary content differs: %s vs %s — no line diff for binary files", desc(a), desc(b)), nil
	}
	out, removed, added := unifiedDiff(a.label, b.label, a.text, b.text)
	if removed == 0 && added == 0 {
		return fmt.Sprintf("%s and %s differ only in a final newline or line endings", desc(a), desc(b)), nil
	}
	if len(out) > maxDiffBytes {
		cut := strings.LastIndexByte(out[:maxDiffBytes], '\n')
		out = out[:cut+1] + fmt.Sprintf("… [diff cut at %s — narrow it: diff a part with bash, or file_read a range]\n", humanBytes(maxDiffBytes))
	}
	return fmt.Sprintf("%s → %s: -%d +%d lines\n%s", desc(a), desc(b), removed, added, strings.TrimSuffix(out, "\n")), nil
}

func (ag *Agent) toolFileInfo(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	r, err := fileArg(cfg, args, "path")
	if err != nil {
		return "", err
	}
	s, err := ag.readSide(ctx, run, cfg, r, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if s.f != nil {
		f := s.f
		kind := orStr(f.Mime, "text")
		fmt.Fprintf(&b, "%s — session file · %s · %s · v%d · updated %s\n", s.label, kind, humanBytes(f.Bytes), f.Version,
			time.Unix(f.Updated, 0).UTC().Format(time.RFC3339))
		fmt.Fprintf(&b, "sha256 %s\n", orStr(s.sha, "unknown"))
		if w := f.Source.words(); w != "" {
			fmt.Fprintf(&b, "source: %s\n", w)
		} else {
			b.WriteString("source: not recorded (written before sources were, or by an older backend)\n")
		}
		if f.Parent > 0 {
			fmt.Fprintf(&b, "replaced v%d\n", f.Parent)
		}
		if f.Source != nil && f.Source.Kind == "sandbox" {
			if note := ag.sandboxDrift(ctx, run, cfg, []*ReplFile{f})[f.Path]; note != "" {
				fmt.Fprintf(&b, "the sandbox copy: %s\n", note)
			} else if sandboxToolsOn(cfg) {
				b.WriteString("the sandbox copy: unchanged since (same etag)\n")
			}
		}
		if same := ag.sameContent(ctx, run.ID)[f.Path]; len(same) > 0 {
			fmt.Fprintf(&b, "same content as: %s\n", strings.Join(same, ", "))
		}
		if vs := ag.db.fileVersions(run.ID, f.Path); len(vs) > 0 {
			b.WriteString("earlier versions kept (file_diff or file_read session:" + f.Path + "@N):\n")
			for _, v := range vs {
				fmt.Fprintf(&b, "  v%d · %s · sha %s", v.Version, humanBytes(v.Bytes), orStr(shortSHA(v.SHA256), "?"))
				if w := v.Source.words(); w != "" {
					b.WriteString(" · " + w)
				}
				b.WriteByte('\n')
			}
		}
		return strings.TrimSuffix(b.String(), "\n"), nil
	}
	st := s.st
	fmt.Fprintf(&b, "%s — in the sandbox %q · %s · %s · mode %s · modified %s\n", s.label, orStr(s.use.Box.Name, s.use.Binding.Ref),
		st.Type, humanBytes(int(st.Size)), orStr(string(st.Mode), "?"), time.UnixMilli(st.MtimeMs).UTC().Format(time.RFC3339))
	if st.ETag != "" {
		fmt.Fprintf(&b, "etag %s\n", st.ETag)
	}
	if s.sha != "" {
		kind := "text"
		if s.binary {
			kind = "binary"
		}
		fmt.Fprintf(&b, "sha256 %s (%s)\n", s.sha, kind)
	}
	files, _ := ag.db.replFiles(run.ID)
	var copies []string
	for _, f := range files {
		if f.Source != nil && f.Source.Kind == "sandbox" && f.Source.Sandbox == s.use.Binding.Ref && f.Source.Path == s.label {
			state := "differs now"
			if sum := ag.fileSHA(ctx, f); sum != "" && sum == s.sha {
				state = "same content"
			}
			copies = append(copies, fmt.Sprintf("%s (v%d, %s)", f.Path, f.Version, state))
		}
	}
	if len(copies) > 0 {
		fmt.Fprintf(&b, "session copies: %s\n", strings.Join(copies, ", "))
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// sameContent maps each session file to the others with the same content.
// Text hashes are computed here when not recorded (cheap: ≤ 512 KiB of text
// in all); a binary file without a recorded hash is left out.
func (ag *Agent) sameContent(ctx context.Context, runID int64) map[string][]string {
	files, err := ag.db.replFiles(runID)
	if err != nil {
		return nil
	}
	by := map[string][]string{}
	for _, f := range files {
		sum := f.SHA256
		if sum == "" && !f.Binary {
			if full, err := ag.db.replFile(runID, f.Path); err == nil {
				sum = shaHex([]byte(full.Content))
			}
		}
		if sum != "" {
			by[sum] = append(by[sum], f.Path)
		}
	}
	out := map[string][]string{}
	for _, ps := range by {
		for _, p := range ps {
			for _, q := range ps {
				if q != p {
					out[p] = append(out[p], q)
				}
			}
		}
	}
	return out
}

// sandboxDrift says, for session files copied from a sandbox this
// conversation still has attached, whether the sandbox's copy changed since:
// one stat each (etag), at most 10, and nothing when no sandbox is bound.
func (ag *Agent) sandboxDrift(ctx context.Context, run *Run, cfg Config, files []*ReplFile) map[string]string {
	out := map[string]string{}
	if !sandboxToolsOn(cfg) {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	uses := map[string]*sbxUse{}
	n := 0
	for _, f := range files {
		s := f.Source
		if s == nil || s.Kind != "sandbox" || s.ETag == "" || n >= 10 {
			continue
		}
		if _, ok := cfg.sandboxBinding(s.Sandbox); !ok {
			continue
		}
		use, ok := uses[s.Sandbox]
		if !ok {
			ref := s.Sandbox
			if cfg.Sandbox != nil && cfg.Sandbox.Ref == ref {
				ref = ""
			}
			use, _ = ag.sandboxUse(sbxCtx(ctx, run, cfg, "file_list"), rootOf(run), cfg, ref)
			uses[s.Sandbox] = use
		}
		if use == nil {
			continue
		}
		n++
		st, err := use.Conn.Stat(ctx, use.ID, s.Path)
		switch {
		case sbxRefusal(err) == "not-found":
			out[f.Path] = "gone from the sandbox since"
		case err == nil && st.ETag != "" && st.ETag != s.ETag:
			out[f.Path] = "changed in the sandbox since (sandbox_download it again to update)"
		}
	}
	return out
}

// fileListing is file_list: every session file with its size, version, a
// short hash and where it came from, flagging copies with the same content
// and sandbox copies that changed since.
func (ag *Agent) fileListing(ctx context.Context, run *Run, cfg Config) (string, error) {
	files, err := ag.db.replFiles(run.ID)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "session files: (none)", nil
	}
	same := ag.sameContent(ctx, run.ID)
	drift := ag.sandboxDrift(ctx, run, cfg, files)
	var b strings.Builder
	total := 0
	for _, f := range files {
		total += f.Bytes
	}
	fmt.Fprintf(&b, "session files — %d file(s), %s:\n", len(files), humanBytes(total))
	for _, f := range files {
		kind := humanBytes(f.Bytes)
		if f.Binary {
			kind = f.Mime + ", " + kind
		}
		sum := f.SHA256
		if sum == "" && !f.Binary {
			if full, err := ag.db.replFile(run.ID, f.Path); err == nil {
				sum = shaHex([]byte(full.Content))
			}
		}
		fmt.Fprintf(&b, "  %s (%s) · v%d", f.Path, kind, f.Version)
		if sum != "" {
			fmt.Fprintf(&b, " · sha %s", shortSHA(sum))
		}
		if w := f.Source.words(); w != "" {
			b.WriteString(" · " + w)
		}
		if s := same[f.Path]; len(s) > 0 {
			sort.Strings(s)
			fmt.Fprintf(&b, " · same content as %s", strings.Join(s, ", "))
		}
		if d := drift[f.Path]; d != "" {
			b.WriteString(" · " + d)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
