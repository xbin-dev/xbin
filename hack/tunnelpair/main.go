// tunnelpair is a phone for the UI harness (D204, native/spec/tunnel.md):
// given a pairing link the shell's add-device dialog or /xbin/devices shows,
// it pairs over the app tunnel as a new device — devclient (the Go reference
// of the app's side: Noise, the pairing messages, device login) over
// `<iroh> client`, the real xbin-iroh's test mode or hack/fakeiroh's — so a
// pass sees the real confirmation with the same code on both screens.
//
//	tunnelpair -iroh bin/xbin-iroh -link 'xbin://enroll?…' [-name "Ann's iPhone"]
//	           [-endpoint <server EndpointId hex>] [-connect]
//
// stdout, one JSON object per line:
//
//	{"t":"code","code":"012 345","endpoint":"<the phone's EndpointId>"}   the phone shows the code
//	{"t":"enrolled","deviceId":…,"name":…,"user":…}                        the computer said Codes match
//	{"t":"rejected","reason":"mismatch|expired|limit|refused","error":…}   …or They don't, or it timed out
//	{"t":"signed-in","deviceId":…}                                         -connect: device login over tunnel/1, its client settings fetched
//	{"t":"left","error":…}                                                 signalled while pairing: it left, xbind hung up
//	{"t":"error","error":…}
//
// It exits 0 once enrolled (with -connect: signed in and holding a tunnel
// connection open — the admin console counts it connected — until stdin
// closes or it is signalled), 1 otherwise. SIGTERM before the
// confirmation abandons the pairing: the phone ends its stream and waits
// for xbind to hang up. -endpoint is needed
// only for fakeiroh's tickets, which don't carry the server's EndpointId
// (GET /api/xbin/app-tunnel's endpointId). TEST ONLY.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/devclient"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

var out = json.NewEncoder(os.Stdout)

func say(v map[string]any) { _ = out.Encode(v) }

// clients are the iroh client processes started: ended before any exit,
// or they would outlive this phone.
var clients []*devclient.IrohClient

func exit(code int) {
	for _, c := range clients {
		c.Close()
	}
	os.Exit(code)
}

func fail(err error) {
	say(map[string]any{"t": "error", "error": err.Error()})
	exit(1)
}

