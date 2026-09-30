package manifestmerge

// tree.go — a JSONC document as offsets into its own bytes: every object
// member and array element with where its key, value and comma are, so a
// merge can rewrite one value, drop one entry or add one and keep every
// other byte (comments, order, spacing) as the file has it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// node is one JSON value in a document: src[start:end].
type node struct {
	start, end int
	kind       byte // '{', '[', or 0 for a scalar
	items      []item
}

// item is an object member (key set) or an array element.
type item struct {
	key   string
	start int // the key's quote, or the element's first byte
	val   *node
	comma int // the comma after the value (a trailing one included), -1 when none
}

// doc is a parsed JSONC document.
type doc struct {
	src  []byte // as written
	flat []byte // jsonc.Strip(src): same offsets, comments and trailing commas blanked
	root *node
}

var errNotJSON = errors.New("not a JSONC document")

func parse(src []byte) (*doc, error) {
	flat := jsonc.Strip(src)
	if !json.Valid(flat) {
		return nil, errNotJSON
	}
	s := &scanner{src: src}
	s.space()
	root, err := s.value()
	if err != nil {
		return nil, err
	}
	return &doc{src: src, flat: flat, root: root}, nil
}

// scanner walks valid JSONC over the original bytes: blanks and comments
// are skipped, a trailing comma is a comma.
type scanner struct {
	src []byte
	i   int
}

func (s *scanner) space() {
	for s.i < len(s.src) {
		switch c := s.src[s.i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.i++
		case c == '/' && s.i+1 < len(s.src) && s.src[s.i+1] == '/':
			for s.i < len(s.src) && s.src[s.i] != '\n' {
				s.i++
			}
		case c == '/' && s.i+1 < len(s.src) && s.src[s.i+1] == '*':
			end := bytes.Index(s.src[s.i+2:], []byte("*/"))
			if end < 0 {
				s.i = len(s.src)
				return
			}
			s.i += 2 + end + 2
		default:
			return
		}
	}
}

// str is the offset just past the string starting at s.i.
func (s *scanner) str() int {
	i := s.i + 1
	for i < len(s.src) {
		switch s.src[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		}
		i++
	}
	return i
}

func (s *scanner) value() (*node, error) {
	if s.i >= len(s.src) {
		return nil, errNotJSON
	}
	n := &node{start: s.i}
	switch c := s.src[s.i]; c {
	case '"':
		s.i = s.str()
	case '{', '[':
		n.kind = c
		s.i++
		for {
			s.space()
			if s.i >= len(s.src) {
				return nil, errNotJSON
			}
			if s.src[s.i] == '}' || s.src[s.i] == ']' {
				s.i++
				break
			}
			it := item{start: s.i, comma: -1}
			if c == '{' {
				end := s.str()
				if err := json.Unmarshal(s.src[s.i:end], &it.key); err != nil {
					return nil, errNotJSON
				}
				s.i = end
				s.space()
				s.i++ // ':'
				s.space()
			}
			v, err := s.value()
			if err != nil {
				return nil, err
			}
			it.val = v
			s.space()
			if s.i < len(s.src) && s.src[s.i] == ',' {
				it.comma = s.i
				s.i++
			}
			n.items = append(n.items, it)
		}
	default:
		for s.i < len(s.src) && !strings.ContainsRune(",}] \t\r\n/", rune(s.src[s.i])) {
			s.i++
		}
	}
	n.end = s.i
	return n, nil
}

// canon is n's value as canonical JSON (object keys sorted); "" for nil.
func (d *doc) canon(n *node) string {
	if n == nil {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(d.flat[n.start:n.end]))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return string(bytes.TrimSpace(d.flat[n.start:n.end]))
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// lineStart is the offset of the start of the line holding i.
func lineStart(src []byte, i int) int {
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
}

// indentAt is the blanks before i on its line, and whether only blanks are
// there (i starts its line).
func indentAt(src []byte, i int) (string, bool) {
	ls := lineStart(src, i)
	ind := string(src[ls:i])
	return ind, strings.TrimLeft(ind, " \t") == ""
}

// restOfLine is the offset just past the newline ending the line at i when
// nothing but blanks or a line comment follows i on it, and -1 otherwise.
func restOfLine(src []byte, i int) int {
	for i < len(src) {
		switch c := src[i]; {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			return i + 1
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		default:
			return -1
		}
	}
	return len(src)
}

// normalized reports whether src is exactly its own two-space re-indent —
// the form json.MarshalIndent writes, which instantiation wrote before
// comments were kept.
func normalized(src []byte) bool {
	c, err := json.Marshal(json.RawMessage(src))
	if err != nil {
		return false
	}
	var buf bytes.Buffer
	if json.Indent(&buf, c, "", "  ") != nil {
		return false
	}
	buf.WriteByte('\n')
	return bytes.Equal(buf.Bytes(), src)
}
