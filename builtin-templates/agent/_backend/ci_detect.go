// ci_detect.go — the branches a coding session pushed (API.md §CI in the
// conversation, "What is watched"): at the end of any run's turn — a root
// conversation's, or a coding agent child's below it — that works in a
// sandbox while an scm provider is bound, one command in that run's sandbox
// reads git's own record of pushes: a remote-tracking ref whose newest
// reflog entry says "update by push", written since the turn began. Each
// one at a host a bound provider serves becomes (or refreshes) a watch of
// the conversation, `run_id` the run that pushed.
//
// Nothing of the command's output is kept: each line is parsed in memory,
// its remote URL's userinfo dropped before anything is kept, answered or
// logged (an https://user:token@host/… remote is common). A project's
// runs are left to its refs job (one branch, one watch). Where the home's
// identity at the provider is the bot (the global instance, an
// unpartitioned agent), a pushed branch is watched only for a repo a
// project of this home names with the conversation's owner taking part in
// it, or one the scm bot rule lets the owner name — and in a conversation
// others share, only the first: a faked remote and reflog can't make the bot
// read a repo nobody authorised.
package main

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func init() { turnEndHooks = append(turnEndHooks, ciTurnEnd) }

// ciPushScript lists what was pushed since $SINCE (unix seconds) from the
// repos at or below the working directory: toplevel, branch, sha, remote URL.
const ciPushScript = `set -eu
for top in $( { git rev-parse --show-toplevel 2>/dev/null || find . -maxdepth 3 -name .git -prune -print | sed 's#/\.git$##'; } | sort -u); do
  git -C "$top" for-each-ref --format='%(refname) %(objectname)' refs/remotes |
  while read -r ref sha; do
    case "$ref" in */HEAD) continue ;; esac
    line=$(git -C "$top" reflog show --date=unix --format='%gd %gs' -n 1 "$ref" 2>/dev/null || true)
    case "$line" in *"update by push"*) ;; *) continue ;; esac
    at=$(printf '%s' "$line" | sed -n 's/.*@{\([0-9]*\)}.*/\1/p')
    [ "${at:-0}" -ge "$SINCE" ] || continue
    rest=${ref#refs/remotes/}; remote=${rest%%/*}; br=${rest#*/}
    printf '%s\t%s\t%s\t%s\n' "$top" "$br" "$sha" "$(git -C "$top" remote get-url "$remote")"
  done
done
`

// ciDetectTimeout bounds the command (the spec's one Run of 10 s).
const ciDetectTimeout = 10 * time.Second

// ciPush is one pushed branch, as kept: no toplevel, no userinfo.
type ciPush struct {
	Host, Repo, Branch, SHA string
}

// ciTurnEnd (turnEndHooks): a run with a sandbox ended its turn while an
// scm provider is bound — look for what it pushed, after the commit and off
// the engine's path. A project's runs (its refs job covers them) and
// hosted ones (no hook reaches them) are left alone.
func ciTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if run == nil || len(scmBound()) == 0 {
		return
	}
	cur, err := t.getRun(run.ID)
	if err != nil {
		return
	}
	root := cur
	if rid := rootOf(cur); rid != cur.ID {
		if root, err = t.getRun(rid); err != nil {
			return
		}
	}
	if root.Origin == originProject || cur.Origin == originProject {
		return
	}
	cfg, err := t.runConfig(cur.ID)
	if err != nil {
		return
	}
	var ref, cwd, by string
	switch {
	case cur.Engine == engineHarness && cfg.Harness != nil && cfg.Harness.Ref != "":
		ref, cwd, by = cfg.Harness.Ref, cfg.Harness.Cwd, cfg.Harness.By
	case cfg.Sandbox != nil && cfg.Sandbox.Ref != "":
		ref, cwd, by = cfg.Sandbox.Ref, cfg.Sandbox.Cwd, cfg.Sandbox.By
	default:
		return
	}
	since := cur.TurnStarted
	if since <= 0 {
		since = time.Now().Add(-time.Hour).Unix()
	}
	d := ciBase(t)
	runID, rootID, owner := cur.ID, root.ID, root.Owner
	t.AfterCommit(func() {
		ciGo(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			ciDetect(ctx, d, runID, rootID, owner, ref, cwd, by, since)
		})
	})
}

