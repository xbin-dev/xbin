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
	mrand "math/rand/v2"
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
	maxBuckets         = 4096 // the failed-login limiter's (source, name) buckets
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

// limiter rate-limits failed logins per source address AND the user name the
// client claims: a token bucket of `burst` failures refilled one per
// `every`. A failure with the bucket empty is answered only after `delay` —
// a tarpit, never a lockout: a good key isn't slowed. Behind xbind's port
// relay every connection arrives from one address, so a bucket per address
// alone would let a flood of bad keys slow everyone's failures down — a
// person whose client offers two old keys before the right one would then
// spend the login grace in the tarpit. Keyed by the claimed name too, a
// flood slows only the name it claims.
type limiter struct {
	burst   float64
	every   time.Duration
	delay   time.Duration
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
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

// fail records a failed attempt from src claiming name; it reports false
// when that pair is over its rate.
func (l *limiter) fail(src, name string) bool {
	now := time.Now()
	key := src + "\x00" + name
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			l.shrink(now)
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()/l.every.Seconds())
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	l.slowed++
	if now.Sub(l.logged) > time.Minute {
		log.Printf("ssh: failed logins over the rate from %s as %q — %d slowed down in the last minute", src, name, l.slowed)
		l.logged, l.slowed = now, 0
	}
	return false
}

// shrink makes room for a bucket (l.mu held): the ones back to full go (at
// most one sweep a second), then arbitrary ones while it is still full —
// a flood of names can't grow the map, and a bucket forgotten early only
// starts full again.
func (l *limiter) shrink(now time.Time) {
	if now.Sub(l.swept) >= time.Second {
		l.swept = now
		for k, x := range l.buckets {
			if x.tokens+now.Sub(x.at).Seconds()/l.every.Seconds() >= l.burst {
				delete(l.buckets, k)
			}
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < maxBuckets {
			break
		}
		delete(l.buckets, k)
	}
}

// failed is a refused key: counted against its source and the name it
// claims, and slowed down when over the rate.
func (t *Tile) failed(c ssh.ConnMetadata) {
	src := c.RemoteAddr().String()
	if h, _, err := net.SplitHostPort(src); err == nil {
		src = h
	}
	name := c.User()
	if len(name) > 64 { // the client's to choose: a bucket's key stays small
		name = name[:64]
	}
	if !t.limit.fail(src, name) {
		time.Sleep(t.limit.delay)
	}
}

// --- handshakes in flight ------------------------------------------------------------

// pending is the connections still in their handshake (sshd's
// MaxStartups): at most max. A connection over it drops a random one of
// the older ones rather than being refused itself — randomized early drop:
// a flood of connections that never finish their handshake can't fill
// every slot and keep people out; a real login finishes in well under the
// login grace and is unlikely to be the one dropped while it does.
type pending struct {
	mu      sync.Mutex
	max     int
	conns   []net.Conn
	dropped int
	logged  time.Time
}

// admit adds nc, dropping (closing) a random older one when full.
func (p *pending) admit(nc net.Conn) {
	p.mu.Lock()
	var victim net.Conn
	if len(p.conns) >= p.max && len(p.conns) > 0 {
		i := mrand.IntN(len(p.conns))
		victim = p.conns[i]
		p.conns[i] = p.conns[len(p.conns)-1]
		p.conns = p.conns[:len(p.conns)-1]
		p.dropped++
		if now := time.Now(); now.Sub(p.logged) > time.Minute {
			log.Printf("ssh: %d handshakes in flight — dropping a random older one for each new connection (%d in the last minute)", p.max, p.dropped)
			p.logged, p.dropped = now, 0
		}
	}
	p.conns = append(p.conns, nc)
	p.mu.Unlock()
	if victim != nil {
		_ = victim.Close()
	}
}

// done takes nc out once its handshake ended (or it was dropped).
func (p *pending) done(nc net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, c := range p.conns {
		if c == nc {
			p.conns[i] = p.conns[len(p.conns)-1]
			p.conns = p.conns[:len(p.conns)-1]
			return
		}
	}
}

// inFlight is how many handshakes are pending.
func (p *pending) inFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// --- the server ------------------------------------------------------------------

func (t *Tile) sshConfig(signer ssh.Signer) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		MaxAuthTries:  6,
		ServerVersion: "SSH-2.0-xbin-sandbox-terminal",
		PublicKeyCallback: func(c ssh.ConnMetadata, pk ssh.PublicKey) (*ssh.Permissions, error) {
			k, ok := t.keyFor(pk)
			if !ok {
				t.failed(c)
				return nil, errors.New("unknown key")
			}
			// the key's person must still use this tile (access.go): one who
			// may not is let in only to be told so (the session says it and
			// exits 1), so the message reaches them instead of a bare
			// "Permission denied"
			ctx, cancel := context.WithTimeout(context.Background(), t.loginGrace/2) // within the login grace
			a, err := t.access(ctx, k.User)
			cancel()
			ext := map[string]string{"xbin-user": k.User, "xbin-key": k.ID}
			if why := refusedWhy(a, err); why != "" {
				ext["xbin-denied"] = why
			}
			return &ssh.Permissions{Extensions: ext}, nil
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
	ln := t.listen(addr)
	log.Printf("ssh: serving %s (host key %s)", ln.Addr(), ssh.FingerprintSHA256(signer.PublicKey()))
	_ = t.serveSSH(ln, signer)
}

// listen binds addr, retrying until it can: after a restart (a rebinding
// restarts the backend) the previous run may still hold the port for a
// moment, and giving up would leave SSH down until the next restart. Until
// then GET /me says why it isn't listening.
func (t *Tile) listen(addr string) net.Listener {
	for wait := t.listenRetry; ; wait = min(2*wait, 30*time.Second) {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln
		}
		t.setSSHState(false, err.Error())
		log.Printf("ssh: %v (retrying in %s)", err, wait)
		time.Sleep(wait)
	}
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
		t.preauth.admit(nc) // over the cap: a random older handshake is dropped
		go t.handleConn(nc, cfg)
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
	denied  string // why the person may not use this tile ("" = they may): its sessions say it, and nothing runs
	nsess   atomic.Int32
	mu      sync.Mutex
	sess    map[*session]struct{}
}

func (t *Tile) handleConn(nc net.Conn, cfg *ssh.ServerConfig) {
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(t.loginGrace))
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	t.preauth.done(nc)
	if err != nil {
		return
	}
	defer sc.Close()
	lc := &liveConn{sc: sc, user: sc.Permissions.Extensions["xbin-user"], key: sc.Permissions.Extensions["xbin-key"],
		login: sc.User(), remote: sc.RemoteAddr().String(), started: time.Now().UnixMilli(), sess: map[*session]struct{}{},
		denied: sc.Permissions.Extensions["xbin-denied"]}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if lc.denied != "" {
		// told on its sessions, then gone: the connection keeps a deadline
		_ = nc.SetDeadline(time.Now().Add(t.loginGrace))
	} else {
		_ = nc.SetDeadline(time.Time{})
		t.mu.Lock()
		t.conns[lc] = struct{}{}
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			delete(t.conns, lc)
			t.mu.Unlock()
		}()
		go t.touchKey(lc.key)
		go keepalive(sc)
		go t.watchAccess(ctx, lc)
	}
	go ssh.DiscardRequests(reqs) // global requests (tcpip-forward, …) are refused
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
