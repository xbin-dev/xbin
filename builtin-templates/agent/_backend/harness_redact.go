// harness_redact.go — a saved sign-in's secret kept out of what a coding
// agent's output becomes here (D179, the security review's M3). The secret
// sits in the adapter's environment, and every process the agent starts
// inherits it: a routine `env`, a hook or a prompt-injected command can
// print it, and the adapter's output becomes transcript rows, events, the
// AgTT parent's context, notes and logs. So the adapter's stdout is redacted
// before the client reads it — the exact secret of the generation, and any
// string shaped like an Anthropic token (sk-ant-…) — and so are the
// adapter's stderr as GET /harness/log serves it, its log lines here, and a
// refusal's words kept with a saved sign-in.
//
// The redaction keeps the stream's length (the same number of bytes, '*'s
// after a "[redacted]" mark), so the offsets the engine stores (read_off)
// and a successor resumes from stay the adapter's own. Lines are redacted
// whole: a partial line waits for its newline (an ACP frame is one line; the
// client's decoder waits for it anyway), so a secret is never split across
// two reads.
package main

import (
	"bytes"
	"errors"
	"io"
	"regexp"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// tokenShape is an Anthropic credential's shape: sk-ant-oat01-…,
// sk-ant-api03-…, sk-ant-admin01-… (a setup-token, an API key).
var tokenShape = regexp.MustCompile(`sk-ant-[a-z]{2,8}\d{2}-[A-Za-z0-9_-]{16,}`)

const redactMark = "[redacted]"

// mask is n bytes that say something was redacted.
func mask(n int) []byte {
	out := bytes.Repeat([]byte{'*'}, n)
	if n >= len(redactMark) {
		copy(out, redactMark)
	}
	return out
}

// redactor masks the exact secrets (none: the token shape alone).
type redactor struct{ exact [][]byte }

func newRedactor(secrets ...string) *redactor {
	r := &redactor{}
	for _, s := range secrets {
		if len(s) >= 8 { // a shorter "secret" would mask ordinary text
			r.exact = append(r.exact, []byte(s))
		}
	}
	return r
}

// apply masks b in place (same length) and answers it.
func (r *redactor) apply(b []byte) []byte {
	if r != nil {
		for _, s := range r.exact {
			for i := 0; ; {
				j := bytes.Index(b[i:], s)
				if j < 0 {
					break
				}
				copy(b[i+j:], mask(len(s)))
				i += j + len(s)
			}
		}
	}
	for _, m := range tokenShape.FindAllIndex(b, -1) {
		copy(b[m[0]:m[1]], mask(m[1]-m[0]))
	}
	return b
}

// text is s redacted.
func (r *redactor) text(s string) string { return string(r.apply([]byte(s))) }

// redactText is s with secrets and the token shape masked.
func redactText(s string, secrets ...string) string { return newRedactor(secrets...).text(s) }

// redactMax bounds a line held back for its newline: past it the bytes go
// on redacted as they are (a frame that long is no ACP frame of a secret).
const redactMax = 16 << 20

// redactReader is r with whole lines redacted (r's errors kept: an
// acp.Gap goes on once, after the bytes that came before it).
type redactReader struct {
	r    io.Reader
	red  func() *redactor // the session's, as it is now
	buf  []byte           // read, not yet returned (a partial line at the end)
	out  []byte           // redacted, to return
	gap  error            // an acp.Gap to return once out is drained
	err  error            // sticky: the end
	read []byte
}

func newRedactReader(r io.Reader, red func() *redactor) *redactReader {
	return &redactReader{r: r, red: red, read: make([]byte, 32<<10)}
}

func (x *redactReader) Read(b []byte) (int, error) {
	for len(x.out) == 0 {
		if x.gap != nil {
			g := x.gap
			x.gap = nil
			return 0, g
		}
		if x.err != nil {
			if len(x.buf) > 0 { // the last, partial line
				x.out, x.buf = x.red().apply(x.buf), nil
				continue
			}
			return 0, x.err
		}
		n, err := x.r.Read(x.read)
		x.buf = append(x.buf, x.read[:n]...)
		var g *acp.Gap
		switch {
		case errors.As(err, &g):
			// bytes before the gap go first, whole or not (the decoder
			// drops the broken line), then the gap
			x.out, x.buf, x.gap = x.red().apply(x.buf), nil, err
			continue
		case err != nil:
			x.err = err
		}
		if i := bytes.LastIndexByte(x.buf, '\n'); i >= 0 {
			line := x.buf[:i+1]
			x.out = x.red().apply(append([]byte(nil), line...))
			x.buf = append([]byte(nil), x.buf[i+1:]...)
		} else if len(x.buf) > redactMax {
			x.out, x.buf = x.red().apply(x.buf), nil
		}
	}
	n := copy(b, x.out)
	x.out = x.out[n:]
	return n, nil
}
