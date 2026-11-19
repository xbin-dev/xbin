package builtins

// tilefs.go — an installed unit's files, read and written as the files at
// their paths and nothing else. The tile's writers (its sandboxes, its
// terminal) control what is there: a symbolic link (to a home's
// credentials, to another tile's backend), a FIFO (which blocks a reader,
// and tells the writer when xbind reads), a directory where a file was.
// xbind reads to compare, to merge and to put in a proposal's series that
// the tile's readers can fetch — so it never follows a link, never opens a
// FIFO, never reads more than maxTileFile; such a file is a conflict, said
// by path ("apps/chat/index.html is a symbolic link: not read"), and no
// update writes it. It writes beneath the tile without following a link on
// the way (fsutil.MkdirAllIn, fsutil.OpenRootIn), one file at a time
// through a temporary file and a rename, and — where it read the file to
// decide — only while the file is still what it read.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// maxTileFile bounds a file an update reads from a tile.
const maxTileFile = 32 << 20

// tileFile is one installed file as an update reads it: its bytes, absent,
// or refused (a link, not a regular file, too large) with why.
type tileFile struct {
	data    []byte
	present bool
	refused string // "is a symbolic link" … ("" when read or absent)
}

// stamp is what a write checks the file is still: its hash, "-" when
// absent, or the refusal.
func (f tileFile) stamp() string {
	switch {
	case f.refused != "":
		return "!" + f.refused
	case !f.present:
		return "-"
	}
	return sha(f.data)
}

// readTile reads installPath/rel beneath root (the workspace) with no
// link on the way (none at all: an in-tree one is refused too).
func readTile(root, installPath, rel string) tileFile {
	full := path.Join(installPath, rel)
	f, err := fsutil.OpenNoSymlinks(root, full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tileFile{}
		}
		return tileFile{refused: whyNot(root, full, err)}
	}
	defer f.Close()
	fi, err := f.Stat()
	switch {
	case err != nil:
		return tileFile{refused: "can't be read (" + err.Error() + ")"}
	case fi.IsDir():
		return tileFile{refused: "is a directory"}
	case fi.Size() > maxTileFile:
		return tileFile{refused: fmt.Sprintf("is larger than %d MiB", maxTileFile>>20)}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTileFile+1))
	switch {
	case err != nil:
		return tileFile{refused: "can't be read (" + err.Error() + ")"}
	case len(data) > maxTileFile:
		return tileFile{refused: fmt.Sprintf("is larger than %d MiB", maxTileFile>>20)}
	}
	return tileFile{data: data, present: true}
}

// whyNot words an open's refusal of full (workspace-relative).
func whyNot(root, full string, err error) string {
	switch {
	case errors.Is(err, fsutil.ErrEscapes), errors.Is(err, syscall.ELOOP):
		if fi, lerr := os.Lstat(filepath.Join(root, filepath.FromSlash(full))); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "is a symbolic link"
		}
		return "is beneath a symbolic link"
	case errors.Is(err, fsutil.ErrNotRegular):
		return "is not a regular file (a FIFO, a socket or a device)"
	case errors.Is(err, syscall.ENOTDIR):
		return "is beneath a file where a directory belongs"
	}
	return "can't be read (" + err.Error() + ")"
}

// refusedNote is how a refused file is said.
func refusedNote(installPath, rel, why string) string {
	return installPath + "/" + rel + " " + why + ": not read, and no update writes it"
}

// hasTile reports whether anything at all is at installPath/rel (a link
// too), without following one on the way.
func hasTile(root, installPath, rel string) bool {
	f, err := fsutil.OpenNoSymlinks(root, path.Join(installPath, rel))
	if err == nil {
		f.Close()
		return true
	}
	return !errors.Is(err, os.ErrNotExist)
}

// beforeWrite, when set (tests only), runs as a write or a removal of a
// file begins: what a tile's writer may do while an update is between
// reading a file and writing it.
var beforeWrite func(full string)

// ChangedError says a file changed between the update reading it and
// writing it: the update stopped there, writing nothing more.
type ChangedError struct{ Path string }

func (e *ChangedError) Error() string {
	return e.Path + " changed while the update was being written: it stopped there and wrote nothing more — look again and update again"
}

