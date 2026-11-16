# Give an agent a tool (MCP)

An agent instance gets tools from MCP server tiles bound to its `mcp` slot. An *agent instance* is a copy of the agent template — an assistant people chat with, a channel's bot, an automation — and a *tool* is something its conversations can call: roll dice, look up an order, file a ticket. A tool lives in a tile of its own that speaks MCP (the [Model Context Protocol](https://modelcontextprotocol.io)), and the agent reaches it through a binding, which is also its grant. Nothing else is wired: no key, no URL, no `uses` entry.

An MCP server tile is an ordinary Go backend tile: a manifest line and one file. This page is the interface and carries that file whole; there is no `mcp-server` template to instantiate. The same tile, with tests and a page to try its tools by hand, is in the xbin repository as `examples/mcp-server/`.

**Related:** [overview/11-interfaces.md](/docs/overview/11-interfaces.md) (slots and bindings) · [llm.md](/docs/llm.md) (the `llm` slot and `xbin:llm`) · [partitions.md](/docs/partitions.md) (personal binds) · [workspace-agent.md](/docs/workspace-agent.md) (a person's agent builds one for them) · the agent template's `API.md` (§Loop & tools, §Direct MCP servers, `GET /tools`).

## The short way

In a root terminal (`bx`), or with a tile's coding agent doing the second step:

```sh
bx new apps/dice --runtime go               # a Go backend tile (xbin.json, backend/main.go, go.mod)
# 1. add "provides" and "expose" to apps/dice/xbin.json   — §The manifest
# 2. replace apps/dice/backend/main.go                     — §A complete server
bx template new agent as apps/assistant     # an assistant
bx bind apps/assistant mcp+=apps/dice       # its tools: the binding is the grant
bx bind apps/assistant llm=xbin:llm         # its model: the workspace's connector
```

Then ask the assistant "Roll a d20 for me": its conversation calls `roll_dice` on `apps/dice` and answers with the roll. Binding a slot restarts the instance's backend, which then lists the new server's tools.

An agent instance needs its `llm` slot bound — `xbin:llm` is the workspace's connector, or any tile that provides `openai` — or it has no model to answer with.

A person's workspace agent does all of this from one request and one review: §For a workspace agent.

## The manifest

```jsonc
{
  "runtime": "go",
  // what a bound consumer holds: POST /mcp, the tools
  "expose": { "roles": { "reader": "Call this tile's tools: MCP at POST /mcp" } },
  // the contract an agent's `mcp` slot binds
  "provides": { "mcp": { "kind": "http", "service": "mcp", "role": "reader" } }
}
```

`provides.mcp` is what makes the tile an MCP server to xbind: kind `http`, service `mcp`, the endpoint at `/mcp` under the tile, and `role` — the role a bound consumer is granted on this tile. `reader` is all a tool's caller needs; a wider role gives a bound agent more of the tile than its tools. A Go backend also needs a `go.mod` beside the manifest (`module dice`, `go 1.24`, `require github.com/xbin-dev/xbin/sdk v0.0.0` — xbind resolves the sdk; the server below uses nothing else, so it builds in its sandbox with no network).

## The `mcp` slot, and the binding as the grant

The agent template asks for MCP servers with one slot:

```jsonc
"interfaces": { "mcp": { "kind": "http", "service": "mcp", "multi": true } }
```

Any tile that provides service `mcp` can be bound to it — several at once (`mcp+=`). The binding grants the agent the provide's role (`reader` here) on the tile: one act wires the dependency and authorizes the calls. The agent reaches each bound tile at `/mcp` under it, through xbind's gateway, attributed to the agent's tile; the MCP tile needs no network, and nothing outside the workspace is involved.

Who may bind is who may bind anything: a workspace admin, an org admin on their org's tiles, a person on their own personal tiles ([overview/11-interfaces.md](/docs/overview/11-interfaces.md)).

## The wire

One JSON-RPC 2.0 request per `POST /mcp` (`Content-Type: application/json`), answered `200` with a JSON-RPC response. No session: every call stands alone, and a client may send `initialize` before each one (the agent template's does). `GET` and `DELETE` on `/mcp` are `405`.

| Method | Params | Result |
|---|---|---|
| `initialize` | `{protocolVersion, capabilities?, clientInfo?}` | `{protocolVersion, capabilities: {tools: {}}, serverInfo: {name, version}}` |
| `ping` | — | `{}` |
| `tools/list` | — | `{tools: [{name, title?, description, inputSchema, annotations?}]}` |
| `tools/call` | `{name, arguments?}` | `{content: [{type: "text", text}], structuredContent?, isError}` |

- **Version negotiation.** `initialize` answers the client's `protocolVersion` when the server speaks it (`2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05`), else its newest; the client decides whether it can go on.
- **Notifications** — a message with no `id` (`notifications/initialized`) — get `202` and no body.
- **Errors** are JSON-RPC's: `-32700` the body isn't JSON (`400`), `-32600` not a request (`400`), `-32601` an unknown method, `-32602` bad params or an unknown tool. A tool's own failure — an argument out of range, an argument the schema doesn't name — is a *result* with `isError: true` and the reason as its text, which the calling model reads and can correct.
- **`readOnlyHint`.** A tool that changes nothing says so in `annotations: {"readOnlyHint": true}`: a person's workspace agent may then call it without asking, once its person turned on "MCP tools marked read-only don't ask me" ([workspace-agent.md §The web and MCP tools](/docs/workspace-agent.md#the-web-and-mcp-tools)). One that deletes or sends says `destructiveHint: true`. xbind reads `initialize`, `ping` and `tools/list` on a tile's declared endpoint as reads; every `tools/call` by a person's agent is that person's approval ([protocol/delegation.md §MCP tiles](/docs/protocol/delegation.md)).

## A complete server

`backend/main.go` — the whole backend of an MCP server tile with one tool, `roll_dice {sides?, count?}` → `rolled 2d6: 3, 5 (total 8)` and `{sides, count, rolls, sum}`:

<!-- examples/mcp-server/backend/main.go: this block is that file, byte for byte (TestDocsMCPServerIsTheExample) -->
```go
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
```

## Adding a tool

A tool is one more entry of `tools`: its name (`a–z`, `0–9`, `_`, `-`), a description a model can act on, the JSON Schema of its arguments object, and `Run`.

```go
{
	Name:        "lookup_order",
	Description: "Look up an order by its number: its status and total.",
	InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"number"},
		"properties": map[string]any{"number": map[string]any{"type": "string", "description": "the order number, e.g. A-1042"}}},
	Annotations: map[string]any{"readOnlyHint": true},
	Run: func(args json.RawMessage) (string, any, error) {
		var a struct {
			Number string `json:"number"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Number == "" {
			return "", nil, fmt.Errorf("number: the order number")
		}
		// … find it …
		return "A-1042: shipped, 42.00 EUR", map[string]any{"number": a.Number, "status": "shipped"}, nil
	},
},
```

The backend rebuilds on save; `tools/list` names the tool at once. A tool that needs data, another tile or the internet needs what any backend does: a resource, a grant in `uses`, a `net` binding.

## Checking it

From the MCP tile's own terminal (or its coding agent's sandbox), as the tile:

```sh
mcp() { curl -s -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' \
  -d "$1" "$XBIN_URL/api/$XBIN_COMPONENT/mcp"; echo; }
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"roll_dice","arguments":{"sides":20}}}'
```

Done is: `tools/list` names the tool with the arguments meant, and a call returns the shape meant. (This page reads the same way from there: `curl -s -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/docs/mcp.md?raw=1"`.)

