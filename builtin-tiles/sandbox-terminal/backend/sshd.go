// sshd.go — the SSH ingress: a Go SSH server on the tile's `ssh` stream
// expose (port 2222 in its sandbox; an admin binds a host port to it with
// `bx expose apps/sandbox-terminal ssh=runtime --listen :2222`).
//
// A login is `ssh <sandbox>@host -p 2222`: the key says who is asking (a key
// registered by a person, keys.go), the user name says which sandbox (its
// login name, id or name — managers.go pick). Every session channel is
// bridged to the sandbox's manager (bridge.go). Nothing else: no port or
// agent forwarding, no X11, no sftp subsystem (v1).
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	hostKeySecret      = "ssh-host-key" // the vault key of the host key (an OpenSSH PEM)
	maxPreauth         = 32             // handshakes in flight at once (sshd's MaxStartups)
	maxSessionsPerConn = 10
)

// --- the host key ----------------------------------------------------------------

// loadHostKey reads the tile's host key from its vault, making one (ed25519)
// the first time. Only a vault that says the key is missing makes a new
// one: an error of any other kind is returned, never papered over with a
// fresh key (every client would then see a changed host).
func (t *Tile) loadHostKey() (ssh.Signer, error) {
	pemText, err := t.secret(hostKeySecret)
	if err != nil && !secretMissing(err) {
		return nil, err
	}
	if strings.TrimSpace(pemText) == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "sandbox-terminal host key")
		if err != nil {
			return nil, err
		}
		if err := t.setSecret(hostKeySecret, string(pem.EncodeToMemory(block))); err != nil {
			return nil, fmt.Errorf("storing the new host key: %w", err)
		}
		// read back what the vault holds: two instances starting at once
		// agree on whichever write landed last
		if pemText, err = t.secret(hostKeySecret); err != nil {
			return nil, err
		}
	}
	return ssh.ParsePrivateKey([]byte(pemText))
}

// secretMissing: the vault said the key isn't there (the SDK's error reads
// "vault: 404 Not Found: …no such key…").
func secretMissing(err error) bool {
	if errors.Is(err, errNotFound) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "404") && strings.Contains(msg, "no such key")
}

// --- auth failures ---------------------------------------------------------------

// limiter rate-limits failed logins per source address: a token bucket of
// `burst` failures refilled one per `every`. A failure with the bucket empty
// is answered only after `delay` — a tarpit, never a lockout: a good key
// isn't slowed, so a flood of bad ones can't lock people out even when every
// connection arrives from one address (xbind's relay into the sandbox).
type limiter struct {
	burst   float64
	every   time.Duration
	delay   time.Duration
	mu      sync.Mutex
	buckets map[string]*bucket
	slowed  int
	logged  time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter() *limiter {
	return &limiter{burst: 20, every: 2 * time.Second, delay: 2 * time.Second, buckets: map[string]*bucket{}}
}

// fail records a failed attempt from src; it reports false when src is over
// its rate.
func (l *limiter) fail(src string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[src]
	if b == nil {
		if len(l.buckets) >= 4096 { // forget sources that are back to full
			for k, x := range l.buckets {
				if x.tokens+now.Sub(x.at).Seconds()/l.every.Seconds() >= l.burst {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[src] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()/l.every.Seconds())
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	l.slowed++
	if now.Sub(l.logged) > time.Minute {
		log.Printf("ssh: failed logins over the rate from %s — %d slowed down in the last minute", src, l.slowed)
		l.logged, l.slowed = now, 0
	}
	return false
}

// failed is a refused key: counted, and slowed down when over the rate.
func (t *Tile) failed(addr net.Addr) {
	src := addr.String()
	if h, _, err := net.SplitHostPort(src); err == nil {
		src = h
	}
	if !t.limit.fail(src) {
		time.Sleep(t.limit.delay)
	}
}

// --- the server ------------------------------------------------------------------

func (t *Tile) sshConfig(signer ssh.Signer) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries:  6,
		ServerVersion: "SSH-2.0-xbin-sandbox-terminal",
		PublicKeyCallback: func(c ssh.ConnMetadata, pk ssh.PublicKey) (*ssh.Permissions, error) {
			k, ok := t.keyFor(pk)
			if !ok {
				t.failed(c.RemoteAddr())
				return nil, errors.New("unknown key")
			}
			return &ssh.Permissions{Extensions: map[string]string{"xbin-user": k.User, "xbin-key": k.ID}}, nil
		},
	}
	cfg.AddHostKey(signer)
	return cfg
}

// startSSH loads the host key (retrying while the vault doesn't answer) and
// serves SSH on addr until the process ends.
func (t *Tile) startSSH(addr string) {
	var signer ssh.Signer
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		s, err := t.loadHostKey()
		if err == nil {
			signer = s
			break
		}
		t.setSSHState(false, "the host key: "+err.Error())
		log.Printf("ssh: the host key: %v (retrying in %s)", err, wait)
		time.Sleep(wait)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.setSSHState(false, err.Error())
		log.Printf("ssh: %v", err)
		return
	}
	log.Printf("ssh: serving %s (host key %s)", ln.Addr(), ssh.FingerprintSHA256(signer.PublicKey()))
	_ = t.serveSSH(ln, signer)
}

func (t *Tile) setSSHState(listening bool, errMsg string) {
	t.mu.Lock()
	t.listening, t.sshErr = listening, errMsg
	t.mu.Unlock()
}

// serveSSH accepts SSH connections on ln with host key signer.
func (t *Tile) serveSSH(ln net.Listener, signer ssh.Signer) error {
	cfg := t.sshConfig(signer)
	t.mu.Lock()
	t.signer = signer
	t.mu.Unlock()
	t.setSSHState(true, "")
	defer t.setSSHState(false, "")
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case t.preauth <- struct{}{}:
			go t.handleConn(nc, cfg)
		default:
			_ = nc.Close() // too many logins in flight: like sshd's MaxStartups
		}
	}
}

