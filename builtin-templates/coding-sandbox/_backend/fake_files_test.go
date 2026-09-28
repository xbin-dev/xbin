package main

// fake_files_test.go — the fake backend's files, trees and snapshots (TEST
// ONLY; see fake_backend_test.go): the sandbox's directory, with every path
// resolved inside it; snapshots are copies of it under <Root>/.snaps/.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func fkETag(p string, fi fs.FileInfo) string {
	if fi.Mode().IsRegular() {
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				return hex.EncodeToString(h.Sum(nil))[:32]
			}
		}
	}
	return fmt.Sprintf("m%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

func fkType(fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "symlink"
	case fi.IsDir():
		return "dir"
	case fi.Mode().IsRegular():
		return "file"
	}
	return "other"
}

func fkStat(p, inside string) (*xbin.FileStat, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, fkNotFound(err)
	}
	st := &xbin.FileStat{Path: inside, Type: fkType(fi), Size: fi.Size(), Mode: fmt.Sprintf("%04o", fi.Mode().Perm()),
		MtimeMs: fi.ModTime().UnixMilli(), ETag: fkETag(p, fi)}
	if fi.Mode()&fs.ModeSymlink != 0 {
		st.Target, _ = os.Readlink(p)
	}
	return st, nil
}

func fkNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fkErr(http.StatusNotFound, "not-found", "no such file or directory")
	}
	return fkErr(http.StatusBadRequest, "invalid", "%v", err)
}

// file resolves p for a file operation on a usable sandbox; follow: the
// operation follows a final symlink.
func (s *fkSandbox) file(op, p string, follow bool) (*fkBox, string, error) {
	b, err := s.use(op)
	if err != nil {
		return nil, "", err
	}
	s.f.mu.Unlock()
	hp, err := b.path(p)
	if follow && err == nil {
		hp, err = b.pathFollow(p)
	}
	return b, hp, err
}

func (s *fkSandbox) Stat(ctx context.Context, p string) (*xbin.FileStat, error) {
	_, hp, err := s.file("stat", p, false)
	if err != nil {
		return nil, err
	}
	return fkStat(hp, p)
}

func (s *fkSandbox) ReadFile(ctx context.Context, p string, off, n int64) (io.ReadCloser, *xbin.FileStat, error) {
	_, hp, err := s.file("read", p, true)
	if err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(hp)
	if err != nil {
		return nil, nil, fkNotFound(err)
	}
	if fi.IsDir() {
		return nil, nil, fkErr(http.StatusBadRequest, "invalid", "%s is a directory", p)
	}
	if off < 0 {
		return nil, nil, fkErr(http.StatusBadRequest, "invalid", "offset is a byte count")
	}
	size := max(fi.Size()-off, 0)
	if n > 0 {
		size = min(size, n)
	}
	if size > s.f.fileMax() {
		return nil, nil, fkErr(http.StatusRequestEntityTooLarge, "too-large", "over limits.fileMax; read a smaller range")
	}
	f, err := os.Open(hp)
	if err != nil {
		return nil, nil, fkNotFound(err)
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, fkErr(http.StatusBadRequest, "invalid", "%v", err)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(f, size), f}, &xbin.FileStat{Path: p, ETag: fkETag(hp, fi)}, nil
}

