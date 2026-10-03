// projects_seams.go — the registration points and hooks of Projects and CI
// (projects_types.go has the shapes): lists each part appends to from its
// init() (routeTables, adapterRouteTables and schemaAdds are wired; the
// others' call sites land with the part that owns the moment, one line
// each), and hooks one part fills from its init(). Each default does
// nothing, or refuses where nothing would be wrong, so every part compiles
// and runs before the one that fills it is in. A list runs its entries in
// registration order — never rely on the order.
//
// Hosted conversations (a person's partition driving them over team,
// hosted_engine.go) never reach a hook: every call site skips a run whose id
// is hostedID (team_runs.go), and schemaAdds never run on team
// (addFeatureSchemas), so nothing of a project or a CI watch is ever written
// to team.
package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// errNotInBuild: the part that does this isn't in this build.
var errNotInBuild = errors.New("not in this build")

var (
	// routeTables are each part's route tables: routes() mounts them like
	// routeTable() (the same agentRole, hostedRoute, guard, partitionRoute
	// chain), so no part edits routes.go.
	routeTables []func() []routeDef
	// adapterRouteTables are each part's /adapter/* routes: adapterRoutes()
	// (channels.go) mounts them with adapterGuard — the channel role a bound
	// adapter or provider holds, never the admin chain routeTables get (a
	// bound provider isn't admin). The handler still checks which tile is
	// calling (X-XBin-From).
	adapterRouteTables []func() map[string]http.HandlerFunc
	// schemaAdds are each part's tables: openDB runs them after migrate()
	// (addFeatureSchemas, migrate.go) — on the agent's own database only,
	// never on team. Additive and idempotent; none may rely on another's.
	schemaAdds []func(d *DB) error
	// turnEndHooks run inside the transaction that ends any run's turn —
	// top-level or a subagent, built-in or a coding agent (endTurnTx,
	// endHarnessTurnTx) — or stops it (stopRun, a coding agent's cancel in
	// harness_pass.go); each entry filters the runs it cares about
	// (run.ParentID, the origin). why is one word for every site:
	// "answered", "finished", "incomplete", "error", "canceled" or
	// "interrupted" (the sites map their own to these); outcome is the link
	// outcome's words, result its text. Database only; anything slower goes
	// in t.AfterCommit. (Projects: the task's state and events, the refs
	// check; CI: pushed branches.)
	turnEndHooks []func(t *DB, run *Run, why, outcome, result string)
	// runStatusHooks run whenever a run's status is written (DB.setStatus,
	// DB.setStatusOnly), in the same handle — a park waiting for a person
	// (an approval, ask_user, a coding agent's question or sign-in), a
	// cancel, a wake — so no move to or from waiting_input is missed; never
	// where the handle's features flag is off (migrate() rewriting a legacy
	// database's rows before any feature table exists; team). Keep an entry
	// to an indexed lookup; anything more in t.AfterCommit.
	runStatusHooks []func(t *DB, runID int64, status string)
	// runViewHooks add keys to a run's answers (viewWith for GET
	// /runs/{id}/view, handleGetRun for GET /runs/{id}): project,
	// projectTask (Projects), ci (the CI view's summary and canWatch). view
	// is the answer being built; w is who asks (what only they may see, a
	// device code, goes in only for them).
	runViewHooks []func(t *DB, w who, run *Run, view map[string]any)
	// runDeletedHooks run inside deleteOneRun's transaction for every run
	// deleted.
	runDeletedHooks []func(t *DB, runID int64) error
	// projectRefsHooks run when a task's branch is pushed, its head moves or
	// a PR opens or changes (in the transaction that recorded it): scm
	// subscriptions and routing, CI watches.
	projectRefsHooks []func(t *DB, p *Project, k *ProjectTask)
	// projectEventHooks see every project event as it is written (in its
	// transaction; use t.AfterCommit): needs and pushes.
	projectEventHooks []func(t *DB, p *Project, ev *ProjectEvent)
	// taskChangedHooks run when a task's row or its run's state changes
	// (what: "created" | "state" | "ws" | "phase" | "prs" | "ci" | "deleted"):
	// the team board's push.
	taskChangedHooks []func(t *DB, p *Project, k *ProjectTask, what string)
	// scmEventHooks see every scm event the home handles (deduped, in the
	// home that owns it), before projects route it: the CI watches.
	scmEventHooks []func(t *DB, ev *scmEvent)
	// ownerLoops are background loops of the engine owner: started beside
	// the project worker at takeover, their ctx cancelled at BeginShutdown.
	// They never hold the engine or wake it (hasWork doesn't count them): a
	// loop works while the engine runs anyway. (CI: refreshing live watches.)
	ownerLoops []func(ctx context.Context, e *Engine)
)

// projectJobKinds is the project worker's table: each part registers its
// kinds from init(). A job of a kind nobody registered fails "not in this
// build".
var projectJobKinds = map[string]projectJobFunc{}

