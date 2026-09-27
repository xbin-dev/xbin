package broker

// deploydata.go — tile deployments beyond main in the broker, declared once
// before the work on them runs in parallel:
//   - DeploymentAnswers, what the deployments plane tells the broker (a
//     tile's deployments and primary, the deployment a principal's request
//     reaches, which registrations are active, the stored edge policy, the
//     per-deployment registration files), installed at boot, and the
//     broker's answers to the server and the obs plane built on them;
//   - Route and resolveTarget: which deployment a call reaches and at which
//     role (09-fabric §4.1), and the single evaluation point of the edge
//     policy (§5.3), whose verdict on a non-primary caller edgepolicy.go
//     supplies (edgeVerdict);
//   - resKeys and scopeKeys, the key function for per-deployment data
//     (08-data §3.2): the one place in the broker that builds a resource's
//     physical keys; the namespace kv files (kvDB); the tile-keyed files of
//     a deployment beyond main, and DropDeploymentFiles, which a new
//     deployment starts from.
//
// Every answer gives a tile without a deployment record exactly what the
// broker answered before tile deployments (F1) (P5): its one deployment is
// main, the primary, and its principals all act in it.

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// DeploymentAnswers are installed into the broker by the deployments plane
// at boot (Broker embeds them). Each is nil-safe: nil answers as a tile
// without a record, whose one deployment, main, is its primary.
type DeploymentAnswers struct {
	// PrimaryOf names tile's primary deployment ("main" without a record).
	// An in-memory lookup, asked on every call a tile makes or receives.
	PrimaryOf func(tile string) string
	// DeploymentsOf names tile's primary and every deployment it has, main
	// first.
	DeploymentsOf func(tile string) (primary string, names []string)
	// AddressedDeployment is the deployment of tile a request by p reaches
	// (08-data §4.1; 11-contract §0.4 DR1): a principal of tile itself
	// reaches the deployment its credential binds (a terminal or agent
	// session that follows the primary reaches the current primary, and
	// none while the primary is protected); anyone else reaches the
	// primary. util.ErrNoDeployment for a bound deployment that no longer
	// exists (404); any other error is a refusal (403) whose text says why.
	AddressedDeployment func(p auth.Principal, tile string) (string, error)
	// RegistrationsActive reports whether deployment dep of tile's
	// registrations take effect (09-fabric §7) (P13): fires for its cron
	// jobs and bus subscriptions (the primary, or deliveries on), routes for
	// its interface instances and ingress hosts (the primary only).
	RegistrationsActive func(tile, dep string) (fires, routes bool)
	// DeploymentEdges is tile's stored edge policy for its non-primary
	// deployments, edge id → value: the overrides only. An absent id takes
	// its kind's default; a value this xbind doesn't know reads as block
	// (09-fabric §5.2) (P27).
	DeploymentEdges func(tile string) map[string]string
	// ReadDeploymentFile, WriteDeploymentFile and RemoveDeploymentFile keep
	// a non-main deployment's registration files beside its tile's record
	// (data/deployments/<TileKey>/<name>/, 11-contract §10.2: cron.json,
	// bus-subscriptions.json, iface-instances.json, ingress-hosts.json,
	// backup-schedule.json, sandboxes.json). Writes go through the plane,
	// under the records directory's lock, so an opt-out never removes the
	// directory under a write. A read of a missing file matches
	// fs.ErrNotExist. main's registrations stay in today's stores.
	ReadDeploymentFile   func(tile, dep, file string) ([]byte, error)
	WriteDeploymentFile  func(tile, dep, file string, data []byte) error
	RemoveDeploymentFile func(tile, dep, file string) error
}

// primaryOf names tile's primary deployment.
func (b *Broker) primaryOf(tile string) string {
	if f := b.PrimaryOf; f != nil {
		return f(tile)
	}
	return util.MainDeployment
}

