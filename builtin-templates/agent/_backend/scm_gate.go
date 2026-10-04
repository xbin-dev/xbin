// scm_gate.go — who a project's credential is, where it may be written, and
// who may name a repo for the bot (API.md §scm providers and credentials).
//
//   - The identity: policy.as, else `person` in a person's partition (their
//     own sign-in at the provider), `bot` elsewhere — the global instance
//     and an unpartitioned agent have no person to be (scmProjectAs).
//   - The gate (scmCredWhy, the pattern of harness_creds.go credWhy): a
//     credential goes into a sandbox only where everyone who can use that
//     sandbox may act through that identity for the project's repos. It is
//     checked before every write and every refresh (scm_creds.go), from the
//     sandbox as its manager reports it now — never from a label, which any
//     participant could set.
//   - The bot rule (scmBotAllowed): where a home's identity is the bot, the
//     binding grants the agent the bot's whole view, and the agent is its
//     only gatekeeper — naming a repo takes one of the agent's managers, or a
//     person the scm bot rule names with a matching repo glob. A person's
//     partition never reads the rule: the person's own identity decides.
package main

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// policyOf is p's policy, its defaults filled (unknown keys ignored here).
func scmPolicyOf(p *Project) ProjectPolicy {
	pol := defaultProjectPolicy()
	if p != nil && len(p.Policy) > 0 {
		_ = json.Unmarshal(p.Policy, &pol)
	}
	return pol
}

// scmProjectAs is the identity p's credentials and scm calls are made as.
func scmProjectAs(p *Project) string {
	if as := scmPolicyOf(p).As; as == scmAsPerson || as == scmAsBot {
		return as
	}
	if userMode() && (p.Kind == projPersonal || p.Kind == projMembership) {
		return scmAsPerson
	}
	return scmAsBot
}

// The gate's refusals (project_creds.why, StatusCred.why): a word for
// programs; scmWhyWords says it to people.
const (
	whyNoPerson      = "no-person"      // as person outside a person's partition (the global instance, unpartitioned)
	whyNotOwn        = "not-own"        // the project isn't this partition's person's
	whyKind          = "kind"           // a team definition: its seed never holds a credential
	whyShared        = "project-shared" // the project has members or team visibility (person)
	whyNotPrivate    = "box-shared"     // the sandbox isn't private, or isn't its owner's own
	whyNotHomed      = "not-homed"      // the sandbox isn't homed in this partition (or at this agent's own identity)
	whyHosted        = "hosted-used"    // a hosted (non-secure) conversation used it, or the sandbox it was forked from
	whySeedClone     = "seed-clone"     // cloned from a team's seed: the whole team can write there
	whyHome          = "home"           // its home directory can't be named in a helper line
	whyNotPart       = "not-participant"
	whyPolicyBot     = "policy"      // the bot in a person's partition without policy.as bot (or membersAsBot)
	whyUnknownBox    = "unknown-box" // the manager didn't say what the sandbox is
	whyBadIdentity   = "identity"    // an identity the agent doesn't know
	scmWhyGateFailed = "credentials can't go into %s: %s"
)

// scmWhyWords is why, said to people.
func scmWhyWords(why string) string {
	switch why {
	case whyNoPerson:
		return "a person's own sign-in is used only in their own space"
	case whyNotOwn:
		return "the project isn't yours"
	case whyKind:
		return "a team project's seed sandbox never holds a credential"
	case whyShared:
		return "the project is shared, and a person's sign-in goes only into a project of theirs alone"
	case whyNotPrivate:
		return "the sandbox is shared with others, or isn't yours"
	case whyNotHomed:
		return "the sandbox isn't homed in this space"
	case whyHosted:
		return "a non-secure (hosted) conversation has worked in it (or in the sandbox it was forked from), and its members could have left something there that reads a credential"
	case whySeedClone:
		return "it was cloned from a team's seed sandbox, which every team member can write to"
	case whyHome:
		return "this sandbox's home can't hold credentials (its path has characters a credential helper line can't carry)"
	case whyNotPart:
		return "someone who can use the sandbox may not act in the project"
	case whyPolicyBot:
		return "the project's identity isn't the bot (its policy says so)"
	case whyUnknownBox:
		return "its manager didn't say who it belongs to"
	case whyBadIdentity:
		return "the project names an identity this agent doesn't know"
	}
	return why
}