## How an assistant picks it up

- **Discovery.** A conversation's agent lists each bound server's tools before it calls the model and offers them as `mcp:<server>:<tool>` (`mcp:apps/dice:roll_dice`). A list is kept for five minutes and refreshed behind the conversation after that; a new server is asked at once. Binding restarts the backend, so a new binding is seen at the next message.
- **Classes.** A conversation's class decides whether it gets MCP tools at all — only with the `internal` toolset — and which servers (its `mcp` list, `"all"` by default for the built-in internal class). The web and coding classes get none: what a conversation passes a tool could leave the workspace with their egress.
- **Checking the hook-up.** `GET /api/<agent instance>/tools` answers what a conversation would be offered now: each class's tools with their source (a built-in, or which MCP server), and each MCP server — bound to which slot, whether it answers, its last error. A person who can use the instance reads it, and so does their workspace agent, without an approval.

## Personal and global binds

On a partitioned agent instance (each person's conversations in their own partition, [partitions.md](/docs/partitions.md)) a binding made as above is **global**: every person's conversations get the tools, and the MCP tile's code sees what each sends it. A person can also bind a personal MCP tile of their own into their own partition — `bx bind --personal apps/assistant mcp=users/alice/dice` — whose tools only their own conversations get (`mcp:users/alice/dice:…`), and which nobody else's partition can call. An admin's bind is always global.

Servers outside the workspace — a vendor's hosted MCP server — are connected by URL in the agent's Settings → MCP instead (the template's `API.md` §Direct MCP servers), with their own sign-in.

## For a workspace agent

When a person asks their workspace agent for a tool ("make a tool that rolls dice and give it to a new assistant"), the agent proposes one plan ([workspace-agent.md](/docs/workspace-agent.md), [protocol/delegation.md §Plans](/docs/protocol/delegation.md)):

1. create the tool's tile from the `starter` template (an empty tile);
2. start that tile's own coding agent with a brief — below — to build the server;
3. create the assistant from the `agent` template, and bind its `llm` slot;
4. bind the assistant's `mcp` slot to the tool's tile — a line that **waits**: the tile provides nothing until its coding agent has written the manifest, so xbind applies this line by itself the moment the tile declares `provides.mcp` as approved (kind `http`, service `mcp`, role `reader`), and not at all if it declares something else.

The brief the agent fills in (`tile_agent_brief`'s `mcp-tool` template):

- **Goal:** the tool's name, its arguments, its result — "an MCP server in this tile with one tool, `roll_dice`: arguments `sides` (default 6) and `count` (default 1); it returns each roll and their sum".
- **Done when:** the manifest declares `provides.mcp` (§The manifest), `tools/list` on this tile's `/mcp` names the tool with that input schema, and a `tools/call` returns that shape.
- **Check by:** the two calls of §Checking it, from the tile's own sandbox.
- **Known:** this page (`/docs/mcp.md`) has the interface and a complete server to start from.
- **Leave alone:** everything outside this tile; the role in `provides.mcp` (`reader`).

When the tile agent reports, the workspace agent checks the hook-up itself — the assistant's `GET /tools` names the tool from the tool's tile, which also shows the waiting line was applied — and says what it checked.
