// ws_xbin_http.go — wsXbin over xbind's delegation (ws_xbin.go): the SDK's
// delegate client (xbin.AsPerson: this partition's token under the delegate
// scheme, no redirects followed), every call in the context of the
// conversation's agent session (xbin.InAgentSession: xbind judges the call
// by that handle's scope and model). The one file that knows xbind's
// routes and shapes for the workspace agent (docs/protocol/delegation.md,
// agents.md, code.md, preview.md): a shape that moves is fixed here.
//
// Reads are bounded: an answer is cut at wsReadMax before it is decoded or
// handed on. A route this xbind doesn't serve answers errWsUnavailable, so
// a tool can say "not available here" instead of failing.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsReadMax is the most of one answer that is read.
const wsReadMax = 1 << 20

// wsHTTP is xbind's delegate API as the person. via is nil but in a test,
// which points it at a made-up xbind.
type wsHTTP struct{ via *http.Client }

// do makes one delegate call. path is the whole path from /api on.
func (h wsHTTP) do(ctx context.Context, sid, method, path string, body []byte, ctype string, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	if sid != "" {
		ctx = xbin.InAgentSession(ctx, sid) // the conversation this call is for
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://xbin"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", orStr(ctype, "application/json"))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if h.via != nil {
		return h.via.Do(req)
	}
	return xbin.AsPerson().Do(req)
}

// refused is resp as an error: nil for 2xx, errWsUnavailable for a route
// xbind answers "not implemented", else the SDK's *DelegateError.
func (wsHTTP) refused(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	err := xbin.DelegateRefusal(resp) // reads and closes the body
	var de *xbin.DelegateError
	if errors.As(err, &de) && de.Refusal == "" && (de.Status == http.StatusNotImplemented || de.Status == http.StatusMethodNotAllowed) {
		return fmt.Errorf("%w (%s)", errWsUnavailable, resp.Request.URL.Path)
	}
	return err
}

// get decodes a JSON read of xbind's API into out.
func (h wsHTTP) get(ctx context.Context, sid, path string, out any) error {
	return h.json(ctx, sid, http.MethodGet, path, nil, out)
}

func (h wsHTTP) json(ctx context.Context, sid, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	resp, err := h.do(ctx, sid, method, path, body, "", nil)
	if err != nil {
		return err
	}
	if err := h.refused(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, wsReadMax)).Decode(out)
}

// missing: err says xbind has no such route (an older xbind, or one still
// being built) — the caller may compose the answer another way.
func wsMissing(err error) bool {
	var de *xbin.DelegateError
	return errors.Is(err, errWsUnavailable) || (errors.As(err, &de) && de.Refusal == "" && de.Status == http.StatusNotFound)
}

// --- handles -----------------------------------------------------------------------

const wsSessionsPath = "/api/xbin/delegate/sessions"

// filled is h with its connection's org rows: a session's own answer
// carries the totals only.
func (h wsHTTP) filled(ctx context.Context, out wsHandle, err error) (wsHandle, error) {
	if err != nil {
		return out, err
	}
	if c, _, cerr := h.Connection(ctx, out.ID, ""); cerr == nil {
		out.Connection = c
	}
	return out, nil
}

func (h wsHTTP) OpenSession(ctx context.Context, scope *wsScope, model string) (wsHandle, error) {
	var out wsHandle
	return h.filled(ctx, out, h.json(ctx, "", http.MethodPost, wsSessionsPath, map[string]any{"scope": scope, "model": model}, &out))
}

func (h wsHTTP) Session(ctx context.Context, sid string) (wsHandle, error) {
	var out wsHandle
	err := h.get(ctx, sid, wsSessionsPath+"/"+url.PathEscape(sid), &out)
	return h.filled(ctx, out, err)
}

func (h wsHTTP) UpdateSession(ctx context.Context, sid string, ch wsHandleChange) (wsHandle, error) {
	in := map[string]any{}
	if ch.SetScope {
		in["scope"] = ch.Scope
	}
	if ch.Model != "" {
		in["model"] = ch.Model
	}
	var out wsHandle
	err := h.json(ctx, sid, http.MethodPatch, wsSessionsPath+"/"+url.PathEscape(sid), in, &out)
	var de *xbin.DelegateError
	if errors.As(err, &de) && errors.Is(err, xbin.ErrModelTaint) {
		return out, &wsTaintError{Model: ch.Model, Orgs: de.Orgs, Reason: de.Reason}
	}
	return h.filled(ctx, out, err)
}

// Allows asks whether the session's model is allowed by every one of orgs.
func (h wsHTTP) Allows(ctx context.Context, sid string, orgs []string) (bool, map[string]string, error) {
	var out struct {
		Allowed bool              `json:"allowed"`
		Reasons map[string]string `json:"reasons"`
	}
	if err := h.json(ctx, sid, http.MethodPost, wsSessionsPath+"/"+url.PathEscape(sid)+"/allows", map[string]any{"orgs": orgs}, &out); err != nil {
		return false, nil, err
	}
	return out.Allowed, out.Reasons, nil
}

// Taint records that the conversation holds data of orgs.
func (h wsHTTP) Taint(ctx context.Context, sid string, orgs []string) error {
	return h.json(ctx, sid, http.MethodPost, wsSessionsPath+"/"+url.PathEscape(sid)+"/taint", map[string]any{"orgs": orgs}, nil)
}

