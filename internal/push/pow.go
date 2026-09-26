package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The relay's proof of work on registration (relay/README.md §Proof of
// work): a relay that asks for one hands out challenges at GET
// /v1/workspaces/challenge; xbind finds a nonce whose SHA-256(challenge ":"
// nonce) starts with the asked zero bits and posts it with the
// registration. A relay without the endpoint (older ones) asks for none.

// maxPoWBits is the most work xbind does for a relay: 2^28 hashes on
// average, tens of seconds of a core — an admin's PUT /push/config waits
// for it.
const maxPoWBits = 28

// powTimeout bounds the solving (the request's own context bounds it too).
const powTimeout = 3 * time.Minute

func powSolves(challenge, nonce string, b int) bool {
	sum := sha256.Sum256([]byte(challenge + ":" + nonce))
	n := 0
	for _, x := range sum {
		if x != 0 {
			n += bits.LeadingZeros8(x)
			break
		}
		n += 8
	}
	return n >= b
}

// solvePoW finds the first decimal nonce from 0 that solves challenge.
func solvePoW(ctx context.Context, challenge string, b int) (string, error) {
	if b > maxPoWBits {
		return "", fmt.Errorf("the relay asks for %d bits of proof of work; xbind does at most %d (ask its operator for a workspace key)", b, maxPoWBits)
	}
	for i := uint64(0); ; i++ {
		if i&0xffff == 0 {
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("solving the relay's proof of work: %w", err)
			}
		}
		n := strconv.FormatUint(i, 10)
		if powSolves(challenge, n, b) {
			return n, nil
		}
	}
}

type powChallenge struct {
	Challenge string `json:"challenge"`
	Bits      int    `json:"bits"`
	Code      string `json:"code"`
}

// proof solves c into the registration body.
func proof(ctx context.Context, c powChallenge) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, powTimeout)
	defer cancel()
	n, err := solvePoW(ctx, c.Challenge, c.Bits)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pow": map[string]string{"challenge": c.Challenge, "nonce": n}}, nil
}

// registerWorkspace mints this workspace's relay key (POST /v1/workspaces),
// with a proof of work when the relay asks for one — before (its challenge
// endpoint) or in its refusal (a 401 pow_* carries a fresh challenge).
func (s *Service) registerWorkspace(ctx context.Context, relay string) (id, key string, err error) {
	body := map[string]any{}
	var c powChallenge
	if code, err := s.relayCall(ctx, http.MethodGet, relay+"/v1/workspaces/challenge", "", &c); err == nil && c.Bits > 0 && c.Challenge != "" {
		if body, err = proof(ctx, c); err != nil {
			return "", "", err
		}
	} else if err != nil && code != http.StatusNotFound && code != http.StatusMethodNotAllowed && code != 0 {
		s.o.Log.Debug("push: the relay's challenge endpoint", "status", code, "err", err)
	}
	var out struct {
		WorkspaceID string `json:"workspaceId"`
		Key         string `json:"key"`
	}
	for try := 0; ; try++ {
		status, raw, err := s.relayPost(ctx, relay+"/v1/workspaces", body, &out)
		if err == nil {
			break
		}
		var refusal powChallenge
		_ = json.Unmarshal(raw, &refusal)
		if status != http.StatusUnauthorized || try > 0 || refusal.Challenge == "" ||
			(refusal.Code != "pow_required" && refusal.Code != "pow_invalid") {
			return "", "", err
		}
		if body, err = proof(ctx, refusal); err != nil {
			return "", "", err
		}
	}
	if out.Key == "" {
		return "", "", errors.New("the relay answered no key")
	}
	return out.WorkspaceID, out.Key, nil
}

// relayPost posts a JSON body; on a refusal it returns the status and the
// raw answer with the error.
func (s *Service) relayPost(ctx context.Context, target string, in, out any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, raw, fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if json.Unmarshal(raw, out) != nil {
		return resp.StatusCode, raw, errors.New("the answer is not the relay's (is the URL the relay's base URL?)")
	}
	return resp.StatusCode, raw, nil
}
