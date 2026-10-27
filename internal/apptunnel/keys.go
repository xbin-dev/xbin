package apptunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/apptunnel/noise"
	"github.com/xbin-dev/xbin/internal/fsutil"
)

// keys are xbind's two tunnel identities (data/app-tunnel/keys.json, 0600,
// outside the vault so a sealed workspace's devices still sign in): the
// iroh endpoint's Ed25519 secret — its public key is the EndpointId — and
// the Noise static key the pairing link's k names.
type keys struct {
	iroh  ed25519.PrivateKey
	noise noise.Key
}

type keysFile struct {
	V     int    `json:"v"`
	Iroh  string `json:"irohSecret"`  // Ed25519 seed, hex
	Noise string `json:"noiseSecret"` // X25519 private key, hex
}

// endpoint is the EndpointId xbind computes itself (never the sidecar's word).
func (k keys) endpoint() [32]byte {
	var id [32]byte
	copy(id[:], k.iroh.Public().(ed25519.PublicKey))
	return id
}

func (k keys) endpointHex() string { id := k.endpoint(); return hex.EncodeToString(id[:]) }

// serverKey is the Noise public key as the link carries it.
func (k keys) serverKey() string { return base64.RawURLEncoding.EncodeToString(k.noise.Public[:]) }

// loadOrCreateKeys reads keys.json, creating it on first use.
func loadOrCreateKeys(dir string) (keys, error) {
	path := filepath.Join(dir, "keys.json")
	b, err := os.ReadFile(path)
	if err == nil {
		var f keysFile
		if err := json.Unmarshal(b, &f); err != nil || f.V != 1 {
			return keys{}, fmt.Errorf("app-tunnel keys.json: unreadable (%v) — not replaced: every paired device would have to pair again", err)
		}
		seed, err1 := hex.DecodeString(f.Iroh)
		priv, err2 := hex.DecodeString(f.Noise)
		if err1 != nil || err2 != nil || len(seed) != ed25519.SeedSize {
			return keys{}, fmt.Errorf("app-tunnel keys.json: malformed — not replaced")
		}
		nk, err := noise.KeyFromPrivate(priv)
		if err != nil {
			return keys{}, err
		}
		return keys{iroh: ed25519.NewKeyFromSeed(seed), noise: nk}, nil
	}
	if !os.IsNotExist(err) {
		return keys{}, err
	}
	_, ik, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return keys{}, err
	}
	nk, err := noise.GenerateKey(nil)
	if err != nil {
		return keys{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return keys{}, err
	}
	out, _ := json.Marshal(keysFile{V: 1, Iroh: hex.EncodeToString(ik.Seed()), Noise: hex.EncodeToString(nk.Private[:])})
	if err := fsutil.WriteFileAtomic(path, append(out, '\n'), 0o600); err != nil {
		return keys{}, err
	}
	return keys{iroh: ik, noise: nk}, nil
}
