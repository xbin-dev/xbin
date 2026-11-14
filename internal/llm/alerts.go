package llm

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
)

// alerts.go — what the connector tells the workspace's admins through the
// alert chain (GET /api/xbin/alerts; boot chains Alerts into the broker's
// AdminAlerts, and into AdminShellAlerts for the shell an admin drives),
// and what a person reads about their own quota.
//
//   - a quota that is reached — one that stops calls, or one whose action
//     is warn — is an alert naming the person (and the org, when the limit
//     is an org's), the limit and when it resets, with the page that
//     changes it. It goes when the window resets or an admin raises the
//     quota; nothing is stored;
//   - while no upstream exists and no admin has touched the connector, one
//     alert says where it is set up. An admin dismisses it for the
//     workspace (POST /llm/setup/dismiss), and it never comes back.
//
// Every message names /xbin/llm in words: a shell or an admin console from
// before the connector shows an alert's text and nothing else.

// PagePath is xbind's own page for the connector (internal/server
// llmpage.go), which every alert names.
const PagePath = "/xbin/llm"

// The alert kinds.
const (
	AlertQuota = "llm-quota" // a person's, or the workspace's, quota is reached
	AlertSetup = "llm-setup" // no upstream is configured yet
)

// SetupDismissRoute is the /api/xbin route a POST to which dismisses the
// setup alert.
const SetupDismissRoute = "/llm/setup/dismiss"

// maxQuotaAlerts: past this many people at a limit, one alert names them
// together.
const maxQuotaAlerts = 5

// Alert is one notice for the workspace's admins.
type Alert struct {
	Level   string // warn | crit
	Kind    string
	Message string
	Dismiss string // the /api/xbin route that dismisses it ("": it goes with its cause)
}

// Alerts are the connector's notices for admins, as they stand now.
func (s *Service) Alerts() []Alert {
	snap := s.current()
	var out []Alert
	if s.setupOpen(snap) {
		out = append(out, Alert{Level: "warn", Kind: AlertSetup, Dismiss: SetupDismissRoute,
			Message: "No LLM is set up for this workspace: its agents, tiles and coding tools can share one provider setup, metered per person — " +
				"a workspace admin adds a provider at " + PagePath + " (the admin console's workspace → LLM, or `bx llm upstream add`). Dismiss this to leave it off"})
	}
	return append(out, s.quotaAlerts(snap)...)
}

// ---- quotas -------------------------------------------------------------------

// everyone copies each person's window and the workspace's.
func (m *meter) everyone(now time.Time) (people map[string]window, workspace window) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(now)
	people = make(map[string]window, len(m.win))
	for k, w := range m.win {
		if k == totalKey {
			workspace = *w
		} else {
			people[k] = *w
		}
	}
	return people, workspace
}

// reached is the limit that speaks for a set: the first that stops, else
// the first that warns; more counts the other reached ones.
func reached(limits []Limit) (l *Limit, more int) {
	v := verdictOf(limits)
	if l = v.Stop; l == nil {
		l = v.Warn
	}
	for _, x := range limits {
		if x.State != "ok" {
			more++
		}
	}
	return l, max(more-1, 0)
}

func (s *Service) quotaAlerts(snap *snapshot) []Alert {
	if !snap.configured() {
		return nil
	}
	now := s.now()
	people, total := s.meter.everyone(now)
	const fix = " — raise or lift it at " + PagePath + " (the admin console's workspace → LLM)"
	var out []Alert
	if l, more := reached(judge(snap.quotas.workspaceLimits(), total, now)); l != nil {
		level, effect := "crit", "every call is refused"
		if l.State == ActionWarn {
			level, effect = "warn", "calls still go through (this quota only warns)"
		}
		who, how := "", fix+", or `bx llm quota set workspace …`"
		if snap.locked("quotas.workspace") {
			who, how = " ("+LockReason+")", " — your host holds this quota; usage is at "+PagePath
		}
		out = append(out, Alert{Level: level, Kind: AlertQuota,
			Message: "LLM quota: the workspace reached its " + l.words() + who + moreWords(more) + ": " + effect + " until " + resetWords(l.Resets) + how})
	}
	type hit struct {
		user string
		l    Limit
		more int
	}
	var hits []hit
	for _, id := range slices.Sorted(maps.Keys(people)) {
		var orgs []string
		if s.opt.OrgsOf != nil && id != OwnerUser {
			orgs = s.opt.OrgsOf(id)
		}
		if l, more := reached(judge(snap.quotas.resolve(id, orgs), people[id], now)); l != nil {
			hits = append(hits, hit{id, *l, more})
		}
	}
	if len(hits) > maxQuotaAlerts {
		names := make([]string, 0, maxQuotaAlerts)
		stopped := 0
		for i, h := range hits {
			if i < maxQuotaAlerts {
				names = append(names, personWords(h.user))
			}
			if h.l.State == ActionStop {
				stopped++
			}
		}
		return append(out, Alert{Level: "warn", Kind: AlertQuota,
			Message: fmt.Sprintf("LLM quota: %d people have reached a limit (%s and %d more); the calls of %d are refused%s", len(hits),
				strings.Join(names, ", "), len(hits)-len(names), stopped, fix)})
	}
	for _, h := range hits {
		effect := "their calls are refused"
		if h.l.State == ActionWarn {
			effect = "their calls still go through (this quota only warns)"
		}
		out = append(out, Alert{Level: "warn", Kind: AlertQuota,
			Message: "LLM quota: " + personWords(h.user) + " reached their " + h.l.words() + " (" + sourceWords(h.l.Source) + ")" + moreWords(h.more) + ": " +
				effect + " until " + resetWords(h.l.Resets) + fix + ", or `bx llm quota set user:" + h.user + " …`"})
	}
	return out
}

