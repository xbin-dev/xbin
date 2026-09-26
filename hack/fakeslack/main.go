// fakeslack is a scripted Slack for the UI harness: the Web API methods the
// slack tile uses and a Socket Mode endpoint, so a harness pass can drive the
// whole chat path — a person DMs the bot, mentions it in a channel, replies
// in its thread — without Slack. Point the tile at it with
// PUT /api/<tile>/config {"apiBase": "http://127.0.0.1:<port>/api/"} and any
// xoxb-/xapp- tokens.
//
// Debug routes a pass drives:
//
//	POST /debug/inject {kind, user?, channel?, text, thread?}
//	     kind: dm | mention | thread | assistant | slash. Sends the envelope
//	     on the open socket and waits for its ack → {envelopeId, eventId, ts}
//	GET  /debug/posts      every chat.postMessage: [{channel, thread_ts, text, ts}]
//	GET  /debug/status     every assistant.threads.setStatus
//	GET  /debug/state      {connected, opens, acks}
//	POST /debug/disconnect {reason?}  a disconnect frame (default refresh_requested)
//	POST /debug/reset      forget posts and statuses
//
//	go run ./hack/fakeslack -addr 127.0.0.1:18978
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var names = map[string]string{"U1": "Ann", "U2": "Bo", "U3": "Cy"}

type fake struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	wmu      sync.Mutex
	opens    int
	acks     map[string]chan struct{}
	ackCount int
	posts    []map[string]any
	status   []map[string]any
	seq      int
	addr     string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18978", "listen address")
	flag.Parse()
	f := &fake{acks: map[string]chan struct{}{}, addr: *addr}
	log.Printf("fakeslack on http://%s (Web API at /api/)", *addr)
	log.Fatal(http.ListenAndServe(*addr, f.routes()))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v map[string]any) {
	v["ok"] = true
	writeJSON(w, v)
}

func fail(w http.ResponseWriter, code string) {
	writeJSON(w, map[string]any{"ok": false, "error": code})
}

func bearer(r *http.Request, prefix string) bool {
	return strings.HasPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), prefix)
}

// body reads a Web API call's arguments, JSON or form.
func body(r *http.Request) map[string]string {
	out := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
		return out
	}
	_ = r.ParseForm()
	for k := range r.Form {
		out[k] = r.Form.Get(k)
	}
	return out
}

func (f *fake) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		if !bearer(r, "xoxb-") {
			fail(w, "invalid_auth")
			return
		}
		ok(w, map[string]any{"team": "Fake Co", "team_id": "TFAKE", "user": "agentbot", "user_id": "UBOT", "bot_id": "BFAKE", "url": "https://fake.slack.test/"})
	})
	mux.HandleFunc("POST /api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		if !bearer(r, "xapp-") {
			fail(w, "invalid_auth")
			return
		}
		f.mu.Lock()
		f.opens++
		f.mu.Unlock()
		ok(w, map[string]any{"url": "ws://" + f.addr + "/link?ticket=" + strconv.Itoa(f.opens)})
	})
	mux.HandleFunc("POST /api/users.info", func(w http.ResponseWriter, r *http.Request) {
		id := body(r)["user"]
		n := names[id]
		if n == "" {
			n = id
		}
		ok(w, map[string]any{"user": map[string]any{"id": id, "name": strings.ToLower(n), "profile": map[string]string{"display_name": n}}})
	})
	mux.HandleFunc("POST /api/conversations.info", func(w http.ResponseWriter, r *http.Request) {
		id := body(r)["channel"]
		ok(w, map[string]any{"channel": map[string]any{"id": id, "name": map[string]string{"C1": "general", "C2": "random"}[id]}})
	})
	mux.HandleFunc("POST /api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		if b["channel"] == "CGONE" {
			fail(w, "channel_not_found")
			return
		}
		f.mu.Lock()
		ts := fmt.Sprintf("%d.%06d", time.Now().Unix(), len(f.posts)+1)
		f.posts = append(f.posts, map[string]any{"channel": b["channel"], "thread_ts": b["thread_ts"], "text": b["text"], "ts": ts})
		f.mu.Unlock()
		ok(w, map[string]any{"channel": b["channel"], "ts": ts})
	})
	mux.HandleFunc("POST /api/assistant.threads.setStatus", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		f.mu.Lock()
		f.status = append(f.status, map[string]any{"channel_id": b["channel_id"], "thread_ts": b["thread_ts"], "status": b["status"]})
		f.mu.Unlock()
		ok(w, map[string]any{})
	})
	mux.HandleFunc("GET /link", f.link)
	mux.HandleFunc("POST /debug/inject", f.inject)
	mux.HandleFunc("GET /debug/posts", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, append([]map[string]any{}, f.posts...))
	})
	mux.HandleFunc("GET /debug/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, append([]map[string]any{}, f.status...))
	})
	mux.HandleFunc("GET /debug/state", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{"connected": f.conn != nil, "opens": f.opens, "acks": f.ackCount})
	})
	mux.HandleFunc("POST /debug/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Reason string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		if err := f.send(map[string]any{"type": "disconnect", "reason": orStr(b.Reason, "refresh_requested")}); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]string{"ok": "true"})
	})
	mux.HandleFunc("POST /debug/reset", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.posts, f.status = nil, nil
		f.mu.Unlock()
		writeJSON(w, map[string]string{"ok": "true"})
	})
	return mux
}

