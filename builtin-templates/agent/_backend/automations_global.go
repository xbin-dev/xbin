// automations_global.go — in a person's partition, an automation the global
// instance keeps (a chat channel; a trigger registry row, ids below 2^40)
// has runs in two homes: the ones handed to this partition (the person's
// own DMs of a channel) and the global instance's (a channel's group
// threads, a team trigger's runs) — those the person may see there. Its
// card counts both, and its run list reads both and merges them by the
// list's own cursor (activity, id): each home answers its page below the
// same cursor, so the merged page's newest `limit` are exactly the list's
// next ones. A run opens at its home by its id (model/homes.js). Marking an
// automation read marks both. Unpartitioned, and at global, nothing here
// changes a thing.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// globalItem: in a person's partition, an automation item the global
// instance keeps — its runs are there too.
func globalItem(kind string, id int64) bool {
	return userMode() && (kind == "channel" || kind == "trigger" && id < partitionIDBase)
}

// runsPage is GET /automations/{kind}/{aid}/runs's answer.
type runsPage struct {
	Items []map[string]any `json:"items"`
	Next  string           `json:"next"`
}

// withGlobalRuns is h (GET …/runs, POST …/read) in a person's partition: for
// an automation the global instance keeps, the same call is made there too,
// as the person, and a run list is merged; anything else is h's alone.
func withGlobalRuns(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("aid"), 10, 64)
		if !globalItem(r.PathValue("kind"), id) {
			h(w, r)
			return
		}
		local := &pageRecorder{header: http.Header{}, code: http.StatusOK}
		h(local, r)
		if local.code != http.StatusOK {
			copyAnswer(w, local)
			return
		}
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		res, err := callGlobal(ctx, r.Method, path, nil, "")
		if r.Method != http.MethodGet || err != nil || res.Status != http.StatusOK {
			if r.Method == http.MethodGet && (err != nil || res.Status != http.StatusNotFound) {
				logf("an automation's runs at the global instance: %v (HTTP %d) — this partition's alone", err, res.Status)
			}
			copyAnswer(w, local)
			return
		}
		var mine, theirs runsPage
		if json.Unmarshal(local.body.Bytes(), &mine) != nil || json.Unmarshal(res.Body, &theirs) != nil {
			copyAnswer(w, local)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 30
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mergeRunPages(mine, theirs, limit))
	}
}

// mergeRunPages is two homes' pages below one cursor as one: newest
// activity first, `limit` of them, and the cursor of the last when either
// home has more.
func mergeRunPages(a, b runsPage, limit int) runsPage {
	all := append(append([]map[string]any{}, a.Items...), b.Items...)
	num := func(m map[string]any, k string) float64 { f, _ := m[k].(float64); return f }
	sort.SliceStable(all, func(i, j int) bool {
		if x, y := num(all[i], "activityMs"), num(all[j], "activityMs"); x != y {
			return x > y
		}
		return num(all[i], "id") > num(all[j], "id")
	})
	out := runsPage{Items: all}
	if len(all) > limit {
		out.Items = all[:limit]
	}
	if len(out.Items) > 0 && (len(all) > limit || a.Next != "" || b.Next != "") {
		last := out.Items[len(out.Items)-1]
		out.Next = fmt.Sprintf("%d.%d", int64(num(last, "activityMs")), int64(num(last, "id")))
	}
	if out.Items == nil {
		out.Items = []map[string]any{}
	}
	return out
}

// pageRecorder keeps a handler's answer (the local half of a merged call).
type pageRecorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (p *pageRecorder) Header() http.Header         { return p.header }
func (p *pageRecorder) WriteHeader(code int)        { p.code = code }
func (p *pageRecorder) Write(b []byte) (int, error) { return p.body.Write(b) }

func copyAnswer(w http.ResponseWriter, p *pageRecorder) {
	for k, v := range p.header {
		w.Header()[k] = v
	}
	w.WriteHeader(p.code)
	_, _ = io.Copy(w, &p.body)
}
