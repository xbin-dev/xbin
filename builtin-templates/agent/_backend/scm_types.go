// scm_types.go — the scm contract, protocol 1, as the agent speaks it
// (docs/scm.md): a provider tile (scm-github, or any tile that provides
// service "scm") hands out credentials for repos on its host and, if it
// offers them, reads repos, pull requests, checks and issues and delivers
// events. The agent binds providers in its `scm` slot (multi): each
// project names one.
//
// Types only: the client that implements scmAPI is scm.go; tests use a
// fake provider (scm_fake_test.go) that implements the same routes. Bodies are JSON, times unix
// milliseconds, a repo is "owner/name".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// scmProtocol is the contract version the agent speaks.
const scmProtocol = 1

// Capabilities a provider's hello may list. Only scmCapCredentials is
// required; a route of a capability it lacks answers 501 unsupported.
const (
	scmCapCredentials = "credentials"
	scmCapRepos       = "repos"
	scmCapPulls       = "pulls"
	scmCapIssues      = "issues"
	scmCapChecks      = "checks"
	scmCapEvents      = "events"
	scmCapPoll        = "poll"
	scmCapPartitions  = "partitions"   // a partitioned provider: people's sign-ins live in their partitions
	scmCapRerun       = "checks.rerun" // optional part of checks: POST /scm/checks/rerun
)

// Identities a token is asked as (the `as` field).
const (
	scmAsPerson = "person"
	scmAsBot    = "bot"
)

// scmHello is GET /scm/hello?protocol=1.
type scmHello struct {
	Protocol   int             `json:"protocol"`
	Protocols  []int           `json:"protocols"`
	SCM        scmProvider     `json:"scm"`
	Hosts      []string        `json:"hosts"`
	Caps       []string        `json:"caps"`
	Identities []string        `json:"identities"` // what this instance can hand out at all: person, bot
	You        scmYou          `json:"you"`
	App        scmApp          `json:"app"`
	Events     scmEventsHealth `json:"events"`
	Limits     scmLimits       `json:"limits"`
	Notes      []string        `json:"notes,omitempty"` // for people: why an identity is missing, setup left to do
}

type scmProvider struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Kind    string `json:"kind"` // github | gitlab | …
}

// scmYou is what the caller may be: the identities open to it, the default
// `as`, and the person's account when signed in.
type scmYou struct {
	Identities      []string    `json:"identities"`
	Default         string      `json:"default"`
	Person          *scmAccount `json:"person"`                    // null: not signed in (or no person here)
	SigninExpiresAt int64       `json:"signinExpiresAt,omitempty"` // when the sign-in itself lapses
}

type scmAccount struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type scmApp struct {
	Slug       string `json:"slug,omitempty"`
	InstallURL string `json:"installUrl,omitempty"`
	Configured bool   `json:"configured"`
}

type scmEventsHealth struct {
	Webhooks       string `json:"webhooks"` // active | inactive | unknown
	Healthy        bool   `json:"healthy"`
	LastDeliveryAt int64  `json:"lastDeliveryAt,omitempty"`
	PollMinMs      int64  `json:"pollMinMs"`
}

type scmLimits struct {
	ReposPerToken int `json:"reposPerToken"`
	MinTTLSec     int `json:"minTtlSec"`
	PageMax       int `json:"pageMax"`
	PollItems     int `json:"pollItems"`
}

func (h *scmHello) has(capName string) bool {
	for _, c := range h.Caps {
		if c == capName {
			return true
		}
	}
	return false
}

// --- credentials ----------------------------------------------------------------------

// scmTokenReq is POST /scm/token.
type scmTokenReq struct {
	Repo        string            `json:"repo,omitempty"`
	Repos       []string          `json:"repos,omitempty"`
	Access      string            `json:"access"`                // read | write
	As          string            `json:"as,omitempty"`          // person | bot ("" = the provider's default for the caller)
	Permissions map[string]string `json:"permissions,omitempty"` // narrows the preset only
	MinTTLSec   int               `json:"minTtlSec,omitempty"`
	Purpose     string            `json:"purpose,omitempty"` // the consumer's key for what it is for ("proj:<uid>:<sandbox>")
}

// scmToken is a credential: give it to git as username/password over https.
type scmToken struct {
	Host         string            `json:"host"`
	Username     string            `json:"username"`
	Token        string            `json:"token"`
	ExpiresAt    int64             `json:"expiresAt"`
	RefreshAfter int64             `json:"refreshAfter"`
	Identity     scmIdentity       `json:"identity"`
	Author       scmAuthor         `json:"author"`
	Repos        []string          `json:"repos"`
	Permissions  map[string]string `json:"permissions"`
}

