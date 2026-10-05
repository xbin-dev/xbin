// projects_types.go — the shapes of Projects (API.md §Projects): a project
// holds a coding sandbox, its repos, a policy, a coordinator and task
// conversations, each task with a git worktree per repo; and CI, as the
// conversation's scm provider reports it, for what a task or a coding
// session pushed. These are the frozen seams the parts of the feature build
// against — the store, the worker and the routes (project_*.go), the scm
// credentials (scm*.go), the coordinator (projects_coord_*.go), scm events
// and polling (scm_*.go), big tasks and upgrades, team projects
// (project_team*.go) and the CI view (ci_*.go).
//
// Types and constants only; the registration points and hooks one part
// calls and another fills are projects_seams.go: each defaults to doing
// nothing, so every part compiles and runs before the one that fills it is
// in.
//
// Stored data is additive (docs/compat.md): a task is a run with origin
// "project" and origin_id its project's id, and Config.Project says which
// task or coordinator it is. An older binary keeps the tables and the
// origin (its chat list leaves such runs out, conversations.go
// chatOrigins) but may drop Config.Project when it rewrites a run's config;
// project_tasks.run_id and the coordinator's session key are what is
// authoritative, and Config.Project is derived again from them when it is
// missing.
package main

import (
	"context"
	"encoding/json"
	"strconv"
)

// originProject is runs.origin for a project's runs: its tasks and its
// coordinators (runs.origin_id is the project's id). Such runs are not
// chats: they stay out of the conversation list and never move between
// homes.
const originProject = "project"

// ProjectRef is Config.Project: which project a run belongs to, and as
// what. Set when the run is made and never changed; a subagent inherits it
// (childConfig copies the pointer — treat it as read-only). PUT /config
// strips it: the global defaults never hold one.
type ProjectRef struct {
	ID   int64  `json:"id"`          // projects.id (in the run's own home)
	Role string `json:"role"`        // projRoleTask | projRoleCoordinator
	N    int64  `json:"n,omitempty"` // a task's number in its project (0 for a coordinator)
}

// Roles a run plays in its project.
const (
	projRoleTask        = "task"
	projRoleCoordinator = "coordinator"
)

func (p *ProjectRef) isTask() bool        { return p != nil && p.Role == projRoleTask }
func (p *ProjectRef) isCoordinator() bool { return p != nil && p.Role == projRoleCoordinator }

// coordSessionKey is runs.session_key of user's coordinator of project pid:
// one per person per project, made on first use.
func coordSessionKey(pid int64, user string) string {
	return "proj:" + strconv.FormatInt(pid, 10) + ":coord:" + user
}

// Stream event types (events.go): a project changed (list subscribers who
// may see it, and the task's own stream), and a conversation's CI.
const (
	evProject = "project" // {id, change, n?}
	evCI      = "ci"      // {root, watch, summary, state, outcome}; coalesced per root
)

// pendKindProject is pendingState.Kind for a task run parked by the
// workspace gate (pendingState gains Project *ProjectPark).
const pendKindProject = "project"

// refusalClassInternal: a project's tasks never run in a class with
// internal reach — every task reads scm content (CI logs, reviews, issues),
// and the coordinator that steers them is in the web lane (409).
const refusalClassInternal = "class-internal"

// scmBotRule is the agent's scm bot rule (the setting scm_bot_rule in a bot
// home's database, as JSON; managers only): who besides the agent's
// managers may name which repos for the bot, where a home's identity is
// the bot (scmBotAllowed). A person's partition never reads it.
type scmBotRule struct {
	Users []string `json:"users"` // xbin people
	Repos []string `json:"repos"` // owner/name globs ("acme/*")
}

// --- projects -----------------------------------------------------------------------

// Kinds of project.
const (
	projPersonal   = "personal"   // one home: a person's partition, or an unpartitioned agent's (with members)
	projTeam       = "team"       // at the global instance: the definition and the board, no tasks
	projMembership = "membership" // in a member's partition: their half of a team project (team_ref)
)

// Project states.
const (
	projActive   = "active"
	projArchived = "archived"
	projDeleting = "deleting" // the worker is taking its workspace down
)

