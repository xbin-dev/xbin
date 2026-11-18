package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// Principal.TermRef, on a terminal token's principal (Via "terminal"),
// names the token without being it: TerminalRef(token), a hash. The
// terminal manager tells by it WHICH session's token a request carries — a
// session opened with a shell's token (`bx agent` in a terminal) gets no
// more than that shell has: its person's real home only when that shell
// has it (term/home.go, D245). "" for every other principal. It selects
// nothing else: not a tile, not a sandbox, not an access level.

// TerminalRef is the reference a terminal token's principal carries
// (Principal.TermRef): a hash of the token, never the token.
func TerminalRef(token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte("xbin terminal token ref\x00" + token))
	return hex.EncodeToString(h[:16])
}
