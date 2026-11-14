package server

// sealedfile.go — a sealed component's files are served from memory (D227).
//
// The registry holds each sealed component's tree as the release embeds it
// (registry.SealedFile). The copy xbind materializes under .xbin/sealed/ is
// what a backend runs in — verified before every start (sealed.Set.Admit) —
// and is never what a browser is sent: between two boots nothing would
// vouch for a file read back from disk. So every open of a sealed
// component's file (openPinned, the one place its code root is read)
// answers an anonymous in-memory file holding the release's bytes, and the
// handlers serve it as they serve any other *os.File.

import (
	"io/fs"
	"os"
	"time"
)

// sealedSince is the modification time a sealed component's files answer
// with: this process's start — they change only with the xbind binary.
var sealedSince = time.Now().Truncate(time.Second)

// sealedInfo is an in-memory sealed file's FileInfo, with a stable time.
type sealedInfo struct {
	os.FileInfo
	name string
}

func (i sealedInfo) Name() string       { return i.name }
func (i sealedInfo) ModTime() time.Time { return sealedSince }
func (i sealedInfo) Mode() fs.FileMode  { return i.FileInfo.Mode().Type() | 0o444 }

// openSealed opens file rel of the sealed component owner: the release's
// bytes for a file, a directory handle for a directory of its tree (the
// handlers ask only whether it is one), fs.ErrNotExist otherwise.
func (s *Server) openSealed(owner, rel string) (*os.File, os.FileInfo, error) {
	if s.Reg == nil {
		return nil, nil, fs.ErrNotExist
	}
	data, dir, ok := s.Reg.SealedFile(owner, rel)
	switch {
	case !ok:
		return nil, nil, fs.ErrNotExist
	case dir:
		f, err := os.Open(os.TempDir()) // any directory: nothing is read from it
		if err != nil {
			return nil, nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return f, fi, nil
	}
	f, err := memFile(data)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	base := rel
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == '/' {
			base = rel[i+1:]
			break
		}
	}
	return f, sealedInfo{FileInfo: fi, name: base}, nil
}