// Project is a row of projects, as the API shows it (a project holds no
// token).
type Project struct {
	ID          int64           `json:"id"`
	UID         string          `json:"uid"`  // 6 × [a-z0-9], random, stable: branch names, paths, clientIds, labels
	Name        string          `json:"name"` // shown
	Slug        string          `json:"slug"` // [a-z0-9-]{1,40}, unique in the home: the workspace directory
	Kind        string          `json:"kind"` // projPersonal | projTeam | projMembership
	TeamRef     int64           `json:"teamRef,omitempty"`
	Owner       string          `json:"owner"`
	Visibility  string          `json:"visibility"` // private | team
	TeamRole    string          `json:"teamRole"`   // what team visibility grants: viewer | participant
	SCM         string          `json:"scm"`        // the scm provider's tile path (scmFor)
	Host        string          `json:"host"`       // github.com
	SandboxRef  string          `json:"sandboxRef,omitempty"`
	SandboxMade bool            `json:"sandboxMade,omitempty"` // the project made it (deleting the project may delete it)
	Dir         string          `json:"dir,omitempty"`         // <workdir>/<slug>, once the sandbox is known
	Policy      json.RawMessage `json:"policy"`                // ProjectPolicy, as stored (unknown keys kept)
	ForkSnap    string          `json:"forkSnap,omitempty"`
	ForkSnapMs  int64           `json:"forkSnapMs,omitempty"`
	State       string          `json:"state"`
	Version     int64           `json:"version"` // bumped on every PATCH; PATCH carries it
	// A membership's own: FromSeed — its sandbox was cloned from the team's
	// seed (never a person's credential there); DefHash — the definition's
	// security-relevant part as the member accepted it (setup scripts,
	// instructions, checks, taskClass, as, reviews, autoPR…); DefPending —
	// the hash of a changed definition awaiting acceptance (the column
	// def_pending keeps its JSON, which GET /memberships/{pid}/pending shows
	// the member beside what they accepted; "" none).
	FromSeed   bool   `json:"fromSeed,omitempty"`
	DefHash    string `json:"defHash,omitempty"`
	DefPending string `json:"defPending,omitempty"`
	CreatedBy  string `json:"createdBy"`
	CreatedMs  int64  `json:"createdMs"`
	UpdatedMs  int64  `json:"updatedMs"`
}

// ProjectView is GET /projects/{pid}: the row with the caller's level, its
// repos, the board's counts and the slots in use. Policy holds every key,
// its defaults filled.
type ProjectView struct {
	Project
	Level  string         `json:"level"` // owner | participant | viewer
	Repos  []ProjectRepo  `json:"repos"`
	Counts map[string]int `json:"counts"` // per board column
	Slots  ProjectSlots   `json:"slots"`
}

// ProjectSlots: tasks holding a slot now, of policy.maxTasks.
type ProjectSlots struct {
	Used int `json:"used"`
	Max  int `json:"max"`
}

// ProjectStatus is GET /projects/{pid}/status.
type ProjectStatus struct {
	Sandbox  StatusSandbox    `json:"sandbox"`
	Repos    []StatusRepo     `json:"repos"`
	Creds    []StatusCred     `json:"creds"`
	Jobs     []ProjectJob     `json:"jobs"` // live ones and the last 20 finished
	Slots    ProjectSlots     `json:"slots"`
	Warnings []ProjectWarning `json:"warnings"`
}

