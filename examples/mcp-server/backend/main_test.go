package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// post sends body to POST /mcp (with extra headers as name, value pairs)
// and returns the status and the decoded answer (nil for none).
func post(t *testing.T, body string, hdr ...string) (int, any) {
	t.Helper()
	srv := httptest.NewServer(routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type %q: %s", ct, raw)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return resp.StatusCode, v
}

// call is one request's result, failing on a JSON-RPC error.
func call(t *testing.T, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	status, v := post(t, body+"}")
	m, _ := v.(map[string]any)
	if status != 200 || m == nil || m["error"] != nil || m["id"] != float64(7) || m["jsonrpc"] != "2.0" {
		t.Fatalf("%s: %d %v", method, status, v)
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		t.Fatalf("%s: no result object: %v", method, v)
	}
	return res
}

// rpcFail is a request's JSON-RPC error: its status, code and message.
func rpcFail(t *testing.T, body string) (int, float64, string, any) {
	t.Helper()
	status, v := post(t, body)
	m, _ := v.(map[string]any)
	e, _ := m["error"].(map[string]any)
	if e == nil || m["result"] != nil {
		t.Fatalf("%s: want an error, got %d %v", body, status, v)
	}
	msg, _ := e["message"].(string)
	return status, e["code"].(float64), msg, m["id"]
}

func TestInitializeNegotiates(t *testing.T) {
	// the agent template's client asks for 2025-06-18: it gets it
	res := call(t, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"xbin-agent","version":"0"}}`)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("negotiated %v", res["protocolVersion"])
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities: %v", caps)
	}
	if info, _ := res["serverInfo"].(map[string]any); info["name"] == "" || info["version"] == "" {
		t.Fatalf("serverInfo: %v", res["serverInfo"])
	}
	// an older revision this server speaks is kept
	if res := call(t, "initialize", `{"protocolVersion":"2024-11-05"}`); res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("2024-11-05 negotiated to %v", res["protocolVersion"])
	}
	// one it doesn't (or none): the newest it does, for the client to decide
	for _, params := range []string{`{"protocolVersion":"1999-01-01"}`, `{}`, ``} {
		if res := call(t, "initialize", params); res["protocolVersion"] != versions[0] {
			t.Fatalf("initialize %s negotiated to %v", params, res["protocolVersion"])
		}
	}
}

func TestPingAndNotifications(t *testing.T) {
	if res := call(t, "ping", ""); len(res) != 0 {
		t.Fatalf("ping: %v", res)
	}
	// a notification: 202, no body
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`,
	} {
		if status, v := post(t, body); status != http.StatusAccepted || v != nil {
			t.Fatalf("%s: %d %v", body, status, v)
		}
	}
	// a string id is echoed as it came
	_, v := post(t, `{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	if m := v.(map[string]any); m["id"] != "abc" {
		t.Fatalf("id: %v", m)
	}
}

var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Every tool is held to what clients need, so one added later is checked too.
func TestToolsList(t *testing.T) {
	res := call(t, "tools/list", "")
	list, _ := res["tools"].([]any)
	if len(list) != len(tools) || len(list) == 0 {
		t.Fatalf("tools: %v", res)
	}
	seen := map[string]bool{}
	for _, x := range list {
		d := x.(map[string]any)
		name, _ := d["name"].(string)
		in, _ := d["inputSchema"].(map[string]any)
		if !toolName.MatchString(name) || seen[name] || d["description"] == "" || in["type"] != "object" || d["Run"] != nil {
			t.Errorf("tool %v: a unique name of a–z, 0–9, _ and -, a description and an object inputSchema", d)
		}
		seen[name] = true
	}
	d := list[0].(map[string]any)
	in := d["inputSchema"].(map[string]any)
	props := in["properties"].(map[string]any)
	if d["name"] != "roll_dice" || props["sides"] == nil || props["count"] == nil || in["required"] != nil {
		t.Fatalf("roll_dice's arguments are optional sides and count: %v", d)
	}
	if ann := d["annotations"].(map[string]any); ann["readOnlyHint"] != true {
		t.Fatalf("roll_dice changes nothing: %v", ann)
	}
	// a cursor this server never gave changes nothing
	if res := call(t, "tools/list", `{"cursor":"x"}`); len(res["tools"].([]any)) != len(tools) {
		t.Fatalf("with a cursor: %v", res)
	}
}

func TestRollDice(t *testing.T) {
	n, real := 0, roll
	roll = func(sides int) int { n++; return sides - n + 1 } // loaded dice: sides, sides-1, …
	t.Cleanup(func() { roll = real })

	res := call(t, "tools/call", `{"name":"roll_dice","arguments":{"sides":20}}`)
	content := res["content"].([]any)[0].(map[string]any)
	if res["isError"] != false || content["type"] != "text" || content["text"] != "rolled 1d20: 20 (total 20)" {
		t.Fatalf("a d20: %v", res)
	}
	sc := res["structuredContent"].(map[string]any)
	if sc["sides"] != float64(20) || sc["count"] != float64(1) || sc["sum"] != float64(20) || len(sc["rolls"].([]any)) != 1 {
		t.Fatalf("structured: %v", sc)
	}

	n = 0
	res = call(t, "tools/call", `{"name":"roll_dice","arguments":{"count":3,"sides":6.0}}`)
	if got := res["content"].([]any)[0].(map[string]any)["text"]; got != "rolled 3d6: 6, 5, 4 (total 15)" {
		t.Fatalf("3d6: %v", got)
	}

	// no arguments at all: one six-sided die
	for _, params := range []string{`{"name":"roll_dice"}`, `{"name":"roll_dice","arguments":{}}`, `{"name":"roll_dice","arguments":null}`} {
		n = 0
		res = call(t, "tools/call", params)
		if got := res["content"].([]any)[0].(map[string]any)["text"]; got != "rolled 1d6: 6 (total 6)" {
			t.Fatalf("%s: %v", params, got)
		}
	}
}

func TestRollDiceRealDice(t *testing.T) {
	for i := 0; i < 200; i++ {
		res := call(t, "tools/call", `{"name":"roll_dice","arguments":{"sides":6,"count":5}}`)
		sc := res["structuredContent"].(map[string]any)
		sum := 0.0
		for _, r := range sc["rolls"].([]any) {
			if v := r.(float64); v < 1 || v > 6 {
				t.Fatalf("a d6 rolled %v", v)
			}
			sum += r.(float64)
		}
		if sum != sc["sum"].(float64) {
			t.Fatalf("sum %v of %v", sc["sum"], sc["rolls"])
		}
	}
}

// A tool's failure is a result the model reads (isError), never a protocol
// error: a bad argument, an argument the schema doesn't name.
func TestToolErrors(t *testing.T) {
	for _, args := range []string{`{"sides":1}`, `{"sides":1001}`, `{"sides":2.5}`, `{"count":0}`, `{"count":101}`, `{"sides":"six"}`, `{"faces":6}`, `[1]`} {
		res := call(t, "tools/call", `{"name":"roll_dice","arguments":`+args+`}`)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] != true || text == "" || res["structuredContent"] != nil {
			t.Fatalf("%s: %v", args, res)
		}
	}
	// an unknown tool is the protocol's invalid params
	if status, code, msg, id := rpcFail(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`); status != 200 || code != -32602 || !strings.Contains(msg, "nope") || id != float64(4) {
		t.Fatalf("unknown tool: %d %v %q %v", status, code, msg, id)
	}
	if _, code, _, _ := rpcFail(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":"x"}`); code != -32602 {
		t.Fatalf("params not an object: %v", code)
	}
}

func TestProtocolErrors(t *testing.T) {
	cases := []struct {
		body   string
		status int
		code   float64
		id     any
	}{
		{`{not json`, 400, -32700, nil},
		{``, 400, -32700, nil},
		{`{"jsonrpc":"1.0","id":1,"method":"ping"}`, 400, -32600, nil},
		{`{"jsonrpc":"2.0","id":1}`, 400, -32600, nil},
		{`"ping"`, 400, -32600, nil},
		{`[]`, 400, -32600, nil},
		{`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`, 200, -32601, float64(2)},
	}
	for _, c := range cases {
		status, code, msg, id := rpcFail(t, c.body)
		if status != c.status || code != c.code || id != c.id || msg == "" {
			t.Errorf("%s: %d %v %v %q, want %d %v %v", c.body, status, code, id, msg, c.status, c.code, c.id)
		}
	}
}

func TestOtherMethodsOnMCP(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+"/mcp", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
			t.Fatalf("%s /mcp: %d %q", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}