func (s *fkSandbox) WriteFile(ctx context.Context, p string, r io.Reader, o xbin.WriteOptions) (*xbin.FileStat, error) {
	_, hp, err := s.file("write", p, false) // it replaces the entry (a symlink too), as a rename does
	if err != nil {
		return nil, err
	}
	mode := fs.FileMode(0o644)
	if o.Mode != "" {
		v, err := strconv.ParseUint(o.Mode, 8, 32)
		if err != nil || v > 0o777 {
			return nil, fkErr(http.StatusBadRequest, "invalid", "mode is octal permission bits")
		}
		mode = fs.FileMode(v)
	}
	body, err := io.ReadAll(io.LimitReader(r, s.f.fileMax()+1))
	if err != nil {
		return nil, fkErr(http.StatusBadRequest, "invalid", "%v", err)
	}
	if int64(len(body)) > s.f.fileMax() {
		return nil, fkErr(http.StatusRequestEntityTooLarge, "too-large", "over limits.fileMax")
	}
	s.f.wmu.Lock() // the check and the rename are one step against another write
	defer s.f.wmu.Unlock()
	cur, statErr := os.Lstat(hp)
	if cur != nil && cur.IsDir() {
		return nil, fkErr(http.StatusBadRequest, "invalid", "%s is a directory", p)
	}
	if im := strings.Trim(o.IfMatch, `"`); im != "" || o.IfNoneMatch != "" {
		etag := ""
		if statErr == nil {
			etag = fkETag(hp, cur)
		}
		if (im != "" && im != etag) || (o.IfNoneMatch == "*" && statErr == nil) {
			return nil, &xbin.SandboxError{Status: http.StatusPreconditionFailed, Refusal: "precondition", Message: p + " changed", ETag: etag}
		}
	}
	if o.Mkdirs {
		_ = os.MkdirAll(filepath.Dir(hp), 0o755)
	}
	if o.Mode == "" && cur != nil && cur.Mode().IsRegular() {
		mode = cur.Mode().Perm() // a replaced file keeps its mode
	}
	tmp, err := os.CreateTemp(filepath.Dir(hp), ".fk-tmp-*")
	if err != nil {
		return nil, fkNotFound(err)
	}
	_, err = tmp.Write(body)
	if err2 := tmp.Close(); err == nil {
		err = err2
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), hp)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, fkNotFound(err)
	}
	return fkStat(hp, p)
}

