package term

// homecopy.go — the copy-on-write layers of people's homes (D245, home.go;
// plans/wsagent/home-on-untrusted-tiles.md §4–5).
//
// A layer is what one person's sessions wrote under ~ on one tile whose
// source they have not trusted:
//
//	data/home-copies/<home key>/<id>/
//	    upper/  work/    the overlay's (written through it only)
//	    meta.json        the tile, its identity, when — xbind's
//	    overlay          the flavour that wrote it — xbind's stamp
//
// <id> hashes the tile's identity (its directory's own), so a tile made
// again at the same path has another, empty layer. data/ is mounted in no
// sandbox and masked in every terminal: the layer is seen through its
// overlay only, by that person's sessions on that tile.
//
// One overlay mount per layer, held by a process in namespaces of its own
// (sandbox.StartHold) and cloned into each session: the sessions on a
// layer are counted, the last to end ends the hold. Nothing is mounted
// where the host sees it. A layer is never read by xbind (it holds what
// untrusted code wrote): removed and measured by confined runs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/signins"
)

// homeCopiesDir is where the layers live, under data/.
const homeCopiesDir = "home-copies"

// overlayHold is a running hold of one overlay (*sandbox.Hold).
type overlayHold interface {
	Mount() (*os.File, error)
	Done() <-chan struct{}
	Close()
}

// homeCopies is the Manager's share of the layers: the live holds.
type homeCopies struct {
	mu       sync.Mutex
	holds    map[string]*copyHold   // layer dir → its hold
	keys     map[string]*sync.Mutex // layer dir → starts, stops and removals of it, one at a time
	removing map[string]bool        // layer dir → being forgotten: no session starts on it
	sizes    map[string]copySize    // layer dir → its size as last measured (a list re-reads within copySizeTTL)
	// tests: stand in for sandbox.StartHold and confine.DiskUsage; prefer
	// names the one flavour a new layer is mounted with ("" = the kernel's,
	// then fuse-overlayfs)
	prefer string
	start  func(sandbox.HoldSpec) (overlayHold, error)
	usage  func(dir string) (int64, error)
}

// copySize is a layer's measured size.
type copySize struct {
	bytes int64
	at    time.Time
}

// copySizeTTL is how long a layer's measured size is reused: measuring is
// a confined run per layer.
const copySizeTTL = 30 * time.Second

// size is the layer's size on disk (-1: it could not be measured).
func (c *homeCopies) size(dir string) int64 {
	c.mu.Lock()
	got, ok := c.sizes[dir]
	usage := c.usage
	c.mu.Unlock()
	if ok && time.Since(got.at) < copySizeTTL {
		return got.bytes
	}
	if usage == nil {
		usage = func(dir string) (int64, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return confine.DiskUsage(ctx, dir)
		}
	}
	n, err := usage(filepath.Join(dir, "upper"))
	if err != nil {
		return -1
	}
	c.mu.Lock()
	if c.sizes == nil {
		c.sizes = map[string]copySize{}
	}
	c.sizes[dir] = copySize{n, time.Now()}
	c.mu.Unlock()
	return n
}

type copyHold struct {
	dir    string
	h      overlayHold
	refs   int
	closed bool // ended by us (the last session went, or it is being forgotten)
}

// copyMeta is a layer's meta.json.
type copyMeta struct {
	V       int    `json:"v"`
	Tile    string `json:"tile"`
	Name    string `json:"name,omitempty"`
	Ident   string `json:"ident"`
	Created int64  `json:"created"`
	// Told: a session said, once, what the copy means (homeNote).
	Told bool `json:"told,omitempty"`
}

// HomeCopyInfo is one layer, as its person's list shows it.
type HomeCopyInfo struct {
	ID      string `json:"id"`
	Tile    string `json:"tile"`
	Name    string `json:"name,omitempty"`
	Created int64  `json:"created"`
	// Bytes is its size on disk (-1: not measured).
	Bytes int64 `json:"bytes"`
	// Sessions: how many live sessions are on it.
	Sessions int `json:"sessions"`
	// Gone: the tile it was made for is not there any more — deleted (the
	// copy goes at the next sweep), or another directory stands at its path
	// (the tile was made again, or the workspace restored): an orphan, not
	// used, kept until its person forgets it.
	Gone bool `json:"gone,omitempty"`
}