// scmHomeRe is what a sandbox's home must look like to be spliced into the
// credential helper's line (scmGitConfigOf): an absolute path of plain
// characters.
var scmHomeRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// scmCredWhy is why p's credential (as identity as) may not be written into
// sandbox ref, box as its manager reports it now ("" = it may). A sandbox
// other than p's own is a task's fork of it: the project's sandbox's history
// counts too.
func scmCredWhy(p *Project, as, ref string, box *sbxSandbox) string {
	if p == nil || box == nil {
		return whyUnknownBox
	}
	if p.Kind == projTeam {
		return whyKind
	}
	if !scmHomeRe.MatchString(box.Home) || strings.Contains(box.Home, "/../") || strings.HasSuffix(box.Home, "/..") {
		return whyHome
	}
	d := scmDB()
	used := func(r string) bool { return r != "" && (d == nil || d.hostedUsed(r)) }
	hosted := used(ref) || (p.SandboxRef != "" && p.SandboxRef != ref && used(p.SandboxRef))
	switch as {
	case scmAsPerson:
		switch {
		case !userMode():
			return whyNoPerson
		case p.Owner != runUser || runUser == "":
			return whyNotOwn
		case p.Kind != projPersonal && p.Kind != projMembership:
			return whyKind
		case p.Visibility != visPrivate || scmMemberCount(p.ID) != 0:
			return whyShared
		case !sandboxPrivate(box) || box.Owner.User != runUser:
			return whyNotPrivate
		case !homedHere(box):
			return whyNotHomed
		case hosted:
			return whyHosted
		case p.FromSeed:
			return whySeedClone
		}
		return ""
	case scmAsBot:
		if userMode() {
			if !homedHere(box) {
				return whyNotHomed
			}
			pol := scmPolicyOf(p)
			if pol.As != scmAsBot || (p.Kind == projMembership && !pol.MembersAsBot) {
				return whyPolicyBot
			}
		} else if o := box.Owner; o.Via != xbin.Self() || o.PartitionID != "" || (o.Partition != "" && o.Partition != "global") || box.Shared {
			return whyNotHomed
		}
		if why := scmBotUsers(p, box); why != "" {
			return why
		}
		if hosted {
			return whyHosted
		}
		return ""
	}
	return whyBadIdentity
}

// scmBotUsers: everyone who may use box may act in p (participant or
// above): its owner, each member, the team when it is team-visible — and it
// has no shares, whose people are the manager's to say (fail closed).
func scmBotUsers(p *Project, box *sbxSandbox) string {
	if sh := strings.TrimSpace(string(box.Shares)); sh != "" && sh != "null" && sh != "[]" && sh != "{}" {
		return whyNotPrivate
	}
	users := append([]string(nil), box.Members...)
	if box.Owner.User != "" { // "": the agent made it as itself, no person
		users = append(users, box.Owner.User)
	}
	for _, u := range users {
		if projectLevelOf(p, u) < lvParticipant {
			return whyNotPart
		}
	}
	switch box.Visibility {
	case "", visPrivate:
	case visTeam:
		if p.Visibility != visTeam || p.TeamRole != roleParticipant {
			return whyNotPart
		}
	default:
		return whyNotPrivate // a visibility a newer manager adds: shared with whom it says
	}
	return ""
}

// projectMemberCount is how many member rows p has (project_members); a
// table that can't be read counts as members (fail closed).
func scmMemberCount(pid int64) int {
	d := scmDB()
	if d == nil {
		return 1
	}
	var n int
	if err := d.q.QueryRow(`SELECT count(*) FROM project_members WHERE project_id=?`, pid).Scan(&n); err != nil {
		return 1
	}
	return n
}

