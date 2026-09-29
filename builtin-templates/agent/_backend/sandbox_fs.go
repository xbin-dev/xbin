// sandbox_fs.go — the coding tools for files in a sandbox (D115): read,
// write, edit, ls, glob and grep, over the contract's file routes and `run`
// (docs/sandbox-manager.md). The contract has no glob or grep of its own (so
// it stays implementable on a cloud over ssh): they run rg where the sandbox
// has it, else find and grep, and the results are matched, capped and
// normalised here.
package main

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	readWhole   = 256 << 10 // a file this small is read whole and sliced here
	readLines   = 2000      // lines read returns by default
	readBudget  = 14 << 10  // read's result (under the transcript's cap)
	readLineMax = 1000      // characters of one line
	editMax     = 4 << 20   // edit reads and rewrites the whole file
	lsMax       = 500
	globMax     = 200     // paths glob returns
	globScan    = 20000   // files it looks at
	grepMax     = 100     // matches grep returns
	grepScan    = 2000    // matching lines it looks at
	runOutMax   = 1 << 20 // a helper command's output
	runTimeout  = 60000   // ms
)

var sandboxFileTools = map[string]bool{"read": true, "write": true, "edit": true, "ls": true, "glob": true, "grep": true}

func sandboxFileSpecs() []toolSpec {
	return []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "read",
			Description: "Read a text file in the coding sandbox — not a session file (file_read reads those) — as numbered lines (cat -n style), the first 2000 unless offset/limit say otherwise (about 14 KB at most). " +
				"Read a file before you edit it.",
			Parameters: obj([]string{"path"}, map[string]any{
				"path":   strProp("the file: absolute, relative to the working directory, or ~/…"),
				"offset": intProp("1-based first line (default 1)"),
				"limit":  intProp("how many lines (default 2000)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "write",
			Description: "Create or replace a whole file in the coding sandbox — not a session file (file_write writes those). Missing directories are created. " +
				"A symlink at path is replaced by the file (read and edit follow symlinks; to write through one, write its target).",
			Parameters: obj([]string{"path", "content"}, map[string]any{
				"path":    strProp("the file: absolute, relative to the working directory, or ~/…"),
				"content": strProp("the whole new content"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "edit",
			Description: "Replace an exact string in a text file (up to 4 MB) in the coding sandbox — not a session file (file_edit edits those). " +
				"old_string must appear EXACTLY ONCE — include surrounding lines to make it unique — unless replace_all is true. " +
				"The write is refused if the file changed meanwhile (then it re-reads and tries once more).",
			Parameters: obj([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        strProp("the file"),
				"old_string":  strProp("exact text to replace, including indentation"),
				"new_string":  strProp("replacement text (empty string deletes it)"),
				"replace_all": boolProp("replace every occurrence instead of requiring exactly one"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "ls",
			Description: "List a directory in the coding sandbox (default: the working directory), at most 500 entries: subdirectories end in /, files show their size.",
			Parameters:  obj(nil, map[string]any{"path": strProp("the directory")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "glob",
			Description: "Find files by name in the coding sandbox, at most 200 paths (relative to the working directory). " +
				"** spans directories ('src/**/*.ts'); a pattern without / matches file names at any depth ('*.go'); {a,b} alternates. " +
				"Skips .git (and, where the sandbox has rg, what .gitignore ignores).",
			Parameters: obj([]string{"pattern"}, map[string]any{
				"pattern": strProp("the glob"),
				"path":    strProp("where to look (default: the working directory)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "grep",
			Description: "Search file contents in the coding sandbox with a regular expression (ripgrep syntax where the sandbox has rg, else grep -E), at most 100 matches. " +
				"Returns path:line: text; glob narrows the files ('*.go'). Skips .git and binary files.",
			Parameters: obj([]string{"pattern"}, map[string]any{
				"pattern":     strProp("the regular expression"),
				"path":        strProp("a directory or file (default: the working directory)"),
				"glob":        strProp("only files whose name matches this glob"),
				"ignore_case": boolProp("match case-insensitively"),
			}),
		}},
	}
}

func (ag *Agent) runSandboxFileTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return "", err
	}
	switch name {
	case "read":
		return sbxRead(ctx, use, args)
	case "write":
		return sbxWriteTool(ctx, use, args)
	case "edit":
		return sbxEdit(ctx, use, args)
	case "ls":
		return sbxLs(ctx, use, args)
	case "glob":
		return sbxGlob(ctx, use, args)
	case "grep":
		return sbxGrep(ctx, use, args)
	}
	return "", fmt.Errorf("unknown sandbox tool %q", name)
}

// noPath words a manager's not-found for a path the model named (its own
// hint is about sandboxes).
func noPath(p string, err error) error {
	if sbxRefusal(err) == "not-found" {
		return &sbxError{Refusal: "not-found", Msg: p + " doesn't exist in the sandbox"}
	}
	return err
}

// looksBinary: a NUL, or bytes that aren't UTF-8, near the start.
func looksBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	if i := lastRuneStart(b); i < len(b) && !utf8.FullRune(b[i:]) {
		b = b[:i] // a sample cut mid-character
	}
	return bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b)
}

func binaryHint(p string, size int64) string {
	return fmt.Sprintf("%s is a binary file (%s) — not readable as text; bash can inspect it (file, xxd … | head), sandbox_download brings it into the session files (file_view shows an image)",
		p, humanBytes(int(size)))
}

// --- read -------------------------------------------------------------------------

func sbxRead(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	p, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	at, st, err := statFollow(ctx, use, p)
	if err != nil {
		return "", err
	}
	if st.Type == "dir" {
		return "", fmt.Errorf("%s is a directory — ls lists it", p)
	}
	if st.Size == 0 {
		return fmt.Sprintf("(%s is empty)", p), nil
	}
	offset, limit := max(1, toInt(args["offset"])), toInt(args["limit"])
	if limit <= 0 {
		limit = readLines
	}
	if st.Size <= readWhole {
		data, _, err := use.Conn.ReadFile(ctx, use.ID, at, 0, 0, 2*readWhole)
		if err != nil {
			return "", err
		}
		if looksBinary(data) {
			return binaryHint(p, int64(len(data))), nil
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if offset > len(lines) {
			return fmt.Sprintf("(offset %d is past the end — %s has %d lines)", offset, p, len(lines)), nil
		}
		end := min(len(lines), offset-1+limit)
		return numbered(lines[offset-1:end], offset, len(lines)), nil
	}
	// a large file: a ranged read says whether it is text, sed cuts the lines
	head, _, err := use.Conn.ReadFile(ctx, use.ID, at, 0, 8192, 8192)
	if err != nil {
		return "", err
	}
	if looksBinary(head) {
		return binaryHint(p, st.Size), nil
	}
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Argv: []string{"sed", "-n", fmt.Sprintf("%d,%dp", offset, offset+limit-1), at},
		TimeoutMs: runTimeout, MaxOutput: runOutMax})
	if err != nil {
		return "", err
	}
	if !res.ok() {
		return "", fmt.Errorf("reading %s: %s", p, runErrText(res))
	}
	lines, _ := runLines(res.Stdout)
	if len(lines) == 0 {
		return fmt.Sprintf("(offset %d is past the end of %s)", offset, p), nil
	}
	return numbered(lines, offset, -1) + fmt.Sprintf("\n[%s is %s — offset/limit read further]", p, humanBytes(int(st.Size))), nil
}

// numbered renders lines from line first on (total -1: unknown), within
// read's budget, saying what it left out.
func numbered(lines []string, first, total int) string {
	var b strings.Builder
	n := first
	for _, l := range lines {
		if b.Len() > readBudget {
			fmt.Fprintf(&b, "… [cut at line %d to keep the result short — read with offset=%d for more]\n", n, n)
			return strings.TrimSuffix(b.String(), "\n")
		}
		if utf8.RuneCountInString(l) > readLineMax {
			l = string([]rune(l)[:readLineMax]) + " …[line clipped]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", n, l)
		n++
	}
	if total >= 0 && n <= total {
		fmt.Fprintf(&b, "… [lines %d–%d of %d not shown — read with offset=%d]\n", n, total, total, n)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// maxLinks bounds a chain of symlinks read and edit follow (the kernel's
// ELOOP bound).
const maxLinks = 40

// statFollow stats p, following a final symlink — and the chain of them —
// inside the sandbox: files/stat describes a link itself (lstat), so read and
// edit resolve it here, each target against its link's directory. It is the
// path the content is at, and its stat.
func statFollow(ctx context.Context, use *sbxUse, p string) (string, *sbxStat, error) {
	at := p
	for hops := 0; ; hops++ {
		st, err := use.Conn.Stat(ctx, use.ID, at)
		switch {
		case err != nil && at != p && sbxRefusal(err) == "not-found":
			return "", nil, &sbxError{Refusal: "not-found", Msg: fmt.Sprintf("%s is a symlink to %s, which doesn't exist in the sandbox", p, at)}
		case err != nil:
			return "", nil, noPath(p, err)
		case st.Type != "symlink":
			return at, st, nil
		case hops == maxLinks:
			return "", nil, fmt.Errorf("%s: too many levels of symbolic links", p)
		case st.Target == "":
			return "", nil, fmt.Errorf("%s is a symlink whose target the sandbox doesn't say", at)
		}
		t := st.Target
		if !strings.HasPrefix(t, "/") {
			t = path.Join(path.Dir(at), t)
		}
		at = path.Clean(t)
	}
}

// --- write, edit ---------------------------------------------------------------------

func sbxWriteTool(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("write needs content (the whole file, as a string)")
	}
	p, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	if _, err := use.Conn.WriteFile(ctx, use.ID, p, strings.NewReader(content), sbxWrite{Mkdirs: true}); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%s)", p, humanBytes(len(content))), nil
}

func sbxEdit(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	p, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	oldS, newS := str(args["old_string"]), str(args["new_string"])
	if err := checkEdit(oldS, newS, "write"); err != nil {
		return "", err
	}
	all, _ := args["replace_all"].(bool)
	for attempt := 0; ; attempt++ {
		// a symlink is edited where it points: the link stays a link, and the
		// write's precondition is the etag of the content that was read
		at, st, err := statFollow(ctx, use, p)
		if err != nil {
			return "", err
		}
		switch {
		case st.Type == "dir":
			return "", fmt.Errorf("%s is a directory", p)
		case st.Size > editMax:
			return "", fmt.Errorf("%s is %s — too large to edit here; use bash (sed -i) or write", p, humanBytes(int(st.Size)))
		}
		data, etag, err := use.Conn.ReadFile(ctx, use.ID, at, 0, 0, editMax)
		if err != nil {
			return "", err
		}
		if looksBinary(data) {
			return "", fmt.Errorf("%s is a binary file and cannot be edited as text", p)
		}
		content := string(data)
		updated, n, err := applyEdit(content, oldS, newS, all, p, "read")
		if err != nil {
			return "", err
		}
		_, err = use.Conn.WriteFile(ctx, use.ID, at, strings.NewReader(updated), sbxWrite{IfMatch: orStr(etag, st.ETag)})
		if sbxRefusal(err) == "precondition" && attempt == 0 {
			continue // it changed between the read and the write: once more, from the top
		}
		if err != nil {
			return "", err
		}
		what := "1 replacement"
		if all && n > 1 {
			what = fmt.Sprintf("%d replacements", n)
		}
		where := p
		if at != p {
			where = p + " → " + at
		}
		return fmt.Sprintf("edited %s (%s)\n%s", where, what, editSnippet(updated, strings.Index(content, oldS), newS)), nil
	}
}

// editSnippet shows the lines around the first replacement, numbered.
func editSnippet(updated string, at int, newS string) string {
	lines := strings.Split(updated, "\n")
	first := strings.Count(updated[:at], "\n") // 0-based line of the change
	last := first + strings.Count(newS, "\n")
	from, to := max(0, first-3), min(len(lines), last+4)
	if to-from > 20 {
		to = from + 20
	}
	return numbered(lines[from:to], from+1, -1)
}

// --- ls, glob, grep ---------------------------------------------------------------------

func sbxLs(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	p, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	l, err := use.Conn.ListDir(ctx, use.ID, p, lsMax)
	if err != nil {
		return "", noPath(p, err)
	}
	if len(l.Entries) == 0 {
		return p + ": (empty)", nil
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		di, dj := l.Entries[i].Type == "dir", l.Entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return l.Entries[i].Name < l.Entries[j].Name
	})
	var b strings.Builder
	b.WriteString(p + ":\n")
	for _, e := range l.Entries {
		switch e.Type {
		case "dir":
			b.WriteString(e.Name + "/\n")
		case "symlink":
			fmt.Fprintf(&b, "%s -> %s\n", e.Name, e.Target)
		case "file":
			fmt.Fprintf(&b, "%s  (%s)\n", e.Name, humanBytes(int(e.Size)))
		default:
			fmt.Fprintf(&b, "%s  (%s)\n", e.Name, e.Type)
		}
	}
	if l.Truncated {
		fmt.Fprintf(&b, "… [more entries — the listing stops at %d]\n", lsMax)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// globList lists the files under $1, relative to it: rg's (which honours
// .gitignore) or find's, without .git.
const globList = `cd -- "$1" 2>/dev/null || { echo "no such directory: $1" >&2; exit 2; }
if command -v rg >/dev/null 2>&1; then rg --files --hidden --glob '!.git' 2>/dev/null
else find . \( -name .git -o -name node_modules \) -prune -o -type f -print 2>/dev/null | sed 's|^\./||'
fi | head -n ` + "20000"

func sbxGlob(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	pattern := strings.TrimPrefix(strings.TrimSpace(str(args["pattern"])), "./")
	if pattern == "" {
		return "", fmt.Errorf("glob needs a pattern")
	}
	base, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "~") {
		// an absolute pattern: its fixed leading directories are where to look
		abs, err := use.path(pattern)
		if err != nil {
			return "", err
		}
		segs := strings.Split(strings.TrimPrefix(abs, "/"), "/")
		i := 0
		for i < len(segs)-1 && !strings.ContainsAny(segs[i], "*?[{") {
			i++
		}
		base, pattern = "/"+strings.Join(segs[:i], "/"), strings.Join(segs[i:], "/")
	}
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Argv: []string{"sh", "-c", globList, "sh", base}, TimeoutMs: runTimeout, MaxOutput: runOutMax})
	if err != nil {
		return "", err
	}
	if !res.ok() {
		return "", fmt.Errorf("listing %s: %s", base, runErrText(res))
	}
	files, cut := runLines(res.Stdout)
	pats := expandBraces(pattern)
	var hits []string
	for _, f := range files {
		for _, pt := range pats {
			if globMatch(pt, f) {
				hits = append(hits, use.rel(path.Join(base, f)))
				break
			}
		}
	}
	sort.Strings(hits)
	var b strings.Builder
	if len(hits) == 0 {
		fmt.Fprintf(&b, "no files match %q under %s", pattern, base)
	}
	for i, h := range hits {
		if i == globMax {
			fmt.Fprintf(&b, "… and %d more — narrow the pattern or the path\n", len(hits)-globMax)
			break
		}
		b.WriteString(h + "\n")
	}
	if cut || len(files) >= globScan {
		fmt.Fprintf(&b, "\n[looked at the first %d files only — narrow the path]", len(files))
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// globMatch matches a slash-separated path: ** spans any number of
// directories; a pattern without / matches the name at any depth.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(name))
		return ok
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for i := 0; i <= len(n); i++ {
				if matchSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], n[0]); !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// expandBraces turns a{b,c}d into abd and acd (nested and repeated groups too).
func expandBraces(p string) []string {
	i := strings.IndexByte(p, '{')
	if i < 0 {
		return []string{p}
	}
	depth, j := 0, i
	var alts []string
	start := i + 1
	for ; j < len(p); j++ {
		switch p[j] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 1 {
				alts = append(alts, p[start:j])
				start = j + 1
			}
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(p) {
		return []string{p} // unbalanced: literal
	}
	alts = append(alts, p[start:j])
	var out []string
	for _, a := range alts {
		out = append(out, expandBraces(p[:i]+a+p[j+1:])...)
	}
	return out
}

// grepRun searches $2 for $1 (glob $3, ignore case when $4): rg, else grep
// -E. Paths end in a NUL; its status goes to stderr as ::rc=N (a pipe hides
// it).
const grepRun = `p=$1 d=$2 g=$3 i=$4
{
if command -v rg >/dev/null 2>&1; then
  set -- --line-number --no-heading --color=never --with-filename --null --hidden --glob '!.git' --max-columns 500 --max-columns-preview
  [ -n "$i" ] && set -- "$@" --ignore-case
  [ -n "$g" ] && set -- "$@" --glob "$g"
  rg "$@" -e "$p" -- "$d"
else
  set -- -rnIH --null -E --exclude-dir=.git
  [ -n "$i" ] && set -- "$@" -i
  [ -n "$g" ] && set -- "$@" --include="$g"
  grep "$@" -e "$p" -- "$d"
fi
echo "::rc=$?" >&2
} | head -n ` + "2000"

func sbxGrep(ctx context.Context, use *sbxUse, args map[string]any) (string, error) {
	pattern := str(args["pattern"])
	if pattern == "" {
		return "", fmt.Errorf("grep needs a pattern")
	}
	base, err := use.path(str(args["path"]))
	if err != nil {
		return "", err
	}
	icase := ""
	if args["ignore_case"] == true {
		icase = "1"
	}
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Argv: []string{"sh", "-c", grepRun, "sh", pattern, base, strings.TrimSpace(str(args["glob"])), icase},
		TimeoutMs: runTimeout, MaxOutput: runOutMax})
	if err != nil {
		return "", err
	}
	errText := stderrText(res)
	rc := -1
	if k := strings.LastIndex(errText, "::rc="); k >= 0 {
		rc, _ = strconv.Atoi(strings.TrimSpace(errText[k+5:]))
		errText = strings.TrimSpace(errText[:k])
	}
	lines, cut := runLines(res.Stdout)
	var b strings.Builder
	shown := 0
	for _, l := range lines {
		file, rest, ok := strings.Cut(l, "\x00")
		if !ok {
			continue
		}
		if shown == grepMax {
			break
		}
		num, text, _ := strings.Cut(rest, ":")
		if utf8.RuneCountInString(text) > 300 {
			text = string([]rune(text)[:300]) + " …"
		}
		fmt.Fprintf(&b, "%s:%s: %s\n", use.rel(path.Clean(file)), num, strings.TrimRight(text, "\r"))
		shown++
	}
	if shown == 0 {
		if rc == 1 || rc == 0 && errText == "" {
			return fmt.Sprintf("no matches for %q under %s", pattern, base), nil
		}
		return "", fmt.Errorf("grep: %s", orStr(errText, fmt.Sprintf("exit %d", rc)))
	}
	switch {
	case len(lines) >= grepScan || cut || rc > 128:
		fmt.Fprintf(&b, "… [%d+ matching lines — narrow the pattern, the path or the glob]\n", len(lines))
	case len(lines) > shown:
		fmt.Fprintf(&b, "… [%d more matching lines — narrow the pattern, the path or the glob]\n", len(lines)-shown)
	}
	if errText != "" && rc == 2 {
		fmt.Fprintf(&b, "[some files could not be searched: %s]\n", clip(errText, 300))
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// runLines is a run's stream as whole lines: when the manager elided its
// middle, the lines the cut went through are dropped (cut says so).
func runLines(o *sbxOutput) (lines []string, cut bool) {
	if o == nil {
		return nil, false
	}
	split := func(s string) []string {
		s = strings.TrimSuffix(s, "\n")
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	if o.Elided == 0 {
		return split(o.Head + o.Tail), false
	}
	head, tail := split(o.Head), split(o.Tail)
	if len(head) > 0 && !strings.HasSuffix(o.Head, "\n") {
		head = head[:len(head)-1]
	}
	if len(tail) > 0 {
		tail = tail[1:] // it starts mid-line (or right after the cut)
	}
	return append(head, tail...), true
}

// ok: the command exited 0 (not a signal, not a failure).
func (r *sbxRunResult) ok() bool { return r.ExitCode != nil && *r.ExitCode == 0 }

// runErrText is what a failed helper command said on stderr.
func runErrText(r *sbxRunResult) string {
	if r.ExitCode == nil {
		return orStr(stderrText(r), "killed by "+orStr(r.Signal, "a signal"))
	}
	return orStr(stderrText(r), fmt.Sprintf("exit %d", *r.ExitCode))
}

func stderrText(r *sbxRunResult) string {
	if r.Stderr == nil {
		return ""
	}
	return strings.TrimSpace(r.Stderr.Head + r.Stderr.Tail)
}
