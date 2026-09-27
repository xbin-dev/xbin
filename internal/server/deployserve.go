package server

// deployserve.go — the /c/ plane of a tile whose primary is pinned to a
// checkpoint (07-runtime §4.1, §4.2). A bare /c/<tile>/ URL serves the
// tile's primary: its work tree while live reload drives it — every tile
// without a deployment record, served exactly as before tile deployments
// (P5) — and its materialized checkpoint while it is pinned (P9). The
// Policy's CodeRoot says which. What a document is served with — inject,
// chrome, the native entry, the import map — is the registry's component,
// which the registry already composes from the primary's code.
//
// A checkpoint is opened beneath its materialized tree only
// (fsutil.OpenBeneath), in every asset mode: an in-tree symlink resolves,
// one that leaves the tree is never followed on the host, and neither the
// dev overlay nor the legacy plane's cross-tile resolution applies (P16).
// The one way out is a deps/<name> link, which is re-dispatched by path to
// the tile it names, so that tile's primary answers under its own gate.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// primaryRoot asks where owner's primary serves its files from: pinned
// false — today's opener of the plane — while that code is the work tree
// (every zero-state tile, and a path no component owns yet), the
// materialized checkpoint's root with pinned true while it is pinned. ok
// false means there is nothing to serve (a record that holds the tile, a
// checkpoint that can't be prepared): the request is a 404, never the work
// tree in the checkpoint's place (06-security C7).
func (s *Server) primaryRoot(owner string) (root string, pinned, ok bool) {
	c, found := s.Reg.Component(owner)
	if !found {
		return "", false, true
	}
	root, pinned, err := s.policy().CodeRoot(c, "")
	switch {
	case err != nil, pinned && root == "":
		return "", false, false
	case !pinned:
		return "", false, true
	}
	return root, true, true
}