func homeCopyID(ident string) string {
	h := sha256.Sum256([]byte(ident))
	return "hc_" + hex.EncodeToString(h[:12])
}

func (m *Manager) homeCopiesRoot(homeKey string) string {
	return filepath.Join(m.Root, "data", homeCopiesDir, homeKey)
}

// homeCopyDir is the layer of homeKey's home on the tile src names.
func (m *Manager) homeCopyDir(homeKey string, src signins.Source) (string, error) {
	if src.ID == "" || src.Tile == "" {
		return "", errors.New("the tile has no identity xbind can read")
	}
	return filepath.Join(m.homeCopiesRoot(homeKey), homeCopyID(src.ID)), nil
}

func (c *homeCopies) lock(dir string) func() {
	c.mu.Lock()
	if c.keys == nil {
		c.keys = map[string]*sync.Mutex{}
	}
	mu := c.keys[dir]
	if mu == nil {
		mu = &sync.Mutex{}
		c.keys[dir] = mu
	}
	c.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func readCopyMeta(dir string) (copyMeta, error) {
	var meta copyMeta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return meta, err
	}
	return meta, json.Unmarshal(b, &meta)
}

func writeCopyMeta(dir string, meta copyMeta) error {
	meta.V = 1
	b, _ := json.MarshalIndent(meta, "", "  ")
	return fsutil.WriteFileAtomic(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o600)
}

// prepareCopy makes the layer's directories and its meta.json on first
// use, and answers the overlay flavour that wrote the layer ("" for one
// nothing was written to yet).
func prepareCopy(dir, lower string, src signins.Source) (stamp string, err error) {
	fresh := false
	if _, err := os.Lstat(filepath.Join(dir, "meta.json")); errors.Is(err, os.ErrNotExist) {
		fresh = true
	}
	mode := os.FileMode(0o700)
	if fi, err := os.Stat(lower); err == nil {
		mode = fi.Mode().Perm() // the copy's root is the home's own: its mode with it
	}
	for _, d := range []struct {
		p    string
		mode os.FileMode
	}{{dir, 0o700}, {filepath.Join(dir, "upper"), mode}, {filepath.Join(dir, "work"), 0o700}} {
		if err := os.MkdirAll(d.p, d.mode); err != nil {
			return "", err
		}
		if fi, err := os.Lstat(d.p); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("%s is not a directory", d.p)
		}
	}
	st, err := layers.Read(dir)
	if err != nil {
		return "", fmt.Errorf("the layer's stamp is unreadable: %w", err)
	}
	if fresh {
		return st.Overlay, writeCopyMeta(dir, copyMeta{Tile: src.Tile, Name: src.Name, Ident: src.ID, Created: time.Now().UnixMilli()})
	}
	return st.Overlay, nil
}

