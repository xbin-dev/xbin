package llm

import (
	"fmt"
	"strconv"
	"time"
)

// quota.go — quotas (docs/llm.md "Quotas"): how much one person, and the
// workspace as a whole, may spend through the connector per UTC day and
// per UTC month, in tokens (input + output) and in USD (where the model has
// a price).
//
// Which quota is a person's:
//
//	their own override (quotas.users)
//	→ else their orgs' overrides (quotas.orgs): the most generous per limit
//	→ else the workspace default (quotas.default)
//
// each limit on its own: a user override that sets only tokensPerDay takes
// the other three limits from the orgs or the default. A person in several
// orgs gets, for each limit, the largest any of their orgs sets (unlimited
// beats every number): an org override is a grant to its members, and
// grants add up as access does. The user-level override always wins, in
// either direction.
//
// quotas.workspace is checked for every call beside the caller's own, and
// is the only quota of a call with no acting person (a tile's backend that
// isn't a person's partition).
//
// A quota is checked before a call starts, against what is already
// metered: a call in flight finishes, so a limit can be passed by the calls
// running when it is reached.

// Quota is a set of limits. A limit of 0 says nothing (the next source
// decides); Unlimited (-1) lifts it.
type Quota struct {
	TokensPerDay   int64   `json:"tokensPerDay,omitempty"`
	TokensPerMonth int64   `json:"tokensPerMonth,omitempty"`
	CostPerDay     float64 `json:"costPerDay,omitempty"`   // USD
	CostPerMonth   float64 `json:"costPerMonth,omitempty"` // USD
	// Action is what reaching a limit does: stop (the default) refuses the
	// call with 429; warn lets it through, marked.
	Action string `json:"action,omitempty"`
}

// Unlimited as a limit's value lifts the limit a lower source set.
const Unlimited = -1

// The actions.
const (
	ActionStop = "stop"
	ActionWarn = "warn"
)

// QuotaRules are every quota of a workspace.
type QuotaRules struct {
	Workspace *Quota           `json:"workspace,omitempty"` // everyone together
	Default   *Quota           `json:"default,omitempty"`   // each person
	Users     map[string]Quota `json:"users,omitempty"`
	Orgs      map[string]Quota `json:"orgs,omitempty"`
}

func (q Quota) validate(what string) error {
	for _, v := range []float64{float64(q.TokensPerDay), float64(q.TokensPerMonth), q.CostPerDay, q.CostPerMonth} {
		if v < 0 && v != Unlimited {
			return fmt.Errorf("%s: a limit is a positive number, 0 (not set) or -1 (unlimited)", what)
		}
	}
	switch q.Action {
	case "", ActionStop, ActionWarn:
		return nil
	}
	return fmt.Errorf("%s: action %q — stop or warn", what, q.Action)
}

func (q Quota) empty() bool { return q == Quota{} }

func (r QuotaRules) validate() error {
	if r.Workspace != nil {
		if err := r.Workspace.validate("quotas.workspace"); err != nil {
			return err
		}
	}
	if r.Default != nil {
		if err := r.Default.validate("quotas.default"); err != nil {
			return err
		}
	}
	for id, q := range r.Users {
		if id == "" {
			return fmt.Errorf("quotas.users: an empty user id")
		}
		if err := q.validate("quotas.users." + id); err != nil {
			return err
		}
	}
	for id, q := range r.Orgs {
		if id == "" {
			return fmt.Errorf("quotas.orgs: an empty org id")
		}
		if err := q.validate("quotas.orgs." + id); err != nil {
			return err
		}
	}
	return nil
}

func (r QuotaRules) clone() QuotaRules {
	out := QuotaRules{Users: cloneMap(r.Users), Orgs: cloneMap(r.Orgs)}
	if r.Workspace != nil {
		q := *r.Workspace
		out.Workspace = &q
	}
	if r.Default != nil {
		q := *r.Default
		out.Default = &q
	}
	return out
}

// Limit is one limit in force, and where it came from.
type Limit struct {
	Name   string  `json:"name"`             // tokensPerDay | tokensPerMonth | costPerDay | costPerMonth
	Value  float64 `json:"value"`            // tokens, or USD
	Source string  `json:"source"`           // user | org:<id> | default | workspace
	Action string  `json:"action"`           // stop | warn
	Used   float64 `json:"used"`             // so far in the limit's window
	Resets string  `json:"resets,omitempty"` // RFC 3339, UTC: when the window ends
	State  string  `json:"state"`            // ok | warn | stop
}

// limitNames in checking order.
var limitNames = []string{"tokensPerDay", "tokensPerMonth", "costPerDay", "costPerMonth"}

func (q Quota) limit(name string) float64 {
	switch name {
	case "tokensPerDay":
		return float64(q.TokensPerDay)
	case "tokensPerMonth":
		return float64(q.TokensPerMonth)
	case "costPerDay":
		return q.CostPerDay
	}
	return q.CostPerMonth
}

// source is one place a person's limits may come from.
type source struct {
	name string
	q    Quota
}