// isPrimary reports whether dep ("" is main, the name rule) is tile's
// primary.
func (b *Broker) isPrimary(tile, dep string) bool {
	return cmp.Or(dep, util.MainDeployment) == b.primaryOf(tile)
}

// deploymentsOf names tile's primary and every deployment it has, main
// first.
func (b *Broker) deploymentsOf(tile string) (string, []string) {
	if f := b.DeploymentsOf; f != nil {
		return f(tile)
	}
	return util.MainDeployment, []string{util.MainDeployment}
}

// hasDeployment reports whether tile has a deployment called name.
func (b *Broker) hasDeployment(tile, name string) bool {
	return brokerPolicy{b}.HasDeployment(tile, name)
}

// addressed is the deployment of tile a request by p reaches. Without the
// plane every principal acts in main, and a credential naming any other
// deployment names one that doesn't exist.
func (b *Broker) addressed(p auth.Principal, tile string) (string, error) {
	if f := b.AddressedDeployment; f != nil {
		return f(p, tile)
	}
	if p.Component == tile && p.Deployment != "" && p.Deployment != util.MainDeployment {
		return "", util.NoDeployment(tile, p.Deployment)
	}
	return util.MainDeployment, nil
}

// registrationsActive reports whether deployment dep of tile's cron jobs and
// bus subscriptions fire, and whether its interface instances and ingress
// hosts route.
func (b *Broker) registrationsActive(tile, dep string) (fires, routes bool) {
	if f := b.RegistrationsActive; f != nil {
		return f(tile, dep)
	}
	main := cmp.Or(dep, util.MainDeployment) == util.MainDeployment
	return main, main
}

// deploymentEdges is tile's stored edge policy; nil when none is stored.
func (b *Broker) deploymentEdges(tile string) map[string]string {
	if f := b.DeploymentEdges; f != nil {
		return f(tile)
	}
	return nil
}

// readDeploymentFile, writeDeploymentFile and removeDeploymentFile are the
// registration-file hooks, nil-safe: without the plane no deployment but
// main exists, so there is nothing to read, nowhere to write and nothing to
// remove.
func (b *Broker) readDeploymentFile(tile, dep, file string) ([]byte, error) {
	if f := b.ReadDeploymentFile; f != nil {
		return f(tile, dep, file)
	}
	return nil, fmt.Errorf("%s: deployment %q has no %s: %w", tile, dep, file, fs.ErrNotExist)
}

func (b *Broker) writeDeploymentFile(tile, dep, file string, data []byte) error {
	if f := b.WriteDeploymentFile; f != nil {
		return f(tile, dep, file, data)
	}
	return util.NoDeployment(tile, dep)
}

func (b *Broker) removeDeploymentFile(tile, dep, file string) error {
	if f := b.RemoveDeploymentFile; f != nil {
		return f(tile, dep, file)
	}
	return nil
}

// brokerPolicy names each tile's primary for the server's event audience
// (server.PrimaryPolicy), and the deployment a principal's request reaches
// for the planes that bind a credential to one (the D4 mint, renewal).
var _ server.PrimaryPolicy = brokerPolicy{}

func (p brokerPolicy) Primary(tile string) string { return p.b.primaryOf(tile) }

func (p brokerPolicy) Addressed(pr auth.Principal, tile string) (string, error) {
	return p.b.addressed(pr, tile)
}

// ---- which deployment a call reaches, at which role ----

// Decision is the outcome of one call to a target (09-fabric §5.3): the
// deployment it reaches and the role it holds there, or why it is refused.
type Decision struct {
	Deployment string   // the target's deployment: its primary, or the caller's own on a self-call
	Role       string   // the effective role on the target; "" when refused
	Clamped    bool     // the read clamp narrowed Role (P3)
	Edges      []string // the caller's edges authorizing the call (09-fabric §5.1's ids)
	// Deny is non-nil when the call is refused: util.ErrNoDeployment is a
	// 404, anything else a 403 whose text names the rule or the edge.
	Deny error
}