// startHold starts the hold of the layer at dir over lower, with the
// overlay flavour the layer was written by — an upper written by one
// overlay is never mounted by the other — and for a new layer the
// kernel's overlayfs first, fuse-overlayfs where the kernel's can't be
// mounted in a user namespace (before Linux 5.11).
//
// The kernel's first, though a sandbox's root prefers fuse-overlayfs: a
// home is used by every uid of the session, and a FUSE mount held in one
// user namespace cannot be. Mounted allow_other it serves that namespace
// and its children only — a session's namespace is a sibling — and without
// it only the one kuid that mounted it, root of every sandbox (hold_linux.go):
// a process that dropped to another uid in the session (a package's
// post-install user, `su`, a build that runs as nobody) gets EACCES on the
// whole home. The kernel's overlay is a filesystem like any other there.
// What it lacks is renaming, in place, a directory that exists in the real
// home (EXDEV: redirect_dir is not for user namespaces).
func (c *homeCopies) startHold(dir, lower string, src signins.Source) (overlayHold, error) {
	stamp, err := prepareCopy(dir, lower, src)
	if err != nil {
		return nil, err
	}
	start := c.start
	if start == nil {
		start = func(hs sandbox.HoldSpec) (overlayHold, error) {
			h, err := sandbox.StartHold(hs, os.Getuid(), os.Getgid())
			if err != nil {
				return nil, err // (a typed nil must not become a non-nil interface)
			}
			return h, nil
		}
	}
	spec := sandbox.HoldSpec{Lower: lower, Upper: filepath.Join(dir, "upper"), Work: filepath.Join(dir, "work")}
	fuse := sandbox.FuseOverlayfs()
	try := []string{sandbox.HoldKernel, sandbox.HoldFuse}
	switch {
	case stamp != "":
		try = []string{stamp}
	case c.prefer != "":
		try = []string{c.prefer}
	}
	var errs []string
	for _, flavour := range try {
		hs := spec
		if flavour == sandbox.HoldFuse {
			if fuse == "" {
				if stamp != "" {
					return nil, errors.New("it was written by the fuse overlay and this xbind has no fuse-overlayfs any more — forget what this tile wrote to your home to start a new copy")
				}
				continue
			}
			hs.Fuse = fuse
		}
		h, err := start(hs)
		if err != nil {
			errs = append(errs, flavour+" overlay: "+err.Error())
			continue
		}
		// the layer is this flavour's from here on: stamped before any
		// session can write through it (a start that failed wrote nothing,
		// and leaves no stamp)
		if err := layers.Stamp(dir, layers.Stamps{Overlay: flavour}); err != nil {
			h.Close()
			return nil, fmt.Errorf("the layer could not be stamped: %w", err)
		}
		return h, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no overlay could be mounted here")
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

// acquire gives one session a clone of the layer's overlay over lower (the
// person's real home), starting the layer's hold when no session has it.
// release is called once that session's sandbox is gone.
func (c *homeCopies) acquire(m *Manager, dir, lower string, src signins.Source) (*os.File, func(), error) {
	unlock := c.lock(dir)
	defer unlock()
	c.mu.Lock()
	if c.removing[dir] {
		c.mu.Unlock()
		return nil, nil, errors.New("it is being forgotten — try again in a moment")
	}
	hh := c.holds[dir]
	c.mu.Unlock()
	if hh != nil {
		select {
		case <-hh.h.Done(): // it died: its sessions are being ended; this one starts a new hold
			c.drop(hh)
			hh = nil
		default:
		}
	}
	if hh == nil {
		h, err := c.startHold(dir, lower, src)
		if err != nil {
			return nil, nil, err
		}
		hh = &copyHold{dir: dir, h: h}
		c.mu.Lock()
		if c.holds == nil {
			c.holds = map[string]*copyHold{}
		}
		c.holds[dir] = hh
		c.mu.Unlock()
		go c.watch(m, hh)
	}
	f, err := hh.h.Mount()
	if err != nil {
		c.mu.Lock()
		idle := hh.refs == 0
		c.mu.Unlock()
		if idle {
			c.drop(hh)
		}
		return nil, nil, err
	}
	c.mu.Lock()
	hh.refs++
	c.mu.Unlock()
	var once sync.Once
	return f, func() {
		once.Do(func() {
			unlock := c.lock(dir)
			defer unlock()
			c.mu.Lock()
			hh.refs--
			last := hh.refs == 0
			c.mu.Unlock()
			if last {
				c.drop(hh) // the last session on it went: the hold ends, reaped before the lock goes
			}
		})
	}, nil
}

// drop ends hh and forgets it (the layer's lock held).
func (c *homeCopies) drop(hh *copyHold) {
	c.mu.Lock()
	hh.closed = true
	if c.holds[hh.dir] == hh {
		delete(c.holds, hh.dir)
	}
	c.mu.Unlock()
	hh.h.Close()
}

// watch ends the sessions on a hold that died by itself (fuse-overlayfs
// gone): their home would answer nothing but errors.
func (c *homeCopies) watch(m *Manager, hh *copyHold) {
	<-hh.h.Done()
	c.mu.Lock()
	ours := hh.closed
	c.mu.Unlock()
	if ours {
		return
	}
	slog.Error("a home copy's overlay ended by itself: its sessions are ended", "layer", hh.dir)
	m.endCopySessions(hh.dir, "This tile's copy of your home stopped being served (its overlay ended), so this session was ended. Start it again.")
}

// tell reports — once per layer — that a session should say what the copy
// means.
func (c *homeCopies) tell(dir string) bool {
	unlock := c.lock(dir)
	defer unlock()
	meta, err := readCopyMeta(dir)
	if err != nil || meta.Told {
		return false
	}
	meta.Told = true
	return writeCopyMeta(dir, meta) == nil
}

// live is how many sessions are on the layer at dir.
func (c *homeCopies) live(dir string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hh := c.holds[dir]; hh != nil {
		return hh.refs
	}
	return 0
}

// endCopySessions ends the live sessions on the layer at dir, saying why.
func (m *Manager) endCopySessions(dir, why string) int {
	n := 0
	for _, s := range m.sorted() {
		if s.home.dir == dir && s.claimEnd() {
			m.endForHome(s, why)
			n++
		}
	}
	return n
}

// HomeCopies lists homeKey's layers, newest first, each with its size on
// disk (measured by a confined run, reused for copySizeTTL).
func (m *Manager) HomeCopies(homeKey string) []HomeCopyInfo {
	out := []HomeCopyInfo{}
	ents, _ := os.ReadDir(m.homeCopiesRoot(homeKey))
	for _, e := range ents {
		dir := filepath.Join(m.homeCopiesRoot(homeKey), e.Name())
		meta, err := readCopyMeta(dir)
		if err != nil || !e.IsDir() {
			continue
		}
		hc := HomeCopyInfo{ID: e.Name(), Tile: meta.Tile, Name: meta.Name, Created: meta.Created, Bytes: m.copies.size(dir), Sessions: m.copies.live(dir)}
		// not that tile any more: deleted, or another directory at its path
		// (made again; a restored workspace). An identity that can't be read
		// just now says nothing
		hc.Gone = tileAbsent(m.Root, meta.Tile)
		if id := ""; m.TileIdentity != nil && !hc.Gone {
			id = m.TileIdentity(meta.Tile)
			hc.Gone = id != "" && id != meta.Ident
		}
		out = append(out, hc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// HomeCopyOn reports homeKey's layer for the tile src names: its id, and
// whether there is one.
func (m *Manager) HomeCopyOn(homeKey string, src signins.Source) (id string, exists bool) {
	dir, err := m.homeCopyDir(homeKey, src)
	if err != nil {
		return "", false
	}
	_, err = readCopyMeta(dir)
	return filepath.Base(dir), err == nil
}

// ErrNoHomeCopy: the person has no such layer.
var ErrNoHomeCopy = errors.New("no such copy of your home")

// ForgetHomeCopy removes homeKey's layer id — "forget what this tile wrote
// to my home": the sessions on it are ended first (why says so in each),
// its hold stopped, and the layer removed by a confined run. It answers
// the tile it was for and how many sessions it ended.
//
// It ENDS the sessions on the copy rather than refusing while one runs:
// the person asked for what the tile wrote to be gone, and a session on it
// is the tile's code still writing. A start that is under way is waited
// for (it holds the layer's lock while its hold starts), then ends itself
// as it registers (registered): no session is left on a layer that was
// removed under it. One that takes longer than the wait to come up — a VM —
// makes the forget fail, with nothing removed.
func (m *Manager) ForgetHomeCopy(homeKey, id, why string) (tile string, ended int, err error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", 0, ErrNoHomeCopy
	}
	dir := filepath.Join(m.homeCopiesRoot(homeKey), id)
	meta, err := readCopyMeta(dir)
	if err != nil {
		return "", 0, ErrNoHomeCopy
	}
	c := &m.copies
	unlock := c.lock(dir) // a start that holds it is done first: its session is counted on the hold, or it failed
	c.mu.Lock()
	if c.removing == nil {
		c.removing = map[string]bool{}
	}
	if c.removing[dir] {
		c.mu.Unlock()
		unlock()
		return meta.Tile, 0, nil // another forget is at it
	}
	c.removing[dir] = true // no session starts on it from here on; one being opened ends as it registers
	c.mu.Unlock()
	unlock()
	defer func() {
		c.mu.Lock()
		delete(c.removing, dir)
		delete(c.sizes, dir)
		c.mu.Unlock()
	}()
	ended = m.endCopySessions(dir, why)
	deadline := time.Now().Add(forgetWait)
	for c.live(dir) > 0 {
		ended += m.endCopySessions(dir, why) // (one that registered since)
		if time.Now().After(deadline) {
			return meta.Tile, ended, errors.New("a session on this copy is still starting or ending — nothing was removed; try again in a moment")
		}
		time.Sleep(20 * time.Millisecond)
	}
	unlock = c.lock(dir)
	defer unlock()
	c.mu.Lock()
	hh := c.holds[dir]
	live := hh != nil && hh.refs > 0
	c.mu.Unlock()
	if live { // (cannot be: nothing starts on a layer being forgotten)
		return meta.Tile, ended, errors.New("a session started on this copy — nothing was removed; try again")
	}
	if hh != nil { // a hold with no session left: its last release is ending it, or did
		c.drop(hh)
	}
	if err := m.removeLayer(dir); err != nil {
		return meta.Tile, ended, fmt.Errorf("the copy could not be removed: %w", err)
	}
	slog.Info("audit", "event", "home.copy.forget", "home", homeKey, "tile", meta.Tile, "ended", ended)
	return meta.Tile, ended, nil
}

// forgetWait is how long a forget waits for the sessions on a copy to be
// gone (a var: tests shorten it).
var forgetWait = 20 * time.Second

// forgetting reports a layer that is being forgotten.
func (c *homeCopies) forgetting(dir string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removing[dir]
}

// ForgetHomeCopies removes every layer of homeKey's (the account was
// deleted), ending the sessions on them.
func (m *Manager) ForgetHomeCopies(homeKey, why string) {
	root := m.homeCopiesRoot(homeKey)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if _, _, err := m.ForgetHomeCopy(homeKey, e.Name(), why); err != nil && !errors.Is(err, ErrNoHomeCopy) {
			slog.Warn("home copies: a deleted account's layer stays", "home", homeKey, "layer", e.Name(), "err", err)
		}
	}
	_ = os.Remove(root) // empty now, or it stays with what could not be removed
}

// SweepHomeCopies removes the layers whose tile was DELETED: its path is
// positively not there any more (tileAbsent). Called at boot and after
// every rescan. It answers how many it removed.
//
// Nothing else removes a layer without its person's click. A tile whose
// directory is there with another identity — made again at the path, or
// the whole workspace restored from a backup or moved to another disk,
// which gives every directory a new one — keeps its old layers as
// orphans: never mounted again (the tile's layer is keyed by its identity
// now), listed for their person as "not used", and forgotten by them. An
// identity that can't be read just now (too many open files, a permission)
// is not a reason either: a transient error once would have removed every
// copy. A tile that is merely unregistered keeps its layers too.
func (m *Manager) SweepHomeCopies() int {
	root := filepath.Join(m.Root, "data", homeCopiesDir)
	homes, _ := os.ReadDir(root)
	n := 0
	for _, h := range homes {
		ents, _ := os.ReadDir(filepath.Join(root, h.Name()))
		for _, e := range ents {
			meta, err := readCopyMeta(filepath.Join(root, h.Name(), e.Name()))
			if err != nil || meta.Tile == "" || !tileAbsent(m.Root, meta.Tile) {
				continue
			}
			if _, _, err := m.ForgetHomeCopy(h.Name(), e.Name(), meta.Tile+" was deleted, so this session — on its copy of your home — was ended."); err != nil {
				slog.Warn("home copies: a deleted tile's layer stays", "home", h.Name(), "tile", meta.Tile, "err", err)
				continue
			}
			n++
		}
		_ = os.Remove(filepath.Join(root, h.Name())) // when empty
	}
	return n
}

// tileAbsent reports that the workspace-relative path tile is positively
// not there: one of its components does not exist, and every component
// before it is a real directory (not a link to somewhere that may be
// unmounted, not something that could not be read). Any doubt is "no".
func tileAbsent(root, tile string) bool {
	at := root
	for _, c := range strings.Split(strings.Trim(filepath.ToSlash(tile), "/"), "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
		at = filepath.Join(at, c)
		fi, err := os.Lstat(at)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return true
		case err != nil || !fi.IsDir():
			return false // unreadable, a link, a file: not known
		}
	}
	return false
}
