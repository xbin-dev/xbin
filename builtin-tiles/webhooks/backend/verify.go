package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// verify checks a delivery against its hook's secret: a token (?token=… or
// Authorization: Bearer …), or an HMAC-SHA256 of the raw body in a header
// (GitHub's X-Hub-Signature-256: sha256=<hex>; the header and prefix are the
// hook's). Comparisons are constant-time; no secret, no entry.
func verify(h hook, secret string, r *http.Request, body []byte) bool {
	if secret == "" {
		return false
	}
	switch h.Auth {
	case "token":
		got := r.URL.Query().Get("token")
		if got == "" {
			got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
	case "hmac":
		sig := strings.ToLower(strings.TrimPrefix(r.Header.Get(h.SigHeader), h.SigPrefix))
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
	}
	return false
}