// openPinned opens cleaned, a /c/ path of owner, beneath root, the
// materialized checkpoint owner's primary is pinned to: fsutil.ErrEscapes
// for a path through a symlink that leaves the tree, ErrNotRegular for a
// FIFO or a device, never a host path outside root (06-security T2).
func openPinned(root, owner, cleaned string) (*os.File, os.FileInfo, error) {
	f, err := fsutil.OpenBeneath(root, tileRel(owner, cleaned))
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

// tileRel is cleaned relative to its owning component's directory.
func tileRel(owner, cleaned string) string {
	return strings.TrimPrefix(strings.TrimPrefix(cleaned, owner), "/")
}

// servePinnedStatic is handleComponentStatic's tail for a pinned primary, in
// every asset mode: the directory, index and injection rules of the legacy
// and strict tails, files opened beneath root (openPinned), then the strict
// modes' gate (assetGate), or legacy's inert non-documents, exactly as for
// a primary on the work tree. Cache-Control stays no-store: pinned bytes
// change only by a deploy, which reloads the frames.
func (s *Server) servePinnedStatic(w http.ResponseWriter, r *http.Request, cleaned, owner, root string) {
	f, fi, err := openPinned(root, owner, cleaned)
	if err != nil {
		if !s.redispatchDeps(w, r, root, owner, cleaned, err) {
			http.NotFound(w, r)
		}
		return
	}
	dirIndex, name := false, cleaned
	if fi.IsDir() {
		f.Close()
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		dirIndex, name = true, path.Join(cleaned, "index.html")
		if f, fi, err = openPinned(root, owner, name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	comp, _, _ := s.Reg.Resolve(cleaned)
	isDoc := isHTMLName(name)
	if s.strictAssets() {
		if s.assetGate(w, r, owner, comp, isDoc) {
			return
		}
	} else if !isDoc {
		s.inertNonDocument(w, r, owner, comp)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isDoc {
		if comp == nil || comp.Manifest.Inject == nil || *comp.Manifest.Inject {
			body, err := io.ReadAll(f)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			s.injectHTML(w, r, body, comp, cleaned, dirIndex)
			return
		}
		s.sandboxDocument(w, r, owner, comp)
		if strings.HasSuffix(r.URL.Path, "/index.html") { // as the work-tree tails do
			localRedirect(w, r, "./")
			return
		}
	}
	http.ServeContent(w, r, path.Base(name), fi.ModTime(), f)
}

// openAssetFile is the asset-token plane's opener (tokens mode): openStrict
// while owner's primary follows the work tree, beneath its checkpoint while
// pinned, where a deps/ link is re-dispatched to the asset plane of the tile
// it names. done reports that the request was answered — a re-dispatch, or a
// 404 when there is nothing to serve.
func (s *Server) openAssetFile(w http.ResponseWriter, r *http.Request, owner, cleaned string) (f *os.File, fi os.FileInfo, done bool) {
	root, pinned, ok := s.primaryRoot(owner)
	var err error
	switch {
	case !ok:
		err = os.ErrNotExist
	case pinned:
		if f, fi, err = openPinned(root, owner, cleaned); err != nil && s.redispatchDeps(w, r, root, owner, cleaned, err) {
			return nil, nil, true
		}
	default:
		f, fi, err = s.openStrict(owner, cleaned)
	}
	if err != nil {
		http.NotFound(w, r)
		return nil, nil, true
	}
	return f, fi, false
}

// depsHopsMax bounds how many deps/ re-dispatches one request may chain
// (each drops a deps/<name>/ from the path, so a chain ends anyway; this
// keeps a hostile URL from making a long one).
const depsHopsMax = 8

type depsHopsKey struct{}

// redispatchDeps answers a request for deps/<name>/<rest> of a pinned
// primary whose path met a symlink leaving the checkpoint (err is
// fsutil.ErrEscapes) when the checkpoint's own deps/<name> entry names a
// registered component: the request is served as /c/<target>/<rest> —
// resolved, authorized and served exactly as a direct request for that URL
// by the same principal, on the same plane (the workspace origin, a tile
// origin, the asset-token plane) — so the other tile's primary answers
// under the other tile's gate (P16). The target comes from the checkpoint,
// never from the work tree's manifest or deps/ links, so a work-tree edit
// can't re-point what a pinned primary imports (P9). It reports whether it
// answered; false leaves the 404 (ErrEscapes) to the caller.
func (s *Server) redispatchDeps(w http.ResponseWriter, r *http.Request, root, owner, cleaned string, err error) bool {
	hops, _ := r.Context().Value(depsHopsKey{}).(int)
	if !errors.Is(err, fsutil.ErrEscapes) || hops >= depsHopsMax {
		return false
	}
	parts := strings.SplitN(tileRel(owner, cleaned), "/", 3)
	if len(parts) < 2 || parts[0] != "deps" {
		return false
	}
	target, ok := s.depsTarget(root, owner, parts[1])
	if !ok {
		return false
	}
	p := target
	if len(parts) == 3 {
		p += "/" + parts[2]
	}
	if strings.HasSuffix(r.URL.Path, "/") {
		p += "/"
	}
	r2 := r.Clone(context.WithValue(r.Context(), depsHopsKey{}, hops+1))
	r2.URL.RawPath = ""
	switch {
	case s.assetMode() == TileAssetsTokens && strings.HasPrefix(r.URL.Path, "/c/~"):
		tok, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/c/~"), "/")
		r2.URL.Path = "/c/~" + tok + "/" + p
		r2.RequestURI = r2.URL.RequestURI()
		s.serveAssetToken(w, r2)
	case tileOriginOf(r) != "":
		r2.URL.Path = "/c/" + p
		r2.RequestURI = r2.URL.RequestURI()
		id, _ := s.tileHostOf(r.Host)
		w.Header().Del("Content-Security-Policy") // the tile origin sets its own again
		s.serveTileOrigin(w, r2, id)
	default:
		r2.URL.Path = "/c/" + p
		r2.RequestURI = r2.URL.RequestURI()
		s.authedStatic(http.HandlerFunc(s.handleComponentStatic)).ServeHTTP(w, r2)
	}
	return true
}

// depsTarget reads the checkpoint's own deps/<name> entry at root without
// following it, resolves its text lexically against <owner>/deps/ at the
// tile's canonical path (an absolute text must lie in the workspace), and
// answers the registered component it names. deps itself must be a
// directory of the checkpoint, not a symlink: a materialized tree is
// written once, by xbind, and never again, so nothing swaps it after the
// check. No component, or anything else: false.
func (s *Server) depsTarget(root, owner, name string) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	deps := filepath.Join(root, "deps")
	if fi, err := os.Lstat(deps); err != nil || !fi.IsDir() {
		return "", false
	}
	text, err := os.Readlink(filepath.Join(deps, name))
	if err != nil || text == "" {
		return "", false
	}
	text = filepath.ToSlash(text)
	var target string
	if path.IsAbs(text) {
		ws := filepath.ToSlash(filepath.Clean(s.Reg.Root)) + "/"
		rel, ok := strings.CutPrefix(path.Clean(text), ws)
		if !ok {
			return "", false
		}
		target = rel
	} else {
		target = path.Join(owner, "deps", text)
	}
	if target == "" || target == "." || target == ".." || strings.HasPrefix(target, "../") {
		return "", false
	}
	if _, ok := s.Reg.Component(target); !ok {
		return "", false
	}
	return target, true
}

// PrimarySummaryPolicy is a Policy that answers /components' deployments
// summary (11-contract §8): for a tile with a deployment record, its
// primary's name, whether that primary is pinned (doesn't follow the work
// tree) and whether it is protected; ok false for any other tile, whose
// entry gains nothing. Without it no entry has a summary, as on every tile
// that never opted in.
type PrimarySummaryPolicy interface {
	PrimarySummary(tile string) (primary string, pinned, protected, ok bool)
}

// deploymentsSummary is the deployments object of a /components entry: the
// same for every caller who sees the row, with no non-primary name and no
// count (P20).
type deploymentsSummary struct {
	Primary   string `json:"primary"`
	Pinned    bool   `json:"pinned"`
	Protected bool   `json:"protected"`
}

// primarySummary is tile's summary, nil without a record (the zero state).
func (s *Server) primarySummary(tile string) *deploymentsSummary {
	sp, ok := s.policy().(PrimarySummaryPolicy)
	if !ok {
		return nil
	}
	primary, pinned, protected, ok := sp.PrimarySummary(tile)
	if !ok {
		return nil
	}
	return &deploymentsSummary{Primary: primary, Pinned: pinned, Protected: protected}
}
