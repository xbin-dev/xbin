package boot

// deployfeatures.go — what the tile-deployments API says this xbind speaks
// (State.features, NP-14-4), and the branch facts of a deployment row (D131).

import "github.com/xbin-dev/xbin/internal/deployments"

// branchFacts adds deployment name's assigned branch to its row, and the
// other branch it takes this time (D131); nothing for one without.
func branchFacts(row map[string]any, rec *deployments.Record, name string) {
	if b := rec.AssignedBranch(name); b != "" {
		row["branch"] = b
		if o := rec.BranchOverride(name); o != "" {
			row["branchOverride"] = o
		}
	}
}

// features is what this xbind speaks (NP-14-4): live-reload/1 once pausing
// is built, deployments/1 once adding is, branches/1 once assigned branches
// are (D131: the branch route, add's branch and newBranch, confirm
// "other-branch" on the ops that feed a deployment from the work tree — new
// body fields a client sends only when this is listed); none while the
// ship-dark switch is off (NP-14-5).
func (a *deploymentsAPI) features() []string {
	out := []string{}
	if !a.dp.OptInClosed && a.ops.registered(deployments.OpPause) {
		out = append(out, "live-reload/1")
	}
	if !a.dp.OptInClosed && a.ops.registered(deployments.OpAdd) {
		out = append(out, "deployments/1")
	}
	if !a.dp.OptInClosed && a.ops.registered(deployments.OpBranch) {
		out = append(out, "branches/1")
	}
	return out
}
