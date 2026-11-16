// ws_xbin_net.go — wsXbin's web and MCP calls over xbind's delegation
// (docs/protocol/delegation.md §Web and MCP; D236). xbind reads the page:
// this backend's sandbox reaches its model and xbind, nothing else.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