type StatusSandbox struct {
	Ref     string `json:"ref"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Workdir string `json:"workdir"`
	Shared  bool   `json:"shared"`
}

type StatusRepo struct {
	Slug      string `json:"slug"`
	State     string `json:"state"`
	FetchedMs int64  `json:"fetchedMs,omitempty"`
	Head      string `json:"head,omitempty"`
	Protected *bool  `json:"protected,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StatusCred is a credential's metadata (project_creds) — never the token.
type StatusCred struct {
	Sandbox   string `json:"sandbox"`
	Host      string `json:"host"`
	Identity  string `json:"identity"` // person | bot
	Login     string `json:"login"`
	State     string `json:"state"` // credLive | credScrubbed | credBlocked
	ExpiresMs int64  `json:"expiresMs,omitempty"`
	Why       string `json:"why,omitempty"`
}

// ProjectWarning is something a person should know (kind "unprotected": a
// repo's default branch has no protection a pushed token can't bypass).
type ProjectWarning struct {
	Kind string `json:"kind"`
	Repo string `json:"repo,omitempty"`
	Text string `json:"text"`
}

// Repo modes and checkout kinds.
const (
	repoBare    = "bare"    // the project's own base repo: <dir>/.repos/<slug>.git
	repoAdopted = "adopted" // a conversation's existing clone (an upgrade): never removed
	coWorktree  = "worktree"
	coClone     = "clone" // a clone of the base borrowing its objects (policy checkout "clone")
	coMain      = "main"  // an adopted repo's own checkout (task 1 of an upgrade)
)

// ProjectRepo is a row of project_repos.
type ProjectRepo struct {
	ProjectID     int64  `json:"-"`
	Slug          string `json:"slug"` // [a-z0-9-]{1,40}, unique in the project
	Repo          string `json:"repo"` // owner/name
	URL           string `json:"url"`  // https clone URL
	DefaultBranch string `json:"defaultBranch"`
	BasePath      string `json:"basePath,omitempty"`
	Mode          string `json:"mode"`     // repoBare | repoAdopted
	Checkout      string `json:"checkout"` // coWorktree | coClone
	Setup         string `json:"setup,omitempty"`
	State         string `json:"state"` // pending | cloning | ready | failed | removing
	Error         string `json:"error,omitempty"`
	FetchedMs     int64  `json:"fetchedMs,omitempty"`
	Head          string `json:"head,omitempty"`      // origin/<default>'s sha at the last fetch
	Protected     *bool  `json:"protected,omitempty"` // nil: unknown (the column's -1)
	CreatedMs     int64  `json:"createdMs,omitempty"`
}

// --- tasks --------------------------------------------------------------------------

// Task sizes.
const (
	sizeSmall = "small" // worktrees in the project's sandbox
	sizeBig   = "big"   // its own sandbox, forked from the project's (provisionFork)
)

// Workspace states (project_tasks.ws): pending → queued → preparing →
// (signin →) ready, or failed; later cleaning → cleaned, or blocked.
const (
	wsPending   = "pending"
	wsQueued    = "queued"
	wsPreparing = "preparing"
	wsSignin    = "signin"
	wsReady     = "ready"
	wsFailed    = "failed"
	wsCleaning  = "cleaning"
	wsCleaned   = "cleaned"
	wsBlocked   = "blocked" // cleanup refused: unpushed or uncommitted work
)

// Task phases (project_tasks.phase).
const (
	phaseOpen    = "open"
	phasePR      = "pr"
	phaseMerged  = "merged"
	phaseClosed  = "closed"
	phaseDone    = "done"
	phaseDeleted = "deleted" // its conversation was deleted; the sweep cleans up
)

// Board columns, derived (never stored).
const (
	colQueued   = "queued"
	colWorking  = "working"
	colNeedsYou = "needs-you"
	colPR       = "pr"
	colDone     = "done"
)

// Who asked for a task's current turn (project_tasks.turn_by), and where a
// queued input came from (project_queue.source, the ledger's source).
const (
	srcHuman       = "human"
	srcCoordinator = "coordinator"
	srcEvent       = "event"
)

// ProjectTask is a row of project_tasks.
type ProjectTask struct {
	ID         int64           `json:"-"`
	ProjectID  int64           `json:"project"`
	N          int64           `json:"n"`
	RunID      int64           `json:"run"` // 0 once its conversation is deleted
	Title      string          `json:"title"`
	Slug       string          `json:"slug"`
	Size       string          `json:"size"`
	Branch     string          `json:"branch"`
	Issue      *IssueRef       `json:"issue,omitempty"`
	Repos      []string        `json:"repos"`                // repo slugs it works in
	SandboxRef string          `json:"sandboxRef,omitempty"` // the project's sandbox, or the task's fork
	ForkMade   bool            `json:"forkMade,omitempty"`
	Dir        string          `json:"dir,omitempty"` // <project dir>/tasks/<n>-<slug>
	PortsBase  int             `json:"portsBase,omitempty"`
	WS         string          `json:"ws"`
	Phase      string          `json:"phase"`
	PRs        json.RawMessage `json:"prs,omitempty"` // []TaskPR
	TurnBy     string          `json:"turnBy,omitempty"`
	Last       string          `json:"-"` // its latest answer, clipped
	SetupTail  string          `json:"-"` // a failed setup's redacted tail
	CIFixes    json.RawMessage `json:"-"` // {"day", "n"}: CI-fix inputs today
	Error      string          `json:"error,omitempty"`
	FromRun    int64           `json:"-"` // the coordinator run that made it (0: a person)
	CreatedBy  string          `json:"createdBy"`
	CreatedMs  int64           `json:"createdMs"`
	UpdatedMs  int64           `json:"updatedMs"`
	CleanedMs  int64           `json:"cleanedMs,omitempty"`
}

// TaskPR is one pull request a task opened (project_tasks.prs holds a list).
type TaskPR struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
	State    string `json:"state"` // open | closed | merged
	Draft    bool   `json:"draft"`
	HeadSHA  string `json:"headSha"`
	Checks   string `json:"checks"` // none | pending | success | failure
	ChecksAt int64  `json:"checksAt,omitempty"`
}

