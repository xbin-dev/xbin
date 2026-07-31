package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// sshProxy is the tile's SSH front door. The SSH username selects the target
// container; on a shell/exec request it runs `podman exec -it <container>` on
// a PTY and bridges the SSH channel to it. Auth is public-key only, against the
// owner's authorized keys (store) — default-deny.
type sshProxy struct {
	cfg *ssh.ServerConfig
	st  *store
	pod *podman
}

func newSSHProxy(st *store, pod *podman) (*sshProxy, error) {
	hk, err := st.hostKey()
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	p := &sshProxy{st: st, pod: pod}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if p.st.authorized(key) {
				return &ssh.Permissions{Extensions: map[string]string{"fp": ssh.FingerprintSHA256(key)}}, nil
			}
			return nil, fmt.Errorf("public key not authorized")
		},
		// A helpful banner naming the container the username maps to.
		BannerCallback: func(c ssh.ConnMetadata) string {
			return fmt.Sprintf("xbin devbox — connecting to container %q\r\n", c.User())
		},
	}
	cfg.AddHostKey(hk)
	p.cfg = cfg
	return p, nil
}

// serve accepts connections on ln until it closes.
func (p *sshProxy) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go p.handleConn(c)
	}
}

func (p *sshProxy) handleConn(nConn net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nConn, p.cfg)
	if err != nil {
		nConn.Close()
		return
	}
	defer conn.Close()
	container := conn.User()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go p.handleSession(ch, chReqs, container)
	}
}

func (p *sshProxy) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request, container string) {
	defer ch.Close()
	if !validName(container) || !p.pod.exists(container) {
		fmt.Fprintf(ch, "no such container: %q\r\n", container)
		sendExit(ch, 1)
		return
	}
	if !p.pod.running(container) {
		if _, err := p.pod.start(container); err != nil {
			fmt.Fprintf(ch, "could not start container %q: %v\r\n", container, err)
			sendExit(ch, 1)
			return
		}
	}

	var sz *pty.Winsize
	term := "xterm-256color"
	started := false
	var ptmx *os.File // the PTY master, set once the shell starts

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			t, w, h, ok := parsePtyReq(req.Payload)
			if ok {
				term = t
				sz = &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}
			}
			req.Reply(true, nil)
		case "window-change":
			w, h, ok := parseWindow(req.Payload)
			if ok {
				sz = &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}
				if ptmx != nil {
					_ = pty.Setsize(ptmx, sz)
				}
			}
		case "shell", "exec":
			if started {
				req.Reply(false, nil)
				continue
			}
			started = true
			cmd := p.execCmd(container, term, req.Type, req.Payload)
			f, err := pty.StartWithSize(cmd, sz)
			if err != nil {
				fmt.Fprintf(ch, "exec failed: %v\r\n", err)
				req.Reply(false, nil)
				sendExit(ch, 1)
				return
			}
			ptmx = f
			req.Reply(true, nil)
			go func() { _, _ = io.Copy(f, ch) }()                  // ssh → pty
			go func() { _, _ = io.Copy(ch, f); ch.CloseWrite() }() // pty → ssh
			go func() {
				err := cmd.Wait()
				f.Close()
				sendExit(ch, exitCode(err))
				ch.Close()
			}()
		default:
			req.Reply(false, nil)
		}
	}
}

// execCmd builds the `podman exec` for a session. A plain shell request runs an
// interactive login shell; an exec request runs the requested command inside a
// shell (so pipes/quoting behave like a normal ssh command).
func (p *sshProxy) execCmd(container, term, reqType string, payload []byte) *exec.Cmd {
	args := []string{"exec", "-it", "--env", "TERM=" + term, container}
	if reqType == "exec" {
		if cmdline, ok := parseExec(payload); ok && cmdline != "" {
			return p.pod.command(append(args, "/bin/sh", "-lc", cmdline)...)
		}
	}
	// Prefer bash, fall back to sh — one command line so we don't probe first.
	return p.pod.command(append(args, "/bin/sh", "-lc", "exec \"$(command -v bash || command -v sh)\" -l")...)
}

// --- SSH payload parsing (RFC 4254 §6.2/§6.5/§6.7) ---

func parsePtyReq(b []byte) (term string, w, h uint32, ok bool) {
	term, b, ok = readString(b)
	if !ok || len(b) < 8 {
		return "", 0, 0, false
	}
	w = binary.BigEndian.Uint32(b[0:4])
	h = binary.BigEndian.Uint32(b[4:8])
	return term, w, h, true
}

func parseWindow(b []byte) (w, h uint32, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(b[0:4]), binary.BigEndian.Uint32(b[4:8]), true
}

func parseExec(b []byte) (string, bool) { s, _, ok := readString(b); return s, ok }

func readString(b []byte) (string, []byte, bool) {
	if len(b) < 4 {
		return "", b, false
	}
	n := binary.BigEndian.Uint32(b[0:4])
	if uint32(len(b)-4) < n {
		return "", b, false
	}
	return string(b[4 : 4+n]), b[4+n:], true
}

func sendExit(ch ssh.Channel, code int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code))
	_, _ = ch.SendRequest("exit-status", false, payload)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

// listenSSH binds the in-netns SSH port (exposed to a host TCP port by the
// owner's ingress binding, plans/ingress.md).
func (p *sshProxy) listenSSH(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("devbox: ssh proxy listening on %s (exposed via ingress)", addr)
	go p.serve(ln)
	return nil
}