func main() {
	iroh := flag.String("iroh", "", "the iroh client: xbin-iroh or fakeiroh (its `client` mode)")
	link := flag.String("link", "", "the xbin://enroll link")
	name := flag.String("name", "Harness iPhone", "the phone's name")
	endpoint := flag.String("endpoint", "", "the server's EndpointId (hex), for a ticket that doesn't carry it (fakeiroh's)")
	connect := flag.Bool("connect", false, "after pairing, sign in over tunnel/1 and hold a connection until stdin closes")
	timeout := flag.Duration("timeout", 3*time.Minute, "give up pairing after this long")
	flag.Parse()
	if *iroh == "" || *link == "" {
		flag.Usage()
		os.Exit(2)
	}
	l, err := devclient.ParseLink(*link)
	if err != nil {
		fail(err)
	}
	if !l.Tunnel() {
		fail(errors.New("not a tunnel link (no tv/t/k/p)"))
	}
	server, err := serverEndpoint(l.Ticket, *endpoint)
	if err != nil {
		fail(err)
	}
	dev, err := devclient.New(*name)
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	relay, err := devclient.RelayFor(l)
	if err != nil {
		fail(err)
	}
	pc, err := devclient.StartIrohClient(ctx, *iroh, l.Ticket, wire.ALPNPairName, dev, relay, os.Stderr)
	if err != nil {
		fail(err)
	}
	clients = append(clients, pc)
	// Signalled while pairing, the phone leaves: it ends its stream (a
	// half-close of the forwarder connection, which the iroh client turns
	// into the stream's FIN) and waits for xbind to hang up — killing the
	// client at once could beat the FIN out, and xbind would notice only
	// when the iroh connection idled out.
	var mu sync.Mutex
	var stream net.Conn
	left := make(chan struct{})
	go func() {
		<-sig
		close(left)
		mu.Lock()
		st := stream
		mu.Unlock()
		if cw, ok := st.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		cancel()
	}()
	dial := devclient.ClientDialer(pc, nil)
	keep := func(ctx context.Context, alpn string) (net.Conn, error) {
		c, err := dial(ctx, alpn)
		mu.Lock()
		stream = c
		mu.Unlock()
		return c, err
	}
	e, err := dev.Pair(ctx, keep, l, devclient.PairOptions{ServerEndpoint: server,
		OnSAS: func(code, _ string) { say(map[string]any{"t": "code", "code": code, "endpoint": dev.EndpointHex()}) }})
	pc.Close()
	signal.Stop(sig)
	var rej *devclient.Rejected
	select {
	case <-left:
		say(map[string]any{"t": "left", "error": fmt.Sprint(err)})
		exit(1)
	default:
	}
	switch {
	case errors.As(err, &rej):
		say(map[string]any{"t": "rejected", "reason": rej.Reason, "error": rej.Err})
		exit(1)
	case err != nil:
		fail(err)
	}
	say(map[string]any{"t": "enrolled", "deviceId": e.DeviceID, "name": e.Name, "user": e.User.ID})
	if !*connect {
		return
	}

	// the app after "enrolled": tunnel/1 with the ticket and relay it was
	// given, device login, then a connection held open
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-stop; cancel() }()
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	if e.Tunnel.Relay.URL != "" {
		relay = &devclient.IrohRelay{Mode: "custom", URL: e.Tunnel.Relay.URL, Token: e.Tunnel.Relay.Token}
	}
	tc, err := devclient.StartIrohClient(ctx, *iroh, e.Tunnel.Ticket, wire.ALPNTunnelName, dev, relay, os.Stderr)
	if err != nil {
		fail(err)
	}
	clients = append(clients, tc)
	defer tc.Close()
	tun := dev.Tunnel(devclient.ClientDialer(nil, tc), server, l.ServerKey)
	lctx, lcancel := context.WithTimeout(ctx, time.Minute)
	sess, err := dev.Login(lctx, tun.Client(), "http://"+tc.Addr, e.Origin, e.DeviceID)
	lcancel()
	if err != nil {
		fail(fmt.Errorf("device login over the tunnel: %w", err))
	}
	// as the app after every connect: the tunnel's client settings (the
	// admin console counts the device synced)
	var cc devclient.ClientConfig
	lctx, lcancel = context.WithTimeout(ctx, time.Minute)
	err = devclient.Do(lctx, tun.Client(), "GET", "http://"+tc.Addr+"/api/xbin/app-tunnel/client", sess.Token, nil, &cc)
	lcancel()
	if err != nil {
		fail(fmt.Errorf("GET /app-tunnel/client over the tunnel: %w", err))
	}
	conn, err := tun.DialConn(ctx)
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	say(map[string]any{"t": "signed-in", "deviceId": e.DeviceID})
	<-ctx.Done()
}

// serverEndpoint is the workspace's EndpointId: from an EndpointTicket, or
// given (fakeiroh's tickets carry only an address).
func serverEndpoint(ticket, given string) ([32]byte, error) {
	var id [32]byte
	if given != "" {
		b, err := hex.DecodeString(given)
		if err != nil || len(b) != 32 {
			return id, errors.New("-endpoint: want 64 hex")
		}
		copy(id[:], b)
		return id, nil
	}
	if strings.HasPrefix(ticket, "fakeiroh:") {
		return id, errors.New("a fakeiroh ticket doesn't carry the server's EndpointId: pass -endpoint (GET /api/xbin/app-tunnel's endpointId)")
	}
	t, err := devclient.ParseTicket(ticket)
	if err != nil {
		return id, err
	}
	return t.Endpoint, nil
}
