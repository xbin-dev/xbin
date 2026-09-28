package server

// deployserve.go — the /c/ plane of tile deployments (07-runtime §4;
// 11-contract §2).
//
// A bare /c/<tile>/ URL serves the tile's primary: its work tree while live
// reload drives it — every tile without a deployment record, served exactly
// as before tile deployments (P5) — and its materialized checkpoint while it
// is pinned (P9). The Policy's CodeRoot says which. What a document is
// served with — inject, chrome, the native entry, the import map — is the
// registry's component, which the registry already composes from the
// primary's code.
//
// A deployment URL, /c/<tile>+<name>/… (P17), serves deployment <name>'s
// code. It resolves (registry.ResolveRef) only for a tile with a deployment
// record, and only when today's resolution finds nothing at the full
// candidate, so every URL that resolves today resolves identically. Who may
// open it is 11-contract §2.3's table (deploymentURLGate): the alias
// <tile>+<primary> as the bare URL, for people and the tile's own
// principals; any other deployment for people with write on the tile at
// their current level, and for the tile's own principals bound to it (P7,
// P12, P20). Its documents are served as the deployment's registry view,
// never as chrome, carry the deployment in the D4 injection, and get a
// frame token only for a principal that may open that very deployment
// (mayMintFrameToken).
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
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
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
	return s.codeRoot(c, "")
}

// deploymentRoot is primaryRoot for deployment dep of the registered tile
// owner; a tile no longer registered has nothing to serve.
func (s *Server) deploymentRoot(owner, dep string) (root string, pinned, ok bool) {
	c, found := s.Reg.Component(owner)
	if !found {
		return "", false, false
	}
	return s.codeRoot(c, dep)
}

// codeRoot is the Policy's CodeRoot for deployment dep of c ("" names the
// primary), with primaryRoot's answers: the materialized root while pinned,
// "" for the work tree, ok false when there is nothing to serve.
func (s *Server) codeRoot(c *registry.Component, dep string) (root string, pinned, ok bool) {
	root, pinned, err := s.policy().CodeRoot(c, dep)
	switch {
	case err != nil, pinned && root == "":
		return "", false, false
	case !pinned:
		return "", false, true
	}
	return root, true, true
}

