// policy.go — what managers set at global (state "policy"; API.md
// §Identities and policy): which people may get bot tokens, which repos and
// accounts tokens may name, workflows and reruns, a person token's life —
// and the permission presets every token is cut from.
package main

import (
	"encoding/json"
	"net/http"
	"path"
	"slices"
	"strings"
)

type policy struct {
	BotForPeople    string   `json:"botForPeople"`    // off | own-access | on
	BotRepos        []string `json:"botRepos"`        // owner/name globs a bot token may name
	AllowWorkflows  bool     `json:"allowWorkflows"`  // workflows: write may be asked for
	AllowedAccounts []string `json:"allowedAccounts"` // the accounts tokens, reads and events may concern
	AllowRerun      bool     `json:"allowRerun"`      // offer checks.rerun (with the App's actions: write)
	PersonTTLMin    int      `json:"personTtlMin"`    // caps a person token's life (0: the epoch decides)
}

// personTTLFloor is the shortest personTtlMin: every minTtlSec allowed
// (up to maxTtlSec) is met.
const personTTLFloor = maxTTLSec / 60

func defaultPolicy() policy {
	return policy{BotForPeople: "off", BotRepos: []string{"*/*"}, AllowedAccounts: []string{}, AllowRerun: true}
}

// policy reads the policy: global's own state; in a person's partition the
// public half from conf (global re-checks everything it relays).
func (s *srv) policy() policy {
	p := defaultPolicy()
	if s.mode == modeUser {
		pub := s.public()
		p.BotForPeople, p.AllowWorkflows = pub.Policy.BotForPeople, pub.Policy.AllowWorkflows
		p.AllowedAccounts = append([]string{}, pub.Policy.AllowedAccounts...)
		if pub.Policy.BotRepos != nil { // absent: global's older shape (it still checks)
			p.BotRepos = append([]string{}, pub.Policy.BotRepos...)
		}
		if p.BotForPeople == "" {
			p.BotForPeople = "off"
		}
		return p
	}
	var raw json.RawMessage
	if s.state.Get("policy", &raw) == nil {
		_ = json.Unmarshal(raw, &p)
	}
	if p.BotRepos == nil {
		p.BotRepos = []string{}
	}
	if p.AllowedAccounts == nil {
		p.AllowedAccounts = []string{}
	}
	return p
}

func (p *policy) validate() error {
	switch p.BotForPeople {
	case "off", "own-access", "on":
	default:
		return refuse(refInvalid, "botForPeople is off, own-access or on")
	}
	for _, g := range p.BotRepos {
		if _, err := path.Match(g, "a/b"); err != nil || strings.Count(g, "/") != 1 {
			return refuse(refInvalid, "botRepos are owner/name globs (%q isn't)", clip(g, 80))
		}
	}
	for _, a := range p.AllowedAccounts {
		if !validLogin(a) {
			return refuse(refInvalid, "allowedAccounts are GitHub logins (%q isn't)", clip(a, 80))
		}
	}
	// Below maxTtlSec a consumer's minTtlSec could go unmet: a token would
	// live less than asked, never be reused, and be scoped anew each time.
	if p.PersonTTLMin != 0 && (p.PersonTTLMin < personTTLFloor || p.PersonTTLMin > 24*60) {
		return refuse(refInvalid, "personTtlMin is 0 (the epoch decides) or %d to 1440 minutes", personTTLFloor)
	}
	return nil
}

// accountAllowed: tokens, reads and events may concern this account.
func (p policy) accountAllowed(owner string) bool {
	return slices.ContainsFunc(p.AllowedAccounts, func(a string) bool { return strings.EqualFold(a, owner) })
}

// itemAllowed: a queued event may still be delivered or listed under the
// policy as it is now — its account allowed, and a tile's (for: global)
// repo within botRepos — so narrowing the policy stops what was queued
// before too.
func (p policy) itemAllowed(it *outItem) bool {
	return p.accountAllowed(ownerOf(it.Repo)) && (strings.HasPrefix(it.For, "user:") || p.botRepoAllowed(it.Repo))
}

// botRepoAllowed: a bot token (any caller) may name this repo.
func (p policy) botRepoAllowed(repo string) bool {
	for _, g := range p.BotRepos {
		if ok, _ := path.Match(strings.ToLower(g), strings.ToLower(repo)); ok {
			return true
		}
	}
	return false
}

