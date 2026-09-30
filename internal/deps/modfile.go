package deps

// modfile.go — the little of go.mod and go.work syntax a Go tile's build
// workspace needs (buildwork.go): directives, factored blocks, comments and
// quoted strings. It never fails: a line it can't make sense of is skipped,
// and what the go command makes of the file is the go command's to report.

import (
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// modFileMax caps what is read of one go.mod or go.work.
const modFileMax = 1 << 20

// modLine is one directive: `require example.com/m v1.2.0` is
// {"require", ["example.com/m", "v1.2.0"]}, and so is each line of a
// factored `require ( … )` block.
type modLine struct {
	verb string
	args []string
}

// parseModLines splits a go.mod or go.work into its directives.
func parseModLines(data []byte) []modLine {
	var out []modLine
	block := ""
	for _, raw := range strings.Split(string(data), "\n") {
		toks := modTokens(raw)
		if len(toks) == 0 {
			continue
		}
		if block != "" {
			if toks[0] == ")" {
				block = ""
				continue
			}
			out = append(out, modLine{block, toks})
			continue
		}
		verb, rest := toks[0], toks[1:]
		if len(rest) == 0 || rest[0] != "(" {
			out = append(out, modLine{verb, rest})
			continue
		}
		inner := rest[1:]
		if n := len(inner); n > 0 && inner[n-1] == ")" { // `use ( ./a ./b )` on one line
			inner = inner[:n-1]
			if verb == "use" {
				for _, t := range inner {
					out = append(out, modLine{verb, []string{t}})
				}
			} else if len(inner) > 0 {
				out = append(out, modLine{verb, inner})
			}
			continue
		}
		if len(inner) > 0 {
			out = append(out, modLine{verb, inner})
		}
		block = verb
	}
	return out
}

// modTokens splits one line into its tokens: `//` starts a comment, "…"
// and `…` are strings (unquoted here), and "(", ")" and "=>" stand alone.
func modTokens(line string) []string {
	var toks []string
	s := line
	for {
		s = strings.TrimLeft(s, " \t\r\f\v")
		switch {
		case s == "", strings.HasPrefix(s, "//"):
			return toks
		case s[0] == '(' || s[0] == ')':
			toks = append(toks, s[:1])
			s = s[1:]
		case strings.HasPrefix(s, "=>"):
			toks = append(toks, "=>")
			s = s[2:]
		case s[0] == '"':
			end := 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				return toks // an unterminated string ends the line
			}
			if u, err := strconv.Unquote(s[:end+1]); err == nil {
				toks = append(toks, u)
			}
			s = s[end+1:]
		case s[0] == '`':
			end := strings.IndexByte(s[1:], '`')
			if end < 0 {
				return toks
			}
			toks = append(toks, s[1:end+1])
			s = s[end+2:]
		default:
			end := 0
			for end < len(s) && !strings.ContainsRune(" \t\r\f\v\"`()", rune(s[end])) &&
				!strings.HasPrefix(s[end:], "//") && !strings.HasPrefix(s[end:], "=>") {
				end++
			}
			toks = append(toks, s[:end])
			s = s[end:]
		}
	}
}

// modToken writes a token back as go.mod syntax, quoted when it has to be:
// as golang.org/x/mod/modfile.MustQuote says (space and quotes, the lexer's
// punctuation — ( ) [ ] { } , — anything unprintable, a comment's start),
// and "=>".
func modToken(s string) string {
	quote := s == "" || strings.Contains(s, "//") || strings.Contains(s, "/*") || strings.Contains(s, "=>")
	for _, r := range s {
		switch {
		case strings.ContainsRune(" \"'`()[]{},", r), !unicode.IsPrint(r):
			quote = true
		}
	}
	if quote {
		return strconv.Quote(s)
	}
	return s
}

// replaceDirective is one `replace old [v] => new [v]`: New is a module
// path when NewVersion is set, else a directory.
type replaceDirective struct {
	Old, OldVersion string
	New, NewVersion string
}

// dir reports whether the replacement is a directory (no version).
func (r replaceDirective) dir() bool { return r.NewVersion == "" }

func (r replaceDirective) render(newPath string) string {
	var sb strings.Builder
	sb.WriteString("replace " + modToken(r.Old))
	if r.OldVersion != "" {
		sb.WriteString(" " + modToken(r.OldVersion))
	}
	sb.WriteString(" => " + modToken(newPath))
	if r.NewVersion != "" {
		sb.WriteString(" " + modToken(r.NewVersion))
	}
	return sb.String()
}

// parseReplace reads a replace directive's arguments; ok=false when they
// aren't one.
func parseReplace(args []string) (replaceDirective, bool) {
	i := -1
	for j, a := range args {
		if a == "=>" {
			i = j
			break
		}
	}
	if i < 1 || i > 2 || len(args)-i-1 < 1 || len(args)-i-1 > 2 {
		return replaceDirective{}, false
	}
	r := replaceDirective{Old: args[0], New: args[i+1]}
	if i == 2 {
		r.OldVersion = args[1]
	}
	if len(args)-i-1 == 2 {
		r.NewVersion = args[i+2]
	}
	return r, true
}

// goMod is what a build's workspace needs of a go.mod.
type goMod struct {
	Path     string            // the module directive's path; "" = none
	Go       string            // the go line's version; "" = none
	Requires map[string]string // module path → version
	Replaces []replaceDirective
}

// replaced reports whether the go.mod replaces module p at version v: a
// replace of p at every version, or at v.
func (m goMod) replaced(p, v string) bool {
	for _, r := range m.Replaces {
		if r.Old == p && (r.OldVersion == "" || r.OldVersion == v) {
			return true
		}
	}
	return false
}

func parseGoMod(data []byte) goMod {
	m := goMod{Requires: map[string]string{}}
	for _, l := range parseModLines(data) {
		switch l.verb {
		case "module":
			if len(l.args) >= 1 && m.Path == "" {
				m.Path = l.args[0]
			}
		case "go":
			if len(l.args) == 1 && m.Go == "" {
				m.Go = l.args[0]
			}
		case "require":
			if len(l.args) >= 2 {
				m.Requires[l.args[0]] = l.args[1]
			}
		case "replace":
			if r, ok := parseReplace(l.args); ok {
				m.Replaces = append(m.Replaces, r)
			}
		}
	}
	return m
}

// readCapped reads at most max bytes of f; ok=false for a larger file or
// one that isn't regular.
func readCapped(f *os.File, max int64) ([]byte, bool) {
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, false
	}
	return b, true
}
