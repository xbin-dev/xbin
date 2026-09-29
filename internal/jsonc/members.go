package jsonc

// members.go — top-level member edits that keep the rest of a JSONC
// document as it is: comments, order, spacing. xbind uses them where it
// must change one manifest key without re-marshalling a builder's file (a
// builtin update keeping the installed "partition", PD-52).

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// member is one top-level "key": value in the original bytes.
type member struct {
	key                        string
	keyStart, valStart, valEnd int
	comma                      int // the comma after the value (a trailing one included), -1 when none
}

// doc is a JSONC document whose top level is an object.
type doc struct {
	open    int // the '{'
	members []member
}

var errNotObject = errors.New("jsonc: the document is not an object")

// TopLevel is the value of the top-level member key, verbatim (comments
// inside it included), and whether it is present. With duplicate keys it
// is the last one, as encoding/json reads it.
func TopLevel(src []byte, key string) ([]byte, bool, error) {
	d, err := parseDoc(src)
	if err != nil {
		return nil, false, err
	}
	var raw []byte
	found := false
	for _, m := range d.members {
		if m.key == key {
			raw, found = src[m.valStart:m.valEnd], true
		}
	}
	return raw, found, nil
}

// SetTopLevel is src with every top-level member key's value replaced by
// value (raw JSONC, written as it is), or with the member added before the
// first one when it is absent. Everything else is kept byte for byte.
func SetTopLevel(src []byte, key string, value []byte) ([]byte, error) {
	if !json.Valid(Strip(value)) {
		return nil, fmt.Errorf("jsonc: the value for %q is not JSON", key)
	}
	d, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	var out []byte
	last, found := 0, false
	for _, m := range d.members {
		if m.key == key {
			out = append(append(out, src[last:m.valStart]...), value...)
			last, found = m.valEnd, true
		}
	}
	if found {
		return append(out, src[last:]...), nil
	}
	name, _ := json.Marshal(key)
	entry := string(name) + ": " + string(value)
	if len(d.members) == 0 {
		at := d.open + 1
		return splice(src, at, at, entry), nil
	}
	first := d.members[0]
	ls := lineStart(src, first.keyStart)
	if indent := string(src[ls:first.keyStart]); strings.TrimSpace(indent) == "" {
		return splice(src, first.keyStart, first.keyStart, entry+",\n"+indent), nil
	}
	return splice(src, first.keyStart, first.keyStart, entry+", "), nil
}

// DeleteTopLevel is src without any top-level member key (unchanged when
// there is none). A member alone on its line takes the line with it;
// everything else is kept byte for byte.
func DeleteTopLevel(src []byte, key string) ([]byte, error) {
	d, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	type cut struct{ from, to int }
	var cuts []cut
	for i, m := range d.members {
		if m.key != key {
			continue
		}
		from, to := m.keyStart, m.valEnd
		if m.comma >= 0 {
			to = m.comma + 1
		} else if i > 0 && d.members[i-1].key != key {
			// The last member: its separator goes with it (a kept previous
			// member would otherwise end in a trailing comma).
			p := d.members[i-1].comma
			cuts = append(cuts, cut{p, p + 1})
		}
		// A member alone on its line (a line comment after it included)
		// takes the whole line.
		ls, le := lineStart(src, from), restOfLine(src, to)
		switch {
		case strings.TrimSpace(string(src[ls:from])) == "" && le >= 0:
			from, to = ls, le
		case m.comma >= 0: // inline: the blanks before the next member go too
			for to < len(src) && (src[to] == ' ' || src[to] == '\t') {
				to++
			}
		}
		cuts = append(cuts, cut{from, to})
	}
	if len(cuts) == 0 {
		return src, nil
	}
	var out []byte
	last := 0
	for _, c := range cuts {
		if c.from < last { // overlapping cuts (duplicates on one line)
			c.from = last
		}
		if c.to < c.from {
			continue
		}
		out = append(out, src[last:c.from]...)
		last = c.to
	}
	return append(out, src[last:]...), nil
}

func splice(src []byte, from, to int, text string) []byte {
	out := make([]byte, 0, len(src)+len(text))
	out = append(out, src[:from]...)
	out = append(out, text...)
	return append(out, src[to:]...)
}

// lineStart is the offset of the start of the line holding i.
func lineStart(src []byte, i int) int {
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
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

// parseDoc reads the top-level members of a JSONC object. The document must
// be valid JSONC (Strip gives valid JSON) and an object at the top.
func parseDoc(src []byte) (*doc, error) {
	if !json.Valid(Strip(src)) {
		return nil, errors.New("jsonc: the document doesn't parse")
	}
	s := &scanner{src: src}
	s.space()
	if s.i >= len(src) || src[s.i] != '{' {
		return nil, errNotObject
	}
	d := &doc{open: s.i}
	s.i++
	for {
		s.space()
		if s.i >= len(src) {
			return nil, errors.New("jsonc: unexpected end")
		}
		if src[s.i] == '}' {
			return d, nil
		}
		m := member{keyStart: s.i, comma: -1}
		end := s.str()
		if err := json.Unmarshal(src[m.keyStart:end], &m.key); err != nil {
			return nil, fmt.Errorf("jsonc: a key: %w", err)
		}
		s.i = end
		s.space()
		s.i++ // ':'
		s.space()
		m.valStart = s.i
		m.valEnd = s.value()
		s.i = m.valEnd
		s.space()
		if s.i < len(src) && src[s.i] == ',' {
			m.comma = s.i
			s.i++
		}
		d.members = append(d.members, m)
	}
}

// scanner walks valid JSONC by offsets into the original bytes.
type scanner struct {
	src []byte
	i   int
}

// space skips blanks and comments.
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
			end := strings.Index(string(s.src[s.i+2:]), "*/")
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

// value is the offset just past the value starting at s.i; s.i is left
// where it was.
func (s *scanner) value() int {
	start := s.i
	defer func() { s.i = start }()
	switch s.src[s.i] {
	case '"':
		return s.str()
	case '{', '[':
		depth := 0
		for s.i < len(s.src) {
			switch c := s.src[s.i]; {
			case c == '"':
				s.i = s.str()
				continue
			case c == '/' && s.i+1 < len(s.src) && (s.src[s.i+1] == '/' || s.src[s.i+1] == '*'):
				s.space()
				continue
			case c == '{' || c == '[':
				depth++
			case c == '}' || c == ']':
				depth--
				if depth == 0 {
					return s.i + 1
				}
			}
			s.i++
		}
		return s.i
	}
	i := s.i
	for i < len(s.src) && !strings.ContainsRune(",}] \t\r\n/", rune(s.src[i])) {
		i++
	}
	return i
}
