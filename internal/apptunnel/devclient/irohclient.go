package devclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// IrohClient is a device's iroh endpoint as a process: `<bin> client`, the
// test mode of xbin-iroh (or of hack/fakeiroh, the same arguments), dialing
// the ticket on one ALPN. Every connection to its local TCP listener
// becomes one bi-stream to the workspace's endpoint, the way the app's
// loopback forwarder works — its Dial is a Dialer's half. One endpoint per
// device at a time: two processes with one key would take each other's
// place at a relay.
type IrohClient struct {
	Addr     string // the local listener (host:port)
	Endpoint string // the EndpointId it dials as (hex)

	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	errs []string
	cond chan struct{} // closed and replaced on each error line
}

// IrohRelay is the client's relay settings (its stdin's "relay"): mode
// "custom" with the ticket's relay URL and the link's token, or
// "disabled". Nil: the ticket's relay, no token (direct only when the
// ticket names none).
type IrohRelay struct {
	Mode  string `json:"mode"`
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// RelayFor is the relay settings a phone takes from a link: the ticket's
// relay with the link's token (r), when there is a token.
func RelayFor(l Link) (*IrohRelay, error) {
	if l.RelayToken == "" {
		return nil, nil
	}
	t, err := ParseTicket(l.Ticket)
	if err != nil {
		return nil, err
	}
	if len(t.Relays) == 0 {
		return nil, errors.New("the link has a relay token but its ticket names no relay")
	}
	return &IrohRelay{Mode: "custom", URL: t.Relays[0], Token: l.RelayToken}, nil
}

// StartIrohClient runs bin's client mode as device d on alpn to ticket,
// and waits for its listener. stderr: where its log goes (nil: discarded).
func StartIrohClient(ctx context.Context, bin, ticket, alpn string, d *Device, relay *IrohRelay, stderr io.Writer) (*IrohClient, error) {
	init := map[string]any{"secretKey": hex.EncodeToString(d.Iroh.Seed())}
	if relay != nil {
		init["relay"] = relay
	}
	line, _ := json.Marshal(init)
	// exec-ok: a test device's own iroh endpoint (xbind's tests and
	// hack/tunnelpair, never the daemon), given no workspace data
	cmd := exec.Command(bin, "client", "--ticket", ticket, "--alpn", alpn, "--listen", "tcp:127.0.0.1:0")
	cmd.Stdin = bytes.NewReader(append(line, '\n')) // the relay token travels here, never in argv
	cmd.Stderr = stderr
	if stderr == nil {
		cmd.Stderr = io.Discard
	}
	cmd.Env = append(os.Environ(), "XBIN_IROH_LOG=info")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &IrohClient{cmd: cmd, done: make(chan struct{}), cond: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.done) }()
	listening := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(out)
		first := true
		for sc.Scan() {
			var m struct {
				T, EndpointID, Addr, Msg string
			}
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			switch {
			case first && m.T == "listening":
				c.Addr, c.Endpoint = m.Addr, m.EndpointID
				first = false
				listening <- nil
			case m.T == "error":
				if first {
					first = false
					listening <- fmt.Errorf("%s client: %s", bin, m.Msg)
				}
				c.mu.Lock()
				c.errs = append(c.errs, m.Msg)
				close(c.cond)
				c.cond = make(chan struct{})
				c.mu.Unlock()
			}
		}
		if first {
			listening <- fmt.Errorf("%s client exited before listening", bin)
		}
	}()
	select {
	case err = <-listening:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil && c.Endpoint != d.EndpointHex() {
		err = fmt.Errorf("%s client dials as %s, not the device's %s", bin, c.Endpoint, d.EndpointHex())
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Dial opens one stream: a connection to the local listener.
func (c *IrohClient) Dial(ctx context.Context) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", c.Addr)
}

// Errors is every error line the client printed (a refused connection, a
// relay that turned it away).
func (c *IrohClient) Errors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errs...)
}

// WaitError waits for an error line after the first n, and returns it.
func (c *IrohClient) WaitError(ctx context.Context, n int) (string, error) {
	for {
		c.mu.Lock()
		if len(c.errs) > n {
			e := c.errs[n]
			c.mu.Unlock()
			return e, nil
		}
		ch := c.cond
		c.mu.Unlock()
		select {
		case <-ch:
		case <-c.done:
			return "", errors.New("the client exited")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// Close ends the process.
func (c *IrohClient) Close() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
	}
}

// ClientDialer is a Dialer over one client per ALPN (nil: that ALPN isn't
// dialed).
func ClientDialer(pair, tunnel *IrohClient) Dialer {
	return func(ctx context.Context, alpn string) (net.Conn, error) {
		c := tunnel
		if alpn == wire.ALPNPairName {
			c = pair
		}
		if c == nil {
			return nil, fmt.Errorf("no iroh client for %s", alpn)
		}
		return c.Dial(ctx)
	}
}