// checkAccount refuses an account outside allowedAccounts.
func (p policy) checkAccount(owner string) error {
	if !p.accountAllowed(owner) {
		return refuse(refNotAllowed, "this scm-github's policy doesn't serve the account %s (a manager adds it to allowedAccounts)", owner)
	}
	return nil
}

// checkBotRepos refuses repos outside botRepos (and their accounts).
func (p policy) checkBotRepos(repos []string) error {
	for _, r := range repos {
		if err := p.checkAccount(ownerOf(r)); err != nil {
			return err
		}
		if !p.botRepoAllowed(r) {
			return refuse(refNotAllowed, "this scm-github's policy (botRepos) doesn't let the bot name %s", r)
		}
	}
	return nil
}

// The permission presets (docs/scm.md §Credentials): never administration;
// workflows: write only on request and under allowWorkflows.
func presetRead() map[string]string {
	return map[string]string{"contents": "read", "metadata": "read", "pull_requests": "read", "issues": "read",
		"checks": "read", "statuses": "read", "actions": "read"}
}

func presetWrite() map[string]string {
	p := presetRead()
	p["contents"], p["pull_requests"] = "write", "write"
	return p
}

var permRank = map[string]int{"read": 1, "write": 2}

// narrow computes a token's permissions: the preset for access, narrowed by
// ask (a key left out of ask keeps the preset; "none" drops it). A
// permission wider than the preset is 400 invalid; workflows: write is 403
// not-allowed without allowWorkflows. Global computes this itself for every
// relayed request.
func narrow(access string, ask map[string]string, allowWorkflows bool) (map[string]string, error) {
	var out map[string]string
	switch access {
	case "read":
		out = presetRead()
	case "write":
		out = presetWrite()
	default:
		return nil, refuse(refInvalid, "access is read or write")
	}
	for k, v := range ask {
		if k == "workflows" {
			if v == "none" || v == "" {
				continue
			}
			if v != "write" && v != "read" {
				return nil, refuse(refInvalid, "workflows is write")
			}
			if !allowWorkflows {
				return nil, refuse(refNotAllowed, "this scm-github's policy doesn't hand out workflows permission (allowWorkflows)")
			}
			out["workflows"] = v
			continue
		}
		have, ok := out[k]
		if !ok {
			return nil, refuse(refInvalid, "the permission %q isn't in the %s preset (a token may only narrow it)", clip(k, 40), access)
		}
		switch {
		case v == "none":
			delete(out, k)
		case permRank[v] == 0:
			return nil, refuse(refInvalid, "a permission is read, write or none (%s: %q)", k, clip(v, 20))
		case permRank[v] > permRank[have]:
			return nil, refuse(refInvalid, "%s: %s is wider than the %s preset's %s", k, v, access, have)
		default:
			out[k] = v
		}
	}
	out["metadata"] = "read" // GitHub always grants it
	return out, nil
}

func validLogin(s string) bool {
	if s == "" || len(s) > 39 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validRepo(r string) bool {
	o, n, ok := strings.Cut(r, "/")
	if !ok || !validLogin(o) || n == "" || len(n) > 100 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return n != "." && n != ".."
}

func ownerOf(repo string) string { o, _, _ := strings.Cut(repo, "/"); return o }
func nameOf(repo string) string  { _, n, _ := strings.Cut(repo, "/"); return n }

func (s *srv) handlePolicyGet(w http.ResponseWriter, r *http.Request, _ who) {
	writeJSON(w, http.StatusOK, s.policy())
}

// handlePolicyPut replaces the policy: the bot cache empties (tokens cut
// under the old policy aren't handed out again) and conf "public" follows.
func (s *srv) handlePolicyPut(w http.ResponseWriter, r *http.Request, _ who) {
	p := defaultPolicy()
	if err := readBody(r, &p); err != nil {
		fail(w, err)
		return
	}
	if p.BotRepos == nil {
		p.BotRepos = []string{}
	}
	if p.AllowedAccounts == nil {
		p.AllowedAccounts = []string{}
	}
	if err := p.validate(); err != nil {
		fail(w, err)
		return
	}
	if err := s.state.Put("policy", p); err != nil {
		fail(w, err)
		return
	}
	s.bot.clear()
	s.reposC.clear()
	if err := s.writePublic(pubTokens); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