// String never shows the token (a %v in a log line).
func (t scmToken) String() string {
	return fmt.Sprintf("scm token for %s as %s/%s until %d", t.Host, t.Identity.Kind, t.Identity.Login, t.ExpiresAt)
}

type scmIdentity struct {
	Kind  string `json:"kind"` // person | bot
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// scmAuthor is what commits made with the token should say.
type scmAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// scmRevokeReq is POST /scm/token/revoke: one token (by value) or every
// token handed out for a purpose.
type scmRevokeReq struct {
	Token   string `json:"token,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

// scmSignin is a person's device-flow sign-in under way.
type scmSignin struct {
	URL        string `json:"url"`
	UserCode   string `json:"userCode"`
	ExpiresAt  int64  `json:"expiresAt"`
	PollID     string `json:"pollId"`
	IntervalMs int64  `json:"intervalMs"`
}

// scmSigninState is GET|POST /scm/signin and GET /scm/signin/{pollId}.
type scmSigninState struct {
	State        string       `json:"state"` // none | pending | done | denied | expired | error
	Identity     *scmIdentity `json:"identity,omitempty"`
	Signin       *scmSignin   `json:"signin,omitempty"`
	Error        string       `json:"error,omitempty"`
	RetryAfterMs int64        `json:"retryAfterMs,omitempty"`
}

// --- reads ------------------------------------------------------------------------------

// scmPage is a paginated list: {items, next}; next "" is the last page.
type scmPage[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
	ETag  string `json:"etag,omitempty"`
}

type scmRepo struct {
	Host          string `json:"host"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	CloneURL      string `json:"cloneUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	Permission    string `json:"permission"` // admin | maintain | write | triage | read | none
	Archived      bool   `json:"archived"`
	URL           string `json:"url,omitempty"`
	ETag          string `json:"etag,omitempty"`
	// Protected: the default branch has protection the identity can't
	// bypass (null: the provider can't tell).
	Protected *bool `json:"protected,omitempty"`
}

// scmQuery is a list's query: what each list reads of it.
type scmQuery struct {
	Repo        string // owner/name (pulls, issues)
	Q           string // repos: words
	As          string
	State       string // open | closed | all (pulls also merged)
	Head        string // pulls: a branch
	Base        string // pulls
	Labels      []string
	Since       int64
	Limit       int
	Cursor      string
	IfNoneMatch string
}

// scmRef is one side of a pull request.
type scmRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo string `json:"repo,omitempty"`
}

// scmActor is who did something; Association is the provider's word for
// their relation to the repo, mapped to OWNER | MEMBER | COLLABORATOR |
// CONTRIBUTOR | NONE.
type scmActor struct {
	Login       string `json:"login"`
	Association string `json:"association,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Self        bool   `json:"self,omitempty"` // the provider's own app or bot
}

// scmPullReq is POST /scm/pulls.
type scmPullReq struct {
	Repo     string `json:"repo"`
	Head     string `json:"head"`
	Base     string `json:"base,omitempty"` // "" = the default branch
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Draft    bool   `json:"draft,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	As       string `json:"as,omitempty"`
}

type scmPull struct {
	Number         int      `json:"number"`
	URL            string   `json:"url"`
	Title          string   `json:"title"`
	Body           string   `json:"body,omitempty"`
	State          string   `json:"state"` // open | closed | merged
	Draft          bool     `json:"draft"`
	Mergeable      *bool    `json:"mergeable"`
	MergeableState string   `json:"mergeableState,omitempty"` // "unknown" while the provider computes it
	Head           scmRef   `json:"head"`
	Base           scmRef   `json:"base"`
	Author         scmActor `json:"author"`
	Labels         []string `json:"labels,omitempty"`
	UpdatedAt      int64    `json:"updatedAt"`
	Existing       bool     `json:"existing,omitempty"` // POST: one was already open for that head
	RetryAfterMs   int64    `json:"retryAfterMs,omitempty"`
	ETag           string   `json:"etag,omitempty"`
}

// scmPullPatch is PATCH /scm/pulls/{n}: nil fields are left alone.
type scmPullPatch struct {
	Repo  string  `json:"repo"`
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"` // open | closed
	Draft *bool   `json:"draft,omitempty"`
	As    string  `json:"as,omitempty"`
}

// scmComment is one entry of a pull request's (or an issue's) timeline.
type scmComment struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // comment | review | review-comment
	Author    scmActor `json:"author"`
	Body      string   `json:"body"`
	State     string   `json:"state,omitempty"` // review: approved | changes_requested | commented | dismissed
	Path      string   `json:"path,omitempty"`  // review-comment
	Line      int      `json:"line,omitempty"`
	URL       string   `json:"url"`
	CreatedAt int64    `json:"createdAt"`
}