// NotGrantedError is the refusal of a caller holding no role on the target:
// the proxy's 403 text, which a tile without a record keeps byte for byte.
type NotGrantedError struct{ From, Target string }

func (e *NotGrantedError) Error() string {
	return fmt.Sprintf("%s is not granted access to %s — declare it in \"uses\" and approve the grant (bx grant, or the grants panel)",
		e.From, e.Target)
}

// edgeVerdict applies the calling tile's edge policy to one call its
// non-primary deployment callerDep makes to target, where the tile holds
// role (09-fabric §5.4–§5.9) (P3) (P23) (P27): read-clamped, or blocked naming
// the edge. edgepolicy.go installs it from an init function. Unset, a
// non-primary deployment reaches no other tile (F6: fail closed).
var edgeVerdict func(b *Broker, d Decision, caller, callerDep, target, role string) Decision

// Route is the proxy's routing function for /api/<target>[+<qualifier>]/…
// (09-fabric §4.1): which deployment of target a call by p reaches, at
// which role, or why it is refused. qualifier is the deployment the URL
// names, "" for the bare URL. For a tile without a record and a bare URL
// the answer is Policy's, and a refusal's text the proxy's own
// (TestZeroStateRoute). The rules, in order:
//  1. a cron or bus delivery reaches its registration's deployment, with
//     the role bound at registration, and names none;
//  2. the tile's own principal reaches its bound deployment as admin (P12):
//     a qualifier naming another is refused, and a user-attributed frame of
//     a non-primary deployment needs its user's current write;
//  3. an admin reaches the qualifier's deployment, or the primary;
//  4. anyone else reaches the primary, with the role resolveTarget gives;
//     another tile names no deployment at all (NP-11-14), and a person
//     names a non-primary one only with write on the tile.
func (b *Broker) Route(p auth.Principal, target *registry.Component, qualifier string) Decision {
	t := target.Path
	switch {
	case p.Component == CronPrincipal || p.Component == BusPrincipal:
		dep := cmp.Or(p.Deployment, util.MainDeployment)
		switch {
		case qualifier != "":
			return Decision{Deny: fmt.Errorf("a %s delivery reaches the deployment its registration belongs to, and names none", p.Component)}
		case !b.hasDeployment(t, dep):
			return Decision{Deny: util.NoDeployment(t, dep)}
		}
		return Decision{Deployment: dep, Role: p.Role}
	case p.Component != "" && p.Component == t:
		dep, err := b.addressed(p, t)
		switch {
		case err != nil:
			return Decision{Deny: err}
		case qualifier != "" && qualifier != dep:
			return Decision{Deny: fmt.Errorf("%s's deployment %q can only call itself: this credential belongs to %q, not %q. "+
				"A tile's code never reaches another deployment of its own tile; a person switches deployments by URL, "+
				"a terminal by changing its target.", t, dep, dep, qualifier)}
		case p.Via == "frame" && p.UserID != "" && !b.isPrimary(t, dep) && (p.Access == nil || !p.Access.CanWriteTile(t)):
			return Decision{Deny: fmt.Errorf("deployments of %s need write access", t)}
		}
		return Decision{Deployment: dep, Role: "admin"}
	case p.IsAdmin():
		dep := cmp.Or(qualifier, b.primaryOf(t))
		if !b.hasDeployment(t, dep) {
			return Decision{Deny: util.NoDeployment(t, dep)}
		}
		return Decision{Deployment: dep, Role: "admin"}
	}
	dep := b.primaryOf(t)
	if qualifier != "" {
		switch {
		case p.Component != "":
			return Decision{Deny: fmt.Errorf("%s calls %s by its bare URL, /api/%s/, which reaches its primary: "+
				"deployment URLs are for the tile itself and the people who work on it", p.Component, t, t)}
		case qualifier != dep && !p.CanWriteTile(t):
			return Decision{Deny: fmt.Errorf("deployment URLs need write access on %s", t)}
		case !b.hasDeployment(t, qualifier):
			return Decision{Deny: util.NoDeployment(t, qualifier)}
		}
		dep = qualifier
	}
	callerDep := ""
	if p.Component != "" {
		var err error
		if callerDep, err = b.addressed(p, p.Component); err != nil {
			return Decision{Deny: err}
		}
	}
	d := b.resolveTarget(p.Component, callerDep, t)
	d.Deployment = dep
	var ng *NotGrantedError
	if errors.As(d.Deny, &ng) {
		ng.From = p.From() // as the proxy names a person today
	}
	return d
}

