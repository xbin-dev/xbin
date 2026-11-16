package server

// mcpjson.go — what a person's agent sends an MCP server, read one way only
// (D239). encoding/json matches keys without regard to case, keeps the
// last of two equal keys and stops at the first value of a stream; a
// tile's own parser may do none of that. So a body the gate judged as one
// thing could be another thing to the tile ({"method":"tools/call", …,
// "Method":"tools/list"}). Every body xbind judges for an MCP server is
// read strictly — one JSON object, UTF-8, exact-case keys from a fixed
// list, no key twice at any depth, nothing after it — and what reaches the
// tile is the request xbind builds from what it read, never the caller's
// bytes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// strictObject reads body as exactly one JSON object whose keys are among
// allowed (exact case), with no key twice in any object at any depth, and
// nothing but white space after it. Numbers stay json.Number.
func strictObject(body []byte, allowed ...string) (map[string]any, error) {
	if !utf8.Valid(body) {
		return nil, errors.New("not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not one JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("something after the JSON object")
	}
	for k := range obj {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return nil, fmt.Errorf("an unknown member %q", k)
		}
	}
	return obj, nil
}

const strictDepth = 64

// strictValue reads one JSON value from dec, refusing a key twice in an
// object.
func strictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > strictDepth {
		return nil, errors.New("nested too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("not valid JSON")
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, errors.New("not valid JSON")
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("not valid JSON")
				}
				if _, dup := obj[k]; dup {
					return nil, fmt.Errorf("the member %q twice", k)
				}
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[k] = v
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, errors.New("not valid JSON")
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := strictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, errors.New("not valid JSON")
			}
			return arr, nil
		}
		return nil, errors.New("not valid JSON")
	default:
		return t, nil // string, json.Number, bool, nil
	}
}

// rpcRequest is one JSON-RPC request as xbind read it.
type rpcRequest struct {
	ID     any // a string or a json.Number
	Method string
	Params map[string]any // nil: none
}

// readRPC reads body as one JSON-RPC 2.0 request with an id — {jsonrpc:
// "2.0", id, method, params?} and nothing else.
func readRPC(body []byte) (rpcRequest, error) {
	obj, err := strictObject(body, "jsonrpc", "id", "method", "params")
	if err != nil {
		return rpcRequest{}, err
	}
	var rq rpcRequest
	if v, ok := obj["jsonrpc"].(string); !ok || v != "2.0" {
		return rq, errors.New(`jsonrpc must be "2.0"`)
	}
	switch id := obj["id"].(type) {
	case string, json.Number:
		rq.ID = id
	default:
		return rq, errors.New("a request has an id: a string or a number")
	}
	m, ok := obj["method"].(string)
	if !ok || m == "" {
		return rq, errors.New("a request has a method")
	}
	rq.Method = m
	if p, has := obj["params"]; has {
		pm, ok := p.(map[string]any)
		if !ok {
			return rq, errors.New("params must be an object")
		}
		rq.Params = pm
	}
	return rq, nil
}

// wire is the request xbind sends the server: built from what it read.
func (rq rpcRequest) wire() []byte {
	out := map[string]any{"jsonrpc": "2.0", "id": rq.ID, "method": rq.Method}
	if rq.Params != nil {
		out["params"] = rq.Params
	}
	b, _ := json.Marshal(out)
	return b
}
