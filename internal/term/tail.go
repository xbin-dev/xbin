package term

import "strings"

// tail.go — how a session's end reads in xbind's log: its exit, and the
// last of what it printed (moved out of term.go, which pump still calls).

// exitString renders a Wait error compactly ("ok", "exit status 127", …).
func exitString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// scrollTail renders the last n bytes of PTY scrollback as plain text for the
// log: ANSI escape sequences (CSI/OSC) and control bytes are stripped so the
// sandbox init's error line comes out readable.
func scrollTail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b: // ESC — skip the sequence it introduces
			if i+1 < len(b) && b[i+1] == '[' { // CSI: until a final byte @…~
				for i += 2; i < len(b) && (b[i] < 0x40 || b[i] > 0x7e); i++ {
				}
			} else if i+1 < len(b) && b[i+1] == ']' { // OSC: until BEL or ESC
				for i += 2; i < len(b) && b[i] != 0x07 && b[i] != 0x1b; i++ {
				}
			} else {
				i++
			}
		case c == '\n':
			out = append(out, ' ')
		case c >= 32 && c < 127:
			out = append(out, c)
		}
	}
	return strings.TrimSpace(string(out))
}