func (s *fkSandbox) List(ctx context.Context, p string, limit int) (*xbin.FileList, error) {
	_, hp, err := s.file("list-dir", p, true)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(hp)
	if err != nil {
		return nil, fkNotFound(err)
	}
	if limit <= 0 {
		limit = 1000
	}
	out := &xbin.FileList{Path: p, Entries: []xbin.FileEntry{}, Truncated: len(ents) > limit}
	for _, d := range ents {
		if len(out.Entries) >= limit {
			break
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		e := xbin.FileEntry{Name: d.Name(), Type: fkType(fi), Size: fi.Size(), MtimeMs: fi.ModTime().UnixMilli(),
			Mode: fmt.Sprintf("%04o", fi.Mode().Perm())}
		if fi.Mode()&fs.ModeSymlink != 0 {
			e.Target, _ = os.Readlink(filepath.Join(hp, d.Name()))
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

func (s *fkSandbox) Mkdir(ctx context.Context, p string, parents bool) error {
	_, hp, err := s.file("mkdir", p, false)
	if err != nil {
		return err
	}
	if parents {
		err = os.MkdirAll(hp, 0o755)
	} else {
		err = os.Mkdir(hp, 0o755)
	}
	if err != nil {
		return fkNotFound(err)
	}
	return nil
}

func (s *fkSandbox) Remove(ctx context.Context, p string, recursive bool) error {
	b, hp, err := s.file("remove", p, false)
	if err != nil {
		return err
	}
	if hp == b.dir {
		return fkErr(http.StatusBadRequest, "invalid", "won't remove the sandbox's own directory")
	}
	if _, err := os.Lstat(hp); err != nil {
		return fkNotFound(err)
	}
	if recursive {
		err = os.RemoveAll(hp)
	} else {
		err = os.Remove(hp)
	}
	if err != nil {
		return fkErr(http.StatusBadRequest, "invalid", "%v", err)
	}
	return nil
}

func (s *fkSandbox) Move(ctx context.Context, from, to string, overwrite bool) error {
	b, hf, err := s.file("move", from, false)
	if err != nil {
		return err
	}
	ht, err := b.path(to)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(ht); err == nil && !overwrite {
		return fkErr(http.StatusPreconditionFailed, "precondition", "%s exists", to)
	}
	if err := os.Rename(hf, ht); err != nil {
		return fkNotFound(err)
	}
	return nil
}

// --- trees ------------------------------------------------------------------------

func (s *fkSandbox) GetTar(ctx context.Context, p string, exclude []string) (io.ReadCloser, error) {
	if !s.f.hasCap("tar") {
		return nil, fkErr(http.StatusNotImplemented, "unsupported", "no tar here")
	}
	_, hp, err := s.file("tar-get", p, true)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(hp); err != nil {
		return nil, fkNotFound(err)
	} else if !fi.IsDir() {
		return nil, fkErr(http.StatusBadRequest, "invalid", "%s is not a directory (read a file with files/content)", p)
	}
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.WalkDir(hp, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(hp, fp)
			if rel == "." {
				return nil
			}
			for _, x := range exclude {
				if ok, _ := filepath.Match(x, rel); ok || x == d.Name() {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			link := ""
			if fi.Mode()&fs.ModeSymlink != 0 {
				link, _ = os.Readlink(fp)
			}
			hdr, err := tar.FileInfoHeader(fi, link)
			if err != nil {
				return nil
			}
			hdr.Name = filepath.ToSlash(rel)
			if fi.IsDir() {
				hdr.Name += "/"
			}
			hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if fi.Mode().IsRegular() {
				f, err := os.Open(fp)
				if err == nil {
					_, err = io.Copy(tw, f)
					f.Close()
				}
				return err
			}
			return nil
		})
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	return pr, nil
}

func (s *fkSandbox) PutTar(ctx context.Context, p string, r io.Reader, mkdirs bool) error {
	if !s.f.hasCap("tar") {
		return fkErr(http.StatusNotImplemented, "unsupported", "no tar here")
	}
	b, hp, err := s.file("tar-put", p, true)
	if err != nil {
		return err
	}
	if mkdirs {
		_ = os.MkdirAll(hp, 0o755)
	}
	if fi, err := os.Stat(hp); err != nil {
		return fkNotFound(err)
	} else if !fi.IsDir() {
		return fkErr(http.StatusBadRequest, "invalid", "%s is not a directory", p)
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if errors.Is(err, tar.ErrInsecurePath) && hdr != nil {
			continue // skipped, as below
		}
		if err != nil {
			return fkErr(http.StatusBadRequest, "invalid", "bad tar: %v", err)
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue // never outside path
		}
		dst := filepath.Join(hp, name)
		if !fkWithin(b.dir, fkResolve(filepath.Dir(dst))) {
			continue // nor through a symlink out of the sandbox
		}
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			_ = os.Remove(dst) // an entry replaces a symlink; it isn't written through it
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dst, 0o755)
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Remove(dst)
			err = os.Symlink(hdr.Linkname, dst)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			var f *os.File
			f, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(hdr.Mode).Perm())
			if err == nil {
				_, err = io.Copy(f, tr)
				if err2 := f.Close(); err == nil {
					err = err2
				}
			}
			if err == nil {
				_ = os.Chmod(dst, fs.FileMode(hdr.Mode).Perm())
				_ = os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
			}
		}
		if err != nil {
			return fkErr(http.StatusBadRequest, "invalid", "%v", err)
		}
	}
}

// fkCopyTree copies a directory's contents into dst (dirs, files, symlinks).
func fkCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			return os.MkdirAll(t, 0o755)
		case fi.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(t)
			return os.Symlink(l, t)
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(t, b, fi.Mode().Perm())
		}
		return nil
	})
}

func fkTreeBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// --- snapshots --------------------------------------------------------------------

type fkSnap struct {
	xbin.Snapshot
	dir string
	seq int
}

