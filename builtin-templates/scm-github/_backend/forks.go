// forks.go — a CI event's ref.branch is a branch of this repo, never a
// fork's (API.md §7). A pull request and a workflow run say whose their
// head is; a check suite, a check run and a job don't — a fork's pull
// request runs CI here under the fork's branch name, and GitHub lists no
// pull request for it. Their head branch is kept only once shown to be
// this repo's: a pull request they list with its head here (normalize),
// the run's head repo (from its workflow_run, else GitHub's run), or the
// branch's head here being the commit. Anything else — a fork, an answer
// GitHub didn't give — leaves ref.branch empty, and subscriptions match by
// pull request or not at all.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	proveWithin = 5 * time.Second  // GitHub waits 10 s for the 202
	headFor     = 10 * time.Minute // a branch's head being the commit, as last read
	headMissFor = time.Minute      // its not being it (or no such branch)
	runFor      = 24 * time.Hour   // a run's head repo never changes
	proofsMax   = 5000
)

// proofRec is a cached answer: a run's head repo ("" none), or whether a
// branch's head is a commit ("1" or "").
type proofRec struct {
	val string
	at  int64
}

// proveBranches settles each event's unproven head branch: kept (topic and
// summary say it) when it is this repo's, dropped otherwise.
func (s *srv) proveBranches(ctx context.Context, evs []*event) {
	h := s.ev()
	now := s.now().UnixMilli()
	for _, e := range evs {
		if e.Kind == kindWorkflow && e.runID > 0 && e.runHead != "" {
			h.putProof(runProofKey(e.Repo, e.runID), e.runHead, now)
		}
	}
	var cancel context.CancelFunc
	for _, e := range evs {
		br := e.unproven
		e.unproven = ""
		if br == "" || e.Ref.SHA == "" || !h.watched(e.Repo) { // no one gets it: nothing to ask GitHub
			continue
		}
		if cancel == nil {
			ctx, cancel = context.WithTimeout(ctx, proveWithin)
			defer cancel()
		}
		own, known := false, false
		if e.Kind == kindJob && e.runID > 0 {
			var head string
			if head, known = s.runHeadRepo(ctx, e.Repo, e.runID, now); known {
				own = strings.EqualFold(head, e.Repo)
			}
		}
		if !known {
			own = s.branchHeadIs(ctx, e.Repo, br, e.Ref.SHA, now)
		}
		if !own {
			continue
		}
		e.Ref.Branch = br
		if !containsFold(e.branches, br) {
			e.branches = append([]string{br}, e.branches...)
		}
		e.Topic, e.Summary = topicOf(e.SCM.Host, e), summaryOf(e)
	}
}

// watched says whether any subscription is on repo.
func (h *hub) watched(repo string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	for _, sub := range h.subs {
		if strings.EqualFold(sub.Repo, repo) {
			return true
		}
	}
	return false
}

func runProofKey(repo string, id int64) string {
	return "run|" + strings.ToLower(repo) + "|" + idStr(id)
}

// runHeadRepo is a workflow run's head repo: as its workflow_run said, else
// as GitHub's run says. known false: GitHub didn't answer.
func (s *srv) runHeadRepo(ctx context.Context, repo string, id, now int64) (string, bool) {
	key := runProofKey(repo, id)
	if v, ok := s.ev().proof(key, runFor, now); ok {
		return v, true
	}
	auth, err := s.instAuth(ctx, ownerOf(repo), nameOf(repo), "read")
	if err != nil {
		return "", false
	}
	var run struct {
		HeadRepository *ghHookRepo `json:"head_repository"`
	}
	if _, err := s.gh.call(ctx, auth, http.MethodGet, s.repoBase(repo)+"/actions/runs/"+idStr(id), nil, &run); err != nil {
		return "", false
	}
	head := ""
	if run.HeadRepository != nil && validRepo(run.HeadRepository.FullName) {
		head = run.HeadRepository.FullName
	}
	s.ev().putProof(key, head, now)
	return head, true
}

// branchHeadIs says whether a branch of this repo has sha at its head (no
// such branch, or no answer: false).
func (s *srv) branchHeadIs(ctx context.Context, repo, branch, sha string, now int64) bool {
	key := "head|" + strings.ToLower(repo) + "|" + branch + "|" + sha
	if v, ok := s.ev().proof(key, headFor, now); ok && (v == "1" || s.ev().fresh(key, headMissFor, now)) {
		return v == "1"
	}
	auth, err := s.instAuth(ctx, ownerOf(repo), nameOf(repo), "read")
	if err != nil {
		return false
	}
	var b struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	_, err = s.gh.call(ctx, auth, http.MethodGet, s.repoBase(repo)+"/branches/"+pathEsc(branch), nil, &b)
	switch {
	case err == nil:
		own := strings.EqualFold(b.Commit.SHA, sha)
		s.ev().putProof(key, map[bool]string{true: "1"}[own], now)
		return own
	case isRefusal(err, refNotFound):
		s.ev().putProof(key, "", now)
	}
	return false
}

func (h *hub) proof(key string, ttl time.Duration, now int64) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.proofs[key]
	if !ok || now-p.at >= ttl.Milliseconds() {
		return "", false
	}
	return p.val, true
}

func (h *hub) fresh(key string, ttl time.Duration, now int64) bool {
	_, ok := h.proof(key, ttl, now)
	return ok
}

// putProof keeps an answer; at the cap the stale ones go, then all.
func (h *hub) putProof(key, val string, now int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.proofs) >= proofsMax {
		for k, p := range h.proofs {
			if now-p.at >= headFor.Milliseconds() {
				delete(h.proofs, k)
			}
		}
		if len(h.proofs) >= proofsMax {
			h.proofs = map[string]proofRec{}
		}
	}
	h.proofs[key] = proofRec{val, now}
}