// ciDetect runs the command in the run's sandbox — only while it is
// running: never one that idled to a stop (an exec would start it) — and
// watches each pushed branch a bound provider serves (and, at a bot home,
// that the owner may have the bot read).
func ciDetect(ctx context.Context, d *DB, runID, root int64, owner, ref, cwd, by string, since int64) {
	conn, id, err := sbxDialRef(ref, sbxUserOf(binderWho(by)))
	if err != nil {
		return
	}
	// A sandbox that isn't running pushed nothing this turn, and an exec
	// would start it (a stopped sandbox starts on one): left as it is.
	if box, err := conn.Get(ctx, id); err != nil || box == nil || box.State != "running" {
		return
	}
	res, err := conn.Run(ctx, id, sbxRunReq{Argv: []string{"sh", "-c", ciPushScript}, Cwd: cwd,
		Env: map[string]string{"SINCE": strconv.FormatInt(since, 10)}, TimeoutMs: int(ciDetectTimeout / time.Millisecond), MaxOutput: 64 << 10})
	if err != nil || res == nil || res.Stdout == nil {
		return
	}
	pushes := ciParsePushes(res.Stdout.Head + res.Stdout.Tail)
	res = nil // the output is never kept
	if len(pushes) == 0 {
		return
	}
	for _, p := range pushes {
		scm := ciProviderFor(ctx, p.Host)
		if scm == "" || !ciMayWatchPushed(d, root, owner, scm, p.Repo) {
			continue
		}
		w := &ciWatch{RootRun: root, RunID: runID, Source: ciPushed, SCM: scm, Host: p.Host, Repo: p.Repo, Ref: p.Branch, SHA: p.SHA}
		_ = d.Tx(func(t *DB) error {
			x, created, err := ciUpsert(t, w)
			switch {
			case err != nil:
				logf("run %d: watching CI of %s %s: %v", runID, p.Repo, p.Branch, err)
			case created:
				ciStarted(t, x.ID)
			default:
				ciReadLater(t, x.ID)
			}
			return nil
		})
	}
}

// ciBranchRe is a branch name git would take, and nothing that could be
// mistaken for anything else (no spaces, no control characters).
var ciBranchRe = regexp.MustCompile(`^[A-Za-z0-9._/+@=-]{1,250}$`)

// ciScpRe is git's scp-like remote: [user@]host:path.
var ciScpRe = regexp.MustCompile(`^(?:[^@/]+@)?([A-Za-z0-9.-]+):([^/].*)$`)

// ciParsePushes reads the command's lines: each a branch at a repo on a
// host, its userinfo gone. A line that doesn't parse is skipped.
func ciParsePushes(out string) []ciPush {
	var res []ciPush
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 4 {
			continue
		}
		br, sha := f[1], ciSHA(f[2])
		host, repo := ciRemote(f[3])
		if host == "" || repo == "" || sha == "" || len(sha) < 40 || !ciBranchRe.MatchString(br) || strings.Contains(br, "..") {
			continue
		}
		key := host + " " + strings.ToLower(repo) + " " + br
		if seen[key] {
			continue
		}
		seen[key] = true
		res = append(res, ciPush{Host: host, Repo: repo, Branch: br, SHA: sha})
	}
	return res
}

// ciRemote is a remote URL's host and owner/name, its userinfo dropped
// ("" when it names no repo there).
func ciRemote(raw string) (host, repo string) {
	raw = strings.TrimSpace(raw)
	var path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", ""
		}
		u.User = nil
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git" {
			return "", ""
		}
		host, path = u.Hostname(), u.Path
	} else if m := ciScpRe.FindStringSubmatch(raw); m != nil {
		host, path = m[1], m[2]
	} else {
		return "", ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !validRepo(path) {
		return "", ""
	}
	return strings.ToLower(host), path
}

// ciProviderFor is the bound provider serving host ("" none).
func ciProviderFor(ctx context.Context, host string) string {
	for _, name := range scmBound() {
		api, err := scmFor(name)
		if err != nil {
			continue
		}
		h, err := api.Hello(ctx)
		if err != nil || !h.has(scmCapChecks) {
			continue
		}
		for _, x := range h.Hosts {
			if strings.EqualFold(x, host) {
				return name
			}
		}
	}
	return ""
}

// ciMayWatchPushed: root's owner may have this home read repo at provider
// scm. In a person's partition their own identity decides (always). At a
// bot home: a project of this home naming the repo in which the owner takes
// part — or, in a conversation nobody else shares, the scm bot rule. A turn
// end carries no request, so whether the owner manages the agent isn't
// known here: a manager's own pushes need the rule (or a project) too.
func ciMayWatchPushed(d *DB, root int64, owner, scm, repo string) bool {
	if userMode() {
		return true
	}
	if ciProjectNames(d, owner, scm, repo) {
		return true
	}
	if ciShared(d, root) {
		return false
	}
	return scmBotAllowed(binderWho(owner), repo)
}

// ciProjectNames: an active project of this home at provider scm names
// repo, and owner is a participant (or its owner) there.
func ciProjectNames(d *DB, owner, scm, repo string) bool {
	if owner == "" {
		return false
	}
	ps, err := d.projectsWhere(`WHERE state='active' AND scm=? AND id IN (SELECT project_id FROM project_repos WHERE lower(repo)=lower(?))`, scm, repo)
	if err != nil {
		return false
	}
	for _, p := range ps {
		if projectLevelOf(p, owner) >= lvParticipant {
			return true
		}
	}
	return false
}

// ciShared: others may see root — team-visible, or with members.
func ciShared(d *DB, root int64) bool {
	var vis string
	var members int
	if d.q.QueryRow(`SELECT visibility FROM runs WHERE id=?`, root).Scan(&vis) != nil {
		return true
	}
	_ = d.q.QueryRow(`SELECT count(*) FROM run_members WHERE run_id=?`, root).Scan(&members)
	return vis == visTeam || members > 0
}
