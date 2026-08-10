package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Cross-tile change proposals ("code PRs", plans/code-prs.md): an agent or
// human who can READ a component may propose changes to it as a git
// format-patch series. Proposals are inert broker state under data/prs/ —
// xbind never applies one; the patch lands only when the target's own plane
// (its terminal, its agent, its human) runs `git am` inside the target's
// writable mount. Opening a PR therefore needs no owner approval: the
// capability to suggest is exactly the capability to read (D48), and the
// mount model keeps applying consensual.

const (
	maxPRSeriesBytes = 4 << 20  // a proposal is a reviewable diff, not a file transfer
	maxPRTextBytes   = 64 << 10 // title/message/comment cap
)

// prStates a PR can be moved to from "open". There is no reopen: a refused
// proposal is refiled (the store is cheap, history stays honest).
var prStates = map[string]bool{"merged": true, "rejected": true, "withdrawn": true}

type prFrom struct {
	Component string `json:"component,omitempty"` // filing element ("" = owner/user plane)
	User      string `json:"user,omitempty"`      // driving user, when attributed
	Via       string `json:"via,omitempty"`       // "terminal" | "instance" | "frame" | ""
}

type prEvent struct {
	TS    time.Time `json:"ts"`
	Who   string    `json:"who"`
	Type  string    `json:"type"` // "comment" | "state"
	Body  string    `json:"body,omitempty"`
	State string    `json:"state,omitempty"` // for type "state"
}

type prMeta struct {
	Number  int       `json:"number"`
	Target  string    `json:"target"`
	From    prFrom    `json:"from"`
	Title   string    `json:"title"`
	Message string    `json:"message,omitempty"`
	Base    string    `json:"base,omitempty"` // rev the series was formatted against
	State   string    `json:"state"`          // open | merged | rejected | withdrawn
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Events  []prEvent `json:"events,omitempty"`
}

func (b *Broker) registerPRs(srv *server.Server) {
	srv.RegisterAPI("POST /code/prs", b.apiPROpen)
	srv.RegisterAPI("GET /code/prs", b.apiPRList)
	srv.RegisterAPI("GET /code/prs/summary", b.apiPRSummary)
	srv.RegisterAPI("GET /code/pr", b.apiPRGet)
	srv.RegisterAPI("GET /code/pr/series", b.apiPRSeries)
	srv.RegisterAPI("POST /code/pr/comment", b.apiPRComment)
	srv.RegisterAPI("POST /code/pr/state", b.apiPRState)
}

func (b *Broker) prsRoot() string            { return filepath.Join(b.Reg.Root, "data", "prs") }
func (b *Broker) prDir(target string) string { return filepath.Join(b.prsRoot(), util.CompKey(target)) }

// prWriteFile writes tmp-then-rename (same idiom as prefs/vault) so a crash
// never leaves a torn meta/series behind.
func prWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// canReadPRs is the whole PR visibility/suggestion gate (read = suggest):
// admins; the target itself; a terminal/frame whose driving user can read the
// tile (the same D40 rule that decides what their shell mounts show); or an
// element holding a code[:target] grant (the runtime-plane equivalent).
func (b *Broker) canReadPRs(p auth.Principal, target string) bool {
	if p.CanReadTile(target) {
		return true
	}
	return p.Component != "" && b.codeGrantAllows(p.Component, target)
}

// canDecidePR gates merged/rejected: the target side — its own element
// principal (terminal/backend/frame), an admin, or a principal whose driving
// user holds write on the tile.
func (b *Broker) canDecidePR(p auth.Principal, target string) bool {
	return p.IsAdmin() || p.Component == target || p.CanWriteTile(target)
}

// prIsAuthor gates withdrawn: element-filed PRs belong to that element
// (any of its principals); owner/user-filed ones to that human.
func prIsAuthor(p auth.Principal, m *prMeta) bool {
	if m.From.Component != "" {
		return p.Component == m.From.Component
	}
	if m.From.User != "" {
		return p.Component == "" && p.UserID == m.From.User
	}
	return p.Owner
}

func prWho(p auth.Principal) string {
	if p.Component != "" {
		if p.UserID != "" {
			return p.Component + " (" + p.UserID + ")"
		}
		return p.Component
	}
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	if p.Owner {
		return "owner"
	}
	return "?"
}

func (b *Broker) prPublish(action, target string, n int, state string) {
	if b.Hub == nil {
		return
	}
	data := map[string]any{"n": n, "action": action}
	if state != "" {
		data["state"] = state
	}
	b.Hub.Publish(events.Event{Type: "pr", Component: target, Data: data})
}

// prTarget validates ?component-style target input and requires it to be a
// live component; PRs against paths that aren't components are refused (the
// store would otherwise leak arbitrary names into summaries).
func (b *Broker) prTarget(w http.ResponseWriter, target string) (string, bool) {
	target = strings.Trim(target, "/")
	if !util.ComponentPathOK(target) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad component path"})
		return "", false
	}
	if _, ok := b.Reg.Component(target); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return "", false
	}
	return target, true
}

