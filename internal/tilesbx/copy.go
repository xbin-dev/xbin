package tilesbx

// copy.go — POST /sandboxes/copy (§3.8): a file or a tree from one of a
// tile's sandboxes to another (or elsewhere in the same one), streamed
// agent to agent. The source's data frames are spliced into the
// destination's; xbind reads their lengths, never their bytes.

import (
	"context"
	"errors"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// copyEnd is one side of a copy: a sandbox of the caller's and a path in it.
type copyEnd struct {
	Sandbox string `json:"sandbox"`
	Path    string `json:"path"`
}

// copyReq is POST /sandboxes/copy's body.
type copyReq struct {
	From      copyEnd `json:"from"`
	To        copyEnd `json:"to"`
	Overwrite bool    `json:"overwrite"`
}

// check validates both ends, and refuses a copy into itself (a tree
// extracted under the tree being read).
func (c *copyReq) check() error {
	for _, e := range []struct {
		field string
		end   *copyEnd
	}{{"from", &c.From}, {"to", &c.To}} {
		if err := validName(e.end.Sandbox); err != nil {
			return err
		}
		if _, err := sandboxPath(e.field+".path", e.end.Path); err != nil {
			return err
		}
	}
	if c.From.Sandbox == c.To.Sandbox && (c.From.Path == "/" || c.To.Path == c.From.Path || strings.HasPrefix(c.To.Path, c.From.Path+"/")) {
		return refuse(RefInvalid, "a copy into itself: %s is %s or under it", c.To.Path, c.From.Path)
	}
	return nil
}

// copyPaths copies req.From (in from) to req.To (in to), both of k: a
// directory's tree extracted at to.path (merged into what is there with
// overwrite; 412 precondition without it when to.path exists), a file
// written there atomically (412 without overwrite when it exists). Missing
// parents of to.path are made; what is created is owned by to's default
// uid/gid. Both sides are bounded by tarMax.
func (m *Manager) copyPaths(ctx context.Context, k Key, from, to *Def, req copyReq) error {
	src, tree, err := m.copySource(ctx, k, from.Name, req.From.Path)
	if err != nil {
		return err
	}
	defer src.end()
	if tree && !req.Overwrite {
		res, err := m.oneLine(ctx, k, to.Name, proto.FileOp{Op: "stat", Path: req.To.Path})
		var e *Error
		switch {
		case err == nil:
			pe := refuse(RefPrecondition, "%s exists in sandbox %q (copy with overwrite to merge into it)", req.To.Path, to.Name)
			if res.Stat != nil {
				pe.ETag = safeETag(res.Stat.ETag)
			}
			return pe
		case !errors.As(err, &e) || e.Refusal != RefNotFound:
			return err
		}
	}
	op := proto.FileOp{Op: "write", Path: req.To.Path, Mkdirs: true, Create: !req.Overwrite, Owner: ownerOf(to), Max: tarMax}
	if tree {
		op = proto.FileOp{Op: "tar-put", Path: req.To.Path, Mkdirs: true, Owner: ownerOf(to), Max: tarMax}
	}
	dst, err := m.begin(ctx, k, to.Name, op)
	if err != nil {
		return err
	}
	defer dst.end()
	return splice(src, dst)
}

// copySource opens a copy's source, its first line read: a tree (tar-get)
// when the path is a directory, or a symlink leading to one; else a file
// (read, which follows a symlink inside the sandbox).
func (m *Manager) copySource(ctx context.Context, k Key, name, p string) (*fileCall, bool, error) {
	st, err := m.oneLine(ctx, k, name, proto.FileOp{Op: "stat", Path: p})
	if err == nil && st.Stat == nil {
		err = errNoStat
	}
	if err != nil {
		return nil, false, err
	}
	var ops []string
	switch st.Stat.Type {
	case "dir":
		ops = []string{"tar-get"}
	case "file":
		ops = []string{"read"}
	case "symlink":
		ops = []string{"tar-get", "read"} // a directory, else a file
	default:
		return nil, false, refuse(RefInvalid, "%s is neither a file nor a directory", p)
	}
	for i, op := range ops {
		fc, err := m.begin(ctx, k, name, proto.FileOp{Op: op, Path: p, Max: tarMax})
		if err != nil {
			return nil, false, err
		}
		if _, err = fc.result(); err == nil {
			return fc, op == "tar-get", nil
		}
		fc.end()
		var e *Error
		if i+1 == len(ops) || !errors.As(err, &e) || e.Refusal != RefInvalid {
			return nil, false, err
		}
	}
	return nil, false, errors.New("unreachable")
}

// splice streams src's data into dst and returns dst's answer. The
// terminator goes on only once src's last line says the data was whole: a
// source that fails part-way (past tarMax, a read error) ends dst's stream
// without it, so a file copy commits nothing (a tree keeps what was
// extracted by then).
func splice(src, dst *fileCall) error {
	got, answered := dst.await()
	err := relayFrames(dst.c.Writer(), src.c.Reader(), answered)
	var de dstError
	switch {
	case errors.As(err, &de): // the destination stopped reading: its answer says why
		<-answered
		if got.err == nil {
			return refuse(RefUnavailable, "the destination answered before the copy ended")
		}
		return got.err
	case err != nil: // the source broke off
		dst.c.Close()
		return src.broken(err)
	}
	if _, err := src.result(); err != nil {
		dst.c.Close()
		return err
	}
	_ = proto.WriteFrame(dst.c.Writer(), nil) // a failure is the destination's, and its answer says why
	<-answered
	return got.err
}
