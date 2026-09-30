package server

// partitionclass.go — xbind's API is default-deny for a person's partition
// (plans/partitions/02 §8; PD-29), as deployclass.go is for non-primary
// deployments (D127r). /api/xbin/* handlers key on p.Component: a user
// partition's credential reaching a handler nobody converted would act on
// the tile's own — its global instance's — state. So every route pattern
// has a partition class in a second table, and handleAPI asks partitionGate
// right after classGate:
//
//   - a principal of a partitioned tile whose partition (the broker's
//     addressedPartition on its own tile) is a user partition asks the
//     table; the answer's partition is stamped on the principal
//     (auth.Principal.Partition) for the handler;
//   - the global instance, people, the root token and every principal of a
//     tile that isn't partitioned never meet the table (global is today's
//     instance) — except PersonOnly routes, which only a person's own
//     session, app or device credential performs (never a tile principal
//     of any partition, view-as, or the root token);
//   - a guard over the route inventory keeps the table complete
//     (internal/apicheck's TestPartitionRouteClasses).
//
// A PartitionScoped route whose handler isn't converted yet is listed in
// partitionUnconverted and refused to user partitions until the pack that
// converts it removes the row.

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionClass is a /api/xbin/* route's class for a user partition's
// credentials.
type PartitionClass uint8

const (
	// PartitionUnclassified: no row. A user partition's credentials are
	// refused, reads included.
	PartitionUnclassified PartitionClass = iota
	// PartitionScoped: the handler acts on the caller's own partition.
	PartitionScoped
	// GlobalOnlyDormant: a registration of the global instance's; a user
	// partition's is stored dormant and answered with success (PD-21).
	GlobalOnlyDormant
	// GlobalOnlyRefused: refused for user partitions.
	GlobalOnlyRefused
	// PersonOnly: only a person's own session, app or device credential —
	// never a tile principal of any partition (tile code must not perform
	// it).
	PersonOnly
	// PartitionNeutral: no partition dimension; unchanged.
	PartitionNeutral
)

func (c PartitionClass) String() string {
	switch c {
	case PartitionScoped:
		return "partition-scoped"
	case GlobalOnlyDormant:
		return "global-only-dormant"
	case GlobalOnlyRefused:
		return "global-only-refused"
	case PersonOnly:
		return "person-only"
	case PartitionNeutral:
		return "neutral"
	}
	return "unclassified"
}

// PartitionClassOf is the partition class of the route RegisterAPI mounted
// as pattern.
func PartitionClassOf(pattern string) PartitionClass { return partitionClasses[pattern] }

// PartitionClasses returns a copy of the table, pattern → class.
func PartitionClasses() map[string]PartitionClass {
	out := make(map[string]PartitionClass, len(partitionClasses))
	for p, c := range partitionClasses {
		out[p] = c
	}
	return out
}

// PartitionUnconverted returns a copy of partitionUnconverted.
func PartitionUnconverted() map[string]string {
	out := make(map[string]string, len(partitionUnconverted))
	for p, why := range partitionUnconverted {
		out[p] = why
	}
	return out
}

// PartitionPolicy is a Policy that answers which partition of tile a
// request by p reaches (the broker's addressedPartition): "" when tile isn't
// partitioned, "global", "user:<id>", or an error that refuses p. Without
// it no tile is partitioned.
type PartitionPolicy interface {
	AddressedPartition(p auth.Principal, tile string) (util.Partition, error)
}

// addressedPartition asks the installed policy; "" without one.
func (s *Server) addressedPartition(p auth.Principal, tile string) (util.Partition, error) {
	if pp, ok := s.policy().(PartitionPolicy); ok {
		return pp.AddressedPartition(p, tile)
	}
	return "", nil
}

// ownPartition is the partition tile principal p acts in on its own tile:
// an xbin.window sub-path's credential (apps/x/editor) acts as its tile's
// (apps/x), as it binds as its tile's deployment (boundDeployment).
func (s *Server) ownPartition(p auth.Principal) (util.Partition, error) {
	tile := s.credentialTile(p)
	p.Component = tile
	return s.addressedPartition(p, tile)
}

// partitionGate applies the partition table to one /api/xbin request, r2 as
// the API mux sees it: the request to run (its principal carrying its user
// partition when it has one) and, when set, the refusal that answers in the
// handler's place.
func (s *Server) partitionGate(r2 *http.Request) (*http.Request, http.HandlerFunc) {
	p := auth.PrincipalOf(r2)
	if s.apiMux == nil {
		return r2, nil
	}
	_, pattern := s.apiMux.Handler(r2)
	if pattern == "" {
		return r2, nil // no route: the mux answers 404 or 405 and runs nothing
	}
	class := partitionClasses[pattern]
	if class == PersonOnly {
		// a person's own session, app or device credential only: never a
		// tile's, view-as, or one that names no person (the root token)
		if p.Component != "" || p.Impersonator != "" || p.UserID == "" {
			return r2, refusal(http.StatusForbidden, "this is a person's own act: sign in and do it yourself — a tile's credentials can't")
		}
		return r2, nil
	}
	if p.Component == "" {
		return r2, nil // people and the owner: their partition is per call
	}
	part, err := s.ownPartition(p)
	switch {
	case err != nil && class == PartitionNeutral:
		return r2, nil // a workspace fact, whichever partition p can't act in
	case err != nil:
		return r2, refusal(http.StatusForbidden, err.Error())
	case !part.IsUser():
		return r2, nil // global, or no partition: today's instance
	}
	switch class {
	case PartitionScoped, PartitionNeutral:
		if why := partitionUnconverted[pattern]; why != "" && class == PartitionScoped {
			return r2, refusal(http.StatusForbidden, "this route isn't available to a partition's credentials yet ("+why+")")
		}
		if why := partitionPersonKeyed[pattern]; why != "" && p.UserID == "" {
			return r2, refusal(http.StatusForbidden, "this route isn't available to a partition's credentials that name no person yet ("+why+")")
		}
		p.Partition = part
		return r2.WithContext(auth.WithPrincipal(r2.Context(), p)), nil
	case GlobalOnlyDormant:
		// stored for the partition and answered with success, but never
		// routed (PD-21): the handler sees the partition and keeps it apart
		p.Partition = part
		return r2.WithContext(auth.WithPrincipal(r2.Context(), p)), nil
	case GlobalOnlyRefused:
		return r2, refusal(http.StatusForbidden, "this route is the global instance's alone: a person's partition ("+string(part)+") can't use it")
	}
	return r2, refusal(http.StatusForbidden, "this route isn't available to a partition's credentials yet")
}

// partitionUnconverted names the PartitionScoped routes whose handler still
// acts on the tile's own state, and what it lacks: refused to user
// partitions until the pack that converts it removes the row
// (plans/partitions/95).
var partitionUnconverted = map[string]string{}

// partitionPersonKeyed names the PartitionScoped routes whose handler keys
// on the credential's person (p.UserID): a person's frames, terminals and
// agent sessions reach their own bucket, which is their partition's; a user
// partition's instance token names no person and would land in the tile's
// person-less bucket — global's and the owner's frames' — so such a
// credential is refused until the handler keys it by its partition.
var partitionPersonKeyed = map[string]string{
	"GET /prefs":          "per-partition prefs",
	"GET /prefs/{key}":    "per-partition prefs",
	"PUT /prefs/{key}":    "per-partition prefs",
	"DELETE /prefs/{key}": "per-partition prefs",
}

// PartitionPersonKeyed returns a copy of partitionPersonKeyed.
func PartitionPersonKeyed() map[string]string {
	out := make(map[string]string, len(partitionPersonKeyed))
	for p, why := range partitionPersonKeyed {
		out[p] = why
	}
	return out
}

// partitionPlanned names rows whose route another pack of the partitions
// plan mounts (plans/partitions/95), and which: the guard
// (TestPartitionRouteClasses) doesn't ask for them to be mounted, so a merge
// in either order stays green. The integrator drops each entry once its
// route is mounted; the handlers judge the person and refuse every
// credential of the target tile (02 §8's governance acts). Wave 1's
// (POST /partitions/limits, POST /partitions/mode) are mounted.
var partitionPlanned = map[string]string{}

// PartitionPlanned returns a copy of partitionPlanned.
func PartitionPlanned() map[string]string {
	out := make(map[string]string, len(partitionPlanned))
	for p, pack := range partitionPlanned {
		out[p] = pack
	}
	return out
}

// partitionClasses is the table (02 §8). Everything D127r made primary-only
// is the global instance's; a person's own account, devices and sessions,
// reads of workspace facts, the code and the governance acts that judge the
// person are neutral.
var partitionClasses = map[string]PartitionClass{
	// ---- the tile's self-scoped stores: the caller's partition's own ----
	"GET /kv/{rest...}":                PartitionScoped,
	"PUT /kv/{rest...}":                PartitionScoped,
	"DELETE /kv/{rest...}":             PartitionScoped,
	"GET /blob/{rest...}":              PartitionScoped,
	"PUT /blob/{rest...}":              PartitionScoped,
	"DELETE /blob/{rest...}":           PartitionScoped,
	"POST /bus/publish":                PartitionScoped,
	"GET /bus/subscriptions":           PartitionScoped,
	"PUT /bus/subscriptions":           PartitionScoped,
	"DELETE /bus/subscriptions/{name}": PartitionScoped,
	"GET /cron/jobs":                   PartitionScoped,
	"PUT /cron/jobs":                   PartitionScoped,
	"DELETE /cron/jobs/{name}":         PartitionScoped,
	"GET /vault/{rest...}":             PartitionScoped,
	"PUT /vault/{rest...}":             PartitionScoped,
	"DELETE /vault/{rest...}":          PartitionScoped,
	"GET /logs":                        PartitionScoped,
	"GET /tile-status":                 PartitionScoped,
	"GET /tile-report":                 PartitionScoped, // stored per partition; its status event reaches that person only
	"POST /tile-report":                PartitionScoped,
	"GET /frame-token":                 PartitionScoped, // the token names the person; the partition follows
	"POST /path-tickets":               PartitionScoped,
	"POST /notify":                     PartitionScoped,
	"POST /term/sessions":              PartitionScoped,
	"POST /term/sessions/{id}/restart": PartitionScoped,
	"GET /prefs":                       PartitionScoped, // per person: refused to one naming none (partitionPersonKeyed)
	"GET /prefs/{key}":                 PartitionScoped,
	"PUT /prefs/{key}":                 PartitionScoped,
	"DELETE /prefs/{key}":              PartitionScoped,

	// ---- the global instance's registrations: a partition's are dormant ----
	"PUT /iface-instances": GlobalOnlyDormant,
	"PUT /ingress-hosts":   GlobalOnlyDormant,
	"GET /ingress-routes":  GlobalOnlyDormant,

	// ---- tile sandboxes (PD-28): the global instance's alone ----
	"GET /sandboxes":                                 GlobalOnlyRefused,
	"POST /sandboxes":                                GlobalOnlyRefused,
	"POST /sandboxes/copy":                           GlobalOnlyRefused,
	"GET /sandboxes/policy":                          GlobalOnlyRefused,
	"PUT /sandboxes/policy":                          GlobalOnlyRefused,
	"GET /sandboxes/runtime":                         GlobalOnlyRefused,
	"GET /sandboxes/{name}":                          GlobalOnlyRefused,
	"PATCH /sandboxes/{name}":                        GlobalOnlyRefused,
	"DELETE /sandboxes/{name}":                       GlobalOnlyRefused,
	"/sandboxes/{name}/ports/{port}/{path...}":       GlobalOnlyRefused,
	"GET /sandboxes/{name}/execs":                    GlobalOnlyRefused,
	"POST /sandboxes/{name}/execs":                   GlobalOnlyRefused,
	"GET /sandboxes/{name}/execs/{id}":               GlobalOnlyRefused,
	"DELETE /sandboxes/{name}/execs/{id}":            GlobalOnlyRefused,
	"GET /sandboxes/{name}/execs/{id}/output":        GlobalOnlyRefused,
	"GET /sandboxes/{name}/execs/{id}/tty":           GlobalOnlyRefused,
	"POST /sandboxes/{name}/execs/{id}/resize":       GlobalOnlyRefused,
	"POST /sandboxes/{name}/execs/{id}/signal":       GlobalOnlyRefused,
	"POST /sandboxes/{name}/execs/{id}/stdin":        GlobalOnlyRefused,
	"GET /sandboxes/{name}/files/content":            GlobalOnlyRefused,
	"PUT /sandboxes/{name}/files/content":            GlobalOnlyRefused,
	"GET /sandboxes/{name}/files/list":               GlobalOnlyRefused,
	"GET /sandboxes/{name}/files/stat":               GlobalOnlyRefused,
	"POST /sandboxes/{name}/files/mkdir":             GlobalOnlyRefused,
	"POST /sandboxes/{name}/files/move":              GlobalOnlyRefused,
	"POST /sandboxes/{name}/files/remove":            GlobalOnlyRefused,
	"GET /sandboxes/{name}/snapshots":                GlobalOnlyRefused,
	"POST /sandboxes/{name}/snapshots":               GlobalOnlyRefused,
	"DELETE /sandboxes/{name}/snapshots/{sid}":       GlobalOnlyRefused,
	"POST /sandboxes/{name}/snapshots/{sid}/restore": GlobalOnlyRefused,
	"GET /sandboxes/{name}/tar":                      GlobalOnlyRefused,
	"PUT /sandboxes/{name}/tar":                      GlobalOnlyRefused,
	"GET /sandboxes/{name}/tty":                      GlobalOnlyRefused,
	"POST /sandboxes/{name}/rebase":                  GlobalOnlyRefused,
	"POST /sandboxes/{name}/reset":                   GlobalOnlyRefused,
	"POST /sandboxes/{name}/run":                     GlobalOnlyRefused,
	"POST /sandboxes/{name}/start":                   GlobalOnlyRefused,
	"POST /sandboxes/{name}/stop":                    GlobalOnlyRefused,

	// ---- the deployments API (11-contract §1): the data-namespace acts are
	// the global instance's (S12); the rest judge the person and refuse
	// instance and frame principals in their handlers ----
	"POST /deployments/seed":               GlobalOnlyRefused,
	"POST /deployments/reset":              GlobalOnlyRefused,
	"POST /deployments/vault-copy":         GlobalOnlyRefused,
	"POST /deployments/backup":             GlobalOnlyRefused,
	"GET /deployments/backups":             GlobalOnlyRefused,
	"POST /deployments/restore":            GlobalOnlyRefused,
	"POST /deployments/backup-schedule":    GlobalOnlyRefused,
	"POST /deployments/run-now":            GlobalOnlyRefused,
	"GET /deployments":                     PartitionNeutral,
	"GET /deployments/log":                 PartitionNeutral,
	"GET /deployments/diff":                PartitionNeutral,
	"POST /deployments/live-reload/pause":  PartitionNeutral,
	"POST /deployments/live-reload/resume": PartitionNeutral,
	"POST /deployments/live-reload/now":    PartitionNeutral,
	"POST /deployments/live-reload/attach": PartitionNeutral,
	"POST /deployments/add":                PartitionNeutral,
	"POST /deployments/remove":             PartitionNeutral,
	"POST /deployments/deploy":             PartitionNeutral,
	"POST /deployments/promote":            PartitionNeutral,
	"POST /deployments/rollback":           PartitionNeutral,
	"POST /deployments/primary":            PartitionNeutral,
	"POST /deployments/protect":            PartitionNeutral,
	"POST /deployments/edge":               PartitionNeutral,
	"POST /deployments/deliveries":         PartitionNeutral,
	"POST /deployments/always-on":          PartitionNeutral,
	"POST /deployments/limits":             PartitionNeutral,
	"POST /deployments/purge":              PartitionNeutral,
	"POST /deployments/branch":             PartitionNeutral,
	"GET /checkpoints/{rest...}":           PartitionNeutral,

	// ---- code: one tree for every partition ----
	"GET /code/tree":                  PartitionNeutral,
	"GET /code/file":                  PartitionNeutral,
	"GET /git/log":                    PartitionNeutral,
	"GET /git/diff":                   PartitionNeutral,
	"GET /git/activity":               PartitionNeutral,
	"GET /git/remote-info":            PartitionNeutral,
	"POST /code/prs":                  PartitionNeutral,
	"GET /code/prs":                   PartitionNeutral,
	"GET /code/prs/summary":           PartitionNeutral,
	"GET /code/pr":                    PartitionNeutral,
	"GET /code/pr/series":             PartitionNeutral,
	"POST /code/pr/comment":           PartitionNeutral,
	"POST /code/pr/state":             GlobalOnlyRefused,
	"GET /templates":                  PartitionNeutral,
	"GET /templates/updates":          PartitionNeutral,
	"GET /templates/{repo}/{rest...}": PartitionNeutral,
	"GET /builtins":                   PartitionNeutral,
	"GET /builtins/updates":           PartitionNeutral,

	// ---- tile creation and a tile's life ----
	"POST /create":          GlobalOnlyRefused,
	"POST /clone":           GlobalOnlyRefused,
	"POST /git/import":      GlobalOnlyRefused,
	"POST /templates/new":   GlobalOnlyRefused,
	"POST /builtins/import": GlobalOnlyRefused,
	"POST /builtins/update": GlobalOnlyRefused,
	"POST /lifecycle":       GlobalOnlyRefused,

	// ---- grants, bindings, ownership and access ----
	"GET /grants":                   PartitionNeutral,
	"POST /grants":                  GlobalOnlyRefused,
	"DELETE /grants":                GlobalOnlyRefused,
	"GET /bindings":                 PartitionNeutral,
	"POST /bindings":                GlobalOnlyRefused,
	"DELETE /bindings":              GlobalOnlyRefused,
	"GET /partitions/binds":         PartitionNeutral,  // personal binds (05 §3): the person's own, or all for admins
	"POST /partitions/binds":        PersonOnly,        // a person wires their own tile into their own partition
	"DELETE /partitions/binds":      GlobalOnlyRefused, // the person, or an admin (the admin tile included)
	"GET /owner":                    PartitionNeutral,
	"GET /owner/preview":            GlobalOnlyRefused,
	"POST /owner":                   GlobalOnlyRefused,
	"GET /access":                   GlobalOnlyRefused,
	"GET /access/{user}":            PartitionNeutral,
	"PUT /access":                   GlobalOnlyRefused,
	"GET /access-matrix":            GlobalOnlyRefused,
	"GET /access-requests":          PartitionNeutral,
	"POST /access-requests":         PartitionNeutral,
	"POST /access-requests/approve": GlobalOnlyRefused,
	"DELETE /access-requests":       PartitionNeutral,

	// ---- users, orgs, sets, policy ----
	"GET /users":                        GlobalOnlyRefused,
	"POST /users":                       GlobalOnlyRefused,
	"PATCH /users/{id}":                 GlobalOnlyRefused,
	"POST /users/{id}/invite":           GlobalOnlyRefused,
	"DELETE /users/{id}":                GlobalOnlyRefused,
	"DELETE /users/{id}/sessions":       GlobalOnlyRefused,
	"GET /users/{id}/devices":           GlobalOnlyRefused,
	"GET /sessions":                     GlobalOnlyRefused,
	"GET /users-directory":              GlobalOnlyRefused,
	"GET /auth-settings":                GlobalOnlyRefused,
	"PATCH /auth-settings":              GlobalOnlyRefused,
	"POST /auth-settings/sso/test":      GlobalOnlyRefused,
	"GET /orgs":                         GlobalOnlyRefused,
	"POST /orgs":                        GlobalOnlyRefused,
	"PATCH /orgs/{org}":                 GlobalOnlyRefused,
	"DELETE /orgs/{org}":                GlobalOnlyRefused,
	"PUT /orgs/{org}/members/{user}":    GlobalOnlyRefused,
	"DELETE /orgs/{org}/members/{user}": GlobalOnlyRefused,
	"PUT /orgs/{org}/sso-groups":        GlobalOnlyRefused,
	"GET /orgs/{org}/policy":            GlobalOnlyRefused,
	"PUT /orgs/{org}/policy":            GlobalOnlyRefused,
	"GET /policy":                       GlobalOnlyRefused,
	"PUT /policy":                       GlobalOnlyRefused,
	"GET /defaults":                     GlobalOnlyRefused,
	"PUT /defaults":                     GlobalOnlyRefused,
	"GET /net-sets":                     GlobalOnlyRefused,
	"PUT /net-sets/{name}":              GlobalOnlyRefused,
	"DELETE /net-sets/{name}":           GlobalOnlyRefused,
	"GET /permission-sets":              GlobalOnlyRefused,
	"PUT /permission-sets/{name}":       GlobalOnlyRefused,
	"DELETE /permission-sets/{name}":    GlobalOnlyRefused,
	"GET /screens":                      PartitionNeutral,
	"PUT /screens/default":              GlobalOnlyRefused,
	"PUT /screens/org":                  GlobalOnlyRefused,
	"DELETE /screens/org":               GlobalOnlyRefused,
	"PUT /screens/folders":              GlobalOnlyRefused,

	// ---- admin APIs: the workspace's operator. The governance acts that
	// judge the person (backup keys, workspace policies) are neutral: their
	// handlers ask Broker.IsAdmin, and no partitioned tile holds xbin:* ----
	"POST /backup":                           GlobalOnlyRefused,
	"GET /backups":                           GlobalOnlyRefused,
	"POST /restore":                          GlobalOnlyRefused,
	"GET /backup-schedule":                   GlobalOnlyRefused,
	"POST /backup-schedule":                  GlobalOnlyRefused,
	"DELETE /backup-schedule":                GlobalOnlyRefused,
	"GET /backup-keys":                       PartitionNeutral,
	"POST /backup-keys/export":               PartitionNeutral,
	"POST /backup-keys/import":               PartitionNeutral,
	"POST /backup/erase":                     GlobalOnlyRefused,
	"GET /vault-status":                      GlobalOnlyRefused,
	"POST /vault-unseal":                     GlobalOnlyRefused,
	"POST /vault-seal":                       GlobalOnlyRefused,
	"POST /vault-rekey":                      GlobalOnlyRefused,
	"GET /vaults":                            GlobalOnlyRefused,
	"GET /resources":                         GlobalOnlyRefused,
	"GET /auth-overview":                     GlobalOnlyRefused,
	"GET /backends":                          GlobalOnlyRefused,
	"GET /runtime":                           GlobalOnlyRefused,
	"GET /ingress":                           GlobalOnlyRefused,
	"GET /gpus":                              GlobalOnlyRefused,
	"PUT /vm/policy":                         GlobalOnlyRefused,
	"POST /auth-rotate-token":                GlobalOnlyRefused,
	"POST /impersonate":                      GlobalOnlyRefused,
	"POST /impersonate/stop":                 GlobalOnlyRefused,
	"PUT /native-runtime":                    GlobalOnlyRefused,
	"PUT /chrome":                            GlobalOnlyRefused,
	"PUT /branding":                          GlobalOnlyRefused,
	"PUT /workspace-policies":                PartitionNeutral,
	"GET /push/config":                       GlobalOnlyRefused,
	"PUT /push/config":                       GlobalOnlyRefused,
	"DELETE /push/config":                    GlobalOnlyRefused,
	"GET /push/devices":                      GlobalOnlyRefused,
	"DELETE /push/devices/{user}":            GlobalOnlyRefused,
	"DELETE /push/devices/{user}/{deviceId}": GlobalOnlyRefused,

	// ---- partitions' governance acts (02 §8): their handlers judge the
	// person and refuse the target tile's own credentials. A mode decision
	// is a tile manager's (or the admin tile's frame driven by one), never
	// a person's partition's (01 §2.5, PD-49) ----
	"POST /partitions/limits": PartitionNeutral,
	"POST /partitions/mode":   GlobalOnlyRefused,
	// the operations (06 §6): a listing whose handler gives tile code only
	// the tile-level fields; stop, reset and purge judge the person
	"GET /partitions":        PartitionNeutral,
	"POST /partitions/stop":  GlobalOnlyRefused,
	"POST /partitions/reset": GlobalOnlyRefused,
	"POST /partitions/purge": GlobalOnlyRefused,
	// the admins' reviewed-code-only switch (06 §4): a person's act
	"POST /partitions/reviewed": GlobalOnlyRefused,
	// a person's own acts: sharing their partition's log, deciding a
	// credential an admin made for them (06 §5, §9)
	"POST /partitions/share-log":          PersonOnly,
	"DELETE /partitions/share-log":        PersonOnly,
	"POST /partitions/credential-confirm": PersonOnly,

	// ---- cross-tile partition edges (05 §2, 06 §6.1): a person's consents
	// and egress ledger are their own acts and reads — never tile code's —
	// and the edges between partitioned tiles are the admins' ----
	"GET /partitions/consents":    PersonOnly,
	"POST /partitions/consents":   PersonOnly,
	"DELETE /partitions/consents": PersonOnly,
	"GET /partitions/ledger":      PersonOnly,
	"GET /partitions/edges":       GlobalOnlyRefused,

	// ---- partition mail (04 §3): the caller's own inbox — its person's
	// partition's, or the global instance's; the handlers refuse everyone
	// else, and a partition mails "global" only ----
	"POST /partitions/mail":     PartitionScoped,
	"GET /partitions/mail":      PartitionScoped,
	"POST /partitions/mail/ack": PartitionScoped,

	// ---- reads of workspace facts ----
	"GET /whoami":               PartitionNeutral,
	"GET /status":               PartitionNeutral,
	"GET /components":           PartitionNeutral,
	"GET /components/{path...}": PartitionNeutral,
	"GET /tile-assets":          PartitionNeutral,
	"GET /openapi.json":         PartitionNeutral,
	"GET /alerts":               PartitionNeutral,
	"GET /vm":                   PartitionNeutral,
	"GET /term-net":             PartitionNeutral,
	"GET /native-runtime":       PartitionNeutral,
	"GET /chrome":               PartitionNeutral,
	"GET /branding":             PartitionNeutral,
	"GET /workspace-policies":   PartitionNeutral,

	// ---- a person's own sign-in, account, devices and sessions ----
	"POST /login":                     PartitionNeutral,
	"GET /login/methods":              PartitionNeutral,
	"POST /invite/check":              PartitionNeutral,
	"POST /invite/redeem":             PartitionNeutral,
	"POST /devices/enroll":            PartitionNeutral,
	"POST /devices/enroll-code":       PartitionNeutral,
	"POST /web-ticket":                PartitionNeutral,
	"POST /account/password":          PartitionNeutral,
	"GET /devices":                    PartitionNeutral,
	"DELETE /devices/{id}":            PartitionNeutral,
	"POST /devices/push":              PartitionNeutral,
	"GET /devices/push":               PartitionNeutral,
	"DELETE /devices/push/{deviceId}": PartitionNeutral,
	"POST /devices/push/activities":   PartitionNeutral,
	"DELETE /devices/push/{deviceId}/activities/{session}": PartitionNeutral,
	"GET /push/prefs":                             PartitionNeutral,
	"PUT /push/prefs":                             PartitionNeutral,
	"POST /push/test":                             PartitionNeutral,
	"GET /term/sessions":                          PartitionNeutral,
	"PATCH /term/sessions/{id}":                   PartitionNeutral,
	"GET /term/sessions/{id}":                     PartitionNeutral,
	"DELETE /term/sessions/{id}":                  PartitionNeutral,
	"POST /term/sessions/{id}/prompt":             PartitionScoped, // 02 §8: every POST /term/sessions*
	"POST /term/sessions/{id}/cancel":             PartitionScoped,
	"POST /term/sessions/{id}/permissions/{pid}":  PartitionScoped,
	"POST /term/sessions/{id}/elicitations/{eid}": PartitionScoped,
	"POST /term/sessions/{id}/options":            PartitionScoped,
	"GET /term/sessions/{id}/events":              PartitionNeutral,
	"GET /term/sessions/{id}/log":                 PartitionNeutral,
	"GET /term/sessions/{id}/diff":                PartitionNeutral,
	"GET /agent/providers":                        PartitionNeutral,
	"GET /agent/history":                          PartitionNeutral,
	"GET /agent/history/{id}/events":              PartitionNeutral,
	"DELETE /agent/history/{id}":                  PartitionNeutral,
}