// ProjectCheckout is a row of project_checkouts: one task × one repo.
type ProjectCheckout struct {
	TaskID    int64  `json:"-"`
	Repo      string `json:"repo"` // project_repos.slug
	Path      string `json:"path"`
	Mode      string `json:"mode"`  // coWorktree | coClone | coMain
	State     string `json:"state"` // pending | added | setup | ready | failed | removed | kept
	SetupExit *int   `json:"setupExit,omitempty"`
	Error     string `json:"error,omitempty"`
	RemoteSHA string `json:"remoteSha,omitempty"` // the task branch on the remote at the last refs check ("" none yet)
}

// IssueRef names an issue a task starts from.
type IssueRef struct {
	Repo   string `json:"repo"` // owner/name
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

// TaskAgent picks a coding agent (a harness) to answer a task; nil = the
// built-in agent, or policy.engine.
type TaskAgent struct {
	Provider string `json:"provider"`       // a catalog id (GET /harnesses)
	Mode     string `json:"mode,omitempty"` // as POST /ask harness.mode
}

// TaskSpec is a new task (POST /projects/{pid}/tasks, task_create).
type TaskSpec struct {
	Title string     `json:"title,omitempty"` // "" = from the text or the issue
	Text  string     `json:"text"`            // the brief; the first prompt ends with it
	Size  string     `json:"size,omitempty"`  // sizeSmall (default) | sizeBig
	Issue *IssueRef  `json:"issue,omitempty"`
	Repos []string   `json:"repos,omitempty"` // repo slugs; empty = every repo of the project
	Agent *TaskAgent `json:"agent,omitempty"`
	Class string     `json:"class,omitempty"` // "" = policy.taskClass
	Model string     `json:"model,omitempty"` // the built-in agent's model pick
	// From is the coordinator run that asked (0: a person): turn_by,
	// project_tasks.from_run and the ledger follow it.
	From int64 `json:"-"`
}

// TaskFilter narrows a task list.
type TaskFilter struct {
	Phase  string // "" | a phase ("" = all but deleted)
	Column string // "" | a board column
	State  string // "" | a task state (taskQueued…) | "open"
	Q      string // words in the title or branch
	Mine   bool   // created by the caller
	Cursor string // opaque
	Limit  int    // default 50, at most 200
}

// ActOpts are a task action's options (cancel, close, retry, refresh,
// cleanup, pr).
type ActOpts struct {
	Reason   string `json:"reason,omitempty"`
	Force    bool   `json:"force,omitempty"`    // cleanup: even with unpushed work (the run's owner only)
	Cleanup  bool   `json:"cleanup,omitempty"`  // close: clean up too
	ClosePRs bool   `json:"closePRs,omitempty"` // close: close its open PRs
	Draft    *bool  `json:"draft,omitempty"`    // pr
	Title    string `json:"title,omitempty"`    // pr
	Body     string `json:"body,omitempty"`     // pr
}

// TaskView is a task as the API shows it (GET /runs/{id}/task, the task
// list, the board, the coordinator's tools): the row, its checkouts and
// what is derived from its run.
type TaskView struct {
	Project    int64             `json:"project"`
	N          int64             `json:"n"`
	Run        int64             `json:"run"`
	Title      string            `json:"title"`
	Size       string            `json:"size"`
	Branch     string            `json:"branch"`
	Issue      *IssueRef         `json:"issue,omitempty"`
	Repos      []string          `json:"repos"`
	WS         string            `json:"ws"`
	Phase      string            `json:"phase"`
	Column     string            `json:"column"`     // a board column
	State      string            `json:"state"`      // task* below
	WaitingFor string            `json:"waitingFor"` // "" | you | signin | review | ci | slot
	RunStatus  string            `json:"runStatus"`  // the run's status ("" once deleted)
	Engine     string            `json:"engine"`     // "" | harness
	Harness    string            `json:"harness"`    // its provider, for a coding agent
	SandboxRef string            `json:"sandboxRef"` // where it works
	Fork       bool              `json:"fork"`       // its own sandbox (a big task)
	Dir        string            `json:"dir"`
	Ports      *TaskPorts        `json:"ports,omitempty"`
	Checkouts  []ProjectCheckout `json:"checkouts"`
	PRs        []TaskPR          `json:"prs"`
	CI         *CISummary        `json:"ci,omitempty"` // its CI watches' aggregate (taskCISummary)
	TurnBy     string            `json:"turnBy,omitempty"`
	Step       string            `json:"step,omitempty"` // the workspace step under way
	Error      string            `json:"error,omitempty"`
	Last       string            `json:"last,omitempty"` // its latest answer, clipped
	CreatedBy  string            `json:"createdBy"`
	CreatedMs  int64             `json:"createdMs"`
	UpdatedMs  int64             `json:"updatedMs"`
}

// TaskPorts are the ports a task may listen on (TASK_PORT_BASE …+SPAN-1).
type TaskPorts struct {
	Base int `json:"base"`
	Span int `json:"span"`
}

// Task states, derived (TaskView.State): what task_list and the board say.
const (
	taskQueued         = "queued"
	taskPreparing      = "preparing"
	taskSignin         = "signin"
	taskWorking        = "working"
	taskNeedsYou       = "needs-you"
	taskCI             = "ci"
	taskCIFailed       = "ci-failed"
	taskAwaitingReview = "awaiting-review"
	taskMerged         = "merged"
	taskClosed         = "closed"
	taskDone           = "done"
	taskFailed         = "failed"
	taskCancelled      = "cancelled"
	taskBlocked        = "blocked"
	taskDeleted        = "deleted"
)

// TaskRefs are what scm events are routed by (project_refs): a task's
// branch, head and pull request in one repo.
type TaskRefs struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"headSha,omitempty"`
	PR      int    `json:"pr,omitempty"`
	Issue   int    `json:"issue,omitempty"`
}

// ProjectPark is pendingState.Project: a task run parked by the workspace
// gate (kind pendKindProject) — its workspace is being prepared, needs a
// sign-in, or failed.
type ProjectPark struct {
	Project int64      `json:"project"`
	N       int64      `json:"n"`
	WS      string     `json:"ws"`
	Step    string     `json:"step,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	Signin  *scmSignin `json:"signin,omitempty"` // only in the view of the person who must sign in
}

