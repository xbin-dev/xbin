// stats.go — the per-backend usage counters (requests, tokens in/out, cost,
// calls in flight) the tile's page shows, GET /stats and GET /metrics. The
// per-caller counters of partitioned tiles are callers.go's.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var (
	statsMu sync.Mutex
	statsBy map[string]*stats // backend -> persisted counters
	active  = map[string]int{}
)

type stats struct {
	Reqs   int64   `json:"reqs"`
	TokIn  int64   `json:"tokIn"`
	TokOut int64   `json:"tokOut"`
	Cost   float64 `json:"cost"`
}

func loadStats() {
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsBy == nil {
		statsBy = map[string]*stats{}
		_ = kv.GetJSON("stats", &statsBy)
	}
}

func bumpStats(name string, tokIn, tokOut int64, cost float64) {
	loadStats()
	statsMu.Lock()
	s := statsBy[name]
	if s == nil {
		s = &stats{}
		statsBy[name] = s
	}
	s.Reqs++
	s.TokIn += tokIn
	s.TokOut += tokOut
	s.Cost += cost
	snapshot, err := json.Marshal(statsBy) // under the lock: another call may add a backend
	statsMu.Unlock()
	if err == nil {
		_ = kv.Put("stats", snapshot)
	}
}

func setActive(name string, d int) {
	statsMu.Lock()
	active[name] += d
	if active[name] < 0 {
		active[name] = 0
	}
	statsMu.Unlock()
}

// handleMetrics renders per-backend counters in Prometheus text format.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	loadStats()
	c := loadConfig()
	names := backendNames(c)
	statsMu.Lock()
	defer statsMu.Unlock()
	var b strings.Builder
	series := func(name, help, typ string, val func(*stats, string) any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for _, n := range names {
			s := statsBy[n]
			if s == nil {
				s = &stats{}
			}
			fmt.Fprintf(&b, "%s{backend=%q} %v\n", name, n, val(s, n))
		}
	}
	series("llmgw_requests_total", "Requests proxied per backend.", "counter", func(s *stats, n string) any { return s.Reqs })
	series("llmgw_tokens_in_total", "Prompt tokens per backend.", "counter", func(s *stats, n string) any { return s.TokIn })
	series("llmgw_tokens_out_total", "Completion tokens per backend.", "counter", func(s *stats, n string) any { return s.TokOut })
	series("llmgw_cost_usd_total", "Estimated USD cost per backend.", "counter", func(s *stats, n string) any { return strconv.FormatFloat(s.Cost, 'f', 6, 64) })
	series("llmgw_active_requests", "In-flight requests per backend.", "gauge", func(s *stats, n string) any { return active[n] })
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	loadStats()
	c := loadConfig()
	statsMu.Lock()
	out := map[string]any{}
	for _, n := range backendNames(c) {
		s := statsBy[n]
		if s == nil {
			s = &stats{}
		}
		out[n] = map[string]any{
			"reqs": s.Reqs, "tokIn": s.TokIn, "tokOut": s.TokOut,
			"active": int64(active[n]), "cost": s.Cost,
		}
	}
	statsMu.Unlock()
	resp := map[string]any{"backends": out}
	if rows := callersFor(r); len(rows) > 0 {
		resp["callers"] = rows // only once a partitioned tile has called
	}
	xbin.WriteJSON(w, http.StatusOK, resp)
}

// Usage is read from the tail of the response body. Chat Completions reports
// prompt_tokens/completion_tokens; the Responses API (/v1/responses) reports
// input_tokens/output_tokens. The LAST match wins: a Responses stream carries
// a usage block only on its final event, and earlier events may mention usage
// as null.
var (
	usageRe     = regexp.MustCompile(`"prompt_tokens"\s*:\s*(\d+)[\s\S]*?"completion_tokens"\s*:\s*(\d+)`)
	usageRespRe = regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)[\s\S]*?"output_tokens"\s*:\s*(\d+)`)
)

// usageOf extracts (tokens in, tokens out) from a response tail.
func usageOf(tail []byte) (in, out int64) {
	for _, re := range []*regexp.Regexp{usageRe, usageRespRe} {
		if all := re.FindAllSubmatch(tail, -1); len(all) > 0 {
			m := all[len(all)-1]
			fmt.Sscan(string(m[1]), &in)
			fmt.Sscan(string(m[2]), &out)
			return in, out
		}
	}
	return 0, 0
}
