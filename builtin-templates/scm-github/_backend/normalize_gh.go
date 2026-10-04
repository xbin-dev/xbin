// normalize_gh.go — what normalize.go reads of GitHub's webhook bodies
// (the parts used) and how each field taken from one is checked: a body
// is attacker-controlled, so repos, logins, shas, branches and urls are
// validated and text is passed on clipped, without control characters and
// with token-shaped strings redacted.
package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// --- GitHub's webhook body (the parts read) ----------------------------------------

type ghHookRepo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	HTMLURL  string `json:"html_url"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type ghHookPull struct {
	Number      int    `json:"number"`
	HTMLURL     string `json:"html_url"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Draft       bool   `json:"draft"`
	Merged      bool   `json:"merged"`
	UpdatedAt   string `json:"updated_at"`
	Association string `json:"author_association"`
	Head        struct {
		Ref  string      `json:"ref"`
		SHA  string      `json:"sha"`
		Repo *ghHookRepo `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type ghPullRef struct {
	Number int `json:"number"`
}

type ghHook struct {
	Action       string `json:"action"`
	Installation *struct {
		ID      int64   `json:"id"`
		Account *ghUser `json:"account"`
	} `json:"installation"`
	Repository   *ghHookRepo `json:"repository"`
	Organization *ghUser     `json:"organization"`
	Sender       *ghUser     `json:"sender"`

	PullRequest *ghHookPull `json:"pull_request"`
	Issue       *struct {
		Number      int             `json:"number"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		HTMLURL     string          `json:"html_url"`
		UpdatedAt   string          `json:"updated_at"`
		Association string          `json:"author_association"`
		PullRequest json.RawMessage `json:"pull_request"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Comment *struct {
		ID          int64  `json:"id"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		Path        string `json:"path"`
		Line        int    `json:"line"`
		CommitID    string `json:"commit_id"`
		UpdatedAt   string `json:"updated_at"`
		Association string `json:"author_association"`
	} `json:"comment"`
	Review *struct {
		ID          int64  `json:"id"`
		State       string `json:"state"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		CommitID    string `json:"commit_id"`
		SubmittedAt string `json:"submitted_at"`
		Association string `json:"author_association"`
	} `json:"review"`
	CheckSuite *struct {
		ID           int64       `json:"id"`
		HeadBranch   string      `json:"head_branch"`
		HeadSHA      string      `json:"head_sha"`
		Conclusion   string      `json:"conclusion"`
		UpdatedAt    string      `json:"updated_at"`
		PullRequests []ghPullRef `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun *struct {
		ghCheckRun
		HeadSHA    string `json:"head_sha"`
		CheckSuite *struct {
			ID         int64  `json:"id"`
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
		PullRequests []ghPullRef `json:"pull_requests"`
	} `json:"check_run"`
	WorkflowRun *struct {
		ghRun
		PullRequests   []ghPullRef `json:"pull_requests"`
		HeadRepository *ghHookRepo `json:"head_repository"`
	} `json:"workflow_run"`
	WorkflowJob *struct {
		ghJob
		RunID      int64  `json:"run_id"`
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
	} `json:"workflow_job"`

	// push
	Ref     string            `json:"ref"`
	Before  string            `json:"before"`
	After   string            `json:"after"`
	Forced  bool              `json:"forced"`
	Commits []json.RawMessage `json:"commits"`
	Compare string            `json:"compare"`

	// status
	SHA         string `json:"sha"`
	State       string `json:"state"`
	Context     string `json:"context"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
	Branches    []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	} `json:"branches"`
	ID int64 `json:"id"`

	// member, membership, organization, installation_repositories
	Member     *ghUser `json:"member"`
	Membership *struct {
		User *ghUser `json:"user"`
	} `json:"membership"`
	RepositoriesAdded   []ghHookRepo `json:"repositories_added"`
	RepositoriesRemoved []ghHookRepo `json:"repositories_removed"`
}

// account is the GitHub account a delivery concerns: the installation's,
// else the repository's owner, else the organization ("" when none).
func (h *ghHook) account() string {
	switch {
	case h.Installation != nil && h.Installation.Account != nil && h.Installation.Account.Login != "":
		return h.Installation.Account.Login
	case h.Repository != nil && h.Repository.Owner.Login != "":
		return h.Repository.Owner.Login
	case h.Repository != nil && validRepo(h.Repository.FullName):
		return ownerOf(h.Repository.FullName)
	case h.Organization != nil:
		return h.Organization.Login
	}
	return ""
}

func (h *ghHook) installationID() int64 {
	if h.Installation == nil {
		return 0
	}
	return h.Installation.ID
}

// --- checking what a body says ------------------------------------------------------

var (
	shaRE   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	loginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)
	wordRE  = regexp.MustCompile(`^[a-z_]{1,40}$`)
	// tokenRE is a token's shape (GitHub's, a private key's header): never
	// passed on, whatever text it is in.
	tokenRE = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
)

func cleanSHA(s string) string {
	s = strings.ToLower(s)
	if !shaRE.MatchString(s) {
		return ""
	}
	return s
}

func cleanLogin(s string) string {
	if !loginRE.MatchString(s) {
		return ""
	}
	return s
}

// cleanWord is a GitHub enum value (a state, a conclusion): lower-case
// letters and underscores, else "".
func cleanWord(s string) string {
	if !wordRE.MatchString(s) {
		return ""
	}
	return s
}

// cleanBranch is a branch name git would take (no control characters,
// spaces or the characters git refuses), else "".
func cleanBranch(s string) string {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) || strings.Contains(s, "..") || strings.HasPrefix(s, "/") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "@{") {
		return ""
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return ""
		}
	}
	return s
}

// cleanURL is an http(s) address without credentials, else "".
func cleanURL(s string) string {
	if s == "" || len(s) > 2048 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f {
			return ""
		}
	}
	return s
}

// cleanText is untrusted text: valid UTF-8, control characters but newline
// and tab dropped, token shapes redacted, clipped to n bytes.
func cleanText(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	return clip(tokenRE.ReplaceAllString(s, "[redacted]"), n)
}

// cleanName is untrusted text of one line.
func cleanName(s string, n int) string {
	return strings.ReplaceAll(strings.ReplaceAll(cleanText(s, n), "\n", " "), "\t", " ")
}