// BoardRow is a team project's board row (project_board at global): one
// member's task, never its transcript.
type BoardRow struct {
	Member    string     `json:"member"` // set by global from the caller, never taken from the body
	MemberPid string     `json:"-"`      // the caller's partition id (a person re-created under the same id starts fresh rows)
	N         int64      `json:"n"`
	Title     string     `json:"title"`
	Column    string     `json:"col"`
	State     string     `json:"state"`
	Waiting   string     `json:"waiting,omitempty"`
	Branch    string     `json:"branch"`
	PRs       []TaskPR   `json:"prs"`
	CI        *CISummary `json:"ci,omitempty"`
	Run       int64      `json:"run"` // the member's own run: only they open it
	UpdatedMs int64      `json:"updatedMs"`
	Stale     bool       `json:"stale,omitempty"`  // its member left the project
	Hidden    bool       `json:"hidden,omitempty"` // hidden by the owner
}

// --- inputs, events, jobs -------------------------------------------------------------

// taskInput is a row of project_queue: a task's start or a message to it,
// waiting for a slot (maxTasks) or for its run to stop waiting for a person.
type taskInput struct {
	ID       int64  `json:"id"`
	Project  int64  `json:"project"`
	N        int64  `json:"n"`
	Kind     string `json:"kind"` // start | input
	Text     string `json:"text"`
	Source   string `json:"source"` // srcHuman | srcCoordinator | srcEvent
	Sender   string `json:"sender"` // the person, or "" for the coordinator and events
	HoldPark bool   `json:"holdPark"`
	Dedupe   string `json:"dedupe,omitempty"` // unique per project when set
	Created  int64  `json:"created"`
}

