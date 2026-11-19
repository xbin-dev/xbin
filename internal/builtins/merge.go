package builtins

// merge.go — the 3-way merge of one of a unit's files (applyMerge), and
// keeping a manifest's "partition" through it (partition.go, PD-52).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// mergeFile runs `git merge-file -p` on (ours, base, theirs) and returns the
// merged bytes (with conflict markers where both sides changed the same lines).
func mergeFile(ours, base, theirs []byte) ([]byte, error) {
	merged, _, err := mergeFileC(ours, base, theirs)
	return merged, err
}

// mergeFileC is mergeFile, and whether the merge conflicted: git
// merge-file's exit status is the number of conflicts it left (D248's safe
// mode writes a file only when that is none).
func mergeFileC(ours, base, theirs []byte) ([]byte, bool, error) {
	dir, err := os.MkdirTemp("", "bx-merge")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	write := func(name string, b []byte) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, b, 0o644)
	}
	op, _ := write("ours", ours)
	bp, _ := write("base", base)
	tp, _ := write("theirs", theirs)
	// confined (D78): "ours" is the installed tile's file
	outs, err := confine.GitCmd(context.Background(), confine.Cmd{Dir: dir}, "merge-file", "-p", "--diff3", op, bp, tp)
	// git merge-file exits with the conflict count (>0) — not a real error.
	conflicted := false
	if err != nil {
		code, ok := confine.ExitCode(err)
		if !ok || code < 0 {
			return nil, false, fmt.Errorf("git merge-file: %w", err)
		}
		conflicted = code > 0
	}
	return []byte(outs), conflicted, nil
}

// mergeKeeping is mergeFile for one of a unit's files, theirs already
// carrying base's partition (mergeTheirs), so git carries ours' line as it
// is. A manifest whose merge leaves ours' partition line inside a conflict
// — so resolving it one way would drop the builder's value — is merged
// again without the key, and ours' value put back: where ours has it when
// that merge is clean, else right after the opening brace, beside the
// markers.
func mergeKeeping(rel string, ours, base, theirs []byte) ([]byte, error) {
	merged, _, err := mergeKeepingC(rel, ours, base, theirs)
	return merged, err
}

// mergeKeepingC is mergeKeeping, and whether the merge it settled on
// conflicted (mergeFileC).
func mergeKeepingC(rel string, ours, base, theirs []byte) ([]byte, bool, error) {
	merged, conflicted, err := mergeFileC(ours, base, theirs)
	if err != nil || !isManifest(rel) {
		return merged, conflicted, err
	}
	if _, clean := readPartition(merged); clean {
		return merged, conflicted, nil
	}
	inst, ok := readPartition(ours)
	if !ok {
		return merged, conflicted, nil // ours can't be read: nothing to keep it against
	}
	if keeps(merged, sideOurs, inst) && keeps(merged, sideTheirs, inst) {
		return merged, conflicted, nil
	}
	merged, conflicted, err = mergeFileC(withoutPartition(ours), withoutPartition(base), withoutPartition(theirs))
	if err != nil || !inst.has {
		return merged, conflicted, err
	}
	if out, err := setPartition(merged, inst, ours); err == nil {
		return out, conflicted, nil
	}
	return insertFirst(merged, inst.raw), conflicted, nil
}

// keeps reports whether resolving every conflict of merged to side leaves
// a manifest carrying v.
func keeps(merged []byte, side int, v pval) bool {
	doc, ok := conflictSide(merged, side)
	if !ok {
		return false
	}
	got, ok := readPartition(doc)
	return ok && got.same(v)
}

// withoutPartition is doc without its top-level "partition" (doc itself
// when it doesn't parse).
func withoutPartition(doc []byte) []byte {
	if out, err := jsonc.DeleteTopLevel(doc, partitionKey); err == nil {
		return out
	}
	return doc
}

// insertFirst puts `"partition": raw,` right after a manifest's opening
// brace, on its own line — for a merge result that doesn't parse (conflict
// markers), where no JSONC-aware splice can.
func insertFirst(doc, raw []byte) []byte {
	i := bytes.IndexByte(doc, '{')
	if i < 0 {
		return doc
	}
	indent := "  "
	if rest := doc[i+1:]; bytes.HasPrefix(rest, []byte("\n")) {
		line := rest[1:]
		if j := bytes.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		if w := len(line) - len(bytes.TrimLeft(line, " \t")); w > 0 {
			indent = string(line[:w])
		}
	}
	return []byte(string(doc[:i+1]) + "\n" + indent + `"` + partitionKey + `": ` + string(raw) + "," + string(doc[i+1:]))
}

// The sides of a conflict block git merge-file leaves (--diff3 style).
const (
	sideOurs   = 0
	sideBase   = 1
	sideTheirs = 2
)

// conflictSide is doc with every conflict block resolved to side; ok is
// false when doc has no conflict markers, or they don't pair up.
func conflictSide(doc []byte, side int) ([]byte, bool) {
	var out bytes.Buffer
	in, found := -1, false // -1: outside a block
	for _, ln := range strings.SplitAfter(string(doc), "\n") {
		switch {
		case isMarker(ln, "<<<<<<<") && in == -1:
			in, found = sideOurs, true
			continue
		case isMarker(ln, "|||||||") && in == sideOurs:
			in = sideBase
			continue
		case isMarker(ln, "=======") && (in == sideOurs || in == sideBase):
			in = sideTheirs
			continue
		case isMarker(ln, ">>>>>>>") && in == sideTheirs:
			in = -1
			continue
		case isMarker(ln, "<<<<<<<"), isMarker(ln, ">>>>>>>"):
			return nil, false
		}
		if in == -1 || in == side {
			out.WriteString(ln)
		}
	}
	if !found || in != -1 {
		return nil, false
	}
	return out.Bytes(), true
}

// isMarker reports whether ln is a conflict marker line m (a label may
// follow after a space).
func isMarker(ln, m string) bool {
	rest, ok := strings.CutPrefix(strings.TrimRight(ln, "\r\n"), m)
	return ok && (rest == "" || rest[0] == ' ')
}
