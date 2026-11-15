package llm

import (
	"fmt"
	"strings"
)

// alerts.go — what the connector tells workspace admins (the broker's
// AdminAlerts chain; docs/llm.md "Quotas").

// AlertKind is the alerts' kind.
const AlertKind = "llm"

// Alert is one thing admins should know.
type Alert struct {
	Level   string // warn
	Message string
}

// costLimited reports a cost limit in force anywhere in the rules.
func (r QuotaRules) costLimited() bool {
	has := func(q *Quota) bool { return q != nil && (q.CostPerDay > 0 || q.CostPerMonth > 0) }
	if has(r.Workspace) || has(r.Default) || has(r.ceiling) {
		return true
	}
	for _, q := range r.Users {
		if has(&q) {
			return true
		}
	}
	for _, q := range r.Orgs {
		if has(&q) {
			return true
		}
	}
	return false
}

// Alerts is at most one alert: cost quotas are in force and this month has
// calls on models without a price, which no cost quota counts. It goes
// when the models are priced (a "*" price covers every model of an
// upstream) and the month's unpriced calls are behind — or the cost quotas
// are removed.
func (s *Service) Alerts() []Alert {
	snap := s.current()
	if !snap.configured() || !snap.quotas.costLimited() {
		return nil
	}
	models, calls := s.meter.unpriced(s.now())
	if calls == 0 {
		return nil
	}
	shown := models
	if len(shown) > 5 {
		shown = append(append([]string{}, shown[:5]...), fmt.Sprintf("and %d more", len(models)-5))
	}
	return []Alert{{Level: "warn", Message: fmt.Sprintf(
		"LLM cost quotas don't count %d call(s) this month: %s have no price. Set their prices, or a default price for an upstream (bx llm upstream set <name> --price '*=<in>:<out>'); token quotas still apply to them.",
		calls, strings.Join(shown, ", "))}}
}