// words: "daily limit of 2M tokens", "monthly cost limit of $50.00".
func (l Limit) words() string {
	per := "daily"
	if l.Name == "tokensPerMonth" || l.Name == "costPerMonth" {
		per = "monthly"
	}
	if l.Name == "costPerDay" || l.Name == "costPerMonth" {
		return per + " cost limit of $" + strconv.FormatFloat(l.Value, 'f', 2, 64)
	}
	return per + " limit of " + tokenWords(int64(l.Value)) + " tokens"
}

// tokenWords writes a token count the way bx llm takes one: 2M, 200k, 1500.
func tokenWords(n int64) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return strconv.FormatInt(n/1_000_000, 10) + "M"
	case n >= 1000 && n%1000 == 0:
		return strconv.FormatInt(n/1000, 10) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func personWords(user string) string {
	if user == OwnerUser {
		return "the owner token"
	}
	return user
}

// sourceWords says where a person's limit comes from.
func sourceWords(source string) string {
	if org, ok := strings.CutPrefix(source, "org:"); ok {
		return "the quota of org " + org
	}
	if source == "user" {
		return "their own quota"
	}
	return "the default quota"
}

func moreWords(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return ", and one more limit"
	}
	return ", and " + strconv.Itoa(n) + " more limits"
}

// resetWords: "2026-11-15 00:00 UTC".
func resetWords(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "it resets"
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// ownQuota is a person's own limits as they stand, for GET /llm when their
// direct calls aren't admitted (the people switch, a connector that is off):
// the calls tiles make for them count against the same limits, so the
// limits are theirs to read either way. nil for a component or an LLM key.
func (s *Service) ownQuota(r *http.Request, snap *snapshot) *QuotaView {
	p := auth.PrincipalOf(r)
	user := p.UserID
	if p.Owner {
		user = OwnerUser
	}
	if user == "" || p.Component != "" || p.Via == "llm" {
		return nil
	}
	q := s.quotaView(snap, Caller{User: user})
	return &q
}

// ---- the setup alert ------------------------------------------------------------

// setupFile marks the setup alert dismissed (data/llm/setup.json). Its
// presence is the state: nothing is cached, so removing the file brings the
// alert back.
const setupFile = "setup.json"

// setupOpen: nothing is configured, an admin could configure it, none has
// touched the connector, and nobody dismissed the alert.
func (s *Service) setupOpen(snap *snapshot) bool {
	if snap.configured() || snap.lockAll {
		return false
	}
	s.mu.Lock()
	touched := s.store.Set != 0 || s.storeErr != nil
	s.mu.Unlock()
	if touched {
		return false
	}
	_, err := os.Stat(filepath.Join(s.opt.Dir, setupFile))
	return os.IsNotExist(err)
}

// apiDismissSetup — POST /llm/setup/dismiss: the setup alert is gone for
// the workspace.
func (s *Service) apiDismissSetup(w http.ResponseWriter, r *http.Request) {
	// an admin — or the shell an admin drives, which shows the alert with
	// its Dismiss (a shell posts with its own token)
	if p := auth.PrincipalOf(r); (s.opt.AdminShell == nil || !s.opt.AdminShell(p)) && !s.admin(w, r) {
		return
	}
	if err := os.MkdirAll(s.opt.Dir, 0o700); err != nil {
		writeErr(w, err)
		return
	}
	b, _ := json.Marshal(map[string]any{"dismissed": s.now().Unix(), "by": auth.PrincipalOf(r).From()})
	if err := fsutil.WriteFileAtomic(filepath.Join(s.opt.Dir, setupFile), append(b, '\n'), 0o600); err != nil {
		writeErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