// openPinned opens cleaned, a /c/ path of owner, beneath root, the
// materialized checkpoint a deployment of owner is pinned to:
// fsutil.ErrEscapes for a path through a symlink that leaves the tree,
// ErrNotRegular for a FIFO or a device, never a host path outside root
// (06-security T2).
//
// Linux's openat2 answers EAGAIN instead of EXDEV when a rename anywhere on
// the host races its check of a ".." step (openat2(2): "the caller may
// choose to retry"), so a deps/ link, which always climbs out with "..",
// would answer 404 on a busy host instead of re-dispatching: the open is
// retried a few times.
func openPinned(root, owner, cleaned string) (*os.File, os.FileInfo, error) {
	f, err := fsutil.OpenBeneath(root, tileRel(owner, cleaned))
	for try := 0; errors.Is(err, syscall.EAGAIN) && try < openRetries; try++ {
		f, err = fsutil.OpenBeneath(root, tileRel(owner, cleaned))
	}
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

// openRetries bounds openPinned's retries of an open that raced a rename.
const openRetries = 16

// tileRel is cleaned relative to its owning component's directory.
func tileRel(owner, cleaned string) string {
	return strings.TrimPrefix(strings.TrimPrefix(cleaned, owner), "/")
}

// servePinnedStatic is handleComponentStatic's tail for a pinned primary, in
// every asset mode: serveCode with files opened beneath root (openPinned),
// served as the registry's component, which is the primary's.
func (s *Server) servePinnedStatic(w http.ResponseWriter, r *http.Request, cleaned, owner, root string) {
	comp, _, _ := s.Reg.Resolve(cleaned)
	s.serveCode(w, r, cleaned, owner, root, comp, func(name string) (*os.File, os.FileInfo, error) {
		return openPinned(root, owner, name)
	})
}

// serveCode is the tail of a /c/ request answered from one deployment's
// code: the directory, index and injection rules of the legacy and strict
// tails, files opened by open, then the strict modes' gate (assetGate), or
// legacy's inert non-documents, exactly as for a primary on the work tree.
// comp is what the documents are served as: the registry's component (the
// primary) or a non-primary deployment's view. root is the materialized
// checkpoint open reads beneath, whose deps/ links re-dispatch; "" for a
// work tree. Cache-Control stays no-store: pinned bytes change only by a
// deploy, which reloads the frames.
func (s *Server) serveCode(w http.ResponseWriter, r *http.Request, cleaned, owner, root string, comp *registry.Component, open func(string) (*os.File, os.FileInfo, error)) {
	f, fi, err := open(cleaned)
	if err != nil {
		if root == "" || !s.redispatchDeps(w, r, root, owner, cleaned, err) {
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
		if f, fi, err = open(name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
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
// while deployment dep of owner ("" names the primary) follows the work
// tree, beneath its checkpoint while pinned, where a deps/ link is
// re-dispatched to the asset plane of the tile it names. done reports that
// the request was answered — a re-dispatch, or a 404 when there is nothing
// to serve.
func (s *Server) openAssetFile(w http.ResponseWriter, r *http.Request, owner, dep, cleaned string) (f *os.File, fi os.FileInfo, done bool) {
	root, pinned, ok := s.primaryRoot(owner)
	if dep != "" {
		root, pinned, ok = s.deploymentRoot(owner, dep)
	}
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
// deployment whose path met a symlink leaving the checkpoint (err is
// fsutil.ErrEscapes) when the checkpoint's own deps/<name> entry names a
// registered component: the request is served as /c/<target>/<rest> —
// resolved, authorized and served exactly as a direct request for that URL
// by the same principal, on the same plane (the workspace origin, a tile
// origin, the asset-token plane) — so the other tile's primary answers
// under the other tile's gate (P16). The target comes from the checkpoint,
// never from the work tree's manifest or deps/ links, so a work-tree edit
// can't re-point what a pinned deployment imports (P9). It reports whether
// it answered; false leaves the 404 (ErrEscapes) to the caller.
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

// ---- deployment URLs: /c/<tile>+<name>/… ----

// AddressedPolicy is a Policy that answers which deployment of tile a
// request by p reaches (11-contract §7.1): for one of tile's own principals,
// the deployment its credential binds — a frame token's claim, an instance
// token's generation, a terminal or agent session's target, the current
// primary for a session that follows it — and for anyone else the primary.
// util.ErrNoDeployment for a bound deployment that no longer exists; any
// other error means p is bound to none (a session that follows a protected
// primary), its text the reason. Without it every credential of a tile
// binds main, and a session follows the primary.
type AddressedPolicy interface {
	Addressed(p auth.Principal, tile string) (string, error)
}

// boundDeployment is the deployment of tile that p, one of tile's own
// principals (ownPrincipalOf), is bound to. An xbin.window sub-path's
// token (apps/x/editor, of tile apps/x) is its opener tile's credential and
// binds as the tile's own: its documents inherit the opener's deployment.
func (s *Server) boundDeployment(p auth.Principal, tile string) (string, error) {
	p.Component = tile
	if ap, ok := s.policy().(AddressedPolicy); ok {
		return ap.Addressed(p, tile)
	}
	switch {
	case p.Via == "terminal" && p.Deployment == "":
		return s.primaryOf(tile), nil
	case p.Deployment == "" || p.Deployment == util.MainDeployment:
		return util.MainDeployment, nil
	}
	return "", util.NoDeployment(tile, p.Deployment)
}

// ownPrincipalOf reports whether p is one of tile's own principals: an
// element whose credential is the tile's, or an xbin.window sub-path of it
// (never a nested component, which is a tile of its own).
func (s *Server) ownPrincipalOf(p auth.Principal, tile string) bool {
	return p.Component != "" && (p.Component == tile || s.owningComponent(p.Component) == tile)
}

// writesTile reports whether the user an element principal carries holds
// write on tile now ("" is the owner, or no user at all). tileLevel passes
// any element on its own tile, so the user's own levels are asked; they are
// resolved when the request's principal is built, so this is the current
// level on every request.
func writesTile(p auth.Principal, tile string) bool {
	return p.UserID == "" || (p.Access != nil && p.Access.CanWriteTile(tile))
}

// userWritesTile is UserCanReadTile's write half: whether uid ("" is the
// owner) may write tile now, by its current levels; a disabled or deleted
// user may not.
func (s *Server) userWritesTile(uid, tile string) bool {
	if s.Auth.NoAuth() || uid == "" {
		return true
	}
	st := s.Auth.Users
	if st == nil {
		return false
	}
	u, ok := st.Get(uid)
	if !ok || u.Disabled {
		return false
	}
	if acc, _ := st.Access(uid); acc != nil {
		return acc.CanWriteTile(tile)
	}
	return u.CanWriteTile(tile)
}

// served is what a /c/ document of tile is served as: the deployment whose
// code it is, and the tile's primary.
type served struct{ tile, dep, primary string }

type servedKey struct{}

// servedDeployment is what a document of compPath on r is served as: the
// deployment a deployment URL named (serveQualified), or the primary, which
// the bare URL serves.
func (s *Server) servedDeployment(r *http.Request, compPath string) served {
	if sv, _ := r.Context().Value(servedKey{}).(*served); sv != nil && sv.tile == compPath {
		return *sv
	}
	prim := s.primaryOf(compPath)
	return served{tile: compPath, dep: prim, primary: prim}
}

// withoutServed drops a deployment URL's served value from a request that
// no longer names it: a deps/ link re-dispatched to a tile's bare URL, the
// tile's own included, serves that tile's primary.
func withoutServed(r *http.Request) *http.Request {
	if sv, _ := r.Context().Value(servedKey{}).(*served); sv == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), servedKey{}, (*served)(nil)))
}

// documentToken mints the frame token of a compPath document on r for p,
// whom mayMintFrameToken admitted, bound to p's login (frametoken.go). Its
// claim names the served deployment under the name rule: none for main, so
// every zero-state document's token keeps today's form; the primary's name
// when the primary is another deployment, whether a terminal session that
// follows it or a person opened the bare URL (P12, P17).
func (s *Server) documentToken(r *http.Request, p auth.Principal, compPath string) string {
	if dep := s.servedDeployment(r, compPath).dep; dep != util.MainDeployment {
		return s.Auth.MintFrameTokenForDeployment(p, compPath, dep, frameTokenTTL)
	}
	return s.Auth.MintFrameTokenFor(p, compPath, frameTokenTTL)
}

// mayOpenServed is mayMintFrameToken's rule for the tile's own principals
// and people (11-contract §7.2, rules 1 and 2): a person who may open the
// served deployment — read for the primary, write for any other, at the
// current level; one of the tile's own principals bound to it, a
// user-attributed one of a non-primary deployment only while its user
// writes the tile. decided is false for everyone else (another tile's
// frame), whom only the navigation rule may admit.
func (s *Server) mayOpenServed(r *http.Request, p auth.Principal, compPath string) (may, decided bool) {
	sv := s.servedDeployment(r, compPath)
	if p.Component == "" {
		return sv.dep == sv.primary || p.CanWriteTile(compPath), true
	}
	if !s.ownPrincipalOf(p, compPath) {
		return false, false
	}
	bound, err := s.boundDeployment(p, compPath)
	return err == nil && bound == sv.dep && (sv.dep == sv.primary || writesTile(p, compPath)), true
}

// boundToPrimary reports, for mayMintFrameToken's navigation rule (§7.2
// rule 3), that the document on r is its tile's primary and that p, a frame
// of tile own, is bound to own's primary: the navigation rule never crosses
// into or out of a non-primary deployment.
func (s *Server) boundToPrimary(r *http.Request, p auth.Principal, own, compPath string) bool {
	if sv := s.servedDeployment(r, compPath); sv.dep != sv.primary {
		return false
	}
	bound, err := s.boundDeployment(p, own)
	return err == nil && bound == s.primaryOf(own)
}

// deploymentHead is the D4 injection's deployment part for a compPath
// document on r (11-contract §2.6, §6): for a deployment other than the
// primary, the xbin-deployment meta (the role rule), and one import-map
// entry, /c/<tile>/ → /c/<tile>+<name>/, so absolute self-imports of modules
// stay in the deployment (tokens mode moves it under the asset token with
// the rest). "" and imports untouched for the primary's documents, so every
// zero-state document keeps today's bytes.
func (s *Server) deploymentHead(r *http.Request, compPath string, imports map[string]string) string {
	sv := s.servedDeployment(r, compPath)
	if sv.dep == sv.primary {
		return ""
	}
	imports["/c/"+compPath+"/"] = "/c/" + compPath + "+" + sv.dep + "/"
	return "<meta name=\"xbin-deployment\" content=\"" + htmlEscape(sv.dep) + "\">\n"
}

// qualifiedRef is a /c/ path that names a deployment: the tile, the
// deployment (the alias names the primary), the tile's primary and the
// remainder beneath the tile. err is util.ErrNoDeployment for an unknown
// name, or registry.ErrNestedTile for a remainder in a nested component,
// answered after the gate.
type qualifiedRef struct {
	c                  *registry.Component
	dep, primary, rest string
	err                error
}

// resolveQualified resolves cleaned when it names a deployment of a tile
// with a record (registry.ResolveRef); false for every other path, which
// today's resolution answers. A path without '+' never gets past the first
// line, and a workspace without deployment records never splits one.
func (s *Server) resolveQualified(cleaned string) (qualifiedRef, bool) {
	if strings.IndexByte(cleaned, '+') < 0 {
		return qualifiedRef{}, false
	}
	d := s.deploymentLookup()
	if d == nil {
		return qualifiedRef{}, false
	}
	c, dep, qualified, rest, err := s.Reg.ResolveRef(cleaned, d)
	if !qualified || c == nil {
		return qualifiedRef{}, false
	}
	return qualifiedRef{c: c, dep: dep, primary: d.Primary(c.Path), rest: rest, err: err}, true
}

// deploymentLookup is what ResolveRef asks about deployment records, from
// the Policy: a record governs exactly the tiles that have a primary summary
// (PrimarySummaryPolicy), each tile's deployments are HasDeployment's and
// its primary is primaryOf's. nil without a PrimarySummaryPolicy: no tile
// has a record, so no path splits.
func (s *Server) deploymentLookup() registry.DeploymentLookup {
	sp, ok := s.policy().(PrimarySummaryPolicy)
	if !ok {
		return nil
	}
	return policyLookup{s, sp}
}

type policyLookup struct {
	s  *Server
	sp PrimarySummaryPolicy
}

func (l policyLookup) HasRecord(tile string) bool {
	_, _, _, ok := l.sp.PrimarySummary(tile)
	return ok
}
func (l policyLookup) HasDeployment(tile, name string) bool {
	return l.s.policy().HasDeployment(tile, name)
}
func (l policyLookup) Primary(tile string) string { return l.s.primaryOf(tile) }

// deploymentURLGate is 11-contract §2.3's gate on /c/<tile>+<name>/… (P7,
// P12, P20); 0 admits, otherwise the status and text of the refusal.
//   - Other tiles' principals are refused on every deployment URL, the
//     alias included, so no tile hard-codes a name a reassignment would
//     break: they use the bare URL.
//   - The alias, <tile>+<primary>: people and the tile's own principals as
//     on the bare URL, whose read gate the caller applies too.
//   - Any other deployment: people with write on the tile, at their
//     current level on every request, never the bare p.Component == tile
//     test that a reader's frame passes too; a credential-less subresource
//     load by legacy's rule (tile code, never a document); the tile's own
//     principals bound to it, a user-attributed one only while its user
//     writes the tile; the tile's other own principals only for files that
//     can't be documents, in legacy mode, as the tile's code its readers may
//     read. Everyone else gets 403, whether or not the name exists.
func (s *Server) deploymentURLGate(r *http.Request, p auth.Principal, q qualifiedRef) (int, string) {
	t := q.c.Path
	own := s.ownPrincipalOf(p, t)
	if p.Component != "" && !own {
		return http.StatusForbidden, fmt.Sprintf("%s opens %s by its bare URL, /c/%s/, which serves its primary: "+
			"deployment URLs are for the tile itself and the people who work on it", p.From(), t, t)
	}
	if q.dep == q.primary {
		return 0, ""
	}
	needsWrite := "deployment URLs need write access on " + t
	if !own {
		switch {
		case p.CanWriteTile(t): // terminal level and admins included
			return 0, ""
		case p == (auth.Principal{}) && s.tileSubresourceAuthed(r):
			return 0, ""
		}
		return http.StatusForbidden, needsWrite
	}
	bound, err := s.boundDeployment(p, t)
	switch {
	case err == nil && bound == q.dep && writesTile(p, t):
		return 0, ""
	case err == nil && bound == q.dep:
		return http.StatusForbidden, needsWrite
	case !s.strictAssets() && !documentRequest(r, q.rest):
		return 0, ""
	case err != nil:
		return http.StatusForbidden, err.Error()
	}
	return http.StatusForbidden, fmt.Sprintf("a tile's own credentials act only on their own deployment (%s)", bound)
}

// documentRequest reports whether a request for rest, a path beneath a
// tile, may be answered with a document: a directory's index, an HTML file,
// a native runtime document.
func documentRequest(r *http.Request, rest string) bool {
	return rest == "" || strings.HasSuffix(r.URL.Path, "/") || isHTMLName(rest) || nativeRuntimeRequest(r)
}

// serveQualified answers a /c/ request for a deployment URL; false leaves
// every other path to today's handling. The gate comes first, so whoever
// may not know the tile's deployments gets its refusal whether or not the
// name exists (11-contract §2.2); then an unknown name or a path into a
// nested tile is a 404, and the deployment's code answers through
// CodeRoot(tile, name): beneath its checkpoint while pinned, by the plane's
// own opener while it follows the work tree (07-runtime §4.1, §4.3). A
// non-primary deployment's documents are served as its registry view, so
// its own inject and native entry apply and chrome never does; ?native=1
// is generated from its code (§2.7).
func (s *Server) serveQualified(w http.ResponseWriter, r *http.Request, cleaned string) bool {
	q, ok := s.resolveQualified(cleaned)
	if !ok {
		return false
	}
	owner, p := q.c.Path, auth.PrincipalOf(r)
	if isChrome(owner) { // workspace chrome has no deployments (P19)
		http.NotFound(w, r)
		return true
	}
	if code, msg := s.deploymentURLGate(r, p, q); code != 0 {
		http.Error(w, msg, code)
		return true
	}
	if q.dep == q.primary && s.tileReadRefused(w, r, p, owner) {
		return true
	}
	if q.err != nil {
		http.Error(w, q.err.Error(), http.StatusNotFound)
		return true
	}
	root, pinned, ok := s.codeRoot(q.c, q.dep)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	comp := q.c
	if q.dep != q.primary {
		v, err := s.deploymentView(q.c, q.dep, root, pinned)
		if err != nil {
			http.NotFound(w, r)
			return true
		}
		comp = v
	}
	r = r.WithContext(context.WithValue(r.Context(), servedKey{}, &served{tile: owner, dep: q.dep, primary: q.primary}))
	if nativeRuntimeRequest(r) && s.serveNativeFor(w, r, comp, q.rest == "") {
		return true
	}
	open := s.openLegacy
	switch {
	case pinned:
		open = func(name string) (*os.File, os.FileInfo, error) { return openPinned(root, owner, name) }
	case s.strictAssets():
		open = func(name string) (*os.File, os.FileInfo, error) { return s.openStrict(owner, name) }
	}
	s.serveCode(w, r, path.Join(owner, q.rest), owner, root, comp, open)
	return true
}

// deploymentView is what a non-primary deployment's documents are served
// as: the registry's view of its code (07-runtime §5.1) — its own inject and
// native entry, the tile's inbound surface with chrome off. A pinned view
// reads the checkpoint materialized at root, whose directory is named by its
// tree (checkpoint.Store.Materialize); the registry caches it by both.
func (s *Server) deploymentView(c *registry.Component, dep, root string, pinned bool) (*registry.Component, error) {
	code := registry.ViewCode{Deployment: dep}
	if pinned {
		code.Tree, code.Root = filepath.Base(root), root
	}
	return s.Reg.View(c, code)
}

// assetDeploymentRefused applies a deployment URL's gate on the asset-token
// plane (11-contract §2.6, tokens), after the plane's own read checks, and
// answers the refusal: a deployment other than the primary needs the
// token's user to write the tile, checked live (the token itself names only
// the tile; the path carries the deployment); then an unknown name or a
// path into a nested tile is a 404.
func (s *Server) assetDeploymentRefused(w http.ResponseWriter, g auth.AssetGrant, q qualifiedRef) bool {
	switch {
	case q.dep != q.primary && !s.userWritesTile(g.UserID, q.c.Path):
		http.Error(w, "deployment URLs need write access on "+q.c.Path, http.StatusForbidden)
	case q.err != nil:
		http.Error(w, q.err.Error(), http.StatusNotFound)
	default:
		return false
	}
	return true
}
