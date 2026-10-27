package devclient

import (
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Ticket is an iroh EndpointTicket as the link's t carries it (spec §2): the
// server's EndpointId and how to reach it — its relay URL (a relay-only
// ticket, as xbind mints them), or with relays off (tests) its direct
// addresses.
type Ticket struct {
	Endpoint [32]byte
	Relays   []string
	Addrs    []string // ip:port
}

// ParseTicket decodes "endpoint" + lowercase base32 (no padding) of the
// ticket's postcard bytes (iroh-tickets 1.0, wire variant 0): the
// EndpointId's 32 bytes, then a set of transport addresses — Relay(url) or
// Ip(socket address). On a phone IrohLib parses it; xbind's tests and
// hack/tunnelpair need the EndpointId for the Noise prologue and the relay
// URL a token goes with.
func ParseTicket(s string) (Ticket, error) {
	rest, ok := strings.CutPrefix(s, "endpoint")
	if !ok {
		return Ticket{}, errors.New("ticket: no endpoint prefix")
	}
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(rest))
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket: %w", err)
	}
	d := postcard{b: b}
	var t Ticket
	if v := d.varint(); v != 0 {
		return Ticket{}, fmt.Errorf("ticket: wire variant %d", v)
	}
	copy(t.Endpoint[:], d.bytes(32))
	for n := d.varint(); n > 0 && d.err == nil; n-- {
		switch d.varint() {
		case 0: // Relay(RelayUrl): a string
			t.Relays = append(t.Relays, string(d.bytes(int(d.varint()))))
		case 1: // Ip(SocketAddr): V4(ip[4], port) | V6(ip[16], port)
			var ip net.IP
			switch d.varint() {
			case 0:
				ip = net.IP(d.bytes(4))
			case 1:
				ip = net.IP(d.bytes(16))
			default:
				d.fail("socket address kind")
			}
			port := d.varint()
			if d.err == nil {
				t.Addrs = append(t.Addrs, net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)))
			}
		default:
			d.fail("transport address kind")
		}
	}
	if d.err != nil {
		return Ticket{}, d.err
	}
	return t, nil
}

// postcard reads the few postcard encodings a ticket uses.
type postcard struct {
	b   []byte
	err error
}

func (d *postcard) fail(what string) {
	if d.err == nil {
		d.err = errors.New("ticket: bad " + what)
	}
}

func (d *postcard) varint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail("varint")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *postcard) bytes(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b) {
		d.fail("length")
		return nil
	}
	out := d.b[:n]
	d.b = d.b[n:]
	return out
}