func (b *Broker) prLoad(target string, n int) (*prMeta, error) {
	data, err := os.ReadFile(filepath.Join(b.prDir(target), strconv.Itoa(n), "meta.json"))
	if err != nil {
		return nil, err
	}
	var m prMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Broker) prSave(m *prMeta) error {
	dir := filepath.Join(b.prDir(m.Target), strconv.Itoa(m.Number))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return prWriteFile(filepath.Join(dir, "meta.json"), data)
}

// prList reads every PR meta under one target dir, newest first.
func (b *Broker) prList(target string) []*prMeta {
	ents, err := os.ReadDir(b.prDir(target))
	if err != nil {
		return nil
	}
	var out []*prMeta
	for _, e := range ents {
		n, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		if m, err := b.prLoad(target, n); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out
}

// prTargets maps the store back to target paths by reading one meta per
// target dir (CompKey is one-way, so the path lives in the metas).
func (b *Broker) prTargets() []string {
	ents, err := os.ReadDir(b.prsRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(b.prsRoot(), e.Name()))
		if err != nil {
			continue
		}
		for _, s := range sub {
			data, err := os.ReadFile(filepath.Join(b.prsRoot(), e.Name(), s.Name(), "meta.json"))
			if err != nil {
				continue
			}
			var m prMeta
			if json.Unmarshal(data, &m) == nil && m.Target != "" {
				out = append(out, m.Target)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// POST /code/prs {target, title, message?, base?, series} — open a proposal.
// Gate: canReadPRs (read = suggest, D48). The series must be a git patch
// (format-patch mbox or raw diff) and is stored verbatim.
func (b *Broker) apiPROpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string `json:"target"`
		Title   string `json:"title"`
		Message string `json:"message"`
		Base    string `json:"base"`
		Series  string `json:"series"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPRSeriesBytes+256<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
		return
	}
	target, ok := b.prTarget(w, body.Target)
	if !ok {
		return
	}
	p := auth.PrincipalOf(r)
	if !b.canReadPRs(p, target) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "proposing changes to " + target + " needs read access to it (or a code:" + target + " grant)",
			"docs":  "/docs/protocol.md",
		})
		return
	}
	title := strings.TrimSpace(body.Title)
	switch {
	case title == "" || len(title) > 200:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "title required (≤200 chars)"})
		return
	case len(body.Message) > maxPRTextBytes:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "message too large"})
		return
	case len(body.Series) > maxPRSeriesBytes:
		server.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "series exceeds 4 MiB — split the proposal"})
		return
	case !strings.Contains(body.Series, "diff --git "):
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "series is not a git patch (expected format-patch/mbox output containing \"diff --git\")"})
		return
	}
	now := time.Now().UTC()
	m := &prMeta{
		Target:  target,
		From:    prFrom{Component: p.Component, User: p.UserID, Via: p.Via},
		Title:   title,
		Message: strings.TrimSpace(body.Message),
		Base:    strings.TrimSpace(body.Base),
		State:   "open",
		Created: now,
		Updated: now,
	}

	b.prsMu.Lock()
	n := 1
	if ents, err := os.ReadDir(b.prDir(target)); err == nil {
		for _, e := range ents {
			if v, err := strconv.Atoi(e.Name()); err == nil && v >= n {
				n = v + 1
			}
		}
	}
	m.Number = n
	dir := filepath.Join(b.prDir(target), strconv.Itoa(n))
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		err = prWriteFile(filepath.Join(dir, "series.mbox"), []byte(body.Series))
	}
	if err == nil {
		err = b.prSave(m)
	}
	b.prsMu.Unlock()
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.prPublish("open", target, n, "")
	server.WriteJSON(w, http.StatusOK, m)
}

// GET /code/prs?target=<path>[&state=open|merged|rejected|withdrawn] — a
// target's PRs; or ?from=1 — the caller's own outgoing PRs across targets;
// admins with neither param get everything.
func (b *Broker) apiPRList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	state := r.URL.Query().Get("state")
	filter := func(list []*prMeta) []*prMeta {
		if state == "" {
			return list
		}
		out := []*prMeta{}
		for _, m := range list {
			if m.State == state {
				out = append(out, m)
			}
		}
		return out
	}

	if target := r.URL.Query().Get("target"); target != "" {
		target, ok := b.prTarget(w, target)
		if !ok {
			return
		}
		if !b.canReadPRs(p, target) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no read access to " + target})
			return
		}
		list := filter(b.prList(target))
		server.WriteJSON(w, http.StatusOK, map[string]any{"target": target, "prs": list})
		return
	}

	mine := r.URL.Query().Get("from") != ""
	if !mine && !p.IsAdmin() {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need ?target=<component> or ?from=1"})
		return
	}
	all := []*prMeta{}
	for _, target := range b.prTargets() {
		for _, m := range filter(b.prList(target)) {
			if mine && !prIsAuthor(p, m) {
				continue
			}
			all = append(all, m)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
	server.WriteJSON(w, http.StatusOK, map[string]any{"prs": all})
}

// GET /code/prs/summary — open-PR counts per component the caller can see;
// feeds the shell's sidebar badges.
func (b *Broker) apiPRSummary(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	counts := map[string]int{}
	for _, target := range b.prTargets() {
		if !b.canReadPRs(p, target) {
			continue
		}
		n := 0
		for _, m := range b.prList(target) {
			if m.State == "open" {
				n++
			}
		}
		if n > 0 {
			counts[target] = n
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// prByQuery resolves ?target=&n= and gates on canReadPRs.
func (b *Broker) prByQuery(w http.ResponseWriter, r *http.Request) (*prMeta, bool) {
	target, ok := b.prTarget(w, r.URL.Query().Get("target"))
	if !ok {
		return nil, false
	}
	if !b.canReadPRs(auth.PrincipalOf(r), target) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no read access to " + target})
		return nil, false
	}
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n < 1 {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad pr number"})
		return nil, false
	}
	m, err := b.prLoad(target, n)
	if err != nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such pr"})
		return nil, false
	}
	return m, true
}

// GET /code/pr?target=<path>&n=<n> — full meta + review thread.
func (b *Broker) apiPRGet(w http.ResponseWriter, r *http.Request) {
	if m, ok := b.prByQuery(w, r); ok {
		server.WriteJSON(w, http.StatusOK, m)
	}
}

// GET /code/pr/series?target=<path>&n=<n> — the raw patch series, verbatim
// (pipe into `git am --3way` after review).
func (b *Broker) apiPRSeries(w http.ResponseWriter, r *http.Request) {
	m, ok := b.prByQuery(w, r)
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(b.prDir(m.Target), strconv.Itoa(m.Number), "series.mbox"))
	if err != nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "series missing"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// POST /code/pr/comment {target, n, body} — review feedback, either
// direction. Anyone who can see the PR can comment; the thread is the
// author↔maintainer channel ("needs X before this can land").
func (b *Broker) apiPRComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
		N      int    `json:"n"`
		Body   string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPRTextBytes+4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" || len(text) > maxPRTextBytes {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "comment required (≤64 KiB)"})
		return
	}
	target, ok := b.prTarget(w, body.Target)
	if !ok {
		return
	}
	p := auth.PrincipalOf(r)
	if !b.canReadPRs(p, target) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no read access to " + target})
		return
	}

	b.prsMu.Lock()
	m, err := b.prLoad(target, body.N)
	if err == nil {
		m.Events = append(m.Events, prEvent{TS: time.Now().UTC(), Who: prWho(p), Type: "comment", Body: text})
		m.Updated = time.Now().UTC()
		err = b.prSave(m)
	}
	b.prsMu.Unlock()
	if err != nil {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such pr"})
		return
	}
	b.prPublish("comment", target, body.N, "")
	server.WriteJSON(w, http.StatusOK, m)
}

// POST /code/pr/state {target, n, state, comment?} — close an open PR.
// merged/rejected are the target side's call (its own principals, write-level
// users, admins); withdrawn is the author's. No reopen — refile instead.
func (b *Broker) apiPRState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string `json:"target"`
		N       int    `json:"n"`
		State   string `json:"state"`
		Comment string `json:"comment"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPRTextBytes+4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
		return
	}
	if !prStates[body.State] {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "state must be merged, rejected, or withdrawn"})
		return
	}
	if len(body.Comment) > maxPRTextBytes {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "comment too large"})
		return
	}
	target, ok := b.prTarget(w, body.Target)
	if !ok {
		return
	}
	p := auth.PrincipalOf(r)

	b.prsMu.Lock()
	m, err := b.prLoad(target, body.N)
	if err != nil {
		b.prsMu.Unlock()
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such pr"})
		return
	}
	allowed := false
	if body.State == "withdrawn" {
		allowed = prIsAuthor(p, m) || p.IsAdmin()
	} else {
		allowed = b.canDecidePR(p, target)
	}
	if !allowed {
		b.prsMu.Unlock()
		who := "the target side (its terminal, a write-level user, or an admin)"
		if body.State == "withdrawn" {
			who = "the PR's author"
		}
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": body.State + " is " + who + "'s call"})
		return
	}
	if m.State != "open" {
		b.prsMu.Unlock()
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "pr is already " + m.State})
		return
	}
	now := time.Now().UTC()
	m.State = body.State
	m.Updated = now
	m.Events = append(m.Events, prEvent{TS: now, Who: prWho(p), Type: "state", State: body.State, Body: strings.TrimSpace(body.Comment)})
	err = b.prSave(m)
	b.prsMu.Unlock()
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.prPublish("state", target, body.N, body.State)
	server.WriteJSON(w, http.StatusOK, m)
}