// writeTile writes data as installPath/rel: parents made beneath the
// workspace with no link on the way, a temporary file beside it renamed
// over it (a link there is replaced, never followed). want, when not "",
// is the stamp the decision read: a file that is no longer that is left as
// it is (*ChangedError). A file already holding data is not written.
func writeTile(root, installPath, rel string, data []byte, want string) (bool, error) {
	full := path.Join(installPath, rel)
	if beforeWrite != nil {
		beforeWrite(full)
	}
	if want != "" {
		now := readTile(root, installPath, rel)
		if now.stamp() != want {
			return false, &ChangedError{Path: full}
		}
		if now.present && string(now.data) == string(data) {
			return false, nil
		}
	}
	dir, base := path.Split(full)
	dir = strings.TrimSuffix(dir, "/")
	if err := fsutil.MkdirAllIn(root, filepath.FromSlash(dir), 0o755); err != nil {
		return false, writeErr(full, err)
	}
	r, err := fsutil.OpenRootIn(root, dir)
	if err != nil {
		return false, writeErr(full, err)
	}
	defer r.Close()
	if fi, err := r.Lstat(base); err == nil && fi.IsDir() {
		return false, fmt.Errorf("%s is a directory: not written", full)
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := "." + base + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = r.Rename(tmp, base)
	}
	if werr != nil {
		_ = r.Remove(tmp)
		return false, fmt.Errorf("%s: %w", full, werr)
	}
	return true, nil
}

// removeTile removes installPath/rel (a link itself, never what it names),
// when it is still what the decision read (want, when not "").
func removeTile(root, installPath, rel, want string) error {
	full := path.Join(installPath, rel)
	if beforeWrite != nil {
		beforeWrite(full)
	}
	if want != "" && readTile(root, installPath, rel).stamp() != want {
		return &ChangedError{Path: full}
	}
	dir, base := path.Split(full)
	r, err := fsutil.OpenRootIn(root, strings.TrimSuffix(dir, "/"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return writeErr(full, err)
	}
	defer r.Close()
	fi, err := r.Lstat(base)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		return fmt.Errorf("%s is a directory: not removed", full)
	}
	if err := r.Remove(base); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeErr words a write's refusal of full.
func writeErr(full string, err error) error {
	switch {
	case errors.Is(err, fsutil.ErrEscapes), errors.Is(err, syscall.ELOOP):
		return fmt.Errorf("%s is beneath a symbolic link: not written", full)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%s is beneath a file where a directory belongs: not written", full)
	}
	return fmt.Errorf("%s: %w", full, err)
}

// nestedIn is the directory of a component of its own (a manifest or a
// page of its own, which the unit doesn't ship) that rel lies in beneath
// installPath, or "": an update writes in the unit's own files only, never
// in a tile nested in it, which may be someone else's.
func nestedIn(root, installPath, rel string, ships func(rel string) bool) string {
	dir := path.Dir(rel)
	var dirs []string
	for dir != "." && dir != "/" && dir != "" {
		dirs = append([]string{dir}, dirs...)
		dir = path.Dir(dir)
	}
	for _, d := range dirs {
		for _, f := range []string{"xbin.json", "index.html"} {
			if ships(d + "/" + f) {
				continue
			}
			tf := readTile(root, installPath, d+"/"+f)
			if tf.present || tf.refused != "" && !strings.HasPrefix(tf.refused, "is beneath") {
				return installPath + "/" + d
			}
		}
	}
	return ""
}

// leftAlone is why an update leaves installPath/rel as it is ("" when it
// may write it): it lies beneath a link or in a nested component — or, but
// for a replace (which puts a regular file in place of a link, a FIFO or
// the like, never following it), it isn't a regular file it could read.
// tf is the file as read.
func leftAlone(root, installPath, rel string, tf tileFile, ships func(string) bool, replace bool) string {
	switch {
	case tf.refused == "":
	case !replace, strings.HasPrefix(tf.refused, "is beneath"), tf.refused == "is a directory":
		return refusedNote(installPath, rel, tf.refused)
	}
	if n := nestedIn(root, installPath, rel, ships); n != "" {
		return nestedNote(installPath, rel, n)
	}
	return ""
}

// shipsOf is "the unit ships rel": a file of upstream's (theirs) or of the
// recorded base.
func shipsOf(theirs map[string][]byte, base map[string]string) func(string) bool {
	return func(rel string) bool {
		_, a := theirs[rel]
		_, b := base[rel]
		return a || b
	}
}

// nestedNote is how a file in a nested component is said.
func nestedNote(installPath, rel, nested string) string {
	return installPath + "/" + rel + " is inside " + nested + ", a component of its own: no update of " + installPath + " writes in it"
}

// writtenSoFar adds to err the files written before it.
func writtenSoFar(err error, written []string) error {
	if err == nil || len(written) == 0 {
		return err
	}
	return fmt.Errorf("%w (written before it stopped: %s)", err, strings.Join(written, ", "))
}
