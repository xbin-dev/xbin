// mcp.go — the MCP wire: JSON-RPC 2.0 over the streamable-HTTP transport,
// the part of it an agent's client uses. One POST to /mcp carries one
// message (or a batch, which the 2025-03-26 revision allows); a request is
// answered with JSON, a notification or a client's response with 202 and no
// body. The methods: initialize (with protocol version negotiation), ping,
// tools/list and tools/call. Stateless — no Mcp-Session-Id is issued — so
// the backend can restart between any two calls, and a client that sends
// initialize before each call (the agent template's does) loses nothing.
//
// You shouldn't need to change this file to add a tool: tools.go is where
// tools go. Keep the wire as it is — an agent instance bound to this tile
// speaks exactly this (docs/mcp.md).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

// serverName and serverVersion are what initialize tells a client.
const (
	serverName    = "mcp-server"
	serverVersion = "1.0.0"
)

// protocolVersions are the MCP revisions this server speaks, newest first.
// initialize answers the client's version when it is one of these, else the
// newest (the client then decides whether it can go on).
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes (JSON-RPC 2.0 §5.1; MCP uses them as they are).
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

// maxBody bounds one POST.
const maxBody = 4 << 20

// toolTimeout bounds one tools/call.
const toolTimeout = 60 * time.Second

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

func rpcErr(code int, format string, a ...any) *rpcError {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, a...)}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// serveMCP is POST /mcp.
func serveMCP(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(protocolVersions, v) {
		// the transport's rule: an unsupported version on a request is a 400
		writeRPC(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: rpcErr(errInvalidRequest, "unsupported MCP-Protocol-Version %q (this server speaks %v)", v, protocolVersions)})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeRPC(w, http.StatusRequestEntityTooLarge, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: rpcErr(errInvalidRequest, "a message is at most %d bytes", maxBody)})
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(body, &batch) != nil {
			writeRPC(w, http.StatusBadRequest, parseError())
			return
		}
		if len(batch) == 0 {
			writeRPC(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(errInvalidRequest, "an empty batch")})
			return
		}
		var out []rpcResponse
		for _, m := range batch {
			if resp := handleMessage(r.Context(), m); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 { // notifications and responses only
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, http.StatusOK, out)
		return
	}
	if !json.Valid(body) {
		writeRPC(w, http.StatusBadRequest, parseError())
		return
	}
	resp := handleMessage(r.Context(), body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	status := http.StatusOK
	if resp.Error != nil && resp.Error.Code == errInvalidRequest {
		status = http.StatusBadRequest
	}
	writeRPC(w, status, *resp)
}

func parseError() rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(errParse, "the body is not JSON")}
}

func writeRPC(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleMessage answers one JSON-RPC message; nil for a notification or a
// client's response, which get no answer.
func handleMessage(ctx context.Context, raw json.RawMessage) *rpcResponse {
	var m struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  *string         `json:"method"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcErr(errInvalidRequest, "a message is a JSON object")}
	}
	id := m.ID
	if !validID(id) {
		if len(id) == 0 && m.Method != nil && m.JSONRPC == "2.0" {
			return nil // a notification (notifications/initialized, …/cancelled): nothing to answer
		}
		id = json.RawMessage("null")
	}
	switch {
	case m.JSONRPC != "2.0":
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErr(errInvalidRequest, `"jsonrpc" must be "2.0"`)}
	case m.Method == nil && (m.Result != nil || m.Error != nil):
		return nil // a client's answer to a request of ours: this server sends none
	case m.Method == nil:
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErr(errInvalidRequest, `a request needs "method"`)}
	case !validID(m.ID):
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErr(errInvalidRequest, `"id" is a string or a number`)}
	}
	result, err := dispatch(ctx, *m.Method, m.Params)
	if err != nil {
		re, ok := err.(*rpcError)
		if !ok {
			re = rpcErr(errInternal, "%v", err)
		}
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: re}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

// validID: a request id is a string or a number (never null, MCP says).
func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	switch c := id[0]; {
	case c == '"':
		var s string
		return json.Unmarshal(id, &s) == nil
	case c == '-' || c >= '0' && c <= '9':
		var n json.Number
		return json.Unmarshal(id, &n) == nil
	}
	return false
}

// dispatch runs one method.
func dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if p.ProtocolVersion == "" {
			return nil, rpcErr(errInvalidParams, `initialize needs "protocolVersion"`)
		}
		v := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
			"instructions":    "Tools of an xbin tile: tools/list names them, tools/call runs one.",
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			list = append(list, t.describe())
		}
		return map[string]any{"tools": list}, nil // every tool fits one page: no nextCursor
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		t, ok := toolNamed(p.Name)
		if !ok {
			return nil, rpcErr(errInvalidParams, "unknown tool %q (tools/list names them)", p.Name)
		}
		ctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		return t.call(ctx, p.Arguments), nil
	}
	return nil, rpcErr(errMethodNotFound, "method not found: %s (this server answers initialize, ping, tools/list and tools/call)", method)
}

// decodeParams reads a request's params object (absent is an empty one).
func decodeParams(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return rpcErr(errInvalidParams, "bad params: %v", err)
	}
	return nil
}
