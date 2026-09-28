package server

// deployclass.go — xbind's API is default-deny for non-primary principals
// (P26; 09-fabric §6's enforcement). Every /api/xbin/* route pattern has a
// class in one table:
//
//   - deployment-scoped: the handler acts on the caller's own deployment
//     (dormant, namespaced or per-deployment), or applies the deployment gates
//     11-contract gives it;
//   - primary-only: a non-primary principal is refused, and the call has no
//     effect for it;
//   - neutral: the route has no deployment dimension, and behaves as today.
//
// handleAPI asks classGate before the API mux runs a handler. A principal
// bound to a deployment that isn't its tile's primary is refused on a
// primary-only route and on a route without a class, reads included: a
// handler nobody converted keys on p.Component and would otherwise act on
// the primary's state. People, the owner token and the cron and bus
// deliveries are bound to nothing and never meet the check, and neither
// does any credential of a tile without a deployment record, whose every
// principal is its primary's (12-compat PO-5). A guard over the route
// inventory keeps the table complete (internal/apicheck's
// TestDeploymentRouteClasses).

import (
	"errors"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// RouteClass is a /api/xbin/* route's class for non-primary principals (P26).
type RouteClass uint8

const (
	// Unclassified: no row. Non-primary principals are refused, reads included.
	Unclassified RouteClass = iota
	// DeploymentScoped: the handler acts on the caller's deployment.
	DeploymentScoped
	// PrimaryOnly: refused for non-primary principals.
	PrimaryOnly
	// Neutral: no deployment dimension; unchanged.
	Neutral
)

func (c RouteClass) String() string {
	switch c {
	case DeploymentScoped:
		return "deployment-scoped"
	case PrimaryOnly:
		return "primary-only"
	case Neutral:
		return "neutral"
	}
	return "unclassified"
}

// RouteClassOf is the class of the route RegisterAPI mounted as pattern.
func RouteClassOf(pattern string) RouteClass { return routeClasses[pattern] }

// RouteClasses returns a copy of the table, pattern → class.
func RouteClasses() map[string]RouteClass {
	out := make(map[string]RouteClass, len(routeClasses))
	for p, c := range routeClasses {
		out[p] = c
	}
	return out
}

// primaryOnlyFor names the neutral routes that are primary-only for one kind
// of principal (Via): deciding a PR is, for instance principals (P26), so a
// non-primary backend never merges into the work tree.
var primaryOnlyFor = map[string]string{
	"POST /code/pr/state": "instance",
}

// routeClasses is the table (09-fabric §6). Governance (users, orgs, sets,
// policy, grants, bindings, ownership, tile creation, lifecycle) and every
// admin API are primary-only, reads included: a non-primary principal never
// holds a governance capability (P19). A person's own account, devices and
// sessions, the public sign-in routes and plain reads of workspace facts are
// neutral.
var routeClasses = map[string]RouteClass{
	// ---- the tile's self-scoped stores (09-fabric §6's rows) ----
	// dormant: stored under the deployment, in effect only while it is the
	// primary, or, for cron and bus, while its deliveries are on (P13)
	"GET /cron/jobs":                   DeploymentScoped,
	"PUT /cron/jobs":                   DeploymentScoped,
	"DELETE /cron/jobs/{name}":         DeploymentScoped,
	"GET /bus/subscriptions":           DeploymentScoped,
	"PUT /bus/subscriptions":           DeploymentScoped,
	"DELETE /bus/subscriptions/{name}": DeploymentScoped,
	"PUT /iface-instances":             DeploymentScoped,
	"PUT /ingress-hosts":               DeploymentScoped,
	"GET /ingress-routes":              DeploymentScoped,
	"POST /notify":                     DeploymentScoped,
	// namespaced: the deployment's own (scope, name) data (08-data.md)
	"POST /bus/publish":      DeploymentScoped,
	"GET /kv/{rest...}":      DeploymentScoped,
	"PUT /kv/{rest...}":      DeploymentScoped,
	"DELETE /kv/{rest...}":   DeploymentScoped,
	"GET /blob/{rest...}":    DeploymentScoped,
	"PUT /blob/{rest...}":    DeploymentScoped,
	"DELETE /blob/{rest...}": DeploymentScoped,
	// per-deployment: a tile-keyed store that gains the deployment key
	"GET /vault/{rest...}":    DeploymentScoped,
	"PUT /vault/{rest...}":    DeploymentScoped,
	"DELETE /vault/{rest...}": DeploymentScoped,
	"GET /tile-report":        DeploymentScoped,
	"POST /tile-report":       DeploymentScoped,
	"GET /logs":               DeploymentScoped,
	"GET /tile-status":        DeploymentScoped,
	"GET /prefs":              DeploymentScoped,
	"GET /prefs/{key}":        DeploymentScoped,
	"PUT /prefs/{key}":        DeploymentScoped,
	"DELETE /prefs/{key}":     DeploymentScoped,
	"GET /frame-token":        DeploymentScoped,
	"GET /sandboxes":          DeploymentScoped,
	// a session's target (11-contract §7.4): the handler applies its gates
	"POST /term/sessions":              DeploymentScoped,
	"POST /term/sessions/{id}/restart": DeploymentScoped,

	// ---- the tile-sandbox runtime (D120): a manager's routes are keyed by
	// the caller's deployment (tilesbx keyOf): main's sandboxes, and 501 for
	// any other deployment until its own set exists (plans/
	// tile-sandbox-runtime.md §14). The policy is the admin's. ----
	"DELETE /sandboxes/{name}":                       DeploymentScoped,
	"DELETE /sandboxes/{name}/execs/{id}":            DeploymentScoped,
	"DELETE /sandboxes/{name}/snapshots/{sid}":       DeploymentScoped,
	"GET /sandboxes/{name}":                          DeploymentScoped,
	"GET /sandboxes/{name}/execs":                    DeploymentScoped,
	"GET /sandboxes/{name}/execs/{id}":               DeploymentScoped,
	"GET /sandboxes/{name}/execs/{id}/output":        DeploymentScoped,
	"GET /sandboxes/{name}/execs/{id}/tty":           DeploymentScoped,
	"GET /sandboxes/{name}/files/content":            DeploymentScoped,
	"GET /sandboxes/{name}/files/list":               DeploymentScoped,
	"GET /sandboxes/{name}/files/stat":               DeploymentScoped,
	"GET /sandboxes/{name}/snapshots":                DeploymentScoped,
	"GET /sandboxes/{name}/tar":                      DeploymentScoped,
	"GET /sandboxes/{name}/tty":                      DeploymentScoped,
	"GET /sandboxes/policy":                          PrimaryOnly,
	"GET /sandboxes/runtime":                         DeploymentScoped,
	"PATCH /sandboxes/{name}":                        DeploymentScoped,
	"POST /sandboxes":                                DeploymentScoped,
	"POST /sandboxes/copy":                           DeploymentScoped,
	"POST /sandboxes/{name}/execs":                   DeploymentScoped,
	"POST /sandboxes/{name}/execs/{id}/resize":       DeploymentScoped,
	"POST /sandboxes/{name}/execs/{id}/signal":       DeploymentScoped,
	"POST /sandboxes/{name}/execs/{id}/stdin":        DeploymentScoped,
	"POST /sandboxes/{name}/files/mkdir":             DeploymentScoped,
	"POST /sandboxes/{name}/files/move":              DeploymentScoped,
	"POST /sandboxes/{name}/files/remove":            DeploymentScoped,
	"POST /sandboxes/{name}/rebase":                  DeploymentScoped,
	"POST /sandboxes/{name}/reset":                   DeploymentScoped,
	"POST /sandboxes/{name}/run":                     DeploymentScoped,
	"POST /sandboxes/{name}/snapshots":               DeploymentScoped,
	"POST /sandboxes/{name}/snapshots/{sid}/restore": DeploymentScoped,
	"POST /sandboxes/{name}/start":                   DeploymentScoped,
	"POST /sandboxes/{name}/stop":                    DeploymentScoped,
	"PUT /sandboxes/{name}/files/content":            DeploymentScoped,
	"PUT /sandboxes/{name}/tar":                      DeploymentScoped,
	"PUT /sandboxes/policy":                          PrimaryOnly,

	// ---- the deployments API (11-contract §1): its handlers apply §0.5's
	// gates, refusing instance and frame principals of every deployment ----
	"GET /deployments":                     DeploymentScoped,
	"GET /deployments/log":                 DeploymentScoped,
	"GET /deployments/diff":                DeploymentScoped,
	"POST /deployments/live-reload/pause":  DeploymentScoped,
	"POST /deployments/live-reload/resume": DeploymentScoped,
	"POST /deployments/live-reload/now":    DeploymentScoped,
	"POST /deployments/live-reload/attach": DeploymentScoped,
	"POST /deployments/add":                DeploymentScoped,
	"POST /deployments/remove":             DeploymentScoped,
	"POST /deployments/deploy":             DeploymentScoped,
	"POST /deployments/promote":            DeploymentScoped,
	"POST /deployments/rollback":           DeploymentScoped,
	"POST /deployments/primary":            DeploymentScoped,
	"POST /deployments/protect":            DeploymentScoped,
	"POST /deployments/edge":               DeploymentScoped,
	"POST /deployments/deliveries":         DeploymentScoped,
	"POST /deployments/always-on":          DeploymentScoped,
	"POST /deployments/limits":             DeploymentScoped,
	"POST /deployments/seed":               DeploymentScoped,
	"POST /deployments/reset":              DeploymentScoped,
	"POST /deployments/vault-copy":         DeploymentScoped,
	"POST /deployments/backup":             DeploymentScoped,
	"GET /deployments/backups":             DeploymentScoped,
	"POST /deployments/restore":            DeploymentScoped,
	"POST /deployments/backup-schedule":    DeploymentScoped,
	"POST /deployments/run-now":            DeploymentScoped,
	"POST /deployments/purge":              DeploymentScoped, // a tile manager's act, judged by the plane
	"GET /checkpoints/{rest...}":           Neutral,          // the fetch remote: refused for instance and frame principals by its handler

	// ---- code: the work tree has no deployment dimension; another tile's
	// code is reached through the edge policy ----
	"GET /code/tree":                  Neutral,
	"GET /code/file":                  Neutral,
	"GET /git/log":                    Neutral,
	"GET /git/diff":                   Neutral,
	"GET /git/activity":               Neutral,
	"GET /git/remote-info":            Neutral,
	"POST /code/prs":                  Neutral,
	"GET /code/prs":                   Neutral,
	"GET /code/prs/summary":           Neutral,
	"GET /code/pr":                    Neutral,
	"GET /code/pr/series":             Neutral,
	"POST /code/pr/comment":           Neutral,
	"POST /code/pr/state":             Neutral, // primary-only for instance principals (primaryOnlyFor)
	"GET /templates":                  Neutral,
	"GET /templates/updates":          Neutral,
	"GET /templates/{repo}/{rest...}": Neutral,
	"GET /builtins":                   Neutral,
	"GET /builtins/updates":           Neutral,

	// ---- tile creation and a tile's life ----
	"POST /create":          PrimaryOnly,
	"POST /clone":           PrimaryOnly,
	"POST /git/import":      PrimaryOnly,
	"POST /templates/new":   PrimaryOnly,
	"POST /builtins/import": PrimaryOnly,
	"POST /builtins/update": PrimaryOnly,
	"POST /lifecycle":       PrimaryOnly,

	// ---- grants, bindings, ownership and access ----
	"GET /grants":                   Neutral,
	"POST /grants":                  PrimaryOnly,
	"DELETE /grants":                PrimaryOnly,
	"GET /bindings":                 Neutral,
	"POST /bindings":                PrimaryOnly,
	"DELETE /bindings":              PrimaryOnly,
	"GET /owner":                    Neutral,
	"GET /owner/preview":            PrimaryOnly,
	"POST /owner":                   PrimaryOnly,
	"GET /access":                   PrimaryOnly,
	"GET /access/{user}":            Neutral, // a person's level on the calling tile: the tile's, as X-XBin-User-Level
	"PUT /access":                   PrimaryOnly,
	"GET /access-matrix":            PrimaryOnly,
	"GET /access-requests":          Neutral,
	"POST /access-requests":         Neutral,
	"POST /access-requests/approve": PrimaryOnly,
	"DELETE /access-requests":       Neutral,

	// ---- users, orgs, sets, policy ----
	"GET /users":                        PrimaryOnly,
	"POST /users":                       PrimaryOnly,
	"PATCH /users/{id}":                 PrimaryOnly,
	"POST /users/{id}/invite":           PrimaryOnly,
	"DELETE /users/{id}":                PrimaryOnly,
	"DELETE /users/{id}/sessions":       PrimaryOnly,
	"GET /users/{id}/devices":           PrimaryOnly,
	"GET /sessions":                     PrimaryOnly,
	"GET /users-directory":              PrimaryOnly,
	"GET /auth-settings":                PrimaryOnly,
	"PATCH /auth-settings":              PrimaryOnly,
	"POST /auth-settings/sso/test":      PrimaryOnly,
	"GET /orgs":                         PrimaryOnly,
	"POST /orgs":                        PrimaryOnly,
	"PATCH /orgs/{org}":                 PrimaryOnly,
	"DELETE /orgs/{org}":                PrimaryOnly,
	"PUT /orgs/{org}/members/{user}":    PrimaryOnly,
	"DELETE /orgs/{org}/members/{user}": PrimaryOnly,
	"PUT /orgs/{org}/sso-groups":        PrimaryOnly,
	"GET /orgs/{org}/policy":            PrimaryOnly,
	"PUT /orgs/{org}/policy":            PrimaryOnly,
	"GET /policy":                       PrimaryOnly,
	"PUT /policy":                       PrimaryOnly,
	"GET /defaults":                     PrimaryOnly,
	"PUT /defaults":                     PrimaryOnly,
	"GET /net-sets":                     PrimaryOnly,
	"PUT /net-sets/{name}":              PrimaryOnly,
	"DELETE /net-sets/{name}":           PrimaryOnly,
	"GET /permission-sets":              PrimaryOnly,
	"PUT /permission-sets/{name}":       PrimaryOnly,
	"DELETE /permission-sets/{name}":    PrimaryOnly,
	"GET /screens":                      Neutral,
	"PUT /screens/default":              PrimaryOnly,
	"PUT /screens/org":                  PrimaryOnly,
	"DELETE /screens/org":               PrimaryOnly,
	"PUT /screens/folders":              PrimaryOnly,

	// ---- admin APIs: the workspace's operator ----
	"POST /backup":                           PrimaryOnly,
	"GET /backups":                           PrimaryOnly,
	"POST /restore":                          PrimaryOnly,
	"GET /backup-schedule":                   PrimaryOnly,
	"POST /backup-schedule":                  PrimaryOnly,
	"DELETE /backup-schedule":                PrimaryOnly,
	"GET /vault-status":                      PrimaryOnly,
	"POST /vault-unseal":                     PrimaryOnly,
	"POST /vault-seal":                       PrimaryOnly,
	"POST /vault-rekey":                      PrimaryOnly,
	"GET /vaults":                            PrimaryOnly,
	"GET /resources":                         PrimaryOnly,
	"GET /auth-overview":                     PrimaryOnly,
	"GET /backends":                          PrimaryOnly,
	"GET /runtime":                           PrimaryOnly,
	"GET /ingress":                           PrimaryOnly,
	"GET /gpus":                              PrimaryOnly,
	"PUT /vm/policy":                         PrimaryOnly,
	"POST /auth-rotate-token":                PrimaryOnly,
	"POST /impersonate":                      PrimaryOnly,
	"POST /impersonate/stop":                 PrimaryOnly,
	"PUT /native-runtime":                    PrimaryOnly,
	"PUT /chrome":                            PrimaryOnly,
	"PUT /branding":                          PrimaryOnly,
	"GET /push/config":                       PrimaryOnly,
	"PUT /push/config":                       PrimaryOnly,
	"DELETE /push/config":                    PrimaryOnly,
	"GET /push/devices":                      PrimaryOnly,
	"DELETE /push/devices/{user}":            PrimaryOnly,
	"DELETE /push/devices/{user}/{deviceId}": PrimaryOnly,

	// ---- reads of workspace facts ----
	"GET /whoami":               Neutral, // gains deployment for a bound element principal (11-contract §7.7)
	"GET /status":               Neutral,
	"GET /components":           Neutral, // the primary summary only (11-contract §8)
	"GET /components/{path...}": Neutral,
	"GET /tile-assets":          Neutral,
	"GET /openapi.json":         Neutral,
	"GET /alerts":               Neutral,
	"GET /vm":                   Neutral,
	"GET /term-net":             Neutral,
	"GET /native-runtime":       Neutral,
	"GET /chrome":               Neutral,
	"GET /branding":             Neutral,

	// ---- a person's own sign-in, account, devices and sessions ----
	"POST /login":                     Neutral,
	"GET /login/methods":              Neutral,
	"POST /invite/check":              Neutral,
	"POST /invite/redeem":             Neutral,
	"POST /devices/enroll":            Neutral,
	"POST /devices/enroll-code":       Neutral,
	"POST /web-ticket":                Neutral,
	"POST /account/password":          Neutral,
	"GET /devices":                    Neutral,
	"DELETE /devices/{id}":            Neutral,
	"POST /devices/push":              Neutral,
	"GET /devices/push":               Neutral,
	"DELETE /devices/push/{deviceId}": Neutral,
	"POST /devices/push/activities":   Neutral,
	"DELETE /devices/push/{deviceId}/activities/{session}": Neutral,
	"GET /push/prefs":                             Neutral,
	"PUT /push/prefs":                             Neutral,
	"POST /push/test":                             Neutral,
	"GET /term/sessions":                          Neutral,
	"PATCH /term/sessions/{id}":                   Neutral,
	"GET /term/sessions/{id}":                     Neutral,
	"DELETE /term/sessions/{id}":                  Neutral,
	"POST /term/sessions/{id}/prompt":             Neutral,
	"POST /term/sessions/{id}/cancel":             Neutral,
	"POST /term/sessions/{id}/permissions/{pid}":  Neutral,
	"POST /term/sessions/{id}/elicitations/{eid}": Neutral,
	"POST /term/sessions/{id}/options":            Neutral,
	"GET /term/sessions/{id}/events":              Neutral,
	"GET /term/sessions/{id}/log":                 Neutral,
	"GET /term/sessions/{id}/diff":                Neutral,
	"GET /agent/providers":                        Neutral,
	"GET /agent/history":                          Neutral,
	"GET /agent/history/{id}/events":              Neutral,
	"DELETE /agent/history/{id}":                  Neutral,
}

// classGate applies P26 to one /api/xbin request, r2 as the API mux sees it.
// dep names the caller's bound deployment when that isn't main, for the
// audit line, and "" otherwise; deny, when set, answers in the handler's
// place. Only a tile principal (an instance, frame or terminal credential)
// bound elsewhere than its tile's primary, or bound to nothing (a removed
// deployment, a session following a protected primary; 11-contract §7.1),
// asks the table.
func (s *Server) classGate(r2 *http.Request) (dep string, deny http.HandlerFunc) {
	p := auth.PrincipalOf(r2)
	if p.Component == "" || (p.Via != "instance" && p.Via != "frame" && p.Via != "terminal") || s.apiMux == nil {
		return "", nil
	}
	tile := s.credentialTile(p) // an xbin.window sub-path binds as its tile
	bound, err := s.boundDeployment(p, tile)
	if err == nil && bound != util.MainDeployment {
		dep = bound
	}
	if err == nil && bound == s.primaryOf(tile) {
		return dep, nil
	}
	_, pattern := s.apiMux.Handler(r2)
	if pattern == "" {
		return dep, nil // no route: the mux answers 404 or 405 and runs nothing
	}
	class := routeClasses[pattern]
	switch {
	case class == DeploymentScoped, class == Neutral && primaryOnlyFor[pattern] != p.Via:
		return dep, nil // the handler decides, on the caller's deployment
	case err != nil && errors.Is(err, util.ErrNoDeployment):
		return dep, refusal(http.StatusNotFound, err.Error())
	case err != nil:
		return dep, refusal(http.StatusForbidden, err.Error())
	case class == Unclassified:
		return dep, refusal(http.StatusForbidden,
			"this route isn't available to a non-primary deployment's credentials yet ("+bound+")")
	case class == Neutral:
		return dep, refusal(http.StatusForbidden,
			"deciding a PR is the primary's act: a non-primary deployment's backend can't do it ("+bound+")")
	}
	return dep, refusal(http.StatusForbidden,
		"this route is the primary's alone: a non-primary deployment's credentials can't use it ("+bound+")")
}

// refusal answers a class refusal with the API's error shape: /docs/auth.md
// for a 403, /docs/protocol.md otherwise (11-contract §1.14).
func refusal(status int, msg string) http.HandlerFunc {
	docs := "/docs/protocol.md"
	if status == http.StatusForbidden {
		docs = "/docs/auth.md"
	}
	return func(w http.ResponseWriter, _ *http.Request) { WriteError(w, status, msg, docs) }
}