func (h wsHTTP) CloseSession(ctx context.Context, sid string) error {
	return h.json(ctx, sid, http.MethodDelete, wsSessionsPath+"/"+url.PathEscape(sid), nil, nil)
}

func (h wsHTTP) Connection(ctx context.Context, sid, whatIf string) (wsConnection, *wsWhatIf, error) {
	p := wsSessionsPath + "/" + url.PathEscape(sid) + "/connection"
	if whatIf != "" {
		p += "?whatIf=" + url.QueryEscape(whatIf)
	}
	// xbind's view also lists the connected tiles by name; the totals and
	// the org rows are all that is kept
	var out struct {
		wsConnection
		WhatIf *wsWhatIf `json:"whatIf"`
	}
	err := h.get(ctx, sid, p, &out)
	return out.wsConnection, out.WhatIf, err
}

// Models: the connector is this component's own binding (its `llm` slot),
// not a delegate route — the catalog and the agent role's default are read
// as the component (llm_providers.go).
func (wsHTTP) Models(ctx context.Context) (wsModels, error) {
	out := wsModels{Default: orStr(preferredModel(ctx, "agent"), wsRoleModel)}
	cat := fullCatalog(ctx)
	if cat == nil {
		return out, nil
	}
	for _, m := range cat.Models {
		wm := wsModel{ID: m.Ref, Name: m.ID}
		if up, _, ok := strings.Cut(m.ID, "/"); ok { // the connector's "<upstream>/<model>"
			wm.Upstream = up
		}
		if m.Private != nil { // only when the connector's list says
			wm.Private, wm.Where = m.Private, wsWhere(*m.Private)
		}
		out.Models = append(out.Models, wm)
	}
	return out, nil
}

func (h wsHTTP) State(ctx context.Context) (wsState, error) {
	var out wsState
	return out, h.get(ctx, "", "/api/xbin/delegate/state", &out)
}

// --- looking ---------------------------------------------------------------------

// Map is xbind's compact map of what the person sees, within the session's
// scope and the orgs' rules, with each tile's own status line (the tiles'
// reports) beside it.
func (h wsHTTP) Map(ctx context.Context, sid string) (wsMap, error) {
	var raw struct {
		Tiles []struct {
			Tile, Title, Kind, State, Agent string
			Orgs                            []string
			TileAgents                      int  `json:"tileAgents"`
			MCP                             bool `json:"mcp"`
		} `json:"tiles"`
		Screens []struct {
			ID, Name string
			Tiles    []string
		} `json:"screens"`
		Orgs []wsOrg `json:"orgs"`
	}
	if err := h.get(ctx, sid, "/api/xbin/delegate/map", &raw); err != nil {
		return wsMap{}, err
	}
	var rep struct {
		Statuses map[string]struct{ Level, Message string } `json:"statuses"`
	}
	_ = h.get(ctx, sid, "/api/xbin/tile-report", &rep) // best effort: a map without status lines is still a map
	out := wsMap{Orgs: raw.Orgs}
	for _, t := range raw.Tiles {
		tile := wsTile{Path: t.Tile, Title: t.Title, Kind: t.Kind, Orgs: t.Orgs, Status: t.State, Agent: orStr(t.Agent, "full"), Running: t.TileAgents > 0, MCP: t.MCP}
		if st, ok := rep.Statuses[t.Tile]; ok && st.Message != "" {
			tile.Report = st.Level + ": " + st.Message
		}
		out.Tiles = append(out.Tiles, tile)
	}
	for _, sc := range raw.Screens {
		out.Screens = append(out.Screens, wsScreen{ID: sc.ID, Name: sc.Name, Tiles: sc.Tiles})
	}
	return out, nil
}

// Tile is one tile of the map with its runtime state and what xbind flags.
func (h wsHTTP) Tile(ctx context.Context, sid, tile string) (wsTile, error) {
	m, err := h.Map(ctx, sid)
	if err != nil {
		return wsTile{}, err
	}
	var out *wsTile
	for i := range m.Tiles {
		if m.Tiles[i].Path == tile {
			out = &m.Tiles[i]
		}
	}
	if out == nil {
		return wsTile{}, &xbin.DelegateError{Status: 403, Refusal: "tile", Message: "no such tile for this session: it isn't there, your person can't read it, or it isn't connected"}
	}
	var st struct {
		Backend struct {
			State string `json:"state"`
		} `json:"backend"`
		Alerts []json.RawMessage `json:"alerts"`
	}
	if err := h.get(ctx, sid, "/api/xbin/tile-status?component="+url.QueryEscape(tile), &st); err == nil {
		if st.Backend.State != "" {
			out.Status = st.Backend.State
		}
		for _, a := range st.Alerts {
			var s string
			if json.Unmarshal(a, &s) != nil {
				var o struct{ Kind, Message string }
				_ = json.Unmarshal(a, &o)
				s = strings.TrimSpace(o.Kind + ": " + o.Message)
			}
			out.Alerts = append(out.Alerts, s)
		}
	}
	return *out, nil
}

