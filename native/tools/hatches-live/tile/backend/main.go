// ptytest — a tile backend with an upload endpoint and a pty-like WebSocket
// speaking the /ws/term framing (binary bytes, {"op":"resize"}; "exit\r"
// answers an exit frame, "close\r" a clean close), plus /flap, a socket that
// closes at once, for the native app's hatches live check. The WebSocket is hand-rolled (RFC 6455,
// stdlib only).
package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"path": "uploads/" + r.URL.Query().Get("name"), "bytes": len(b),
			"mime": r.Header.Get("Content-Type"), "from": xbin.Caller(r).From, "user": xbin.Caller(r).User})
	})
	mux.HandleFunc("GET /pty", pty)
	mux.HandleFunc("GET /flap", flap)
	xbin.Serve(mux)
}

// upgrade answers the WebSocket handshake and hands over the connection.
func upgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, bool) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "websocket only", 400)
		return nil, nil, false
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, false
	}
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	rw.Flush()
	return conn, rw, true
}

// flap accepts every socket and closes it at once with 1011 (a shell that
// can't start): the app must give up after its retries, not loop.
func flap(w http.ResponseWriter, r *http.Request) {
	conn, _, ok := upgrade(w, r)
	if !ok {
		return
	}
	defer conn.Close()
	write(conn, 8, []byte{0x03, 0xf3}) // 1011
}

func pty(w http.ResponseWriter, r *http.Request) {
	conn, rw, ok := upgrade(w, r)
	if !ok {
		return
	}
	defer conn.Close()
	write(conn, 2, []byte(fmt.Sprintf("hello from=%s user=%s\r\n", xbin.Caller(r).From, xbin.Caller(r).User)))
	for {
		op, payload, err := read(rw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 1:
			var m struct {
				Op         string
				Cols, Rows int
			}
			if json.Unmarshal(payload, &m) == nil && m.Op == "resize" {
				write(conn, 2, []byte(fmt.Sprintf("resized %dx%d\r\n", m.Cols, m.Rows)))
			}
			if string(payload) == `{"op":"please-exit"}` {
				write(conn, 1, []byte(`{"op":"exit"}`))
			}
		case 2:
			if string(payload) == "exit\r" {
				write(conn, 1, []byte(`{"op":"exit"}`))
				continue
			}
			if string(payload) == "close\r" {
				// the shell ended without an exit frame: a clean close (1000)
				write(conn, 8, []byte{0x03, 0xe8})
				return
			}
			write(conn, 2, append([]byte("echo: "), append(bytes.ToUpper(payload), '\r', '\n')...))
		case 8:
			write(conn, 8, nil)
			return
		case 9:
			write(conn, 10, payload)
		}
	}
}

func read(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 0x0f
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return op, p, nil
}

func write(c net.Conn, op byte, p []byte) {
	h := []byte{0x80 | op}
	switch {
	case len(p) < 126:
		h = append(h, byte(len(p)))
	case len(p) < 65536:
		h = append(h, 126, byte(len(p)>>8), byte(len(p)))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(len(p)))
	}
	c.Write(append(h, p...))
}