// Project event kinds (project_events.kind).
const (
	pevTaskCreated = "task.created"
	pevTaskState   = "task.state"  // a turn ended: answered, failed, waiting for a person
	pevTaskHuman   = "task.human"  // a person wrote to the task directly
	pevTaskCancel  = "task.cancel" // cancelled
	pevWorkspace   = "workspace"   // a workspace step finished or failed
	pevPROpened    = "pr.opened"
	pevPRReady     = "pr.ready" // checks green on the head
	pevCIFailed    = "ci.failed"
	pevCIStuck     = "ci.stuck"
	pevReview      = "review"  // forwarded to the task
	pevComment     = "comment" // not forwarded (someone outside the project)
	pevMerged      = "merged"
	pevClosed      = "closed"
	pevPush        = "push" // someone else pushed to the task's branch
	pevIssue       = "issue"
	pevNote        = "note" // anything else worth a line ("lost track of CI")
)

// ProjectEvent is a row of project_events: the coordinators' feed (and the
// project page's). Body is JSON: {text, by?, sha?, url?, …}; Wake asks the
// coordinator (of CoordUser) to take a turn for it.
type ProjectEvent struct {
	ID        int64           `json:"id"`
	Project   int64           `json:"project"`
	N         int64           `json:"n,omitempty"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	Wake      bool            `json:"wake"`
	CoordUser string          `json:"coordUser,omitempty"` // the task's creator; the owner for n=0
	Delivered int64           `json:"delivered,omitempty"` // unix ms it reached the coordinator
	MsgID     int64           `json:"msgId,omitempty"`
	Dedupe    string          `json:"-"`
	Created   int64           `json:"created"`
}

// Worker job kinds (project_jobs.kind); a kind registers its runner in
// projectJobKinds.
const (
	pjSandbox   = "sandbox"
	pjCreds     = "creds"
	pjRepo      = "repo"
	pjFetch     = "fetch"
	pjPrepare   = "prepare"
	pjSetup     = "setup"
	pjBind      = "bind"
	pjSnapshot  = "snapshot"
	pjFork      = "fork"
	pjPR        = "pr"
	pjRefs      = "refs" // after a task's turn: did its branch move on the remote? (its PRs, projectRefsHooks)
	pjPoll      = "poll"
	pjSubscribe = "subscribe"
	pjCleanup   = "cleanup"
	pjScrub     = "scrub"
)

// Job states.
const (
	pjQueued  = "queued"
	pjRunning = "running"
	pjWaiting = "waiting" // on an exec, a sign-in or a quiet sandbox; next_ms says when to look
	pjDone    = "done"
	pjFailed  = "failed"
)

// ProjectJob is a row of project_jobs.
type ProjectJob struct {
	ID       int64  `json:"id"`
	Project  int64  `json:"project"`
	Task     int64  `json:"task,omitempty"` // project_tasks.id; 0 = the project's
	Repo     string `json:"repo,omitempty"` // a repo slug, for per-repo kinds
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Step     string `json:"step,omitempty"`
	Attempts int    `json:"attempts"`
	NextMs   int64  `json:"nextMs,omitempty"`
	ExecRef  string `json:"-"` // the sandbox an exec runs in
	ExecID   string `json:"-"`
	ClientID string `json:"-"`
	Out      string `json:"out,omitempty"` // the redacted tail
	Error    string `json:"error,omitempty"`
	By       string `json:"by,omitempty"`
	Epoch    int64  `json:"-"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
}

