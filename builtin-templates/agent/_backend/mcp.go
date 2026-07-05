// mcp.go — a deliberately small MCP client for the streamable-HTTP transport:
// JSON-RPC initialize → tools/list → tools/call. Configured servers' tools are
// merged into the agent's tool set as `mcp:<server>:<tool>`. This is a working
// seam, not a full MCP stack — extend it (stdio transport, resources, prompts,
// auth) per instance. Disable by leaving Config.MCP empty.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

type MCPServer struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var rpcID int64

// rpc sends one JSON-RPC call and returns the result. sessionID (if non-empty)
// is sent and any server-issued session id is returned for reuse.
func (s MCPServer) rpc(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, string, error) {
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: atomic.AddInt64(&rpcID, 1), Method: method, Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, sessionID, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, sessionID, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		sessionID = sid
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, sessionID, fmt.Errorf("mcp %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// A streamable-HTTP server may answer with SSE; pull the first data: frame.
	payload := raw
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = sseFirstData(raw)
	}
	var rr rpcResp
	if err := json.Unmarshal(payload, &rr); err != nil {
		return nil, sessionID, fmt.Errorf("mcp %s: bad response: %w", method, err)
	}
	if rr.Error != nil {
		return nil, sessionID, fmt.Errorf("mcp %s: %s", method, rr.Error.Message)
	}
	return rr.Result, sessionID, nil
}

func sseFirstData(raw []byte) []byte {
	for _, line := range strings.Split(string(raw), "\n") {
		if d, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			return []byte(strings.TrimSpace(d))
		}
	}
	return raw
}

// session runs initialize (required before tools/*) and returns the session id.
func (s MCPServer) session(ctx context.Context) (string, error) {
	_, sid, err := s.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "xbin-agent", "version": "0"},
	}, "")
	return sid, err
}

// mcpTools lists every configured server's tools, prefixed for the LLM.
func (ag *Agent) mcpTools(ctx context.Context, cfg Config) []toolSpec {
	var out []toolSpec
	for _, srv := range cfg.MCP {
		sid, err := srv.session(ctx)
		if err != nil {
			continue // a down server shouldn't break the loop
		}
		res, _, err := srv.rpc(ctx, "tools/list", map[string]any{}, sid)
		if err != nil {
			continue
		}
		var list struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		}
		if json.Unmarshal(res, &list) != nil {
			continue
		}
		for _, t := range list.Tools {
			schema := t.InputSchema
			if schema == nil {
				schema = obj(nil, map[string]any{})
			}
			out = append(out, toolSpec{Type: "function", Function: funcDef{
				Name:        "mcp:" + srv.Name + ":" + t.Name,
				Description: t.Description,
				Parameters:  schema,
			}})
		}
	}
	return out
}

// mcpCall dispatches a `mcp:<server>:<tool>` call.
func (ag *Agent) mcpCall(ctx context.Context, cfg Config, name string, args map[string]any) (string, error) {
	rest := strings.TrimPrefix(name, "mcp:")
	server, tool, ok := strings.Cut(rest, ":")
	if !ok {
		return "", fmt.Errorf("bad mcp tool name %q", name)
	}
	var srv *MCPServer
	for i := range cfg.MCP {
		if cfg.MCP[i].Name == server {
			srv = &cfg.MCP[i]
			break
		}
	}
	if srv == nil {
		return "", fmt.Errorf("no configured MCP server %q", server)
	}
	sid, err := srv.session(ctx)
	if err != nil {
		return "", err
	}
	res, _, err := srv.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, sid)
	if err != nil {
		return "", err
	}
	// MCP tool results are {content: [{type:"text", text:"..."}], ...}. Flatten
	// text parts; fall back to the raw JSON for anything else.
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(res, &out) == nil && len(out.Content) > 0 {
		var sb strings.Builder
		for _, c := range out.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
				sb.WriteByte('\n')
			}
		}
		if sb.Len() > 0 {
			return strings.TrimSpace(sb.String()), nil
		}
	}
	return string(res), nil
}