// resolveTarget is the single evaluation point of a call from deployment
// callerDep of tile caller to target, a tile path or a res: id (09-fabric
// §5.3; F5): the tile's authority, grantedRole, unchanged (P11); for the
// primary, every zero-state tile's included, exactly that role; for a
// non-primary deployment, governance refused (P19) and every other edge
// through the edge policy. It is the only reader of the edge policy.
func (b *Broker) resolveTarget(caller, callerDep, target string) Decision {
	role, ok := b.grantedRole(caller, target)
	d := Decision{Deployment: b.primaryOf(target)}
	switch {
	case !ok:
		d.Deny = &NotGrantedError{From: caller, Target: target}
	case b.isPrimary(caller, callerDep):
		d.Role = role
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		d.Deny = fmt.Errorf("%s's non-primary deployment %q can't use %s: governance grants serve the tile's primary only",
			caller, callerDep, target)
	case edgeVerdict == nil:
		d.Deny = fmt.Errorf("%s's non-primary deployment %q can't call %s: this xbind has no edge policy, so non-primary deployments reach no other tile",
			caller, callerDep, target)
	default:
		d = edgeVerdict(b, d, caller, callerDep, target, role)
	}
	return d
}

// ---- the key function for per-deployment data ----
//
// Every physical key of a resource is computed here and nowhere else in the
// broker (TestNoAdHocResourceKeys): a scope's data key, its kv bucket and
// labels, its encrypted volumes and mounts, its disk-quota key (08-data §2,
// §3.2) (P6). main's keys are today's, byte for byte, whether or not main is
// the primary. Every other deployment's live under a ".deployments" level
// inside today's roots, keyed by escS, an injective encoding of the scope
// path, and the deployment name. No scope key starts with ".", so no path
// today's or an older xbind computes can reach that level (08-data §3.3).

// String is the resource's id, res:<scope>/<name> ("workspace" for the
// workspace scope): its grant target, its kv bucket in every namespace, its
// bus topic prefix.
func (rt resTarget) String() string {
	s := rt.Scope
	if s == "" {
		s = "workspace"
	}
	return "res:" + s + "/" + rt.Name
}

// deploymentsLevel is the directory level, inside data/resources-enc and
// .xbin/resenc, that holds every namespace but main's.
const deploymentsLevel = ".deployments"

// maxEscS bounds an encoded scope: a scope whose escS is longer can't have
// deployments beyond main (08-data §3.2).
const maxEscS = 200

// escS is the injective encoding of a scope path for the keys of namespaces
// beyond main (08-data §3.3): "%" becomes "%25", then "~" becomes "%7E",
// then "/" becomes "~". util.ScopeKey maps apps~cal and apps/cal to one key;
// escS maps them to apps%7Ecal and apps~cal. Afterwards "%" appears only as
// %25 or %7E and "~" only for "/", so the scope is recoverable (unescS),
// and the result is one path segment.
func escS(scope string) string { return escSReplacer.Replace(scope) }

var (
	escSReplacer   = strings.NewReplacer("%", "%25", "~", "%7E", "/", "~")
	unescSReplacer = strings.NewReplacer("~", "/", "%7E", "~", "%25", "%")
)

// unescS recovers the scope path escS encoded into seg; false when seg is
// no encoding escS produces (a directory the key function never made).
func unescS(seg string) (string, bool) {
	scope := unescSReplacer.Replace(seg)
	return scope, seg != "" && escS(scope) == seg
}