// jobOutcome is what a job's runner says: done, or wait and look again
// (with an error, the job counts an attempt and backs off).
type jobOutcome struct {
	Done   bool
	WaitMs int64  // > 0: state waiting, next_ms = now + WaitMs
	Step   string // what it is doing, shown on the task
}

// projectJobFunc runs one step of a job (registered in projectJobKinds,
// projects_seams.go). It is called again after a restart: it looks before
// it acts.
type projectJobFunc func(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error)

// Credential states (project_creds.state).
const (
	credLive     = "live"
	credScrubbed = "scrubbed"
	credBlocked  = "blocked" // the gate refuses this sandbox (shared since, say)
)

// --- policy ---------------------------------------------------------------------------

// ProjectPolicy is projects.policy. Unknown keys are kept (a newer build's);
// a missing key is its default (defaultProjectPolicy). Per-repo setup
// scripts are project_repos.setup, not policy.
type ProjectPolicy struct {
	Instructions  string        `json:"instructions,omitempty"` // for every task, after the repos' own AGENTS.md
	Checks        []string      `json:"checks,omitempty"`       // commands a task runs before it pushes
	PRConventions string        `json:"prConventions,omitempty"`
	TaskClass     string        `json:"taskClass"`         // "coding"
	Engine        string        `json:"engine"`            // auto | builtin | harness
	Harness       string        `json:"harness,omitempty"` // the default coding agent ("" = the person's last)
	As            string        `json:"as,omitempty"`      // person | bot ("" = person in a person's partition, else bot)
	MembersAsBot  bool          `json:"membersAsBot"`      // team: members may work as the bot (the provider's botForPeople must allow it too)
	MaxTasks      int           `json:"maxTasks"`          // 3 (1–16): tasks holding a slot at once
	MaxOpenTasks  int           `json:"maxOpenTasks"`      // 20: open tasks coordinators may have made
	MaxCreatesDay int           `json:"maxTaskCreatesPerDay"`
	BranchPrefix  string        `json:"branchPrefix,omitempty"` // "" = "xbin/<uid>"
	AutoPR        string        `json:"autoPR"`                 // off | draft | ready
	Checkout      string        `json:"checkout"`               // worktree | clone
	SetupTimeout  int           `json:"setupTimeoutSec"`        // 600
	SetupBlocking bool          `json:"setupBlocking"`          // true: bind waits for setup (a failure still starts the task)
	Ports         PolicyPorts   `json:"ports"`
	FetchEveryMin int           `json:"fetchEveryMin"` // 10
	Protection    string        `json:"protection"`    // warn | refuse: a base branch without protection
	Workflows     bool          `json:"workflows"`     // tokens may write .github/workflows (needs the provider's allowWorkflows)
	CI            PolicyCI      `json:"ci"`
	Reviews       PolicyReviews `json:"reviews"`
	AutoLabel     string        `json:"autoLabel,omitempty"` // an issue with this label wakes the coordinator
	BigTasks      PolicyBig     `json:"bigTasks"`
	Cleanup       PolicyCleanup `json:"cleanup"`
	Coordinator   PolicyCoord   `json:"coordinator"`
}

// PolicyPorts: task n listens on base + ((n-1) mod slots) × span, span ports.
type PolicyPorts struct {
	Base  int `json:"base"`  // 20000
	Span  int `json:"span"`  // 10
	Slots int `json:"slots"` // 100
}

// PolicyCI is what a failing check does.
type PolicyCI struct {
	AutoFix   bool `json:"autoFix"`   // true: the task is told to fix it
	MaxPerDay int  `json:"maxPerDay"` // 5 per task, then ci.stuck
	DelaySec  int  `json:"delaySec"`  // 60: let the other checks finish
	LogBytes  int  `json:"logBytes"`  // 8192
}

// PolicyReviews is which review comments reach the task.
type PolicyReviews struct {
	Forward  string   `json:"forward"`         // trusted | all | off
	Allow    []string `json:"allow,omitempty"` // logins forwarded whatever their association
	BatchSec int      `json:"batchSec"`        // 120
}