// The scm client and credentials (K, scm*.go): who may name a repo for the
// bot; write, refresh and scrub a project's token in a sandbox, behind
// scmCredWhy.
var (
	// scmBotAllowed says whether w may name repo for the bot — where the
	// home's identity is the bot (the global instance, an unpartitioned
	// agent): creating a project, adding a repo, making a conversation a
	// project, a CI watch. The agent's managers may; others only as the
	// agent's scm bot rule (scmBotRule, the bot home's setting
	// scm_bot_rule) allows. Not consulted in a person's partition, where
	// the person's own identity decides.
	scmBotAllowed = func(w who, repo string) bool { return w.manager() }
	// scmEnsureCreds makes sure ref holds a live credential for p (and k,
	// when it is a task's own sandbox) with at least minLeft to run. It
	// calls the provider and the sandbox: never on the engine's path (the
	// gate queues a creds job instead).
	scmEnsureCreds = func(ctx context.Context, p *Project, k *ProjectTask, ref string, minLeft time.Duration) error {
		return nil
	}
	// scmScrubCreds empties p's credential files in ref ("" = every sandbox
	// p wrote them to) and revokes what the provider handed out; why is
	// recorded (share, stop, archive, delete, repo-removed, forget, left).
	scmScrubCreds = func(ctx context.Context, p *Project, ref, why string) error { return nil }
	// scmProjectEnv is what a project's execs and coding agents get from
	// the credentials (GH_CONFIG_DIR); P1's projectEnv merges it with the
	// task's own (ports, branch).
	scmProjectEnv = func(p *Project, home string) map[string]string { return nil }
	// scmGitConfig is the git config a base repo (or a clone) of p gets for
	// host — key/value pairs, in order (the credential helper lines); home
	// is the sandbox's home.
	scmGitConfig = func(p *Project, host, home string) [][2]string { return nil }
	// scmRedact masks scm tokens — their shapes and every live one — in
	// text a row, a log or a job's output keeps.
	scmRedact = func(s string) string { return s }
	// scmPendingSignin is the device-flow sign-in under way for user at
	// provider scm, as the last 409 signin from the provider said (nil:
	// none): the workspace gate's park shows it to that person only.
	scmPendingSignin = func(user, scm string) *scmSignin { return nil }
	// scmCredsDue says, from the database alone, whether k's credential (or
	// p's, for no task) is missing or due for a refresh within 10 min: the
	// workspace gate then queues a creds job and parks.
	scmCredsDue = func(t *DB, p *Project, k *ProjectTask) bool { return false }
)

// Projects (P1), for the parts that build on them in parallel.
var (
	// projectsInSandbox lists the active projects whose workspace (or a
	// task's fork) is sandbox ref: whose credentials to scrub.
	projectsInSandbox = func(ref string) []*Project { return nil }
	// projectReposOf is project pid's repos.
	projectReposOf = func(pid int64) []ProjectRepo { return nil }
	// projectLevelOf is user's level on p's ACL (owner, participant,
	// viewer; lvNone: none, or Projects isn't in): the credential gate's
	// "every user of the sandbox may act in the project".
	projectLevelOf = func(p *Project, user string) level { return lvNone }
	// projectRefsCheck queues task n's refs job (its pushed branch and open
	// PRs read again): a PR opened outside the task's turns.
	projectRefsCheck = func(t *DB, pid, n int64) {}
	// projectTaskChanged says task n of project pid changed (what as
	// taskChangedHooks'): it emits the project stream event and runs
	// taskChangedHooks — what a part that changes a task's derived state
	// (its CI) calls.
	projectTaskChanged = func(t *DB, pid, n int64, what string) {}
)

// Big tasks, upgrades and pull requests (P2).
var (
	// provisionFork makes k's own sandbox from p's fork-base snapshot (or a
	// fresh one) and answers its ref; errNotInBuild runs a big task in the
	// project's sandbox instead.
	provisionFork = func(ctx context.Context, p *Project, k *ProjectTask) (string, error) { return "", errNotInBuild }
	// projectRested: k's run came to rest — auto-PR, if the policy says so.
	projectRested = func(t *DB, p *Project, k *ProjectTask) {}
)

// The coordinator (C).
var (
	// projectWakeHook says whether a coordinator run has an undelivered
	// event that should wake it (the idle-wake checks in actor.go call it).
	projectWakeHook = func(d *DB, run *Run) bool { return false }
	// projectDeliverHook is a coordinator's undelivered project events as
	// one message's text, at a step boundary (deliverBoundary, beside
	// deliverNotices); the caller writes the message and then calls mark
	// with its id, which marks the events delivered. "": nothing to deliver.
	projectDeliverHook = func(t *DB, run *Run) (text string, mark func(msgID int64)) { return "", nil }
	// projectCoordPrompt is a coordinator's # Project block.
	projectCoordPrompt = func(p *Project, run *Run) string { return "" }
)

// The CI view (V): a task's CI watches' aggregate (TaskView.ci, the board
// row's ci); nil: none.
var taskCISummary = func(runID int64) *CISummary { return nil }
