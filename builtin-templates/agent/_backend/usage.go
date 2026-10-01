// usage.go — each person's usage of a partitioned agent, for its managers
// (API.md "Partitioned instances"). The model budget is the tile's to govern,
// but a person's conversations live in their own partition, which no
// manager opens. So each partition mails the global instance its daily
// totals — how many conversations it started and how many model calls and
// tokens it used, per UTC day; numbers, never content or times of day — and
// `GET /usage` shows managers those totals per person. Coding agents'
// sessions count too (how many adapters it started: harness_partition.go).
//
// A partition sends the days it hasn't sent yet (up to yesterday, at most
// the last 31) when it starts and at each UTC midnight it is still running
// (a one-shot timer, re-armed after it fires). The global instance keeps
// the last 90 days. An unpartitioned agent has no usage route: its managers
// see every conversation anyway.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// usageDay is one person's totals for one UTC day.
type usageDay struct {
	Day              string `json:"day"` // YYYY-MM-DD, UTC
	Runs             int    `json:"runs"`
	LLMCalls         int    `json:"llmCalls"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	HarnessSessions  int    `json:"harnessSessions"` // coding agents' sessions started (an adapter each)
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func init() {
	mailHandlers[topicUsage] = handleUsageMail
}

// --- a person's partition ------------------------------------------------------------

var usageTimer struct {
	mu sync.Mutex
	t  *time.Timer
}

// usageAtStart sends what wasn't sent, and arms the midnight timer.
func (ag *Agent) usageAtStart() {
	go ag.sendUsage(context.Background())
	armUsage()
}

func armUsage() {
	usageTimer.mu.Lock()
	defer usageTimer.mu.Unlock()
	if usageTimer.t != nil {
		usageTimer.t.Stop()
	}
	n := time.Now().UTC()
	next := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 5, 0, 0, time.UTC) // five past: the day is over everywhere it is counted
	usageTimer.t = time.AfterFunc(time.Until(next), func() {
		if agent != nil {
			agent.sendUsage(context.Background())
		}
		armUsage()
	})
}

// addUsageDay counts one model call in today's (UTC) totals, where its
// tokens were spent (addRunCost; a person's partition only — the table is
// nowhere else).
func (d *DB) addUsageDay(prompt, completion int) {
	if !userMode() {
		return
	}
	_, _ = d.q.Exec(`INSERT INTO usage_daily (day, llm_calls, prompt_tokens, completion_tokens) VALUES (?, 1, ?, ?)
		ON CONFLICT(day) DO UPDATE SET llm_calls=llm_calls+1, prompt_tokens=prompt_tokens+excluded.prompt_tokens,
		  completion_tokens=completion_tokens+excluded.completion_tokens`, time.Now().UTC().Format("2006-01-02"), prompt, completion)
}

// usageDays are this partition's totals for the days after `after` up to
// yesterday (UTC), at most 31, oldest first; days with nothing are left out.
// Conversations count on the day they started, model calls and tokens on
// the day they were spent (usage_daily).
func (d *DB) usageDays(after string, today time.Time) []usageDay {
	from := today.AddDate(0, 0, -31)
	if t, err := time.Parse("2006-01-02", after); err == nil && t.AddDate(0, 0, 1).After(from) {
		from = t.AddDate(0, 0, 1)
	}
	rows, err := d.q.Query(`SELECT day, SUM(n), SUM(calls), SUM(p), SUM(c), SUM(h) FROM (
		  SELECT strftime('%Y-%m-%d', created, 'unixepoch') AS day, 1 AS n, 0 AS calls, 0 AS p, 0 AS c, 0 AS h
		    FROM runs WHERE parent_id=0 AND created>=? AND created<?
		  UNION ALL
		  SELECT day, 0, llm_calls, prompt_tokens, completion_tokens, harness_sessions FROM usage_daily WHERE day>=? AND day<?)
		GROUP BY day ORDER BY day`, from.Unix(), today.Unix(), from.Format("2006-01-02"), today.Format("2006-01-02"))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []usageDay
	for rows.Next() {
		var u usageDay
		if rows.Scan(&u.Day, &u.Runs, &u.LLMCalls, &u.PromptTokens, &u.CompletionTokens, &u.HarnessSessions) == nil {
			out = append(out, u)
		}
	}
	return out
}

// sendUsage mails the days not sent yet; the last one sent is remembered
// only once xbind took the mail (a failure is tried at the next start or
// midnight).
func (ag *Agent) sendUsage(ctx context.Context) {
	if !userMode() {
		return
	}
	n := time.Now().UTC()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1).Format("2006-01-02")
	sent := ag.db.getSetting("usage_sent")
	if sent >= yesterday {
		return
	}
	days := ag.db.usageDays(sent, today)
	if len(days) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := sendMail(cctx, "global", topicUsage, map[string]any{"days": days}, "")
		cancel()
		if err != nil {
			logf("usage: mailing the daily totals: %v (tried again later)", err)
			return
		}
	}
	_ = ag.db.putSetting("usage_sent", yesterday)
	_, _ = ag.db.q.Exec(`DELETE FROM usage_daily WHERE day < ?`, today.AddDate(0, 0, -40).Format("2006-01-02")) // past what is ever sent
}

// --- the global instance -------------------------------------------------------------------

// handleUsageMail (global) keeps a person's daily totals: a day sent again
// replaces the one kept.
func handleUsageMail(_ context.Context, t *DB, it mailItem) error {
	person, ok := strings.CutPrefix(it.From, "user:")
	if !globalMode() || !ok || person == "" {
		logf("usage/day %s from %q: only a person's partition reports usage to the global instance — refused", it.ID, it.From)
		return nil
	}
	t.markRan(person) // handoff_people.go
	var in struct {
		Days []usageDay `json:"days"`
	}
	if err := json.Unmarshal(it.Data, &in); err != nil || len(in.Days) > 62 {
		logf("usage/day %s from %s: malformed — dropped", it.ID, it.From)
		return nil
	}
	for _, u := range in.Days {
		if !dayRe.MatchString(u.Day) || u.Runs < 0 || u.LLMCalls < 0 || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.HarnessSessions < 0 {
			continue
		}
		if _, err := t.q.Exec(`INSERT INTO usage_days (person, day, runs, llm_calls, prompt_tokens, completion_tokens, harness_sessions, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(person, day) DO UPDATE SET runs=excluded.runs, llm_calls=excluded.llm_calls, prompt_tokens=excluded.prompt_tokens,
			  completion_tokens=excluded.completion_tokens, harness_sessions=excluded.harness_sessions, at=excluded.at`,
			person, u.Day, u.Runs, u.LLMCalls, u.PromptTokens, u.CompletionTokens, u.HarnessSessions, now()); err != nil {
			return err
		}
	}
	_, _ = t.q.Exec(`DELETE FROM usage_days WHERE day < ?`, time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02"))
	return nil
}

// handleUsage: GET /usage[?days=30] — managers: each person's daily totals
// (a partitioned agent; in a person's partition it is forwarded to the
// global instance).
func handleUsage(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, 404, "usage totals are a partitioned agent's: its managers can't open people's conversations")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if n <= 0 || n > 90 {
		n = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02")
	rows, err := agent.db.q.Query(`SELECT person, day, runs, llm_calls, prompt_tokens, completion_tokens, harness_sessions FROM usage_days
		WHERE day >= ? ORDER BY person, day`, since)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type person struct {
		User  string     `json:"user"`
		Days  []usageDay `json:"days"`
		Total usageDay   `json:"total"`
	}
	byUser := map[string]*person{}
	for rows.Next() {
		var u usageDay
		var who string
		if rows.Scan(&who, &u.Day, &u.Runs, &u.LLMCalls, &u.PromptTokens, &u.CompletionTokens, &u.HarnessSessions) != nil {
			continue
		}
		p := byUser[who]
		if p == nil {
			p = &person{User: who, Days: []usageDay{}}
			byUser[who] = p
		}
		p.Days = append(p.Days, u)
		p.Total.Runs += u.Runs
		p.Total.LLMCalls += u.LLMCalls
		p.Total.PromptTokens += u.PromptTokens
		p.Total.CompletionTokens += u.CompletionTokens
		p.Total.HarnessSessions += u.HarnessSessions
	}
	out := []*person{}
	for _, p := range byUser {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	xbin.WriteJSON(w, 200, map[string]any{"days": n, "since": since, "people": out})
}