// PolicyBig is how a big task gets its sandbox.
type PolicyBig struct {
	Mode     string `json:"mode"`     // fork | fresh
	KeepFork bool   `json:"keepFork"` // false: cleanup deletes it
}

// PolicyCleanup is what ends a task's workspace.
type PolicyCleanup struct {
	OnMerge bool `json:"onMerge"` // true
	OnClose bool `json:"onClose"` // true
}

// PolicyCoord is the coordinator's.
type PolicyCoord struct {
	Web   bool   `json:"web"`             // false: web_search/web_fetch denied
	Model string `json:"model,omitempty"` // "" = the agent's default
}

// defaultProjectPolicy is every key's default (the spec's policy table;
// API.md §Projects).
func defaultProjectPolicy() ProjectPolicy {
	return ProjectPolicy{
		TaskClass: "coding", Engine: "auto", MaxTasks: 3, MaxOpenTasks: 20, MaxCreatesDay: 50,
		AutoPR: "off", Checkout: coWorktree, SetupTimeout: 600, SetupBlocking: true,
		Ports: PolicyPorts{Base: 20000, Span: 10, Slots: 100}, FetchEveryMin: 10, Protection: "warn",
		CI:          PolicyCI{AutoFix: true, MaxPerDay: 5, DelaySec: 60, LogBytes: 8192},
		Reviews:     PolicyReviews{Forward: "trusted", BatchSec: 120},
		BigTasks:    PolicyBig{Mode: "fork"},
		Cleanup:     PolicyCleanup{OnMerge: true, OnClose: true},
		Coordinator: PolicyCoord{},
	}
}

// --- CI -------------------------------------------------------------------------------

// CI watch sources (ci_watch.source).
const (
	ciTask   = "task"   // a project task's branch and PR head
	ciPushed = "pushed" // a branch a coding session pushed (found at turn end)
	ciManual = "manual" // a person's "Watch CI for…"
)

// CISummary is a conversation's (or a task's) CI at a glance: the chip, the
// board, the ci stream event.
type CISummary struct {
	State     string `json:"state"` // none | pending | success | failure
	Jobs      CIJobs `json:"jobs"`
	Current   string `json:"current,omitempty"` // "job › step" of the first running job
	StartedAt int64  `json:"startedAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	URL       string `json:"url,omitempty"` // the run (or the PR's checks) on the platform
}

// CIJobs counts jobs across a conversation's watches (checks and statuses
// without a job count as one each).
type CIJobs struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Running int `json:"running"`
	Queued  int `json:"queued"`
}

// CIView is GET /runs/{id}/ci.
type CIView struct {
	Root     int64         `json:"root"`
	Summary  CISummary     `json:"summary"`
	Live     bool          `json:"live"` // the provider's webhooks are healthy
	CanRerun bool          `json:"canRerun"`
	CanWatch bool          `json:"canWatch"`
	Watches  []CIWatchView `json:"watches"`
}

// CIWatchView is one ci_watch row as the API shows it.
type CIWatchView struct {
	ID        int64      `json:"id"`
	Source    string     `json:"source"` // ciTask | ciPushed | ciManual
	Run       int64      `json:"run"`    // the run whose push it came from
	SCM       string     `json:"scm"`
	Host      string     `json:"host"`
	Repo      string     `json:"repo"`
	Ref       string     `json:"ref"`
	PR        int        `json:"pr,omitempty"`
	SHA       string     `json:"sha"`
	State     string     `json:"state"` // none | pending | success | failure | gone
	Since     int64      `json:"since"`
	UpdatedMs int64      `json:"updatedMs"`
	FetchedMs int64      `json:"fetchedMs"`
	Error     string     `json:"error,omitempty"`
	Refusal   string     `json:"refusal,omitempty"` // the provider's, on the last read: signin → the dock offers a sign-in
	Outcome   string     `json:"outcome,omitempty"` // "<sha>:<state>" of its last final outcome (success | failure): the inline card's key
	URLs      CIURLs     `json:"urls"`
	Checks    *scmChecks `json:"checks,omitempty"` // the snapshot, redacted
}

type CIURLs struct {
	PR     string `json:"pr,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Checks string `json:"checks,omitempty"`
}