// scmHostedUse is the other order of the gate's hosted-used clause
// (sandboxUse, at every call of a hosted conversation in a person's
// partition, before it does a thing there): a sandbox that holds a
// project's credential is refused to the hosted conversation — its members
// could have the agent read the files (or push with them) — and one that
// doesn't is noted as hosted-used (noteHostedUse), so no credential goes
// into it from then on. Under the sandbox's lock: a write either finished
// first (its row is live: refused) or finds the note (the gate refuses).
// A live row whose files couldn't be emptied counts; a table that can't be
// read refuses (fail closed). "" = it may use the sandbox.
func scmHostedUse(d *DB, ref, name string) string {
	defer scmHoldSandbox(ref)()
	var n int
	if err := d.q.QueryRow(`SELECT EXISTS(SELECT 1 FROM project_creds WHERE sandbox_ref=? AND state='live')`, ref).Scan(&n); err != nil || n != 0 {
		return fmt.Sprintf("a non-secure (hosted) conversation doesn't work in %s: a project's credential for its code host is there, "+
			"and the conversation's members could have the agent read it — create another sandbox for this conversation", name)
	}
	d.noteHostedUse(ref)
	return ""
}

// --- the bot rule ---------------------------------------------------------------------

const settingSCMBotRule = "scm_bot_rule"

// scmBotHome: this home's scm identity is the bot (the global instance, an
// unpartitioned agent).
func scmBotHome() bool { return !userMode() }

// loadBotRule is the bot home's rule (empty when none, or unreadable).
func scmLoadBotRule(d *DB) scmBotRule {
	r := scmBotRule{Users: []string{}, Repos: []string{}}
	if d == nil {
		return r
	}
	if raw := d.getSetting(settingSCMBotRule); raw != "" {
		_ = json.Unmarshal([]byte(raw), &r)
	}
	if r.Users == nil {
		r.Users = []string{}
	}
	if r.Repos == nil {
		r.Repos = []string{}
	}
	return r
}

// cleanBotRule checks and normalises a rule a manager PUTs.
func scmCleanBotRule(r scmBotRule) (scmBotRule, error) {
	out := scmBotRule{Users: []string{}, Repos: []string{}}
	if len(r.Users) > 200 || len(r.Repos) > 200 {
		return out, fmt.Errorf("at most 200 people and 200 repo patterns")
	}
	seen := map[string]bool{}
	for _, u := range r.Users {
		u = strings.TrimSpace(u)
		if !userIDRe.MatchString(u) {
			return out, fmt.Errorf("users: %q isn't a person's id (a login name)", u)
		}
		if !seen["u:"+u] {
			seen["u:"+u] = true
			out.Users = append(out.Users, u)
		}
	}
	for _, g := range r.Repos {
		g = strings.ToLower(strings.TrimSpace(g))
		owner, name, ok := strings.Cut(g, "/")
		if _, err := path.Match(g, ""); err != nil || !ok || owner == "" || name == "" || strings.Contains(name, "/") || len(g) > 200 {
			return out, fmt.Errorf("repos: %q isn't an owner/name pattern (a glob such as acme/* or acme/web)", g)
		}
		if !seen["r:"+g] {
			seen["r:"+g] = true
			out.Repos = append(out.Repos, g)
		}
	}
	return out, nil
}

// botRuleAllows: the rule names user and a pattern matching repo.
func (r scmBotRule) allows(user, repo string) bool {
	if user == "" || repo == "" {
		return false
	}
	named := false
	for _, u := range r.Users {
		named = named || u == user
	}
	if !named {
		return false
	}
	repo = strings.ToLower(repo)
	for _, g := range r.Repos {
		if ok, _ := path.Match(g, repo); ok {
			return true
		}
	}
	return false
}

func init() {
	// scmBotAllowed: the agent's managers (every element holding a grant to
	// it is one) and, at a bot home, a person the rule names for a repo it
	// matches — never someone viewed as (view-as), never at a person's
	// partition, where the rule isn't read.
	scmBotAllowed = func(w who, repo string) bool {
		if w.manager() {
			return true
		}
		d := scmDB()
		if !scmBotHome() || w.kind != whoUser || w.viewedBy != "" || d == nil {
			return false
		}
		return scmLoadBotRule(d).allows(w.user, repo)
	}
}
