# Give an agent a tool (MCP)

An agent instance gets tools from MCP tiles bound to its `mcp` slot; make one from the `mcp-server` template. An *agent instance* is a copy of the agent template — an assistant people chat with, a channel's bot, an automation — and a *tool* is something its conversations can call: roll dice, look up an order, file a ticket. A tool lives in a tile of its own that speaks MCP (the [Model Context Protocol](https://modelcontextprotocol.io)), and the agent reaches it through a binding, which is also its grant. Nothing else is wired: no key, no URL, no `uses` entry.

**Related:** [overview/11-interfaces.md](/docs/overview/11-interfaces.md) (slots and bindings) · [llm.md](/docs/llm.md) (the `llm` slot and `xbin:llm`) · [partitions.md](/docs/partitions.md) (personal binds) · the agent template's `API.md` (§Loop & tools, §Direct MCP servers, `GET /tools`).

## The short way

Four steps, in a terminal (`bx`) or in the Tile Manager and the admin console's wiring view:

```sh
bx template new mcp-server as apps/dice     # an MCP tile with one example tool, roll_dice
bx template new agent as apps/assistant     # an assistant
bx bind apps/assistant mcp+=apps/dice       # its tools: the binding is the grant
bx bind apps/assistant llm=xbin:llm         # its model: the workspace's connector
```

Then ask the assistant "Roll a d20 for me": its conversation calls `roll_dice` on `apps/dice` and answers with the roll. Binding a slot restarts the instance's backend, which then lists the new server's tools.

An agent instance needs its `llm` slot bound — `xbin:llm` is the workspace's connector, or any tile that provides `openai` — or it has no model to answer with.

A person's workspace agent can do the same for them, asking before each step changes anything ([workspace-agent.md](/docs/workspace-agent.md)).

## The `mcp-server` template

