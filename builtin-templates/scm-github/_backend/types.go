// types.go — the scm contract's shapes, protocol 1, as this provider
// answers them (docs/scm.md). Times are unix milliseconds; a repo is
// "owner/name". Text GitHub's users wrote (bodies, titles, logs) passes
// through untrusted.
package main

import "encoding/json"

const protocolVersion = 1

var jsonUnmarshal = json.Unmarshal

// Capabilities (docs/scm.md §hello).
const (
	capCredentials = "credentials"
	capRepos       = "repos"
	capPulls       = "pulls"
	capIssues      = "issues"
	capChecks      = "checks"
	capRerun       = "checks.rerun"
	capEvents      = "events"
	capPoll        = "poll"
	capPartitions  = "partitions"
)

// Identities.
const (
	asPerson = "person"
	asBot    = "bot"
)

// Limits this provider keeps.
const (
	reposPerToken = 100
	minTTLSec     = 900
	pageDefault   = 30
	pageMax       = 100
	pollItemsMax  = 50
	pollMinMs     = 120000
)

type helloResp struct {
	Protocol   int              `json:"protocol"`
	Protocols  []int            `json:"protocols"`
	SCM        providerInfo     `json:"scm"`
	Hosts      []string         `json:"hosts"`
	Caps       []string         `json:"caps"`
	Identities []string         `json:"identities"`
	You        youInfo          `json:"you"`
	App        appInfo          `json:"app"`
	Events     eventsHealthInfo `json:"events"`
	Limits     limitsInfo       `json:"limits"`
	Notes      []string         `json:"notes"`
}

type providerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

type youInfo struct {
	Identities      []string `json:"identities"`
	Default         string   `json:"default"`
	Person          *account `json:"person"`
	SigninExpiresAt int64    `json:"signinExpiresAt,omitempty"`
}

type account struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type appInfo struct {
	Slug       string `json:"slug,omitempty"`
	InstallURL string `json:"installUrl,omitempty"`
	Configured bool   `json:"configured"`
}

type eventsHealthInfo struct {
	Webhooks       string `json:"webhooks"` // active | inactive | unknown
	Healthy        bool   `json:"healthy"`
	LastDeliveryAt int64  `json:"lastDeliveryAt,omitempty"`
	PollMinMs      int64  `json:"pollMinMs"`
}

type limitsInfo struct {
	ReposPerToken int `json:"reposPerToken"`
	MinTTLSec     int `json:"minTtlSec"`
	PageMax       int `json:"pageMax"`
	PollItems     int `json:"pollItems"`
}

// tokenReq is POST /scm/token (and the relay's bot-token body).
type tokenReq struct {
	Repo        string            `json:"repo,omitempty"`
	Repos       []string          `json:"repos,omitempty"`
	Access      string            `json:"access"`
	As          string            `json:"as,omitempty"`
	Permissions map[string]string `json:"permissions,omitempty"`
	MinTTLSec   int               `json:"minTtlSec,omitempty"`
	Purpose     string            `json:"purpose,omitempty"`
}

// tokenResp is a handed-out token. Token is a secretString: the one route
// that sends it (POST /scm/token, and the relay to a person's partition)
// writes it with tokenJSON, never json.Marshal of this.
type tokenResp struct {
	Host         string            `json:"host"`
	Username     string            `json:"username"`
	Token        secretString      `json:"token"`
	ExpiresAt    int64             `json:"expiresAt"`
	RefreshAfter int64             `json:"refreshAfter"`
	Identity     identity          `json:"identity"`
	Author       author            `json:"author"`
	Repos        []string          `json:"repos"`
	Permissions  map[string]string `json:"permissions"`
}

// tokenJSON is the explicit body that carries a token's value.
func tokenJSON(t *tokenResp) json.RawMessage {
	type out tokenResp
	b, _ := json.Marshal(struct {
		out
		Token string `json:"token"`
	}{out(*t), t.Token.Reveal()})
	return b
}

type identity struct {
	Kind  string `json:"kind"`
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// signinInfo is a device-flow sign-in under way.
type signinInfo struct {
	URL        string `json:"url"`
	UserCode   string `json:"userCode"`
	ExpiresAt  int64  `json:"expiresAt"`
	PollID     string `json:"pollId"`
	IntervalMs int64  `json:"intervalMs"`
}

type signinState struct {
	State        string      `json:"state"` // none | pending | done | denied | expired | error
	Identity     *identity   `json:"identity,omitempty"`
	Signin       *signinInfo `json:"signin,omitempty"`
	Error        string      `json:"error,omitempty"`
	RetryAfterMs int64       `json:"retryAfterMs,omitempty"`
}

// page is a list: {items, next}; next absent on the last page.
type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

type repoInfo struct {
	Host          string `json:"host"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	CloneURL      string `json:"cloneUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	Permission    string `json:"permission"`
	Archived      bool   `json:"archived"`
	URL           string `json:"url"`
	Protected     *bool  `json:"protected,omitempty"`
}

type refSide struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo string `json:"repo,omitempty"`
}

