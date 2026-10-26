package registry

import (
	"regexp"
	"strings"
	"unicode"
)

// The caps on what a tile calls itself (D194) — the xbin app's for
// xbin.native.meta, so a manifest's title shows the same everywhere.
const (
	MaxTitleRunes = 80
	MaxIconLen    = 40
)

var iconName = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)

// DisplayTitle is the manifest's title as the shell may show it: control
// and bidi-override characters out, whitespace collapsed, at most
// MaxTitleRunes runes; "" when it has none.
func (m Manifest) DisplayTitle() string {
	var b strings.Builder
	n, space := 0, false
	for _, r := range m.Title {
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			continue
		}
		if n >= MaxTitleRunes {
			break
		}
		if space {
			if n+1 >= MaxTitleRunes {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// DisplayIcon is the manifest's icon when it is an icon name (lowercase
// words joined by '-', at most MaxIconLen); "" otherwise. Whether the
// workspace's icon set has it is the client's to say.
func (m Manifest) DisplayIcon() string {
	if len(m.Icon) > MaxIconLen || !iconName.MatchString(m.Icon) {
		return ""
	}
	return m.Icon
}