func (h wsHTTP) Logs(ctx context.Context, sid, tile string, lines int) (string, error) {
	resp, err := h.do(ctx, sid, http.MethodGet, "/api/xbin/logs?component="+url.QueryEscape(tile)+"&tail=65536", nil, "", nil)
	if err != nil {
		return "", err
	}
	if err := h.refused(resp); err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	ls := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if lines > 0 && len(ls) > lines {
		ls = ls[len(ls)-lines:]
	}
	return strings.Join(ls, "\n"), nil
}

// The docs reads need no handle (an agent can read them before it has one).
func (h wsHTTP) DocsSearch(ctx context.Context, sid, q string, limit int) ([]wsDocHit, error) {
	var out struct {
		Results []struct{ Path, Title, Section, Heading, Snippet string } `json:"results"`
	}
	err := h.get(ctx, sid, "/api/xbin/docs/search?q="+url.QueryEscape(clip(q, 200))+"&limit="+strconv.Itoa(limit), &out)
	hits := make([]wsDocHit, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, wsDocHit{Path: r.Path, Section: r.Section, Title: orStr(r.Heading, r.Title), Snippet: r.Snippet})
	}
	return hits, err
}

func (h wsHTTP) DocsPage(ctx context.Context, sid, path, section string) (wsDocPage, error) {
	var raw struct {
		Path, Title, Markdown string
		Truncated             bool
		Sections              []struct{ Section, Heading string }
	}
	if err := h.get(ctx, sid, "/api/xbin/docs/page?path="+url.QueryEscape(path)+"&section="+url.QueryEscape(section), &raw); err != nil {
		return wsDocPage{}, err
	}
	out := wsDocPage{Path: raw.Path, Title: raw.Title, Text: raw.Markdown}
	if raw.Truncated {
		out.Text += "\n\n(the page is longer: read it a section at a time)"
	}
	for _, s := range raw.Sections {
		out.Sections = append(out.Sections, s.Section)
	}
	return out, nil
}

func (h wsHTTP) CodeTree(ctx context.Context, sid, tile string) ([]wsCodeFile, error) {
	var out struct {
		Files []wsCodeFile `json:"files"`
	}
	err := h.get(ctx, sid, "/api/xbin/code/tree?component="+url.QueryEscape(tile), &out)
	return out.Files, err
}

func (h wsHTTP) CodeFile(ctx context.Context, sid, tile, file string) (wsCodeText, error) {
	var out wsCodeText
	return out, h.get(ctx, sid, "/api/xbin/code/file?component="+url.QueryEscape(tile)+"&file="+url.QueryEscape(file), &out)
}

// --- one call ----------------------------------------------------------------------

func (h wsHTTP) Call(ctx context.Context, sid string, c wsCall) (wsAnswer, error) {
	hdr := map[string]string{}
	if c.DryRun {
		hdr["X-XBin-Dry-Run"] = "1"
	}
	if c.Ticket != "" {
		hdr["X-XBin-Ticket"] = c.Ticket
	}
	resp, err := h.do(ctx, sid, c.Method, c.Path, c.Body, c.ContentType, hdr)
	if err != nil {
		return wsAnswer{}, err
	}
	if c.DryRun {
		if err := h.refused(resp); err != nil {
			return wsAnswer{}, err
		}
		defer resp.Body.Close()
		var d wsDry
		if err := json.NewDecoder(io.LimitReader(resp.Body, wsReadMax)).Decode(&d); err != nil {
			return wsAnswer{}, err
		}
		// A dry run xbind answers with allowed: false carries the refusal in
		// words — `refusal` is the sentence of the tile's or its org's rule,
		// with no code and no `error` (every other refusal is a 403, above).
		// Here it is a refusal like the others: the code "tile", the words
		// in Error.
		if !d.Allowed && d.Error == "" && d.Refusal != "" {
			d.Error, d.Refusal = d.Refusal, orStr(d.RefusalCode, "tile")
		}
		return wsAnswer{Status: resp.StatusCode, Dry: &d}, nil
	}
	// xbind's own refusal (a code) is an error; a tile's own answer, whatever
	// its status, is the call's result
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		var b struct{ Error, Refusal, Class string }
		_ = json.Unmarshal(raw, &b)
		if b.Refusal != "" || strings.HasPrefix(c.Path, "/api/xbin/") {
			return wsAnswer{}, &xbin.DelegateError{Status: resp.StatusCode, Refusal: b.Refusal, Class: b.Class, Message: orStr(b.Error, strings.TrimSpace(string(raw)))}
		}
		return wsAnswer{Status: resp.StatusCode, Type: resp.Header.Get("Content-Type"), Body: raw}, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, wsReadMax))
	return wsAnswer{Status: resp.StatusCode, Type: resp.Header.Get("Content-Type"), Body: raw, Under: resp.Header.Get("X-XBin-Under")}, nil
}

func (h wsHTTP) Ask(ctx context.Context, sid string, c wsCall, summary string) (wsRequest, error) {
	in := map[string]any{"method": c.Method, "path": c.Path, "summary": summary} // the path as it will be sent
	if len(c.Body) > 0 {
		in["bodyBase64"] = c.Body // []byte marshals as standard base64
	}
	if c.ContentType != "" {
		in["contentType"] = c.ContentType
	}
	if c.Path == "/api/xbin/term/sessions" {
		// this backend knows a start that failed for a reason not the agent's: it waits for its person's Try
		// again, and makes the same start when told (D250 review L3) — an older one gets the open's answer
		in["knows"] = []string{"retry"}
	}
	var raw wsRequestWire
	if err := h.json(ctx, sid, http.MethodPost, "/api/xbin/delegate/requests", in, &raw); err != nil {
		return wsRequest{}, err
	}
	return raw.request(), nil
}

