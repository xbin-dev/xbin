// Package jsonc parses JSON with comments and trailing commas (JSONC), the
// manifest format used by xbin.json / scope.json (decision D5).
package jsonc

import (
	"encoding/json"
	"fmt"
)

// Strip converts JSONC to plain JSON by removing // and /* */ comments and
// trailing commas. String contents are preserved verbatim. Byte offsets of
// unremoved characters are kept stable where possible (comments are replaced
// with spaces) so json.Unmarshal errors still point near the real location.
func Strip(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	inStr := false
	esc := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '/':
			if i+1 < len(out) && out[i+1] == '/' {
				for i < len(out) && out[i] != '\n' {
					out[i] = ' '
					i++
				}
			} else if i+1 < len(out) && out[i+1] == '*' {
				out[i], out[i+1] = ' ', ' '
				i += 2
				for i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/') {
					if out[i] != '\n' {
						out[i] = ' '
					}
					i++
				}
				if i+1 < len(out) {
					out[i], out[i+1] = ' ', ' '
					i++
				}
			}
		case ',':
			// Trailing comma: next non-space, non-comment char is } or ].
			j := i + 1
			for j < len(out) {
				if out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r' {
					j++
					continue
				}
				if out[j] == '/' && j+1 < len(out) && (out[j+1] == '/' || out[j+1] == '*') {
					// Comments after the comma are stripped on a later pass of
					// the outer loop; conservatively skip line comments here.
					if out[j+1] == '/' {
						for j < len(out) && out[j] != '\n' {
							j++
						}
					} else {
						j += 2
						for j+1 < len(out) && !(out[j] == '*' && out[j+1] == '/') {
							j++
						}
						j += 2
					}
					continue
				}
				break
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				out[i] = ' '
			}
		}
	}
	return out
}

// Unmarshal parses JSONC into v.
func Unmarshal(src []byte, v any) error {
	if err := json.Unmarshal(Strip(src), v); err != nil {
		return fmt.Errorf("jsonc: %w", err)
	}
	return nil
}
