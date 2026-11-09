// project_wiki_routes.go — the wiki's routes, for people (API.md §Project
// wiki): read at the project's viewer level, written at participant — the
// same compare-and-swap as the agents', the base version named: a PUT or
// DELETE against a version that is no longer current answers 409 with the
// conflict's words (wikiConflict) and the current version.
package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { routeTables = append(routeTables, wikiRoutes) }

func wikiRoutes() []routeDef {
	return []routeDef{
		{"GET /projects/{pid}/wiki", needAny, projectNeed(lvViewer, handleWikiList)},
		{"GET /projects/{pid}/wiki/{slug}", needAny, projectNeed(lvViewer, handleWikiGet)},
		{"PUT /projects/{pid}/wiki/{slug}", needAny, projectNeed(lvParticipant, handleWikiPut)},
		{"DELETE /projects/{pid}/wiki/{slug}", needAny, projectNeed(lvParticipant, handleWikiArchive)},
		{"GET /projects/{pid}/wiki/{slug}/history", needAny, projectNeed(lvViewer, handleWikiHistory)},
	}
}

// writeWikiErr answers a wiki refusal: {error, refusal?, current?}.
func writeWikiErr(w http.ResponseWriter, err error) {
	var we *wikiErr
	if !errors.As(err, &we) {
		writeProjErr(w, err)
		return
	}
	body := map[string]any{"error": we.msg}
	if we.conflict {
		body["refusal"], body["current"] = "conflict", we.current
	}
	xbin.WriteJSON(w, we.code, body)
}

// handleWikiList: GET /projects/{pid}/wiki[?q=&archived=1] — the pages
// (no bodies), the matches of q, and the footer's measures.
func handleWikiList(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	d := projAg().db
	out := map[string]any{"pages": orEmpty(d.wikiPages(p.ID, r.URL.Query().Get("archived") == "1")), "stats": d.wikiStats(p.ID),
		"dream": policyOf(p.Policy).Dream}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		hits, mode, err := d.wikiSearch(p.ID, q, 50)
		if err != nil {
			xbin.WriteError(w, 400, "q: "+err.Error())
			return
		}
		out["hits"], out["match"] = orEmpty(hits), mode // any: no page had every word, these have some
	}
	xbin.WriteJSON(w, 200, out)
}

// handleWikiGet: GET /projects/{pid}/wiki/{slug}[?v=] — a page with its
// body; with v, that version's text as revision.
func handleWikiGet(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	d := projAg().db
	slug, err := wikiSlug(r.PathValue("slug"))
	if err != nil {
		writeWikiErr(w, err)
		return
	}
	pg, err := d.wikiPage(p.ID, slug)
	if err != nil {
		writeWikiErr(w, err)
		return
	}
	out := map[string]any{"page": pg}
	if v, _ := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); v > 0 {
		rev, err := d.wikiRevision(pg.ID, v)
		if err != nil {
			writeWikiErr(w, err)
			return
		}
		out["revision"] = rev
	}
	xbin.WriteJSON(w, 200, out)
}

// handleWikiHistory: GET /projects/{pid}/wiki/{slug}/history[?v=] — its
// versions, newest first; with v, that version in full too.
func handleWikiHistory(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	d := projAg().db
	slug, err := wikiSlug(r.PathValue("slug"))
	if err != nil {
		writeWikiErr(w, err)
		return
	}
	pg, err := d.wikiPage(p.ID, slug)
	if err != nil {
		writeWikiErr(w, err)
		return
	}
	out := map[string]any{"slug": slug, "version": pg.Version, "revisions": d.wikiHistory(pg.ID, int(wikiRevKeep))}
	if v, _ := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); v > 0 {
		rev, err := d.wikiRevision(pg.ID, v)
		if err != nil {
			writeWikiErr(w, err)
			return
		}
		out["revision"] = rev
	}
	xbin.WriteJSON(w, 200, out)
}

// handleWikiPut: PUT /projects/{pid}/wiki/{slug} {title?, body, note?,
// sources?, baseVersion} — create (baseVersion 0) or replace the page.
func handleWikiPut(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var body struct {
		Title       string   `json:"title"`
		Body        string   `json:"body"`
		Note        string   `json:"note"`
		Sources     []string `json:"sources"`
		BaseVersion int64    `json:"baseVersion"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	wikiWriteAs(w, r, p, wikiChange{Slug: r.PathValue("slug"), Title: body.Title, Body: body.Body, Note: body.Note, Sources: body.Sources,
		Base: &body.BaseVersion})
}

// handleWikiArchive: DELETE /projects/{pid}/wiki/{slug}?baseVersion=N
// (or {baseVersion, note}) — archive the page; its history stays.
func handleWikiArchive(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var body struct {
		Note        string `json:"note"`
		BaseVersion *int64 `json:"baseVersion"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &body) {
		return
	}
	if s := r.URL.Query().Get("baseVersion"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			xbin.WriteError(w, 400, "baseVersion: the version you are archiving")
			return
		}
		body.BaseVersion = &v
	}
	if body.BaseVersion == nil {
		xbin.WriteError(w, 400, "baseVersion: the version you are archiving (the page's version as you last read it)")
		return
	}
	wikiWriteAs(w, r, p, wikiChange{Slug: r.PathValue("slug"), Archive: true, Note: body.Note, Base: body.BaseVersion})
}

func wikiWriteAs(w http.ResponseWriter, r *http.Request, p *Project, c wikiChange) {
	if p.State != projActive {
		xbin.WriteError(w, 409, "this project is "+p.State+": its wiki is read-only")
		return
	}
	who := callerOf(r)
	pg, err := projAg().db.wikiPut(p.ID, wikiActor{By: orStr(who.tag(), "a person")}, c)
	if err != nil {
		writeWikiErr(w, err)
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"page": pg})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