// wsRequestWire is a request as xbind writes it (its times are RFC 3339).
type wsRequestWire struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	State   string         `json:"state"`
	Grant   string         `json:"grant"`
	Tile    string         `json:"tile"`
	Preview map[string]any `json:"preview"`
	Expires json.RawMessage
	Decided json.RawMessage
	Via     string     `json:"decidedVia"`
	Chosen  *wsChosen  `json:"chosen"`
	Failure *wsFailure `json:"failure"`
}

func (w wsRequestWire) request() wsRequest {
	return wsRequest{ID: w.ID, Kind: w.Kind, State: w.State, Grant: w.Grant, Tile: w.Tile, Preview: w.Preview, Expires: wireUnix(w.Expires),
		Decided: wireUnix(w.Decided), Via: w.Via, Chosen: w.Chosen, Failure: w.Failure}
}

// Standing lists the person's standing approvals.
func (h wsHTTP) Standing(ctx context.Context, sid string) ([]wsStanding, error) {
	var raw struct {
		Approvals []struct {
			ID, Tile, Scope, Method, Path, Request string
			Until                                  json.RawMessage
		} `json:"approvals"`
	}
	if err := h.get(ctx, sid, "/api/xbin/delegate/approvals", &raw); err != nil {
		return nil, err
	}
	out := make([]wsStanding, 0, len(raw.Approvals))
	for _, a := range raw.Approvals {
		st := wsStanding{ID: a.ID, Tile: a.Tile, Scope: a.Scope, Method: a.Method, Path: a.Path, Request: a.Request, Until: wireUnix(a.Until)}
		out = append(out, st)
	}
	return out, nil
}

func (h wsHTTP) Request(ctx context.Context, sid, id string) (wsRequest, error) {
	var raw wsRequestWire
	if err := h.get(ctx, sid, "/api/xbin/delegate/requests/"+url.PathEscape(id), &raw); err != nil {
		return wsRequest{}, err
	}
	return raw.request(), nil
}

func (h wsHTTP) Withdraw(ctx context.Context, sid, id string) error {
	return h.json(ctx, sid, http.MethodDelete, "/api/xbin/delegate/requests/"+url.PathEscape(id), nil, nil)
}

// --- tile agents -------------------------------------------------------------------

// startCall is a's start as xbind's session directory takes it: the tile,
// the brief as the session's first prompt (bound into the ticket, shown by
// xbind, sent by xbind), a name, and the harness only when the person
// picked one. Never a mode, a network or an option.
func (wsHTTP) startCall(a wsAgentStart) wsCall {
	in := map[string]any{"cwd": a.Tile, "kind": "agent"}
	if a.Resume != "" {
		in["resume"] = a.Resume
	} else {
		if a.Brief != "" { // xbind writes it, and the person's review says so
			in["brief"] = map[string]string{"template": a.Brief}
		} else {
			in["prompt"] = a.Prompt
		}
		if a.Name != "" {
			in["name"] = a.Name
		}
		if a.Harness != "" { // the agent's suggestion (D250): the person picks on the approval
			in["provider"] = a.Harness
		}
		if a.Model != "" {
			in["model"] = a.Model
		}
		if a.Mode != "" {
			in["mode"] = a.Mode
		}
		if a.Continue != "" {
			in["continue"] = a.Continue
		}
	}
	body, _ := json.Marshal(in)
	return wsCall{Method: http.MethodPost, Path: "/api/xbin/term/sessions", Body: body, ContentType: "application/json"}
}

// StartDefaults reads what a start in tile resolves to.
func (h wsHTTP) StartDefaults(ctx context.Context, sid, tile string) (wsStartDefaults, error) {
	var out wsStartDefaults
	if err := h.get(ctx, sid, "/api/xbin/delegate/start-defaults?tile="+url.QueryEscape(tile), &out); err != nil {
		return out, err
	}
	out.Tile = orStr(out.Tile, tile)
	def, _ := out.of(out.Harness)
	out.Mode, out.Asks, out.Net, out.Ticket = def.Mode, def.Asks, def.Net, def.NeedsTicket
	return out, nil
}

// wsDefaultsFresh is how long xbind's start-defaults for a tile are used
// to name the harness of the tile agents running there (named) before they
// are read again; wsDefaultsSeen what was last read, per conversation and
// tile. The brief's card always reads them anew (StartDefaults).
const wsDefaultsFresh = 30 * time.Second

var wsDefaultsSeen sync.Map // sid + "\x00" + tile → wsDefaultsAt

type wsDefaultsAt struct {
	d  wsStartDefaults
	at time.Time
}

