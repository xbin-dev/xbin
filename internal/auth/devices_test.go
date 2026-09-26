package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

// The device-login test vector — the app-side contract's self-check
// (native/spec/device-login.md repeats these values verbatim; change both or
// neither). The key is derived deterministically (scalar = vecPriv) so a
// client can also re-derive the public key; the signature is one fixed
// ECDSA signature (randomized — clients verify it, they can't reproduce it).
const (
	vecPriv   = "56972329cd96fbb1c5ed326c9930baa3921e2c7660e0485e439293ce4432cd5f"
	vecPubKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEpVrcD7rXkoAfUgG-zMQbXbm6_8DAlKHGjvB5plisVHw_cypnpN6IdyZpAD3skiCZvuksA6pE7T7Iai4J8zi6Xw"
	vecOrigin = "https://xbin.example.com"
	vecDevice = "dev-00112233445566778899aabb"
	vecNonce  = "q2Yf0GJwq0t5J7m8sK0b3Qn9eW1xZ4vC6uR2pL8yT5A"
	vecMsgSHA = "34a9d6a88b9e1a0e7f4bde58507c25b0ea9a5f2101ceb5c6e7abd2d414f55d2e"
	vecSig    = "MEUCIBivy2f2ukCTylHbrik4FnmCw9rGMMivDcsqDMYb5YjHAiEA1o7xq4pOOITOwO4bUHKXsLICRr2_E2ey7sm8tvcGi54"
)

func TestDeviceLoginVector(t *testing.T) {
	msg := DeviceLoginMessage(vecOrigin, vecDevice, vecNonce)
	if string(msg) != "xbin-device-login-v1\nhttps://xbin.example.com\ndev-00112233445566778899aabb\n"+vecNonce {
		t.Fatalf("message bytes: %q", msg)
	}
	sum := sha256.Sum256(msg)
	if hex.EncodeToString(sum[:]) != vecMsgSHA {
		t.Fatalf("message sha256 = %x", sum)
	}
	// The published public key is the one the scalar derives.
	d, _ := new(big.Int).SetString(vecPriv, 16)
	x, y := elliptic.P256().ScalarBaseMult(d.Bytes())
	der, err := x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y})
	if err != nil || base64.RawURLEncoding.EncodeToString(der) != vecPubKey {
		t.Fatalf("public key does not match the scalar: %v", err)
	}
	if !VerifyDeviceSignature(vecPubKey, vecOrigin, vecDevice, vecNonce, vecSig) {
		t.Fatal("the vector signature must verify")
	}
	// Padding is tolerated on input; everything else is exact.
	if !VerifyDeviceSignature(vecPubKey+"=", vecOrigin, vecDevice, vecNonce, vecSig+"=") {
		t.Fatal("padded base64url must still decode")
	}
	for name, c := range map[string][4]string{
		"wrong origin":      {"https://evil.example.com", vecDevice, vecNonce, vecSig},
		"origin with slash": {vecOrigin + "/", vecDevice, vecNonce, vecSig},
		"wrong device":      {vecOrigin, "dev-ffffffffffffffffffffffff", vecNonce, vecSig},
		"wrong nonce":       {vecOrigin, vecDevice, vecNonce[:len(vecNonce)-1] + "B", vecSig},
		"garbage signature": {vecOrigin, vecDevice, vecNonce, "AAAA"},
		"empty signature":   {vecOrigin, vecDevice, vecNonce, ""},
	} {
		if VerifyDeviceSignature(vecPubKey, c[0], c[1], c[2], c[3]) {
			t.Errorf("%s: verified", name)
		}
	}
	// A raw r||s (IEEE P1363, WebCrypto's form) signature is refused: DER only.
	priv := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: d}
	r, s, _ := ecdsa.Sign(rand.Reader, priv, sum[:])
	raw := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	if VerifyDeviceSignature(vecPubKey, vecOrigin, vecDevice, vecNonce, base64.RawURLEncoding.EncodeToString(raw)) {
		t.Fatal("a raw r||s signature must not verify")
	}
}