// link is the Socket Mode endpoint: hello, then acks come back.
func (f *fake) link(w http.ResponseWriter, r *http.Request) {
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	_ = f.send(map[string]any{"type": "hello", "num_connections": 1, "connection_info": map[string]string{"app_id": "AFAKE"}})
	defer func() {
		f.mu.Lock()
		if f.conn == c {
			f.conn = nil
		}
		f.mu.Unlock()
		c.Close()
	}()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var a struct {
			EnvelopeID string `json:"envelope_id"`
		}
		if json.Unmarshal(data, &a) == nil && a.EnvelopeID != "" {
			f.mu.Lock()
			f.ackCount++
			if ch := f.acks[a.EnvelopeID]; ch != nil {
				close(ch)
				delete(f.acks, a.EnvelopeID)
			}
			f.mu.Unlock()
		}
	}
}

func (f *fake) send(v any) error {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no socket connected")
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	return c.WriteJSON(v)
}

// inject sends one event and waits (up to 5 s) for the tile's ack.
func (f *fake) inject(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Kind, User, Channel, Text, Thread string
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "need {kind, user?, channel?, text, thread?}", 400)
		return
	}
	f.mu.Lock()
	f.seq++
	n := f.seq
	f.mu.Unlock()
	envID := fmt.Sprintf("env-%d", n)
	eventID := fmt.Sprintf("EvFAKE%d", n)
	ts := fmt.Sprintf("%d.%06d", time.Now().Unix(), 100000+n)
	user := orStr(b.User, "U1")
	var env map[string]any
	ev := map[string]any{"user": user, "text": b.Text, "ts": ts, "event_ts": ts}
	switch b.Kind {
	case "dm", "assistant":
		ev["type"], ev["channel"], ev["channel_type"] = "message", orStr(b.Channel, "D1"), "im"
		if b.Thread != "" {
			ev["thread_ts"] = b.Thread
		}
		if b.Kind == "assistant" && b.Thread == "" {
			// the pane opens a thread first
			root := ts
			start := map[string]any{"type": "assistant_thread_started", "assistant_thread": map[string]any{
				"user_id": user, "channel_id": orStr(b.Channel, "D1"), "thread_ts": root}}
			if err := f.push(w, envID+"-start", eventID+"s", start); err != nil {
				return
			}
			ev["thread_ts"], ev["ts"] = root, fmt.Sprintf("%d.%06d", time.Now().Unix(), 200000+n)
		}
	case "mention":
		ev["type"], ev["channel"] = "app_mention", orStr(b.Channel, "C1")
		ev["text"] = "<@UBOT> " + b.Text
		if b.Thread != "" {
			ev["thread_ts"] = b.Thread
		}
	case "thread":
		ev["type"], ev["channel"], ev["channel_type"], ev["thread_ts"] = "message", orStr(b.Channel, "C1"), "channel", b.Thread
	case "slash":
		env = map[string]any{"type": "slash_commands", "envelope_id": envID, "accepts_response_payload": true, "payload": map[string]any{
			"command": "/agent", "text": b.Text, "user_id": user, "user_name": strings.ToLower(names[user]),
			"channel_id": orStr(b.Channel, "D1"), "team_id": "TFAKE", "trigger_id": "trig" + strconv.Itoa(n)}}
	default:
		http.Error(w, "kind is dm, mention, thread, assistant or slash", 400)
		return
	}
	if env == nil {
		if err := f.push(w, envID, eventID, ev); err != nil {
			return
		}
	} else if err := f.sendWaiting(w, envID, env); err != nil {
		return
	}
	writeJSON(w, map[string]string{"envelopeId": envID, "eventId": eventID, "ts": fmt.Sprint(ev["ts"])})
}

func (f *fake) push(w http.ResponseWriter, envID, eventID string, ev map[string]any) error {
	return f.sendWaiting(w, envID, map[string]any{"type": "events_api", "envelope_id": envID, "accepts_response_payload": false,
		"payload": map[string]any{"type": "event_callback", "team_id": "TFAKE", "api_app_id": "AFAKE", "event_id": eventID,
			"event_time": time.Now().Unix(), "event": ev}})
}

func (f *fake) sendWaiting(w http.ResponseWriter, envID string, env map[string]any) error {
	ch := make(chan struct{})
	f.mu.Lock()
	f.acks[envID] = ch
	f.mu.Unlock()
	if err := f.send(env); err != nil {
		http.Error(w, err.Error(), 409)
		return err
	}
	select {
	case <-ch:
		return nil
	case <-time.After(5 * time.Second):
		http.Error(w, "no ack within 5 s", 504)
		return fmt.Errorf("no ack")
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