// named fills what xbind says of a's harness — GET /delegate/start-defaults
// for its tile, the row of a's provider: the name people call it, and
// whether the mode the session is in asks its person before it acts. This
// backend keeps no catalog of harnesses: a harness xbind doesn't list for
// the tile (its rules moved, the read was refused) has no name here (the
// caller keeps the one it had, or shows the id), and a mode that isn't the
// one xbind names for the harness isn't known to ask.
func (h wsHTTP) named(ctx context.Context, sid string, a wsAgent) wsAgent {
	a.Name, a.Asks = "", false
	if a.Provider == "" || a.Tile == "" {
		return a
	}
	key := sid + "\x00" + a.Tile
	var d wsStartDefaults
	if c, ok := wsDefaultsSeen.Load(key); ok && time.Since(c.(wsDefaultsAt).at) < wsDefaultsFresh {
		d = c.(wsDefaultsAt).d
	} else {
		var err error
		if d, err = h.StartDefaults(ctx, sid, a.Tile); err != nil {
			return a
		}
		wsDefaultsSeen.Range(func(k, v any) bool { // what went stale goes: a closed conversation's entries don't stay
			if time.Since(v.(wsDefaultsAt).at) >= wsDefaultsFresh {
				wsDefaultsSeen.Delete(k)
			}
			return true
		})
		wsDefaultsSeen.Store(key, wsDefaultsAt{d, time.Now()})
	}
	if hn, ok := d.of(a.Provider); ok {
		// only the mode xbind names as asking asks (its ask or plan mode); a
		// session whose mode isn't known isn't known to ask
		a.Name, a.Asks = orStr(hn.Name, hn.ID), hn.Asks && a.Mode != "" && a.Mode == hn.Mode
	}
	return a
}

func (h wsHTTP) AskAgentStart(ctx context.Context, sid string, a wsAgentStart, summary string) (wsRequest, error) {
	return h.Ask(ctx, sid, h.startCall(a), summary)
}

// AgentStart opens the session. xbind sends the brief itself, as the
// session's first prompt.
func (h wsHTTP) AgentStart(ctx context.Context, sid string, a wsAgentStart, ticket string, dry bool) (wsAgent, *wsDry, error) {
	c := h.startCall(a)
	c.Ticket, c.DryRun = ticket, dry
	ans, err := h.Call(ctx, sid, c)
	if err == nil && dry && ans.Dry != nil && !ans.Dry.Allowed && ans.Dry.RefusalCode == "" { // an xbind that names no code
		// a start refused whatever approves it carries xbind's sentence only;
		// it has two reasons (docs/protocol/delegation.md §Dry run): a first
		// prompt that begins with a harness command, or the conversation's
		// taint — told apart by the one this side can see
		ans.Dry.Refusal = wsStartRefusal(a.Prompt)
	}
	if err != nil || dry {
		return wsAgent{}, ans.Dry, err
	}
	var out wsSessionWire
	if err := json.Unmarshal(ans.Body, &out); err != nil || out.ID == "" {
		return wsAgent{}, nil, fmt.Errorf("xbin started the session but its answer couldn't be read")
	}
	ag := out.agent()
	ag.Mine = true
	if ag.Tile == "" {
		ag.Tile = a.Tile
	}
	return h.named(ctx, sid, ag), nil, nil
}

// wsStartRefusal is the code of a start's dry-run refusal: "command" for a
// first prompt that begins with "/", else "taint".
func wsStartRefusal(prompt string) string {
	if strings.HasPrefix(strings.TrimSpace(prompt), "/") {
		return "command"
	}
	return "taint"
}

func (wsHTTP) createCall(t wsTileCreate) wsCall {
	body, _ := json.Marshal(map[string]any{"source": t.Template, "path": t.Path})
	return wsCall{Method: http.MethodPost, Path: "/api/xbin/templates/new", Body: body, ContentType: "application/json"}
}

func (h wsHTTP) AskTileCreate(ctx context.Context, sid string, t wsTileCreate, summary string) (wsRequest, error) {
	return h.Ask(ctx, sid, h.createCall(t), summary)
}

func (h wsHTTP) TileCreate(ctx context.Context, sid string, t wsTileCreate, ticket string, dry bool) (string, *wsDry, error) {
	c := h.createCall(t)
	c.Ticket, c.DryRun = ticket, dry
	ans, err := h.Call(ctx, sid, c)
	if err != nil || dry {
		return "", ans.Dry, err
	}
	var out struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(ans.Body, &out)
	return orStr(out.Path, t.Path), nil, nil
}

// wsSessionWire is a session row of xbind's directory. Turn and Last are
// the compact read's, for the caller's own agent sessions: the prompts it
// took so far and the last line it wrote since its last prompt.
type wsSessionWire struct {
	ID, Cwd, Kind, Provider, Mode, Status, Origin, Name string
	Model                                               string
	Continued                                           bool // a start its person chose to continue this session with (D250)
	Pending, Questions, Turn                            int
	Last                                                string
	// for an id no longer live (the compact read with ids): status "ended",
	// and by what — "exit", or "restart" when xbind has no record of an end
	EndedBy string
}

// endedBy is "restart" when xbind has no record of the session's end: its
// own restart cut the session off.
func (w wsSessionWire) endedBy() string {
	if w.Status == "ended" && w.EndedBy == "restart" {
		return "restart"
	}
	return ""
}

