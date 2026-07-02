# examples/counter-go API

A single shared counter. The smallest useful example of the buxon API
contract: roles in the manifest, this file describing the endpoints, and
role-guarded routes in the backend.

## Roles

| Role   | Grants |
|--------|--------|
| reader | Read the counter value |
| writer | Increment the counter |

## Endpoints

### GET /count — role: reader

```json
{"count": 3, "servedTo": "apps/other-app"}
```

`servedTo` echoes the verified caller (`X-Buxon-From`).

### POST /count — role: writer

Increments and returns the new value.

```json
{"count": 4}
```

## Use it

```jsonc
// caller's buxon.json
{ "uses": [{ "target": "examples/counter-go", "role": "reader" }] }
```

```go
resp, _ := buxon.Client().Get("http://buxon/api/examples/counter-go/count")
```