type actor struct {
	Login       string `json:"login"`
	Association string `json:"association,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Self        bool   `json:"self,omitempty"`
}

type pullReq struct {
	Repo     string `json:"repo"`
	Head     string `json:"head"`
	Base     string `json:"base,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Draft    bool   `json:"draft,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	As       string `json:"as,omitempty"`
}

type pullInfo struct {
	Number         int      `json:"number"`
	URL            string   `json:"url"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	State          string   `json:"state"`
	Draft          bool     `json:"draft"`
	Mergeable      *bool    `json:"mergeable"`
	MergeableState string   `json:"mergeableState,omitempty"`
	RetryAfterMs   int64    `json:"retryAfterMs,omitempty"`
	Head           refSide  `json:"head"`
	Base           refSide  `json:"base"`
	Author         actor    `json:"author"`
	Labels         []string `json:"labels"`
	UpdatedAt      int64    `json:"updatedAt"`
	Existing       bool     `json:"existing,omitempty"`
}

type pullPatch struct {
	Repo  string  `json:"repo"`
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
	Draft *bool   `json:"draft,omitempty"`
	As    string  `json:"as,omitempty"`
}

type comment struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // comment | review | review-comment
	Author    actor  `json:"author"`
	Body      string `json:"body"`
	State     string `json:"state,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"createdAt"`
}

type checksResp struct {
	SHA          string         `json:"sha"`
	Ref          string         `json:"ref,omitempty"`
	State        string         `json:"state"`
	Counts       counts         `json:"counts"`
	WorkflowRuns []workflowRun  `json:"workflowRuns"`
	Checks       []checkRun     `json:"checks"`
	Statuses     []commitStatus `json:"statuses"`
}

type counts struct {
	Total     int `json:"total"`
	Success   int `json:"success"`
	Failure   int `json:"failure"`
	Pending   int `json:"pending"`
	Neutral   int `json:"neutral"`
	Skipped   int `json:"skipped"`
	Cancelled int `json:"cancelled"`
}

type workflowRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	StartedAt  int64  `json:"startedAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	Attempt    int    `json:"attempt"`
	HeadSHA    string `json:"headSha"`
	HeadBranch string `json:"headBranch"`
	Jobs       []job  `json:"jobs"`
}

type job struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	URL         string `json:"url"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt"`
	Runner      string `json:"runner"`
	Check       string `json:"check"`
	Steps       []step `json:"steps"`
}

type step struct {
	N           int    `json:"n"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt"`
}

type checkRun struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	App         string `json:"app"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	URL         string `json:"url"`
	DetailsURL  string `json:"detailsUrl"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Annotations int    `json:"annotations"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt"`
	Suite       string `json:"suite"`
	Job         string `json:"job"`
}

type commitStatus struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	URL         string `json:"url"`
	Description string `json:"description"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type jobLog struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	From      int64  `json:"from"`
	Complete  bool   `json:"complete"`
	Truncated bool   `json:"truncated"`
	URL       string `json:"url"`
}

type annotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Level     string `json:"level"`
	Title     string `json:"title"`
	Message   string `json:"message"`
}

type rerunReq struct {
	Repo       string `json:"repo"`
	RunID      string `json:"runId"`
	FailedOnly bool   `json:"failedOnly"`
	As         string `json:"as,omitempty"`
}

type rerunResp struct {
	RunID   string `json:"runId"`
	Attempt int    `json:"attempt"`
}

type issueInfo struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	Author    actor     `json:"author"`
	URL       string    `json:"url"`
	UpdatedAt int64     `json:"updatedAt"`
	Comments  []comment `json:"comments,omitempty"`
}

type pollReq struct {
	As    string     `json:"as,omitempty"`
	Items []pollItem `json:"items"`
}

type pollItem struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // pull | checks | issue | comments
	Repo   string `json:"repo"`
	Number int    `json:"number,omitempty"`
	Ref    string `json:"ref,omitempty"`
	ETag   string `json:"etag,omitempty"`
	Since  int64  `json:"since,omitempty"`
}

type pollResult struct {
	ID      string          `json:"id"`
	Changed bool            `json:"changed"`
	ETag    string          `json:"etag,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Error   *scmErr         `json:"error,omitempty"`
}

type pollResp struct {
	Items        []pollResult `json:"items"`
	RetryAfterMs int64        `json:"retryAfterMs"`
}