// agent is the row as a tile agent. Its harness's name and whether its
// mode asks are not the row's to say: named reads them from xbind.
func (w wsSessionWire) agent() wsAgent {
	a := wsAgent{ID: w.ID, Tile: w.Cwd, Provider: w.Provider, Mode: w.Mode, Status: wsAgentStatus(w.Status), Pending: w.Pending, Questions: w.Questions,
		Turn: w.Turn, Last: w.Last, Origin: w.Origin, Mine: w.Origin == "agent", EndedBy: w.endedBy(), Model: w.Model, Continued: w.Continued}
	if w.Status == "waiting_permission" && !a.waits() {
		a.Pending = 1
	}
	return a
}

// wsAgentStatus is xbind's session status in the agent's five words.
func wsAgentStatus(s string) string {
	switch s {
	case "starting":
		return "starting"
	case "running", "cancelling", "waiting_permission":
		return "running"
	case "idle":
		return "idle"
	case "error":
		return "failed"
	case "exited", "":
		return "ended"
	}
	return s
}

// Agents are the tile agents this agent started for the person.
func (h wsHTTP) Agents(ctx context.Context, sid string) ([]wsAgent, error) {
	var rows []wsSessionWire
	if err := h.get(ctx, sid, "/api/xbin/term/sessions?origin=agent&compact=1", &rows); err != nil {
		return nil, err
	}
	var out []wsAgent
	for _, r := range rows {
		if r.Kind == "agent" {
			out = append(out, h.named(ctx, sid, r.agent()))
		}
	}
	return out, nil
}

// Agent is where one tile agent stands. Its status, the turns it took and
// its last line are xbind's compact read of the session directory — which,
// asked about an id that left the directory, says how it ended: by its own
// exit, or by an xbind restart (no record of an end). Asked only about a
// session this agent started: for an id that never existed xbind would
// answer "restart" too. What a waiting session waits for is in the
// session's own read, made only then.
func (h wsHTTP) Agent(ctx context.Context, sid, id string) (wsAgent, error) {
	var rows []wsSessionWire
	if err := h.get(ctx, sid, "/api/xbin/term/sessions?origin=agent&compact=1&ids="+url.QueryEscape(id), &rows); err != nil {
		return wsAgent{}, err
	}
	var row *wsSessionWire
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
		}
	}
	if row == nil {
		return wsAgent{ID: id, Status: "ended"}, nil
	}
	a := row.agent()
	if row.Kind == "" { // not in the live directory any more
		a.Status = "ended"
		return a, nil
	}
	a = h.named(ctx, sid, a)
	if !a.waits() {
		return a, nil
	}
	var out struct {
		Permissions []struct {
			ToolCall struct{ Title string } `json:"toolCall"`
		} `json:"permissions"`
		Elicitations []struct{ Message string } `json:"elicitations"`
	}
	if err := h.get(ctx, sid, "/api/xbin/term/sessions/"+url.PathEscape(id), &out); err != nil {
		return a, nil // it waits; for what is read next time
	}
	switch {
	case len(out.Elicitations) > 0:
		a.Waiting = out.Elicitations[0].Message
	case len(out.Permissions) > 0:
		a.Waiting = "permission: " + out.Permissions[0].ToolCall.Title
	}
	return a, nil
}

// messageCall is a message to session id as a request binds it and its
// ticket's call sends it: the same bytes (D250).
func messageCall(id, text string) wsCall {
	body, _ := json.Marshal(map[string]any{"text": text})
	return wsCall{Method: http.MethodPost, Path: "/api/xbin/term/sessions/" + url.PathEscape(id) + "/queue", Body: body, ContentType: "application/json"}
}

func (h wsHTTP) AskMessage(ctx context.Context, sid, id, text, summary string) (wsRequest, error) {
	return h.Ask(ctx, sid, messageCall(id, text), summary)
}

func (h wsHTTP) AgentSayTicket(ctx context.Context, sid, id, text, ticket string) (string, error) {
	c := messageCall(id, text)
	c.Ticket = ticket
	ans, err := h.Call(ctx, sid, c)
	if err != nil {
		return "", err
	}
	var out struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(ans.Body, &out)
	return out.Outcome, nil
}

func (h wsHTTP) AgentSay(ctx context.Context, sid, id, text string) (string, error) {
	var out struct {
		Outcome string `json:"outcome"`
	}
	err := h.json(ctx, sid, http.MethodPost, "/api/xbin/term/sessions/"+url.PathEscape(id)+"/queue", map[string]any{"text": text}, &out)
	return out.Outcome, err
}

func (h wsHTTP) AgentCancel(ctx context.Context, sid, id string) error {
	return h.json(ctx, sid, http.MethodPost, "/api/xbin/term/sessions/"+url.PathEscape(id)+"/cancel", nil, nil)
}

// AgentReport folds the tail of a session's log — live, or its history once
// it ended — into what its last turn left. Patches and file contents in the
// log are never copied: a change is its path and line counts.
// wsReportPages bounds how far back a report reads for its turn's start
// (pages of 400 events); wsReportCommands is how many commands it lists.
const (
	wsReportPages    = 12
	wsReportCommands = 20
)

