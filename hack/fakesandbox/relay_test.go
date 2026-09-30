package main

// relay_test.go — a consumer's backend relaying a person's terminal to the
// reference manager with the SDK (xbin.RelayManagerTTY), as any consumer's
// does (docs/sandbox-manager.md §Terminals): a real pseudo-terminal at the
// far end, the person asserted, the page's own headers never passing.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// pageTerm is the page's end of a relayed terminal.
type pageTerm struct {
	t    *testing.T
	c    *ws.Conn
	out  strings.Builder
	exit map[string]any
}

// next reads one message: output kept, the exit frame noted, other control
// frames returned.
func (p *pageTerm) next() map[string]any {
	p.t.Helper()
	_ = p.c.SetReadDeadline(time.Now().Add(20 * time.Second))
	typ, msg, err := p.c.ReadMessage()
	if err != nil {
		p.t.Fatalf("reading the terminal: %v; it said %q", err, p.out.String())
	}
	if typ == ws.BinaryMessage {
		p.out.Write(msg)
		return nil
	}
	var ctl map[string]any
	if json.Unmarshal(msg, &ctl) != nil {
		p.t.Fatalf("a text frame that isn't JSON: %q", msg)
	}
	if ctl["op"] == "exit" {
		p.exit = ctl
	}
	return ctl
}

// expect reads until the output holds each of want, in order.
func (p *pageTerm) expect(want ...string) {
	p.t.Helper()
	for {
		rest, ok := p.out.String(), true
		for _, w := range want {
			i := strings.Index(rest, w)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(w):]
		}
		if ok {
			return
		}
		if p.exit != nil {
			p.t.Fatalf("exited (%v) without %q: %q", p.exit, want, p.out.String())
		}
		p.next()
	}
}

func (p *pageTerm) send(typ int, b string) {
	p.t.Helper()
	if err := p.c.WriteMessage(typ, []byte(b)); err != nil {
		p.t.Fatal(err)
	}
}

// exited reads to the exit frame, wants code, then a normal close.
func (p *pageTerm) exited(code int) {
	p.t.Helper()
	for p.exit == nil {
		p.next()
	}
	if c, ok := p.exit["code"].(float64); !ok || int(c) != code {
		p.t.Fatalf("exit %v, want %d", p.exit, code)
	}
	_ = p.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, _, err := p.c.ReadMessage(); !ws.IsClose(err, ws.CloseNormalClosure) {
		p.t.Fatalf("after the exit frame: %v", err)
	}
}

func TestRelayManagerTTY(t *testing.T) {
	if !fsbHasPTY() {
		t.Skip("no pseudo-terminals on this host")
	}
	t.Parallel()
	m, tg := newFake(t)
	// xbind in front of the manager: the calling tile's path, no person of
	// its own
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-xbin-") {
				r.Header.Del(k)
			}
		}
		r.Header.Set("X-XBin-From", "apps/consumer")
		m.ServeHTTP(w, r)
	}))
	defer gw.Close()
	a := tg.As(t, "apps/consumer")
	sb := a.Verified("alice").Create(map[string]any{"name": "relay", "visibility": "private"})
	script := `stty size; echo ready; read line; stty size; echo "got:$line"; exit 3`
	// the consumer's route: its own checks first (here none), then the relay
	// for the person it acts for — bob, whom the consumer let in
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := xbin.ManagerTTYOptions{User: "bob", Client: gw.Client()}
		if eid := r.URL.Query().Get("exec"); eid != "" {
			o.ExecID = eid
		} else {
			o.Cmd, o.Rows, o.Cols = script, 10, 20
		}
		xbin.RelayManagerTTY(w, r, gw.URL+"/", sb.ID, o)
	}))
	defer consumer.Close()
	open := func(query string) *pageTerm {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		h := http.Header{"Cookie": {"s=1"}, "Sbx-User": {"mallory"}}
		c, resp, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(consumer.URL, "http")+"/?"+query, h, nil)
		if err != nil {
			t.Fatalf("open: %v %v", err, resp)
		}
		t.Cleanup(func() { c.Close() })
		p := &pageTerm{t: t, c: c}
		if s := p.next(); s == nil || s["op"] != "session" || s["sandbox"] != sb.ID {
			t.Fatalf("the first frame: %v", s)
		}
		return p
	}

	p := open("")
	p.expect("10 20", "ready")
	p.send(ws.TextMessage, `{"op":"resize","cols":100,"rows":40}`)
	p.send(ws.TextMessage, `{"op":"ping","t":"p1"}`)
	p.send(ws.BinaryMessage, "hi\r")
	p.expect("40 100", "got:hi")
	p.exited(3)
	var tty []fsbCall
	for _, c := range m.Calls() {
		if strings.HasSuffix(c.Path, "/tty") {
			tty = append(tty, c)
		}
	}
	if len(tty) != 1 || tty[0].From != "apps/consumer" || tty[0].SbxUser != "bob" || tty[0].User != "" || !strings.Contains(tty[0].Query, "rows=10") {
		t.Fatalf("the manager was called %+v", tty)
	}

	// a tty exec the consumer started, relayed by id
	x := a.Asserting("bob").Exec(sb.ID, map[string]any{"cmd": `echo started; read l; echo "line:$l"`, "tty": true})
	p = open("exec=" + x.ID)
	p.expect("started")
	p.send(ws.BinaryMessage, "x\r")
	p.expect("line:x")
	p.exited(0)
}