// nsKeys are the keys of one scope's data namespace in one deployment, which
// all its resources share (08-data §2, §3.1). Paths are slash-separated and
// relative to the workspace root.
type nsKeys struct {
	NS     string // "" for main | ".deployments/<escS>/<d>"
	DirKey string // resenc's scopeKey argument: ScopeKey(S) | NS+"/fs"
	Quota  string // the disk-quota key: ScopeKey(S) | NS
	Plain  string // main's plaintext resource dir, data/resources/<ScopeKey(S)>; "" beyond main, which has none
	Enc    string // what the quota measures besides Plain: data/resources-enc/<ScopeKey(S)> | data/resources-enc/<NS>, its kv file included
}

// scopeKeys computes scope's namespace keys in deployment dep ("" is main).
// main's are today's and never an error. Beyond main: dep must be a
// deployment name, the workspace scope is never split (its resources are
// reached as an edge, 08-data §4.2), the scope must be a workspace path, and
// its encoding at most maxEscS bytes.
func scopeKeys(scope, dep string) (nsKeys, error) {
	if dep == "" || dep == util.MainDeployment {
		sk := util.ScopeKey(scope)
		return nsKeys{DirKey: sk, Quota: sk, Plain: "data/resources/" + sk, Enc: "data/resources-enc/" + sk}, nil
	}
	switch {
	case !util.DeploymentNameOK(dep):
		return nsKeys{}, fmt.Errorf("%q is not a deployment name", dep)
	case scope == "":
		return nsKeys{}, fmt.Errorf("the workspace's resources have one namespace: deployment %q reaches res:workspace/* as an edge, never a copy of its own", dep)
	case !util.ComponentPathOK(scope):
		return nsKeys{}, fmt.Errorf("scope %q is not a workspace path", scope)
	}
	e := escS(scope)
	if len(e) > maxEscS {
		return nsKeys{}, fmt.Errorf("scope %s is too long for deployments beyond main: its data key would be %d bytes, over %d", scope, len(e), maxEscS)
	}
	ns := deploymentsLevel + "/" + e + "/" + dep
	return nsKeys{NS: ns, DirKey: ns + "/fs", Quota: ns, Enc: "data/resources-enc/" + ns}, nil
}

// resKeys is every physical key of one resource in one data namespace
// (08-data §3.2). main's are today's; another deployment's are the
// namespace's own, which no path produces today (P6).
type resKeys struct {
	NS      string // "" for main | ".deployments/<escS>/<d>" (also the disk-quota key)
	DirKey  string // resenc's scopeKey argument: ScopeKey(S) | NS+"/fs"
	Name    string // the resource name, validated
	FSLabel string // resenc resID: resLabel(ScopeKey(S), name) | NS+"/fs/"+name
	KVFile  string // "" = data/kv.db | "data/resources-enc/"+NS+"/kv.db"
	Bucket  string // rt.String() in both cases
	KVLabel string // "kv:"+Bucket | "kv:"+NS+"/"+Bucket
}

// quotaKey is the disk-quota key of k's namespace (nsKeys.Quota).
func (k resKeys) quotaKey() string { return cmp.Or(k.NS, k.DirKey) }