func (h wsHTTP) AgentReport(ctx context.Context, sid, id string) (wsAgentReport, error) {
	type event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	type evPage struct {
		Events     []event `json:"events"`
		HasOlder   bool    `json:"hasOlder"`
		NextBefore int     `json:"nextBefore"`
	}
	base := "/api/xbin/term/sessions/" + url.PathEscape(id) + "/events"
	var page evPage
	err := h.get(ctx, sid, base+"?limit=400", &page)
	if err != nil {
		var de *xbin.DelegateError
		if !errors.As(err, &de) || de.Status != http.StatusNotFound {
			return wsAgentReport{}, err
		}
		base = "/api/xbin/agent/history/" + url.PathEscape(id) + "/events"
		if err := h.get(ctx, sid, base+"?limit=400", &page); err != nil {
			return wsAgentReport{}, err
		}
	}
	// The report is of the session's last turn — from its last prompt on. The tail page alone often
	// doesn't reach back to it: an agent that writes its answer token by token (Codex) fills 400 events
	// with the answer, and its commands and file changes are in the pages before (the feedback run,
	// 2026-11-17: "Files changed: none reported" of a turn that changed two). Read back to the prompt.
	prompted := func(evs []event) bool {
		for _, ev := range evs {
			if ev.Type == "message.delta" {
				var d struct {
					Role    string
					Steered bool
				}
				if json.Unmarshal(ev.Data, &d) == nil && d.Role == "user" && !d.Steered {
					return true
				}
			}
		}
		return false
	}
	events := page.Events
	for n := 0; page.HasOlder && page.NextBefore > 0 && !prompted(events) && n < wsReportPages; n++ {
		var older evPage
		if err := h.get(ctx, sid, fmt.Sprintf("%s?before=%d&limit=400", base, page.NextBefore), &older); err != nil || len(older.Events) == 0 {
			break
		}
		events, page = append(older.Events, events...), older
	}
	var rep wsAgentReport
	var text strings.Builder
	files := map[string]wsFileStat{}
	// a command is a tool.call (its title and kind) and the tool.update that ends it (its exit code): two
	// events for most agents, one for some
	type call struct {
		title, kind string
		told        bool
	}
	calls := map[string]*call{}
	for _, ev := range events {
		switch ev.Type {
		case "message.delta":
			var d struct {
				Role, Text, Parent string
				Steered            bool
			}
			_ = json.Unmarshal(ev.Data, &d)
			switch {
			case d.Role == "user" && !d.Steered: // a new prompt: what came before is an earlier turn's
				text.Reset()
				rep.Verified, files, calls = nil, map[string]wsFileStat{}, map[string]*call{}
			case d.Role == "user":
			case d.Parent == "":
				text.WriteString(d.Text)
			}
		case "tool.call", "tool.update":
			var d struct {
				ID, Title, Kind, Status string
				ExitCode                *int `json:"exitCode"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if ev.Type == "tool.call" {
				text.Reset() // the answer is what it said after its last call
			}
			c := calls[d.ID]
			if c == nil {
				c = &call{}
				calls[d.ID] = c
			}
			if d.Title != "" {
				c.title = d.Title
			}
			if d.Kind != "" {
				c.kind = d.Kind
			}
			if c.kind == "execute" && d.ExitCode != nil && c.title != "" && !c.told {
				c.told = true
				rep.Verified = append(rep.Verified, fmt.Sprintf("%s → exit %d", clip(oneLineTitle(c.title), 160), *d.ExitCode))
			}
		case "files.changed":
			var d struct {
				Changes []struct {
					Path     string
					Add, Del int
				} `json:"changes"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			for _, c := range d.Changes {
				f := files[c.Path]
				f.Path, f.Add, f.Del = c.Path, f.Add+c.Add, f.Del+c.Del
				files[c.Path] = f
			}
		case "turn.end":
			var d struct {
				Turn       int
				StopReason string `json:"stopReason"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			rep.Turn, rep.Status = d.Turn, d.StopReason
		case "status":
			var d struct{ Status string }
			_ = json.Unmarshal(ev.Data, &d)
			if d.Status == "running" || d.Status == "starting" {
				rep.Status = "running"
			}
		}
	}
	if n := len(rep.Verified); n > wsReportCommands { // the last ones: the checks come after the looking around
		rep.Verified = append([]string{fmt.Sprintf("(%d earlier commands not shown)", n-wsReportCommands)}, rep.Verified[n-wsReportCommands:]...)
	}
	rep.Answer = strings.TrimSpace(text.String())
	for _, f := range files {
		rep.Files = append(rep.Files, f)
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	return rep, nil
}

func (h wsHTTP) Templates(ctx context.Context, sid string) ([]wsTemplate, error) {
	var rows []struct{ ID, Title, Description string }
	if err := h.get(ctx, sid, "/api/xbin/templates", &rows); err != nil {
		return nil, err
	}
	out := make([]wsTemplate, 0, len(rows))
	for _, r := range rows {
		out = append(out, wsTemplate{ID: r.ID, Name: r.Title, Description: r.Description})
	}
	return out, nil
}

// --- admin change sets, previews ---------------------------------------------------

func (h wsHTTP) Propose(ctx context.Context, sid, title string, items []xbin.Change) (wsChangeSet, error) {
	var out wsChangeSet
	return out, h.json(ctx, sid, http.MethodPost, "/api/xbin/delegate/changesets", map[string]any{"title": title, "items": items}, &out)
}

func (h wsHTTP) ProposePlan(ctx context.Context, sid, title string, items []xbin.Change) (wsChangeSet, error) {
	var out wsChangeSet
	return out, h.json(ctx, sid, http.MethodPost, "/api/xbin/delegate/plans", map[string]any{"title": title, "items": items}, &out)
}

// DescribePlan asks xbind to describe a plan and propose nothing (dryRun).
// An xbind that doesn't know the flag proposes it: that plan is withdrawn
// at once — a dry run never leaves something in front of a person.
func (h wsHTTP) DescribePlan(ctx context.Context, sid, title string, items []xbin.Change) (wsChangeSet, error) {
	var out struct {
		wsChangeSet
		DryRun bool `json:"dryRun"`
	}
	if err := h.json(ctx, sid, http.MethodPost, "/api/xbin/delegate/plans", map[string]any{"title": title, "items": items, "dryRun": true}, &out); err != nil {
		if wsNoDryRun(err) {
			return wsChangeSet{}, fmt.Errorf("%w (this xbind describes no plan without proposing it)", errWsUnavailable)
		}
		return wsChangeSet{}, err
	}
	if !out.DryRun && out.ID != "" { // an xbind that read past the field proposed it: withdrawn
		_ = h.json(ctx, sid, http.MethodDelete, "/api/xbin/delegate/changesets/"+url.PathEscape(out.ID), nil, nil)
		return wsChangeSet{}, errWsUnavailable
	}
	return out.wsChangeSet, nil
}

// wsNoDryRun: err is how an xbind from before plans had a dry run answers
// one — its body is read strictly, so the field it doesn't know is a 400
// whose sentence lists the fields it does ("need {title, items: […]}",
// without dryRun). Nothing was proposed; the caller says the dry run in
// its own words. A newer xbind's 400 names dryRun, or is about a line.
func wsNoDryRun(err error) bool {
	var de *xbin.DelegateError
	return errors.As(err, &de) && de.Refusal == "" && de.Status == http.StatusBadRequest &&
		strings.Contains(de.Message, "need {title, items") && !strings.Contains(de.Message, "dryRun")
}

func (h wsHTTP) ChangeSet(ctx context.Context, sid, id string) (wsChangeSet, error) {
	var out wsChangeSet
	return out, h.get(ctx, sid, "/api/xbin/delegate/changesets/"+url.PathEscape(id), &out)
}

func (h wsHTTP) ChangeSets(ctx context.Context, sid string) ([]wsChangeSet, error) {
	var out struct {
		Changesets []wsChangeSet `json:"changesets"`
	}
	return out.Changesets, h.get(ctx, sid, "/api/xbin/delegate/changesets", &out)
}

// Preview is one tile as its person sees it, read-only. It needs no
// ticket and counts against the person's preview limits; the picture stays
// with xbind's answer — a model is given the text.
func (h wsHTTP) Preview(ctx context.Context, sid string, p wsPreviewAsk) (wsPreview, error) {
	in := map[string]any{"tile": p.Tile}
	if p.At != "" {
		in["at"] = p.At
	}
	if p.Phone {
		in["viewport"] = map[string]int{"width": 390, "height": 844}
	}
	var raw struct {
		OK                           bool
		Notice, Title, Snapshot, PNG string
		Note, Error                  string
		Clipped                      bool
		Console, PageErrors          []json.RawMessage
		RequestsFailed               []struct {
			URL, Method, Error string
			Status             int
		} `json:"requestsFailed"`
	}
	err := h.json(ctx, sid, http.MethodPost, "/api/xbin/preview", in, &raw)
	var de *xbin.DelegateError
	if errors.As(err, &de) && de.Refusal == "" && (de.Status == http.StatusServiceUnavailable || de.Status == http.StatusNotFound) {
		return wsPreview{}, fmt.Errorf("%w: %s", errWsUnavailable, orStr(de.Message, "previews are off here"))
	}
	if err != nil {
		return wsPreview{}, err
	}
	out := wsPreview{Notice: raw.Notice, Title: raw.Title, Snapshot: raw.Snapshot, Picture: raw.PNG != "", Clipped: raw.Clipped, Note: orStr(raw.Note, raw.Error)}
	line := func(m json.RawMessage) string {
		var s string
		if json.Unmarshal(m, &s) == nil {
			return s
		}
		var o struct{ Type, Text, Message string }
		_ = json.Unmarshal(m, &o)
		return strings.TrimSpace(o.Type + " " + orStr(o.Text, o.Message))
	}
	for _, m := range append(raw.PageErrors, raw.Console...) {
		out.Errors = append(out.Errors, line(m))
	}
	for _, f := range raw.RequestsFailed {
		out.Failed = append(out.Failed, strings.TrimSpace(fmt.Sprintf("%s %s → %s", f.Method, f.URL, orStr(f.Error, strconv.Itoa(f.Status)))))
	}
	return out, nil
}

// wireUnix reads a time xbind wrote as RFC 3339 (or as unix seconds).
func wireUnix(raw json.RawMessage) int64 {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return max(n, 0)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return max(t.Unix(), 0) // Go's zero time: none
		}
	}
	return 0
}
