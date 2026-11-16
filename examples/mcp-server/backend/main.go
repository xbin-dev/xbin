// An MCP server tile: tools an agent calls, over JSON-RPC 2.0 at POST /mcp.
// The whole server is this file; a tool is an entry of `tools`.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// tool is one tool: what tools/list says of it, and what tools/call runs.
type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"` // for the model: what it does, what it returns
	InputSchema map[string]any `json:"inputSchema"` // JSON Schema of the arguments object
	Annotations map[string]any `json:"annotations,omitempty"`
	// Run returns what the model reads, and the same as data. An error is the
	// tool's failure (a bad argument): the model reads it and can try again.
	Run func(args json.RawMessage) (text string, data any, err error) `json:"-"`
}

var tools = []tool{{
	Name: "roll_dice", Title: "Roll dice",
	Description: "Roll dice: count dice (default 1) with sides faces each (default 6). Returns each roll and their sum.",
	InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"sides": map[string]any{"type": "integer", "minimum": 2, "maximum": 1000, "default": 6, "description": "faces on each die"},
		"count": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 1, "description": "how many dice"},
	}},
	// it changes nothing: a client may call it without asking its person
	Annotations: map[string]any{"readOnlyHint": true},
	Run:         rollDice,
}}

// roll is one die (a var so a test can load the dice).
var roll = func(sides int) int { return 1 + rand.IntN(sides) }

func rollDice(args json.RawMessage) (string, any, error) {
	var a struct {
		Sides *float64 `json:"sides"` // JSON numbers: a model may write 20.0
		Count *float64 `json:"count"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields() // a misspelt argument is an error, not a default
	if err := dec.Decode(&a); err != nil {
		return "", nil, fmt.Errorf("bad arguments: %v", err)
	}
	sides, err := whole("sides", a.Sides, 2, 1000, 6)
	if err != nil {
		return "", nil, err
	}
	count, err := whole("count", a.Count, 1, 100, 1)
	if err != nil {
		return "", nil, err
	}
	rolls, words, sum := make([]int, count), make([]string, count), 0
	for i := range rolls {
		rolls[i] = roll(sides)
		words[i] = strconv.Itoa(rolls[i])
		sum += rolls[i]
	}
	return fmt.Sprintf("rolled %dd%d: %s (total %d)", count, sides, strings.Join(words, ", "), sum),
		map[string]any{"sides": sides, "count": count, "rolls": rolls, "sum": sum}, nil
}

// whole is a whole-number argument within lo..hi, def when it was left out.
func whole(name string, v *float64, lo, hi, def int) (int, error) {
	if v == nil {
		return def, nil
	}
	if *v != math.Trunc(*v) || *v < float64(lo) || *v > float64(hi) {
		return 0, fmt.Errorf("%s: a whole number from %d to %d (got %v)", name, lo, hi, *v)
	}
	return int(*v), nil
}

// versions are the MCP revisions this server speaks, newest first.
var versions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() { xbin.Serve(routes()) }

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", serveMCP)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) { // no event stream, no sessions
		w.Header().Set("Allow", "POST")
		xbin.WriteError(w, http.StatusMethodNotAllowed, "POST a JSON-RPC message to /mcp")
	})
	return mux
}

// serveMCP answers one JSON-RPC message per POST. It keeps no session, so
// the backend may restart between any two calls.
func serveMCP(w http.ResponseWriter, r *http.Request) {
	var m struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	switch {
	case !json.Valid(body):
		reply(w, http.StatusBadRequest, nil, nil, &rpcError{-32700, "the body is not JSON"})
	case json.Unmarshal(body, &m) != nil || m.JSONRPC != "2.0" || m.Method == "":
		reply(w, http.StatusBadRequest, nil, nil, &rpcError{-32600, `one request per POST: {"jsonrpc":"2.0","id":…,"method":…}`})
	case len(m.ID) == 0 || string(m.ID) == "null":
		w.WriteHeader(http.StatusAccepted) // a notification (notifications/initialized): nothing to answer
	default:
		result, err := dispatch(m.Method, m.Params)
		reply(w, http.StatusOK, m.ID, result, err)
	}
}

func reply(w http.ResponseWriter, status int, id json.RawMessage, result any, err *rpcError) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		out["error"] = err
	} else {
		out["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize": // the client's version when this server speaks it, else the newest
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(params, &p)
		v := versions[0]
		if slices.Contains(versions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "mcp-server", "version": "1.0.0"}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(params, &p) != nil {
			return nil, &rpcError{-32602, "bad params: {name, arguments}"}
		}
		i := slices.IndexFunc(tools, func(t tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q (tools/list names them)", p.Name)}
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		text, data, err := tools[i].Run(p.Arguments)
		if err != nil { // the tool's own failure is a result, never a broken call
			return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "structuredContent": data, "isError": false}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + method + " (this server answers initialize, ping, tools/list and tools/call)"}
}