// resKeys computes rt's keys in deployment dep's namespace ("" is main).
// main's are today's, byte for byte, refusing only a resource name with a
// ".." segment or a NUL, which would leave data/resources-enc. Beyond main
// scopeKeys's rules apply, and a name must also be non-empty, with no
// leading "/" and no "." or empty segment: such names alias today (./x and
// x are one directory), and a new namespace doesn't inherit that (NP-08-11).
// The registry refuses more before a name gets here (D118's charset); this
// is the last line wherever a name comes from.
func (b *Broker) resKeys(rt resTarget, dep string) (resKeys, error) {
	ns, err := scopeKeys(rt.Scope, dep)
	if err != nil {
		return resKeys{}, fmt.Errorf("%s: %w", rt, err)
	}
	if !mainResNameOK(rt.Name) || ns.NS != "" && !nsResNameOK(rt.Name) {
		where := "the workspace's xbin.json"
		if rt.Scope != "" {
			where = rt.Scope + "/scope.json"
		}
		return resKeys{}, fmt.Errorf("resource name %q in %s is not a plain relative path", rt.Name, where)
	}
	bucket := rt.String()
	if ns.NS == "" {
		return resKeys{DirKey: ns.DirKey, Name: rt.Name, FSLabel: resLabel(ns.DirKey, rt.Name),
			Bucket: bucket, KVLabel: "kv:" + bucket}, nil
	}
	return resKeys{NS: ns.NS, DirKey: ns.DirKey, Name: rt.Name, FSLabel: ns.DirKey + "/" + rt.Name,
		KVFile: nsKVFile(ns.NS), Bucket: bucket, KVLabel: "kv:" + ns.NS + "/" + bucket}, nil
}

// nsKVFile is the kv file of namespace ns (an nsKeys.NS beyond main).
func nsKVFile(ns string) string { return "data/resources-enc/" + ns + "/kv.db" }

// mainResNameOK reports whether name may name a resource in main's
// namespace: no ".." segment, no NUL. Every name that works today and stays
// inside its scope's directory keeps working.
func mainResNameOK(name string) bool {
	if strings.IndexByte(name, 0) >= 0 {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// nsResNameOK is the rest of the name rule beyond main: non-empty, no
// leading "/", no "." or empty segment.
func nsResNameOK(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." {
			return false
		}
	}
	return true
}

// ---- the namespace kv files ----

// kvDB is the bbolt file holding k's bucket: data/kv.db for main, the
// namespace's own file beyond it (08-data §2), so a namespace's writes never
// take kv.db's writer lock, a reset frees its disk, and an older xbind never
// sees it. create opens a namespace file that doesn't exist yet, making it;
// without create a missing file answers nil, nil: a namespace nothing wrote
// to reads as empty, and a read creates nothing (08-data §3.7). nil, nil
// also for a broker without kv (unit fixtures).
func (b *Broker) kvDB(k resKeys, create bool) (*bolt.DB, error) {
	switch {
	case b.kv == nil && create:
		return nil, errors.New("the kv store isn't open")
	case b.kv == nil:
		return nil, nil
	case k.KVFile == "":
		return b.kv.db, nil
	}
	return b.kv.nsFile(k.KVFile, create)
}

// scopeKV is the kv file of scope's namespace in dep, the one kvDB answers
// for each of its resources.
func (b *Broker) scopeKV(scope, dep string, create bool) (*bolt.DB, error) {
	ns, err := scopeKeys(scope, dep)
	if err != nil {
		return nil, err
	}
	k := resKeys{NS: ns.NS}
	if ns.NS != "" {
		k.KVFile = nsKVFile(ns.NS)
	}
	return b.kvDB(k, create)
}

// kvView runs fn in a read transaction of db; a nil db, a namespace nothing
// wrote to, has no buckets.
func kvView(db *bolt.DB, fn func(*bolt.Tx) error) error {
	if db == nil {
		return nil
	}
	return db.View(fn)
}