func (s *fkSandbox) Snapshots(ctx context.Context) ([]xbin.Snapshot, error) {
	if !s.f.hasCap("snapshots") {
		return nil, fkErr(http.StatusNotImplemented, "unsupported", "no snapshots here")
	}
	b, err := s.find("snapshots")
	if err != nil {
		return nil, err
	}
	var all []*fkSnap
	for _, sn := range b.snaps {
		all = append(all, sn)
	}
	s.f.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	out := []xbin.Snapshot{}
	for _, sn := range all {
		out = append(out, sn.Snapshot)
	}
	return out, nil
}

func (s *fkSandbox) Snapshot(ctx context.Context, name, clientID string) (*xbin.Snapshot, error) {
	if !s.f.hasCap("snapshots") {
		return nil, fkErr(http.StatusNotImplemented, "unsupported", "no snapshots here")
	}
	b, err := s.find("snapshot")
	if err != nil {
		return nil, err
	}
	h := fkHash([]string{name, clientID})
	if prev, ok := b.sidem[clientID]; clientID != "" && ok && b.snaps[prev.id] != nil {
		defer s.f.mu.Unlock()
		if prev.hash != h {
			return nil, fkErr(http.StatusConflict, "exists", "clientId %s was used for a different snapshot", clientID)
		}
		sn := b.snaps[prev.id].Snapshot
		return &sn, nil
	}
	b.sseq++
	sn := &fkSnap{seq: b.sseq, dir: filepath.Join(s.f.Root, ".snaps", s.name, fmt.Sprintf("s%d", b.sseq))}
	sn.Snapshot = xbin.Snapshot{ID: fmt.Sprintf("s%d", b.sseq), Name: orStr(name, fmt.Sprintf("snapshot %d", b.sseq)), Created: fkNow()}
	src := b.dir
	s.f.mu.Unlock()
	// Copied first, published after: nobody sees (or clones) a half-made one.
	err = fkCopyTree(src, sn.dir)
	sn.Bytes = fkTreeBytes(sn.dir)
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if err == nil && s.f.boxes[s.name] != b {
		err = errors.New("the sandbox was deleted")
	}
	if err != nil {
		_ = os.RemoveAll(sn.dir)
		return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "snapshot: %v", err)
	}
	b.snaps[sn.ID] = sn
	if clientID != "" {
		b.sidem[clientID] = fkIdem{id: sn.ID, hash: h}
	}
	out := sn.Snapshot
	return &out, nil
}

func (s *fkSandbox) RestoreSnapshot(ctx context.Context, id string) (*xbin.SandboxInfo, error) {
	if !s.f.hasCap("snapshots") {
		return nil, fkErr(http.StatusNotImplemented, "unsupported", "no snapshots here")
	}
	b, err := s.find("restore")
	if err != nil {
		return nil, err
	}
	sn := b.snaps[id]
	if sn == nil {
		s.f.mu.Unlock()
		return nil, fkErr(http.StatusNotFound, "not-found", "no snapshot %s", id)
	}
	killed := b.stopExecs()
	dir := b.dir
	s.f.mu.Unlock()
	fkAwait(killed, 5*time.Second) // nothing writes into the tree while it's replaced
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
	if err := fkCopyTree(sn.dir, dir); err != nil {
		return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "%v", err)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	b.info.Version++
	in := s.f.view(b)
	return &in, nil
}

func (s *fkSandbox) DeleteSnapshot(ctx context.Context, id string) error {
	if !s.f.hasCap("snapshots") {
		return fkErr(http.StatusNotImplemented, "unsupported", "no snapshots here")
	}
	b, err := s.find("snapshot-delete")
	if err != nil {
		return err
	}
	sn := b.snaps[id]
	delete(b.snaps, id)
	s.f.mu.Unlock()
	if sn == nil {
		return fkErr(http.StatusNotFound, "not-found", "no snapshot %s", id)
	}
	_ = os.RemoveAll(sn.dir)
	return nil
}
