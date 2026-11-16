# Working on this tile

A copy of the **mcp-server** template: an MCP server whose tools an agent
calls. An agent instance (a copy of the agent template) gets them once its
`mcp` slot is bound to this tile; the binding is its grant. The workspace's
own `AGENTS.md` (at its root) and `/docs/mcp.md` say the rest.

## Where tools go

`_backend/tools.go` holds the list; `_backend/dice.go` is the example. To
add a tool:

1. Write a function that returns a `Tool` — in a file of its own
   (`_backend/<tool>.go`) or beside `rollDice`:
   - `Name`: `a–z 0–9 _ -`, what the model calls (`lookup_order`);
   - `Description`: one or two sentences a model can act on — what it
     does, what it returns, when to use it;
   - `Input`: the arguments' JSON Schema — `Object(map[string]any{…},
     "required", "names")` with `IntProp`, `StringProp` or your own;
   - `Output` (optional): the schema of `Result.Data`;
   - `Annotations`: `{"readOnlyHint": true}` for a tool that changes
     nothing; `{"destructiveHint": true}` for one that deletes or sends;
   - `Run`: decode with `Args[struct{…}](raw)`, do the work, return
     `Result{Text: "what the model reads", Data: theResultAsData}`. An
     error you return is the tool's failure, which the model reads — say
     what was wrong and what would work.
2. Add its line to `tools` in `tools.go`.
3. Add a test beside `mcp_test.go`'s `TestRollDice`, and a row to
   `API.md`'s table.

The backend rebuilds when you save. A tool that needs data, another tile's
API or the internet needs what any backend does: a resource in a
`scope.json`, a grant in `uses`, an egress `net` interface — the root
`AGENTS.md` says how.

## The contract — don't break it

- **The manifest's `provides`** (`"mcp": {"kind": "http", "service":
  "mcp", "role": "reader"}`) is what agents bind. Renaming it, changing its
  service or its role breaks every binding there is. Leave it.
- **The wire** (`_backend/mcp.go`): JSON-RPC 2.0 over streamable HTTP at
  `POST /mcp`, the methods `initialize`, `ping`, `tools/list`,
  `tools/call`, answered as JSON. Agents speak exactly this; you shouldn't
  need to touch the file.
- Tools return text a model can read: keep `Result.Text` short and
  factual.

## Checking from this sandbox

Run the tests: `cd _backend && go test ./...`. Then call the running
backend the way an agent does — through xbind, as this tile
(`$XBIN_URL`, `$XBIN_TOKEN` and `$XBIN_COMPONENT` are set in this
terminal):

```sh
mcp() { curl -s -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' \
  -d "$1" "$XBIN_URL/api/$XBIN_COMPONENT/mcp"; echo; }

# the tools: your new one must be named here, with its inputSchema
mcp '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# a call: the result's content[0].text, and isError false
mcp '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"roll_dice","arguments":{"sides":20}}}'
```

A `502` or an HTML answer means the backend isn't up: its build error or
log is in this tile's log (`bx logs $XBIN_COMPONENT`). Done is: `tools/list`
names the tool and a `tools/call` returns the shape you meant.

## A person's workspace agent

`"agent": "full"` in `xbin.json`: a person's workspace agent may call these
tools as that person, each `tools/call` only after they approved it. If a
tool you add hands out secrets or acts in the world (sends mail, spends
money), narrow it to `"read"` or `"off"` and say why in a comment.