// scmChecks is GET /scm/checks?repo=&ref=: everything CI reported on a
// commit — the host's workflow runs with their jobs and steps, every check
// run, the commit statuses — combined. Text in it (titles, summaries, step
// names) is untrusted.
type scmChecks struct {
	SHA          string           `json:"sha"`
	Ref          string           `json:"ref,omitempty"`
	State        string           `json:"state"` // none | pending | success | failure
	Counts       scmCounts        `json:"counts"`
	WorkflowRuns []scmWorkflowRun `json:"workflowRuns,omitempty"` // absent: the host has none
	Checks       []scmCheck       `json:"checks"`
	Statuses     []scmStatus      `json:"statuses"`
	ETag         string           `json:"etag,omitempty"`
}

// scmCounts buckets checks and statuses (each in one).
type scmCounts struct {
	Total     int `json:"total"`
	Success   int `json:"success"`
	Failure   int `json:"failure"`
	Pending   int `json:"pending"`
	Neutral   int `json:"neutral"`
	Skipped   int `json:"skipped"`
	Cancelled int `json:"cancelled"`
}

// scmWorkflowRun is one CI run on the commit (GitHub: an Actions workflow
// run), at its latest attempt.
type scmWorkflowRun struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Event      string   `json:"event"`
	Status     string   `json:"status"`     // queued | waiting | in_progress | completed
	Conclusion string   `json:"conclusion"` // as a check's; "" until completed
	URL        string   `json:"url"`
	StartedAt  int64    `json:"startedAt,omitempty"`
	UpdatedAt  int64    `json:"updatedAt,omitempty"`
	Attempt    int      `json:"attempt"`
	HeadSHA    string   `json:"headSha,omitempty"`
	HeadBranch string   `json:"headBranch,omitempty"`
	Jobs       []scmJob `json:"jobs,omitempty"` // absent in a workflow event
}

// scmJob is one job of a run; progress is its steps completed of all.
type scmJob struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"` // queued | waiting | in_progress | completed
	Conclusion  string    `json:"conclusion"`
	URL         string    `json:"url"`
	StartedAt   int64     `json:"startedAt,omitempty"`
	CompletedAt int64     `json:"completedAt,omitempty"`
	Runner      string    `json:"runner,omitempty"`
	Check       string    `json:"check,omitempty"` // the check run that reports it
	Steps       []scmStep `json:"steps"`
}

type scmStep struct {
	N           int    `json:"n"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
}

// scmCheck is one check run (Job set: it reports that job).
type scmCheck struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	App         string `json:"app,omitempty"`
	Status      string `json:"status"`     // queued | in_progress | completed
	Conclusion  string `json:"conclusion"` // success | failure | neutral | cancelled | skipped | timed_out | action_required | stale | startup_failure | ""
	URL         string `json:"url"`
	DetailsURL  string `json:"detailsUrl,omitempty"`
	Title       string `json:"title,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Annotations int    `json:"annotations"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Suite       string `json:"suite,omitempty"`
	Job         string `json:"job,omitempty"`
}

// scmStatus is a commit status: the latest per context.
type scmStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"` // pending | success | failure | error
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