func TestParseDeviceKey(t *testing.T) {
	if _, canon, err := ParseDeviceKey(vecPubKey); err != nil || canon != vecPubKey {
		t.Fatalf("P-256: %v %q", err, canon)
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	if _, _, err := ParseDeviceKey(base64.RawURLEncoding.EncodeToString(der)); err == nil {
		t.Fatal("P-384 accepted")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	der, _ = x509.MarshalPKIXPublicKey(&rk.PublicKey)
	if _, _, err := ParseDeviceKey(base64.RawURLEncoding.EncodeToString(der)); err == nil {
		t.Fatal("RSA accepted")
	}
	for _, bad := range []string{"", "!!", "AAAA"} {
		if _, _, err := ParseDeviceKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNormalizeOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://xbin.example.com":        "https://xbin.example.com",
		"HTTPS://XBin.Example.com/":       "https://xbin.example.com",
		"https://xbin.example.com:443/x":  "https://xbin.example.com",
		"http://box.lan:80":               "http://box.lan",
		"http://127.0.0.1:8697":           "http://127.0.0.1:8697",
		"https://[2001:DB8::1]:8443/path": "https://[2001:db8::1]:8443",
	} {
		if got, err := NormalizeOrigin(in); err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "xbin.example.com", "https://u:p@x.example"} {
		if got, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestEnrollCodeSingleUse(t *testing.T) {
	a, _ := Load(t.TempDir(), false)
	code, exp, err := a.MintEnrollCode("alice", "https://x.example")
	if err != nil || len(code) != 26 || time.Until(exp) > EnrollCodeTTL || time.Until(exp) < EnrollCodeTTL-time.Minute {
		t.Fatalf("mint: %q %v %v", code, exp, err)
	}
	// Typed forms normalise: any case, dashes and spaces.
	typed := code[:4] + "-" + code[4:10] + " " + code[10:]
	uid, origin, ok := a.RedeemEnrollCode(typed)
	if !ok || uid != "alice" || origin != "https://x.example" {
		t.Fatalf("redeem: %q %q %v", uid, origin, ok)
	}
	if _, _, ok := a.RedeemEnrollCode(code); ok {
		t.Fatal("code redeemed twice")
	}
	if _, _, ok := a.RedeemEnrollCode("NOPE"); ok {
		t.Fatal("bogus code")
	}
	// Expired codes refuse.
	code, _, _ = a.MintEnrollCode("alice", "https://x.example")
	a.mu.Lock()
	for _, c := range a.dev.codes {
		c.expires = time.Now().Add(-time.Second)
	}
	a.mu.Unlock()
	if _, _, ok := a.RedeemEnrollCode(code); ok {
		t.Fatal("expired code redeemed")
	}
}

func TestDeviceChallenges(t *testing.T) {
	a, _ := Load(t.TempDir(), false)
	n1, exp, err := a.NewDeviceChallenge("dev-a")
	if err != nil || len(n1) != 43 || time.Until(exp) > ChallengeTTL {
		t.Fatalf("challenge: %q %v %v", n1, exp, err)
	}
	if a.ConsumeDeviceChallenge("dev-b", n1) {
		t.Fatal("nonce accepted for another device")
	}
	if a.ConsumeDeviceChallenge("dev-a", n1) {
		t.Fatal("a nonce tried for the wrong device must be spent")
	}
	n2, _, _ := a.NewDeviceChallenge("dev-a")
	if !a.ConsumeDeviceChallenge("dev-a", n2) {
		t.Fatal("fresh nonce refused")
	}
	if a.ConsumeDeviceChallenge("dev-a", n2) {
		t.Fatal("nonce replayed")
	}
	n3, _, _ := a.NewDeviceChallenge("dev-a")
	a.mu.Lock()
	a.dev.challenges[n3].expires = time.Now().Add(-time.Second)
	a.mu.Unlock()
	if a.ConsumeDeviceChallenge("dev-a", n3) {
		t.Fatal("expired nonce accepted")
	}
	// A device holds at most maxChallengesPerDevice: the oldest goes first.
	var first string
	for i := 0; i < maxChallengesPerDevice+3; i++ {
		n, _, _ := a.NewDeviceChallenge("dev-c")
		if i == 0 {
			first = n
		}
		time.Sleep(time.Millisecond)
	}
	a.mu.Lock()
	cnt := 0
	for _, c := range a.dev.challenges {
		if c.deviceID == "dev-c" {
			cnt++
		}
	}
	a.mu.Unlock()
	if cnt != maxChallengesPerDevice || a.ConsumeDeviceChallenge("dev-c", first) {
		t.Fatalf("per-device cap: %d outstanding", cnt)
	}
}

func TestAppTicketPKCE(t *testing.T) {
	a, _ := Load(t.TempDir(), false)
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk" // RFC 7636 appendix B
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if !ValidPKCEChallenge(challenge) || ValidPKCEChallenge("short") || ValidPKCEChallenge(verifier+"x") {
		t.Fatal("challenge validation")
	}
	if _, err := a.MintAppTicket("alice", "nope"); err == nil {
		t.Fatal("bad challenge minted a ticket")
	}
	tk, err := a.MintAppTicket("alice", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.RedeemAppTicket(tk, "wrong-verifier"); ok {
		t.Fatal("wrong verifier redeemed")
	}
	if _, ok := a.RedeemAppTicket(tk, verifier); ok {
		t.Fatal("a ticket survives a failed redemption")
	}
	tk, _ = a.MintAppTicket("alice", challenge)
	if uid, ok := a.RedeemAppTicket(tk, verifier); !ok || uid != "alice" {
		t.Fatalf("redeem: %q %v", uid, ok)
	}
	if _, ok := a.RedeemAppTicket(tk, verifier); ok {
		t.Fatal("ticket redeemed twice")
	}
}
