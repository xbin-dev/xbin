package relay

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math/bits"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Proof of work on anonymous workspace registration (Config.RegistrationPoW,
// `-registration-pow <bits>`; relay/README.md §Proof of work). POST
// /v1/workspaces is the one call anyone may make that adds a durable
// entry, so on a public relay each one can be made to cost CPU time:
//
//	GET /v1/workspaces/challenge → {challenge, bits, expires}
//	find a nonce with SHA-256(challenge ":" nonce) starting with `bits` zero bits
//	POST /v1/workspaces {"pow": {"challenge", "nonce"}}
//
// A challenge is stateless — version, difficulty, expiry and 16 random
// bytes under an HMAC with a key the relay makes at start — so handing them
// out costs nothing and stores nothing. A solved one is spent: the relay
// remembers it until it expires (replay), which the registration limits
// bound. A restart makes every outstanding challenge invalid; a client gets
// pow_invalid with a fresh challenge and solves again.

// MaxRegistrationPoW is the most work the relay may ask for (2^32 hashes
// on average: minutes of a CPU core).
const MaxRegistrationPoW = 32

// powTTL is how long a challenge may be solved and spent.
const powTTL = 10 * time.Minute

const (
	powVersion  = 1
	powRawLen   = 1 + 1 + 8 + 16 + 16 // version, bits, expires, random, mac
	powMACLen   = 16
	maxPoWSpent = 1 << 16 // spent challenges remembered at once (then 503: try later)
)

// Error codes of POST /v1/workspaces under proof of work (401, with a fresh
// challenge in the body).
const (
	ErrPoWRequired = "pow_required" // no proof given
	ErrPoWInvalid  = "pow_invalid"  // wrong, expired, too easy, or already spent
)

var powNonceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type powState struct {
	key   []byte
	mu    sync.Mutex
	spent map[string]int64 // challenge → its expiry (unix)
	sweep int64
}

func newPoWState() *powState {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return &powState{key: k, spent: map[string]int64{}}
}

func (p *powState) mac(b []byte) []byte {
	m := hmac.New(sha256.New, p.key)
	m.Write(b)
	return m.Sum(nil)[:powMACLen]
}

// challenge mints one for difficulty b.
func (p *powState) challenge(b int, now time.Time) (string, int64) {
	exp := now.Add(powTTL).Unix()
	raw := make([]byte, 0, powRawLen)
	raw = append(raw, powVersion, byte(b))
	raw = binary.BigEndian.AppendUint64(raw, uint64(exp))
	r := make([]byte, 16)
	if _, err := rand.Read(r); err != nil {
		panic(err)
	}
	raw = append(raw, r...)
	raw = append(raw, p.mac(raw)...)
	return base64.RawURLEncoding.EncodeToString(raw), exp
}

// verify checks a solution against the difficulty in force (want); ok
// returns the challenge's expiry for spend.
func (p *powState) verify(challenge, nonce string, want int, now time.Time) (exp int64, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(raw) != powRawLen || raw[0] != powVersion || !powNonceRe.MatchString(nonce) {
		return 0, false
	}
	body, sum := raw[:powRawLen-powMACLen], raw[powRawLen-powMACLen:]
	if !hmac.Equal(sum, p.mac(body)) {
		return 0, false
	}
	b := int(raw[1])
	exp = int64(binary.BigEndian.Uint64(raw[2:10]))
	if b < want || exp < now.Unix() || !PoWSolves(challenge, nonce, b) {
		return 0, false
	}
	return exp, true
}

// spend records a verified challenge; false when it was spent before (or
// too many are outstanding).
func (p *powState) spend(challenge string, exp int64, now time.Time) (ok, full bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := now.Unix()
	if t-p.sweep >= 60 || len(p.spent) >= maxPoWSpent {
		p.sweep = t
		for c, e := range p.spent {
			if e < t {
				delete(p.spent, c)
			}
		}
	}
	if _, used := p.spent[challenge]; used {
		return false, false
	}
	if len(p.spent) >= maxPoWSpent {
		return false, true
	}
	p.spent[challenge] = exp
	return true, false
}

// PoWSolves reports whether nonce solves challenge at difficulty b:
// SHA-256 of the ASCII string challenge + ":" + nonce begins with b zero
// bits.
func PoWSolves(challenge, nonce string, b int) bool {
	sum := sha256.Sum256([]byte(challenge + ":" + nonce))
	return leadingZeroBits(sum[:]) >= b
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, x := range b {
		if x != 0 {
			return n + bits.LeadingZeros8(x)
		}
		n += 8
	}
	return n
}

// SolvePoW finds a nonce for challenge at difficulty b (the xbind side
// does the same; this one serves tests and tools). Decimal nonces from 0.
func SolvePoW(ctx context.Context, challenge string, b int) (string, error) {
	for i := uint64(0); ; i++ {
		if i&0xffff == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		n := strconv.FormatUint(i, 10)
		if PoWSolves(challenge, n, b) {
			return n, nil
		}
	}
}

// handleChallenge is GET /v1/workspaces/challenge: {challenge, bits,
// expires}, or {bits: 0} when registration asks for no work.
func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	b := s.cfg.RegistrationPoW
	if b <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"bits": 0})
		return
	}
	c, exp := s.pow.challenge(b, s.cfg.Now())
	writeJSON(w, http.StatusOK, map[string]any{"challenge": c, "bits": b, "expires": exp})
}

// powRefused answers 401 with a fresh challenge, so a client solves and
// retries in one step.
func (s *Server) powRefused(w http.ResponseWriter, code, msg string) {
	b := s.cfg.RegistrationPoW
	c, exp := s.pow.challenge(b, s.cfg.Now())
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": msg, "code": code, "challenge": c, "bits": b, "expires": exp})
}