// scmJobLog is GET /scm/checks/jobs/{id}/log: a job's log from a byte
// offset — whatever the build printed, untrusted.
type scmJobLog struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`    // the whole log's length
	Complete  bool   `json:"complete"` // false while the job runs
	Truncated bool   `json:"truncated"`
	URL       string `json:"url,omitempty"`
}

// scmAnnotation is one of a check run's annotations.
type scmAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Level     string `json:"level"` // notice | warning | failure
	Title     string `json:"title,omitempty"`
	Message   string `json:"message"`
}

// scmRerunReq is POST /scm/checks/rerun (cap scmCapRerun).
type scmRerunReq struct {
	Repo       string `json:"repo"`
	RunID      string `json:"runId"`
	FailedOnly bool   `json:"failedOnly"`
	As         string `json:"as,omitempty"`
}

type scmRerun struct {
	RunID   string `json:"runId"`
	Attempt int    `json:"attempt"`
}

type scmIssue struct {
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	Body      string       `json:"body"`
	State     string       `json:"state"`
	Labels    []string     `json:"labels,omitempty"`
	Author    scmActor     `json:"author"`
	URL       string       `json:"url"`
	Comments  []scmComment `json:"comments,omitempty"`
	UpdatedAt int64        `json:"updatedAt"`
	ETag      string       `json:"etag,omitempty"`
}

// --- poll -------------------------------------------------------------------------------

// scmPollReq is POST /scm/poll: conditional reads in one call.
type scmPollReq struct {
	As    string        `json:"as,omitempty"`
	Items []scmPollItem `json:"items"`
}

type scmPollItem struct {
	ID     string `json:"id"`   // the consumer's, echoed
	Kind   string `json:"kind"` // pull | checks | issue | comments
	Repo   string `json:"repo"`
	Number int    `json:"number,omitempty"`
	Ref    string `json:"ref,omitempty"`
	ETag   string `json:"etag,omitempty"`
	Since  int64  `json:"since,omitempty"`
}

type scmPollResp struct {
	Items        []scmPollResult `json:"items"`
	RetryAfterMs int64           `json:"retryAfterMs,omitempty"`
}

type scmPollResult struct {
	ID      string          `json:"id"`
	Changed bool            `json:"changed"`
	ETag    string          `json:"etag,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"` // the route's own answer: scmPull, scmChecks, scmIssue, scmPage[scmComment]
	Error   *scmError       `json:"error,omitempty"`
}

// --- events -----------------------------------------------------------------------------

// scmSubscription is POST /scm/subscriptions (and its answer): which
// events of a repo reach the consumer.
type scmSubscription struct {
	ID       string   `json:"id,omitempty"`
	Repo     string   `json:"repo"`
	Branches []string `json:"branches,omitempty"`
	PRs      []int    `json:"prs,omitempty"`
	Issues   bool     `json:"issues,omitempty"` // issue events of the repo
	Kinds    []string `json:"kinds,omitempty"`  // empty = every kind
	Key      string   `json:"key,omitempty"`    // the consumer's own key: one subscription per key (a repeat replaces it)
	For      string   `json:"for,omitempty"`    // set by the provider: global | user:<id>
	Expires  int64    `json:"expires,omitempty"`
}

// Event kinds. The last three are progress: shown, never acted on alone;
// an empty subscription kinds list means every kind but those.
const (
	scmKindPull     = "pull"
	scmKindChecks   = "checks"
	scmKindComment  = "comment"
	scmKindReview   = "review"
	scmKindPush     = "push"
	scmKindIssue    = "issue"
	scmKindWorkflow = "workflow"
	scmKindJob      = "job"
	scmKindCheck    = "check"
)

// scmEvent is an event v1, as POST /adapter/scm/event carries it.
type scmEvent struct {
	Protocol   int                        `json:"protocol"`
	EventID    string                     `json:"eventId"` // scm:<host>:<delivery>[:<n>]
	For        string                     `json:"for"`     // global | user:<id>
	SCM        scmEventSource             `json:"scm"`
	Kind       string                     `json:"kind"` // scmKind*
	Action     string                     `json:"action"`
	Topic      string                     `json:"topic"` // scm/<host>/<owner>/<repo>/(pull/<n>|issue/<n>|branch/<ref>|repo)/<kind>.<action>
	Repo       string                     `json:"repo"`
	Private    bool                       `json:"private"`
	Ref        scmEventRef                `json:"ref"`
	Actor      scmActor                   `json:"actor"`
	Conclusion string                     `json:"conclusion,omitempty"`
	Summary    string                     `json:"summary"`
	URL        string                     `json:"url,omitempty"`
	At         int64                      `json:"at"`
	Subs       []string                   `json:"subs,omitempty"` // the keys of this consumer's subscriptions it matched
	Data       map[string]json.RawMessage `json:"data,omitempty"` // keyed by kind
}

type scmEventSource struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
}

type scmEventRef struct {
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
	PR     int    `json:"pr,omitempty"`
	Issue  int    `json:"issue,omitempty"`
}

// --- refusals ---------------------------------------------------------------------------

// Refusals: the sandbox-manager contract's, plus the scm contract's own.
const (
	scmRefProtocol     = "protocol"      // 400
	scmRefInvalid      = "invalid"       // 400
	scmRefNotFound     = "not-found"     // 404
	scmRefNotAllowed   = "not-allowed"   // 403 (sso: a SAML block)
	scmRefExists       = "exists"        // 409
	scmRefPrecondition = "precondition"  // 412
	scmRefLimit        = "limit"         // 429 (retryAfterMs)
	scmRefUnsupported  = "unsupported"   // 501
	scmRefUnavailable  = "unavailable"   // 503 (retryAfterMs)
	scmRefSignin       = "signin"        // 409: the person must sign in (signin)
	scmRefNotInstalled = "not-installed" // 409: the app isn't on that account (install)
	scmRefIdentity     = "identity"      // 403: not as that identity here (identities)
	scmRefSetup        = "setup"         // 503: the provider isn't set up yet
	scmRefUpstream     = "upstream"      // 502: the host answered an error (upstream)
	scmRefInProgress   = "in-progress"   // 409: a running job's log, from a host that serves logs once a job ends (url)
)

// scmError is a provider's refusal: {error, refusal, …}.
type scmError struct {
	Status       int          `json:"-"`
	Message      string       `json:"error"`
	Refusal      string       `json:"refusal"`
	RetryAfterMs int64        `json:"retryAfterMs,omitempty"`
	Protocols    []int        `json:"protocols,omitempty"`
	Signin       *scmSignin   `json:"signin,omitempty"`
	Install      *scmInstall  `json:"install,omitempty"`
	Identities   []string     `json:"identities,omitempty"`
	Upstream     *scmUpstream `json:"upstream,omitempty"`
	SSO          *scmSSO      `json:"sso,omitempty"`
	URL          string       `json:"url,omitempty"` // in-progress: the host's live log page
}

type scmInstall struct {
	URL   string `json:"url"`
	Owner string `json:"owner"`
}

type scmUpstream struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

type scmSSO struct {
	URL string `json:"url"`
}

func (e *scmError) Error() string {
	if e.Refusal == "" {
		return e.Message
	}
	return e.Refusal + ": " + e.Message
}

// scmRefused says whether err is a provider's refusal of that kind.
func scmRefused(err error, refusal string) bool {
	var e *scmError
	return errors.As(err, &e) && e.Refusal == refusal
}

// --- the client -------------------------------------------------------------------------

// scmAPI is one bound provider, as the agent calls it: from a person's
// partition the call reaches that person's partition of a partitioned
// provider (they are the person asking), from the global instance or an
// unpartitioned agent its global instance (no person: bot only). Every
// method answers *scmError for a refusal.
type scmAPI interface {
	Provider() string // the binding's provider path: Project.SCM
	Hello(ctx context.Context) (*scmHello, error)

	Token(ctx context.Context, req scmTokenReq) (*scmToken, error)
	Revoke(ctx context.Context, req scmRevokeReq) error
	Signin(ctx context.Context) (*scmSigninState, error)
	SigninPoll(ctx context.Context, pollID string) (*scmSigninState, error)
	Forget(ctx context.Context) error

	Repos(ctx context.Context, q scmQuery) (*scmPage[scmRepo], error)
	Repo(ctx context.Context, repo, as string) (*scmRepo, error)

	PullCreate(ctx context.Context, req scmPullReq) (*scmPull, error)
	Pulls(ctx context.Context, q scmQuery) (*scmPage[scmPull], error)
	Pull(ctx context.Context, repo string, n int, as string) (*scmPull, error)
	PullPatch(ctx context.Context, n int, p scmPullPatch) (*scmPull, error)
	Comments(ctx context.Context, repo string, n int, since int64, as string) (*scmPage[scmComment], error)

	Checks(ctx context.Context, repo, ref, ifNoneMatch, as string) (*scmChecks, error) // nil, nil: not modified
	JobLog(ctx context.Context, repo, job string, tailBytes int, since int64, as string) (*scmJobLog, error)
	Annotations(ctx context.Context, repo, check, cursor, as string) (*scmPage[scmAnnotation], error)
	Rerun(ctx context.Context, req scmRerunReq) (*scmRerun, error)

	Issues(ctx context.Context, q scmQuery) (*scmPage[scmIssue], error)
	Issue(ctx context.Context, repo string, n int, comments bool, as string) (*scmIssue, error)

	Poll(ctx context.Context, req scmPollReq) (*scmPollResp, error)
	Subscribe(ctx context.Context, s scmSubscription) (*scmSubscription, error)
	Subscriptions(ctx context.Context) ([]scmSubscription, error)
	Unsubscribe(ctx context.Context, id string) error
}

// errScmUnbound: no provider of that name is bound in the `scm` slot.
var errScmUnbound = &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no scm provider of that name is bound to this agent (bind one in its scm slot)"}

// scmFor is the bound provider named provider (Project.SCM); scmBound lists
// them. The client (K, scm.go) sets both; until then nothing is bound.
var (
	scmFor   = func(provider string) (scmAPI, error) { return nil, errScmUnbound }
	scmBound = func() []string { return nil }
)
