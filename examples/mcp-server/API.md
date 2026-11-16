# mcp-server — API

An MCP server tile — the example [/docs/mcp.md](/docs/mcp.md) carries
inline: tools an agent calls, served as
[MCP](https://modelcontextprotocol.io) over HTTP. Bind an agent instance's
`mcp` slot to this tile and its conversations get the tools.

## What it provides

```jsonc
"provides": { "mcp": { "kind": "http", "service": "mcp", "role": "reader" } }
```

The contract an agent's `mcp` slot (`{"kind": "http", "service": "mcp",
"multi": true}`) binds: the endpoint is `/mcp` under this tile, and the
binding grants the consumer the `reader` role — the binding is the grant.

## `POST /mcp`

One JSON-RPC 2.0 request per POST, `Content-Type: application/json`,
answered `200` with a JSON-RPC response; a notification (no `id`) with
`202` and no body. No session id is issued: every call stands alone, and a client
may send `initialize` before each one. `GET` and `DELETE` on `/mcp` are
`405` (this server opens no event stream and keeps no sessions).

| Method | Params | Result |
|---|---|---|
| `initialize` | `{protocolVersion, capabilities?, clientInfo?}` | `{protocolVersion, capabilities: {tools: {}}, serverInfo: {name, version}}` — the client's version when it is one of `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05`, else the newest |
| `ping` | — | `{}` |
| `tools/list` | — | `{tools: [{name, title?, description, inputSchema, annotations?}]}` |
| `tools/call` | `{name, arguments?}` | `{content: [{type: "text", text}], structuredContent?, isError}` |

Errors are JSON-RPC errors: `-32700` the body isn't JSON (`400`), `-32600`
not a valid request (`400`), `-32601` an unknown method, `-32602` bad params
or an unknown tool. A tool's own failure — an argument out of range, an
argument the schema doesn't name — is a result with `isError: true` and the
reason as its text, for the calling model to read.

## The tools

| Tool | Arguments | Result |
|---|---|---|
| `roll_dice` | `sides?` (2–1000, default 6), `count?` (1–100, default 1) | text `rolled 2d6: 3, 5 (total 8)`; `structuredContent: {sides, count, rolls, sum}`. `readOnlyHint: true` |

A tool is an entry of `tools` in `backend/main.go`; list yours here.

## Agents

`"agent": "full"`: a person's workspace agent may call `/mcp` as that
person — each `tools/call` only after the person approved that call. If a
tool you add hands out secrets or acts in the world, narrow it in
`xbin.json` (`"read"` or `"off"`, docs/auth.md §Delegation).

## The page

`index.html` lists the tools (`tools/list`) and calls one with the
arguments filled in (`tools/call`): a bench to try them by hand.