// liveConn is one logged-in SSH connection.
type liveConn struct {
	sc      *ssh.ServerConn
	user    string // the person (their key's)
	key     string // the key's id
	login   string // the SSH user name: the sandbox asked for
	remote  string
	started int64
	nsess   atomic.Int32
	mu      sync.Mutex
	sess    map[*session]struct{}
}

func (t *Tile) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(t.loginGrace))
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	<-t.preauth
	if err != nil {
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sc.Close()
	lc := &liveConn{sc: sc, user: sc.Permissions.Extensions["xbin-user"], key: sc.Permissions.Extensions["xbin-key"],
		login: sc.User(), remote: sc.RemoteAddr().String(), started: time.Now().UnixMilli(), sess: map[*session]struct{}{}}
	t.mu.Lock()
	t.conns[lc] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.conns, lc)
		t.mu.Unlock()
	}()
	go t.touchKey(lc.key)
	go ssh.DiscardRequests(reqs) // global requests (tcpip-forward, …) are refused
	go keepalive(sc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.Prohibited, "sandbox-terminal opens sessions only: no port, agent or X11 forwarding")
			continue
		}
		if lc.nsess.Load() >= maxSessionsPerConn {
			_ = nch.Reject(ssh.ResourceShortage, "too many sessions on one connection")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		lc.nsess.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer lc.nsess.Add(-1)
			t.serveSession(ctx, lc, ch, creqs)
		}()
	}
	cancel() // the connection is gone: every session's command is ended
	wg.Wait()
}

// keepalive notices a peer that vanished without closing (a laptop lid):
// a keepalive unanswered for a minute and a half ends the connection.
func keepalive(sc *ssh.ServerConn) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for range tick.C {
		done := make(chan error, 1)
		go func() {
			_, _, err := sc.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				return
			}
		case <-time.After(90 * time.Second):
			_ = sc.Close()
			return
		}
	}
}

// closeKey ends every live connection that logged in with key id (a
// revoked key logs nobody in any more, and keeps nobody logged in).
func (t *Tile) closeKey(id string) {
	t.mu.Lock()
	var cs []*liveConn
	for lc := range t.conns {
		if lc.key == id {
			cs = append(cs, lc)
		}
	}
	t.mu.Unlock()
	for _, lc := range cs {
		_ = lc.sc.Close()
	}
}

// sessionView is one live SSH session as GET /sessions shows it.
type sessionView struct {
	User     string `json:"user"`
	Login    string `json:"login"`
	Provider string `json:"provider,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind"` // terminal | command | starting
	Remote   string `json:"remote"`
	Started  int64  `json:"started"`
	Key      string `json:"key"`
}

// sessions lists the live SSH sessions (user "" = everyone's).
func (t *Tile) sessions(user string) []sessionView {
	t.mu.Lock()
	cs := make([]*liveConn, 0, len(t.conns))
	for lc := range t.conns {
		if user == "" || lc.user == user {
			cs = append(cs, lc)
		}
	}
	t.mu.Unlock()
	out := []sessionView{}
	for _, lc := range cs {
		lc.mu.Lock()
		for s := range lc.sess {
			v := sessionView{User: lc.user, Login: lc.login, Remote: lc.remote, Started: s.started, Key: lc.key, Kind: "starting"}
			if s.info != nil {
				v.Provider, v.Sandbox, v.Name, v.Kind = s.info.M.Provider, s.info.SB.ID, s.info.SB.Name, s.kind
			}
			out = append(out, v)
		}
		lc.mu.Unlock()
	}
	return out
}