// resolve is the person's quota: for each limit the first of the user's
// override, the orgs' overrides (the most generous) and the default that
// sets it. The action comes the same way.
func (r QuotaRules) resolve(user string, orgs []string) []Limit {
	var own, def *source
	if q, ok := r.Users[user]; ok && user != "" {
		own = &source{"user", q}
	}
	if r.Default != nil {
		def = &source{"default", *r.Default}
	}
	var orgSrc []source
	for _, o := range orgs {
		if q, ok := r.Orgs[o]; ok {
			orgSrc = append(orgSrc, source{"org:" + o, q})
		}
	}
	// a limit's action: its own source's, else the user override's, the
	// orgs' (warn, the more generous, when any org says so), the default's
	action := func(first *source) string {
		for _, s := range []*source{first, own} {
			if s != nil && s.q.Action != "" {
				return s.q.Action
			}
		}
		a := ""
		for _, s := range orgSrc {
			if s.q.Action == ActionWarn {
				return ActionWarn
			}
			if s.q.Action != "" {
				a = s.q.Action
			}
		}
		if a == "" && def != nil {
			a = def.q.Action
		}
		if a == "" {
			a = ActionStop
		}
		return a
	}
	var out []Limit
	for _, name := range limitNames {
		if own != nil && own.q.limit(name) != 0 {
			if v := own.q.limit(name); v != Unlimited {
				out = append(out, Limit{Name: name, Value: v, Source: own.name, Action: action(own)})
			}
			continue
		}
		best, bestSrc, unlimited := 0.0, (*source)(nil), false
		for i := range orgSrc {
			switch v := orgSrc[i].q.limit(name); {
			case v == Unlimited:
				unlimited = true
			case v > best:
				best, bestSrc = v, &orgSrc[i]
			}
		}
		if unlimited {
			continue
		}
		if bestSrc != nil {
			out = append(out, Limit{Name: name, Value: best, Source: bestSrc.name, Action: action(bestSrc)})
			continue
		}
		if def != nil && def.q.limit(name) > 0 {
			out = append(out, Limit{Name: name, Value: def.q.limit(name), Source: def.name, Action: action(def)})
		}
	}
	return out
}

// workspaceLimits are quotas.workspace's limits.
func (r QuotaRules) workspaceLimits() []Limit {
	if r.Workspace == nil {
		return nil
	}
	var out []Limit
	action := r.Workspace.Action
	if action == "" {
		action = ActionStop
	}
	for _, name := range limitNames {
		if v := r.Workspace.limit(name); v > 0 {
			out = append(out, Limit{Name: name, Value: v, Source: "workspace", Action: action})
		}
	}
	return out
}

// window is a subject's usage in the current UTC day and month.
type window struct {
	dayTokens, monthTokens int64
	dayCost, monthCost     float64
}

func (w window) used(name string) float64 {
	switch name {
	case "tokensPerDay":
		return float64(w.dayTokens)
	case "tokensPerMonth":
		return float64(w.monthTokens)
	case "costPerDay":
		return w.dayCost
	}
	return w.monthCost
}

// resetOf is when name's window ends: the next UTC midnight, or the first
// of the next UTC month.
func resetOf(name string, now time.Time) time.Time {
	now = now.UTC()
	if name == "tokensPerDay" || name == "costPerDay" {
		return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// judge fills each limit's use, reset and state against w.
func judge(limits []Limit, w window, now time.Time) []Limit {
	for i := range limits {
		l := &limits[i]
		l.Used, l.Resets, l.State = w.used(l.Name), resetOf(l.Name, now).Format(time.RFC3339), "ok"
		if l.Used >= l.Value {
			l.State = l.Action
		}
	}
	return limits
}

// Verdict is what the quotas say about a call about to start.
type Verdict struct {
	Stop *Limit // the limit that refuses the call (nil: it may run)
	Warn *Limit // a reached limit whose action is warn
}

// verdictOf picks the first stopping limit, and the first warning one.
func verdictOf(sets ...[]Limit) Verdict {
	var v Verdict
	for _, limits := range sets {
		for i := range limits {
			switch l := &limits[i]; {
			case l.State == ActionStop && v.Stop == nil:
				v.Stop = l
			case l.State == ActionWarn && v.Warn == nil:
				v.Warn = l
			}
		}
	}
	return v
}

// describe says a reached limit in a sentence, for whoever met it.
func (l Limit) describe() string {
	whose := "your"
	if l.Source == "workspace" {
		whose = "the workspace's"
	}
	per, unit := "daily", "token"
	if l.Name == "tokensPerMonth" || l.Name == "costPerMonth" {
		per = "monthly"
	}
	amount, used := strconv.FormatInt(int64(l.Value), 10)+" tokens", strconv.FormatInt(int64(l.Used), 10)
	if l.Name == "costPerDay" || l.Name == "costPerMonth" {
		unit, amount, used = "cost", "$"+strconv.FormatFloat(l.Value, 'f', 2, 64), "$"+strconv.FormatFloat(l.Used, 'f', 2, 64)
	}
	return fmt.Sprintf("%s %s LLM %s quota of %s is used up (%s used); it resets at %s", whose, per, unit, amount, used, l.Resets)
}

// quotaName is the limit as the API names it: user.tokensPerDay,
// workspace.costPerMonth, org:eng.tokensPerDay, default.tokensPerMonth.
func (l Limit) quotaName() string { return l.Source + "." + l.Name }