`bx template new mcp-server as apps/<name>` (or the Tile Manager's **New from template**) makes a tile you own:

| File | What |
|---|---|
| `xbin.json` | `"runtime": "go"`, `"provides": {"mcp": {"kind": "http", "service": "mcp", "role": "reader"}}`, `"agent": "full"` |
| `_backend/mcp.go` | the wire: JSON-RPC 2.0 over streamable HTTP at `POST /mcp` |
| `_backend/tools.go` | the list of tools — where a new one goes |
| `_backend/dice.go` | the example: `roll_dice {sides?, count?}` → each roll and their sum |
| `index.html` | lists the tools and calls one: a bench to try them by hand |
| `API.md`, `AGENTS.md` | what it serves; where tools go and how to check, for the tile's own coding agent |

It is a starting point, not a dependency: change it as you like, but keep the manifest's `provides` and the wire, which are what agents bind. It uses the standard library and the zero-dependency sdk only, so it builds in its sandbox with no network.

## The `mcp` slot, and the binding as the grant

The agent template asks for MCP servers with one slot:

```jsonc
"interfaces": { "mcp": { "kind": "http", "service": "mcp", "multi": true } }
```

Any tile that provides service `mcp` can be bound to it — several at once (`mcp+=`). The binding grants the agent the provide's role (`reader` here) on the tile: one admin's act wires the dependency and authorizes the calls. The agent reaches each bound tile at `/mcp` under it, through xbind's gateway, attributed to the agent's tile; the MCP tile needs no network, and nothing outside the workspace is involved.

Who may bind is who may bind anything: a workspace admin, an org admin on their org's tiles, a person on their own personal tiles ([overview/11-interfaces.md](/docs/overview/11-interfaces.md)).

## The wire: three methods

One JSON-RPC 2.0 message per `POST /mcp`, answered as JSON. A client sends:

| Method | Asks | The template answers |
|---|---|---|
| `initialize` | `{protocolVersion, capabilities, clientInfo}` | `{protocolVersion, capabilities: {tools: {}}, serverInfo}` — the client's version if it speaks it (`2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05`), else its newest |
| `tools/list` | — | `{tools: [{name, title?, description, inputSchema, outputSchema?, annotations?}]}` |
| `tools/call` | `{name, arguments}` | `{content: [{type: "text", text}], structuredContent?, isError}` |

`ping` answers `{}`; a notification (`notifications/initialized`) gets `202` and no body. Errors are JSON-RPC's: `-32700` not JSON, `-32600` not a request, `-32601` an unknown method, `-32602` bad params or an unknown tool. A tool's own failure — a bad argument — is a result with `isError: true`, which the model reads and can correct. The agent template sends `initialize` before each `tools/list` and `tools/call` and reads the text parts of a result; a server that keeps no session (the template's) loses nothing by that.

## Adding a tool

In the MCP tile, `_backend/tools.go`:

```go
var tools = []Tool{
	rollDice(),    // dice.go
	lookupOrder(), // orders.go: the new one
}
```

and the function:

```go
func lookupOrder() Tool {
	return Tool{
		Name:        "lookup_order",
		Description: "Look up an order by its number: its status and total.",
		Input:       Object(map[string]any{"number": StringProp("the order number, e.g. A-1042")}, "number"),
		Annotations: map[string]any{"readOnlyHint": true},
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			decode := Args[orderArgs] // its arguments, by the struct below
			a, err := decode(raw)
			if err != nil {
				return Result{}, err
			}
			// … find it …
			return Result{Text: "A-1042: shipped, 42.00 EUR"}, nil
		},
	}
}

type orderArgs struct {
	Number string `json:"number"`
}
```

The backend rebuilds on save; `tools/list` names the tool at once. Mark a tool that changes nothing `readOnlyHint: true` (a person's workspace agent then calls it without asking, once its person turned on "MCP tools marked read-only don't ask me" — [workspace-agent.md §The web and MCP tools](/docs/workspace-agent.md#the-web-and-mcp-tools)), one that deletes or sends `destructiveHint: true`. A tool that needs data, another tile or the internet needs what any backend does: a resource, a grant in `uses`, a `net` binding.

## Checking it

From the MCP tile's own terminal (or its coding agent's sandbox), as the tile:

```sh
mcp() { curl -s -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' \
  -d "$1" "$XBIN_URL/api/$XBIN_COMPONENT/mcp"; echo; }
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"roll_dice","arguments":{"sides":20}}}'
```

Done is: `tools/list` names the tool, and a call returns the shape meant. The tile's page does the same with a form.

## How an assistant picks it up

- **Discovery.** A conversation's agent lists each bound server's tools before it calls the model and offers them as `mcp:<server>:<tool>` (`mcp:apps/dice:roll_dice`). A list is kept for five minutes and refreshed behind the conversation after that; a new server is asked at once. Binding restarts the backend, so a new binding is seen at the next message.
- **Classes.** A conversation's class decides whether it gets MCP tools at all — only with the `internal` toolset — and which servers (its `mcp` list, `"all"` by default for the built-in internal class). The web and coding classes get none: what a conversation passes a tool could leave the workspace with their egress.
- **Checking the hook-up.** `GET /api/<agent instance>/tools` answers what a conversation would be offered now: each class's tools with their source (a built-in, or which MCP server), and each MCP server — bound to which slot, whether it answers, its last error. A person who can use the instance reads it, and so does their workspace agent, without an approval.

## Personal and global binds

On a partitioned agent instance (each person's conversations in their own partition, [partitions.md](/docs/partitions.md)) a binding made as above is **global**: every person's conversations get the tools, and the MCP tile's code sees what each sends it. A person can also bind a personal MCP tile of their own into their own partition — `bx bind --personal apps/assistant mcp=users/alice/dice` — whose tools only their own conversations get (`mcp:users/alice/dice:…`), and which nobody else's partition can call. An admin's bind is always global.

Servers outside the workspace — a vendor's hosted MCP server — are connected by URL in the agent's Settings → MCP instead (the template's `API.md` §Direct MCP servers), with their own sign-in.

## For a workspace agent: the brief

When a person asks their workspace agent for a tool ("make a tool that rolls dice and give it to a new assistant"), the agent creates the MCP tile from this template, binds it, and hands the tool itself to the tile's own coding agent with a brief it fills in:

- **Goal:** the tool's name, its arguments, its result — "an MCP tool `roll_dice` in this tile: arguments `sides` (default 6) and `count` (default 1); it returns each roll and their sum".
- **Done when:** `tools/list` on this tile's `/mcp` names the tool with that input schema, and a `tools/call` returns that shape.
- **Check by:** the two calls above, from the tile's own sandbox.
- **Leave alone:** the manifest's `provides` (the `mcp` slot's contract) and the wire in `_backend/mcp.go`.

Because the template's manifest provides `mcp` from the first second, the binding is valid before the coding agent has written anything; the agent then checks the hook-up with the instance's `GET /tools`.

## Use an outside MCP server

A person's workspace agent can use an MCP server outside the workspace — a vendor's hosted server, one your team runs — once a workspace admin lists it and switches MCP to *the MCP tiles, and listed servers* on the workspace agent's card on `/xbin/system` (D240): its name, its address, how xbin signs in to it (none, a bearer token, or a header) with the secret — which xbin keeps sealed in its vault and never shows again — and, when only some people should use it, the organisations it is for. **Test** on the card runs `initialize` and `tools/list` and shows the tools or the exact error. Whoever runs xbind can list them instead in the host's file, and then the list is theirs ([protocol/delegation.md §Outside MCP servers](/docs/protocol/delegation.md)).

The agent names such a server `ext:<id>`: `mcp_tools` lists its tools beside the MCP tiles', and `mcp_call` takes `server` instead of `tile`. xbin is the client — the agent never sees the server's address beyond its host, its secret or its session — and every call goes from xbind, to that address alone, public addresses only unless the admin marked the server as on a private network. A tool asks its person the first time, and the sheet says *Outside this workspace:* and the host; a server's "read-only" marks count only where the admin trusts them. What a call sends leaves the workspace, so once a conversation holds an organisation's data each call asks — unless that organisation trusts the listed servers (its agent rule `mcp: "listed"`) or the server is listed for it — and an organisation can keep its data from every server outside it (`org`) or from all MCP servers (`never`). What a server answers is an outsider's words: data, never instructions.
