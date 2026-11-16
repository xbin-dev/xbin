// Package mcpfake is a fake outside MCP server for xbind's tests (D240):
// the streamable-HTTP transport as a server speaks it — JSON or an event
// stream, a session id, a credential it checks — with the ways a server
// can misbehave a client must survive: slow, oversized, redirecting,
// asking the client things, echoing the credential back. Tests only;
// never the internet.
package mcpfake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Seen is one message the server was sent.
type Seen struct {
	Method, Session, Version, Credential, Agent string
	Body                                        string
}

// Server is the fake. Set its fields before serving; read Seen with Log.
type Server struct {
	Name  string
	Tools []map[string]any
	// Header and Want: the credential it takes ("Authorization",
	// "Bearer x"); "" takes anything. A wrong one is 401.
	Header, Want string
	SSE          bool          // answer with an event stream: a notification, then the answer
	Delay        time.Duration // before each answer
	Huge         int           // bytes of padding in every tools/call result
	Flood        bool          // an event stream of notifications that never answers
	RedirectTo   string        // every POST answered 307 to this Location
	Moved        string        // every POST answered 301 to this Location
	AskSampling  bool          // tools/call: a sampling request from the server before the answer
	Echo         bool          // tools/call and errors carry the credential it was sent
	Batch        bool          // answers wrapped in an array
	NoSession    bool          // gives no session id
	ForgetAfter  int           // after this many messages, 404 for the session (0 never)
	Version      string        // the protocol version it answers ("" 2025-06-18)

	mu    sync.Mutex
	log   []Seen
	calls int
	gen   int
}

// Log is what the server was sent, in order.
func (f *Server) Log() []Seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Seen(nil), f.log...)
}

// Methods are the methods it was sent, in order.
func (f *Server) Methods() []string {
	var out []string
	for _, s := range f.Log() {
		out = append(out, s.Method)
	}
	return out
}

func (f *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var rq struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	_ = json.Unmarshal(b, &rq)
	cred := ""
	if f.Header != "" {
		cred = r.Header.Get(f.Header)
	} else {
		cred = r.Header.Get("Authorization")
	}
	f.mu.Lock()
	f.log = append(f.log, Seen{Method: rq.Method, Session: r.Header.Get("Mcp-Session-Id"), Version: r.Header.Get("MCP-Protocol-Version"),
		Credential: cred, Agent: r.Header.Get("User-Agent"), Body: string(b)})
	n := len(f.log)
	f.mu.Unlock()
	if f.RedirectTo != "" {
		w.Header().Set("Location", f.RedirectTo)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	if f.Moved != "" {
		w.Header().Set("Location", f.Moved)
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if f.Want != "" && cred != f.Want {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		msg := `{"error":"bad credentials"}`
		if f.Echo {
			msg = fmt.Sprintf(`{"error":"bad credentials: %s"}`, cred)
		}
		_, _ = io.WriteString(w, msg)
		return
	}
	if f.ForgetAfter > 0 && n > f.ForgetAfter && r.Header.Get("Mcp-Session-Id") == f.sessionID() {
		f.mu.Lock()
		f.gen++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if len(rq.ID) == 0 { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch rq.Method {
	case "initialize":
		if !f.NoSession {
			w.Header().Set("Mcp-Session-Id", f.sessionID())
		}
		result = map[string]any{"protocolVersion": orStr(f.Version, "2025-06-18"), "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": orStr(f.Name, "fake"), "version": "1.0"}}
	case "tools/list":
		result = map[string]any{"tools": f.Tools}
	case "tools/call":
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		text := "called " + rq.Params.Name
		if f.Echo {
			text += " with " + cred
		}
		if f.Huge > 0 {
			text += strings.Repeat("x", f.Huge)
		}
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	case "ping":
		result = map[string]any{}
	default:
		f.answer(w, rq.ID, nil, map[string]any{"code": -32601, "message": "no method " + rq.Method})
		return
	}
	f.answer(w, rq.ID, result, nil)
	_ = rq.Params.Arguments
}

func (f *Server) sessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("fake-session-%d", f.gen+1)
}

func (f *Server) answer(w http.ResponseWriter, id json.RawMessage, result any, rpcErr any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	b, _ := json.Marshal(msg)
	if f.Batch {
		b = append(append([]byte("["), b...), ']')
	}
	if !f.SSE && !f.Flood && !f.AskSampling {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	note := `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`
	fmt.Fprintf(w, ": a comment\nevent: message\ndata: %s\n\n", note)
	if f.Flood {
		pad := strings.Repeat("p", 4000)
		for i := 0; i < 1000; i++ {
			if _, err := fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"data\":%q}}\n\n", pad); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		return
	}
	if f.AskSampling {
		fmt.Fprint(w, `data: {"jsonrpc":"2.0","id":"s-1","method":"sampling/createMessage","params":{"messages":[]}}`+"\n\n")
	}
	// the answer split over two data lines, as a server may write it
	half := len(b) / 2
	for half > 0 && b[half-1] != ',' {
		half--
	}
	if half == 0 {
		fmt.Fprintf(w, "data: %s\n\n", b)
	} else {
		fmt.Fprintf(w, "data: %s\ndata: %s\n\n", b[:half], b[half:])
	}
	if fl != nil {
		fl.Flush()
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Tools are two tools: lookup, marked read-only, and post, which isn't.
func Tools() []map[string]any {
	return []map[string]any{
		{"name": "lookup", "description": "Look a ticket up.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
			"annotations": map[string]any{"readOnlyHint": true}},
		{"name": "post", "description": "Post a comment on a ticket.", "inputSchema": map[string]any{"type": "object"}},
	}
}