// nsFile opens (once) the namespace kv file at rel, relative to the
// workspace root.
func (s *kvStore) nsFile(rel string, create bool) (*bolt.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.ns[rel]; db != nil {
		return db, nil
	}
	p := filepath.Join(s.root, filepath.FromSlash(rel)) // data/ is xbind's own: no sandbox sees it
	if !create {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(p, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	if s.ns == nil {
		s.ns = map[string]*bolt.DB{}
	}
	s.ns[rel] = db
	return db, nil
}

// closeNamespace closes namespace ns's kv file, if open, so it can be
// removed (a reset or a removal, 08-data §9); the next use opens it again.
func (s *kvStore) closeNamespace(ns string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := nsKVFile(ns)
	db := s.ns[rel]
	if db == nil {
		return nil
	}
	delete(s.ns, rel)
	return db.Close()
}

// close closes kv.db and every namespace file.
func (s *kvStore) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for rel, db := range s.ns {
		_ = db.Close()
		delete(s.ns, rel)
	}
	if s.db != nil {
		_ = s.db.Close()
	}
}

// ---- the tile-keyed files of a deployment ----

// depFiles are the files of one deployment beyond main that belong to its
// tile rather than to a (scope, name) namespace (08-data §2, §3.3): each
// keyed by the tile's TileKey, never CompKey, whose 32 bits can be ground,
// so a tile at a colliding path shares none of them (P29). Paths are
// slash-separated and relative to the workspace root. main has none here:
// its files keep today's keys.
type depFiles struct {
	Vault   string // data/vault/.deployments/<TK>/<d>.json
	Prefs   string // inside each user's data/prefs/<CompKey(user)>/: .deployments/<TK>/<d>.json
	Records string // data/deployments/<TK>/<d>: its registration files, which the plane writes
	Derived string // .xbin/deploy/<TK>/d/<d>: its backend log, and D113 sandbox state
}

// deploymentFiles computes deployment dep of tile's files: dep must be a
// deployment name other than main.
func deploymentFiles(tile, dep string) (depFiles, error) {
	if dep == util.MainDeployment || !util.DeploymentNameOK(dep) {
		return depFiles{}, fmt.Errorf("%s: deployment %q keeps no files of its own (main's keep today's keys)", tile, dep)
	}
	tk := util.TileKey(tile)
	return depFiles{
		Vault:   "data/vault/" + deploymentsLevel + "/" + tk + "/" + dep + ".json",
		Prefs:   deploymentsLevel + "/" + tk + "/" + dep + ".json",
		Records: "data/deployments/" + tk + "/" + dep,
		Derived: ".xbin/deploy/" + tk + "/d/" + dep,
	}, nil
}

// registrationFileNames are the registration files a deployment beyond main
// keeps under depFiles.Records (11-contract §10.2), which the plane validates
// again.
var registrationFileNames = []string{"cron.json", "bus-subscriptions.json", "iface-instances.json",
	"ingress-hosts.json", "backup-schedule.json", "sandboxes.json"}

// DropDeploymentFiles deletes deployment dep of tile's own files: its vault,
// every user's prefs for it, its registration files (through the plane,
// under its records lock) and its derived-state directory (08-data §3.3,
// §9.2). Adding a deployment calls it first, so nothing an earlier
// deployment of the same name left applies (P29) and the new vault starts
// with placeholders only (P14); removing one calls it once the deployment
// is stopped. It never touches main, the tile's other deployments, or the
// (scope, name) data namespace, which the scope's siblings may share
// (§6.4). Files already gone are no error.
func (b *Broker) DropDeploymentFiles(tile, dep string) error {
	f, err := deploymentFiles(tile, dep)
	if err != nil {
		return err
	}
	root := b.Reg.Root
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	var errs []error
	remove := func(rel string, prune ...string) {
		if err := os.Remove(at(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, dir := range prune { // parents, innermost first, each only once empty
			if os.Remove(at(dir)) != nil {
				break
			}
		}
	}
	vaultDir := path.Dir(f.Vault)
	remove(f.Vault, vaultDir, path.Dir(vaultDir))
	if homes, err := os.ReadDir(at("data/prefs")); err == nil { // walk-ok: data/ is xbind's own; no sandbox sees it
		for _, h := range homes {
			if h.IsDir() {
				p := "data/prefs/" + h.Name() + "/" + f.Prefs
				remove(p, path.Dir(p), path.Dir(path.Dir(p)))
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, name := range registrationFileNames {
		if err := b.removeDeploymentFile(tile, dep, name); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(at(f.Derived)); err != nil { // unlinks, never follows a link out
		errs = append(errs, err)
	}
	_ = os.Remove(at(path.Dir(f.Derived))) // d/, once no deployment of the tile has state there
	return errors.Join(errs...)
}
