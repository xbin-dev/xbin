package main

// deploybranch.go — branch-assigned deployments in bx (D131, feature
// branches/1): the feature gate, the state's branch facts, `bx deployment
// branch`, and the report line naming both branches.

import (
	"fmt"
	"strings"
)

// branchImpact is Impact.branch: the target's assigned branch and the work
// tree's, and whether the request takes the work tree's this time.
type branchImpact struct {
	Deployment string `json:"deployment"`
	Assigned   string `json:"assigned"`
	WorkTree   string `json:"workTree"`
	Other      bool   `json:"other"`
}

// featureBranches is branch-assigned deployments (D131): the branch route,
// add's branch and newBranch, confirm:"other-branch". bx sends none of them
// to an xbind whose state doesn't list it (strict request decoding).
const featureBranches = "branches/1"

// speaks reports whether the xbind that answered lists feature.
func (st *deployState) speaks(feature string) bool {
	for _, f := range st.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// workTreeBranch is the work tree's branch as the state says it, "" for
// none or unknown.
func (st *deployState) workTreeBranch() string {
	if st.WorkTree == nil {
		return ""
	}
	return st.WorkTree.Branch
}

// branchAware reports whether any deployment of the tile has an assigned
// branch.
func (st *deployState) branchAware() bool {
	for _, d := range st.Deployments {
		if d.Branch != "" {
			return true
		}
	}
	return false
}

// branchNameOK is bx's check of a branch name before xbind's own (D131).
func branchNameOK(b string) bool {
	return b != "" && len(b) <= 200 && !strings.HasPrefix(b, "-") && !strings.HasPrefix(b, ".") &&
		!strings.Contains(b, "..") && strings.Trim(b, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+/-") == ""
}

// depBranch sets or clears the branch a deployment requires (D131): the
// work tree feeds it — saves while live reload follows it, attach, resume,
// reload now, a deploy of the work tree — only while it is on that branch.
func depBranch(cmd string, a dcArgs) error {
	arity := 3
	if a.has("--clear") {
		arity = 2
	}
	op, rest, err := depOp(cmd, "branch", "branch", a, arity)
	if err != nil {
		return err
	}
	b := ""
	switch {
	case a.has("--clear") && len(rest) > 0:
		return usageError(cmd, "--clear takes no branch")
	case !a.has("--clear") && len(rest) == 0:
		return usageError(cmd, "which branch? name it, or --clear")
	case !a.has("--clear"):
		if b = rest[0]; !branchNameOK(b) {
			return usageError(cmd, "%q: a branch name is letters, digits and . _ + / -, not starting with - or .", b)
		}
	}
	op.feature = featureBranches
	if b == "" {
		op.body["branch"] = nil
	} else {
		op.body["branch"] = b
	}
	op.report = func(st *deployState, x string, imp *deployImpact) deployReport {
		if b == "" {
			r := deployReport{question: "Clear " + x + "'s branch"}
			r.add("Branch", x+" takes the work tree on any branch again")
			return r
		}
		r := deployReport{question: "Assign " + x + " branch " + b}
		line := x + " requires branch " + b + ": the work tree feeds it only while it is on " + b
		if ib := imp.Branch; ib != nil && ib.WorkTree != b {
			line += "; the work tree is on " + firstOf(ib.WorkTree, "no branch") + " now"
		}
		r.add("Branch", line)
		if imp.PausesLiveReload {
			r.add("Pauses", "live reload is on "+x+": the next save pauses it, until you check out "+b)
		}
		return r
	}
	op.result = func(_ []byte, _, _ *deployState, x string) string {
		if b == "" {
			return "Cleared " + x + "'s branch."
		}
		return x + " requires branch " + b + "."
	}
	return runDeployOp(op, a)
}

// branchLine says what the dry run found of the target's assigned branch
// and the work tree's (D131); "" for a target without one.
func branchLine(how string, imp *deployImpact) string {
	b := imp.Branch
	if b == nil || b.Assigned == "" {
		return ""
	}
	wt := b.WorkTree
	if wt == "" {
		wt = "no branch"
	}
	if !b.Other || wt == b.Assigned {
		return fmt.Sprintf("%s requires branch %s; the work tree is on %s", b.Deployment, b.Assigned, wt)
	}
	if how == "resume" || how == "attach" || how == "add" {
		return fmt.Sprintf("%s requires branch %s, and the work tree is on %s: it takes %s this time, until live reload moves or the branch changes again", b.Deployment, b.Assigned, wt, wt)
	}
	return fmt.Sprintf("%s requires branch %s, and the work tree is on %s: this ships %s's work tree this time", b.Deployment, b.Assigned, wt, wt)
}
