// ws_xbin_net.go — wsXbin's web and MCP calls over xbind's delegation
// (docs/protocol/delegation.md §Web and MCP; D239). xbind reads the page:
// this backend's sandbox reaches its model and xbind, nothing else.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const wsWebFetchPath = "/api/xbin/delegate/web/fetch"

// wsWebBody is the body a fetch is made with — and, for a fetch its person
// approves, the bytes the ticket binds: the same function builds both.
func wsWebBody(rawURL string) []byte {
	b, _ := json.Marshal(map[string]string{"url": rawURL})
	return b
}

func (h wsHTTP) WebFetch(ctx context.Context, sid, rawURL, ticket string) (wsWebPage, error) {
	var hdr map[string]string
	if ticket != "" {
		hdr = map[string]string{"X-XBin-Ticket": ticket}
	}
	resp, err := h.do(ctx, sid, http.MethodPost, wsWebFetchPath, wsWebBody(rawURL), "application/json", hdr)
	if err != nil {
		return wsWebPage{}, err
	}
	if err := h.refused(resp); err != nil {
		return wsWebPage{}, err
	}
	defer resp.Body.Close()
	var out wsWebPage
	err = json.NewDecoder(io.LimitReader(resp.Body, wsReadMax)).Decode(&out)
	return out, err
}

func (h wsHTTP) AskWeb(ctx context.Context, sid, rawURL, summary string) (wsRequest, error) {
	return h.Ask(ctx, sid, wsCall{Method: http.MethodPost, Path: wsWebFetchPath, Body: wsWebBody(rawURL), ContentType: "application/json"}, summary)
}

func (h wsHTTP) MarkWeb(ctx context.Context, sid string) error {
	return h.json(ctx, sid, http.MethodPost, wsSessionsPath+"/"+url.PathEscape(sid)+"/taint", map[string]any{"web": true}, nil)
}

// --- MCP tiles ------------------------------------------------------------------------

const wsMCPCallPath = "/api/xbin/delegate/mcp/call"

// wsMCPBody is a tool's call as it is sent: the bytes its ticket binds (a
// map's keys are written sorted, so the same call is the same bytes).
func wsMCPBody(c wsMCPCall) []byte {
	if c.Arguments == nil {
		c.Arguments = map[string]any{}
	}
	b, _ := json.Marshal(c)
	return b
}

// wsDelegateMCP is an xbind refusal behind the MCP client's HTTP error, as
// the SDK's *DelegateError (an MCP server's own error stays as it is).
func wsDelegateMCP(err error) error {
	var he *mcpHTTPError
	if !errors.As(err, &he) {
		return err
	}
	var b struct{ Error, Refusal, Class string }
	if json.Unmarshal([]byte(he.Body), &b) == nil && b.Refusal != "" {
		return &xbin.DelegateError{Status: he.Status, Refusal: b.Refusal, Class: b.Class, Message: b.Error}
	}
	return err
}

// MCPTools reads a tile's tools on its own endpoint, with the template's
// MCP client: initialize, then tools/list — reads at xbind, under the
// tile's rules and the session's reach.
func (h wsHTTP) MCPTools(ctx context.Context, sid, tile string) ([]wsMCPTool, error) {
	ctx = xbin.InAgentSession(ctx, sid)
	srv := MCPServer{Name: tile, URL: "http://xbin/api/" + strings.Trim(tile, "/") + "/mcp", delegate: true}
	session, err := srv.session(ctx)
	if err != nil {
		return nil, wsDelegateMCP(err)
	}
	res, _, err := srv.rpc(ctx, "tools/list", map[string]any{}, session)
	if err != nil {
		return nil, wsDelegateMCP(err)
	}
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			Title       string         `json:"title"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
			Annotations struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return nil, err
	}
	out := make([]wsMCPTool, 0, len(list.Tools))
	for _, t := range list.Tools {
		out = append(out, wsMCPTool{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema, ReadOnly: t.Annotations.ReadOnlyHint})
	}
	return out, nil
}

func (h wsHTTP) MCPCall(ctx context.Context, sid string, c wsMCPCall, ticket string, dry bool) (wsMCPResult, *wsDry, error) {
	hdr := map[string]string{}
	if ticket != "" {
		hdr["X-XBin-Ticket"] = ticket
	}
	if dry {
		hdr["X-XBin-Dry-Run"] = "1"
	}
	resp, err := h.do(ctx, sid, http.MethodPost, wsMCPCallPath, wsMCPBody(c), "application/json", hdr)
	if err != nil {
		return wsMCPResult{}, nil, err
	}
	if err := h.refused(resp); err != nil {
		return wsMCPResult{}, nil, err
	}
	defer resp.Body.Close()
	if dry {
		var d wsDry
		err := json.NewDecoder(io.LimitReader(resp.Body, wsReadMax)).Decode(&d)
		if !d.Allowed && d.Error == "" && d.Refusal != "" {
			d.Error, d.Refusal = d.Refusal, "mcp"
		}
		return wsMCPResult{}, &d, err
	}
	var out wsMCPResult
	err = json.NewDecoder(io.LimitReader(resp.Body, wsReadMax)).Decode(&out)
	return out, nil, err
}

func (h wsHTTP) AskMCP(ctx context.Context, sid string, c wsMCPCall, summary string) (wsRequest, error) {
	return h.Ask(ctx, sid, wsCall{Method: http.MethodPost, Path: wsMCPCallPath, Body: wsMCPBody(c), ContentType: "application/json"}, summary)
}
