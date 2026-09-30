package broker

// templaterepo_driver_cfg.go — reading an instance repository's config and
// attributes the way git does, as far as the manifest's merge driver needs
// (templaterepo_driver.go): xbind never runs git on the builder's data to
// ask (D78), so it parses the files it read through fsutil.OpenBeneath.

import (
	"bufio"
	"bytes"
	"path"
	"strings"
)

// parseGitConfig is a git config file's values by name — the section and
// key lower-cased, a subsection as written (`merge.xbin-manifest.driver`),
// the last of repeated ones — each value unquoted and unescaped as git
// reads it; a key without "=" is "true". Includes aren't followed: xbind
// asks only for keys it writes itself and the `template` remote.
func parseGitConfig(src []byte) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || ln[0] == '#' || ln[0] == ';' {
			continue
		}
		if ln[0] == '[' {
			end := strings.LastIndexByte(ln, ']')
			if end < 0 {
				section = ""
				continue
			}
			hdr := ln[1:end]
			if name, sub, ok := strings.Cut(hdr, " "); ok { // [remote "template"]
				sub = strings.TrimSpace(sub)
				if len(sub) >= 2 && sub[0] == '"' && sub[len(sub)-1] == '"' {
					sub = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(sub[1 : len(sub)-1])
				}
				section = strings.ToLower(name) + "." + sub
			} else { // [section] or the old [section.sub] (sub lower-cased)
				section = strings.ToLower(hdr)
			}
			continue
		}
		k, v, has := strings.Cut(ln, "=")
		key := section + "." + strings.ToLower(strings.TrimSpace(k))
		if !has {
			out[key] = "true"
			continue
		}
		out[key] = gitConfigValue(v)
	}
	return out
}

// gitConfigValue is a config value as git reads it: blanks around it
// dropped, "…" quoting removed, \" \\ \n \t \b unescaped, a # or ; outside
// quotes starting a comment.
func gitConfigValue(v string) string {
	var b strings.Builder
	quoted := false
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
			quoted = !quoted
		case c == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(v[i])
			}
		case !quoted && (c == '#' || c == ';'):
			return strings.TrimRight(b.String(), " \t")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// gitTrue is whether a config value is one git reads as true.
func gitTrue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1", "":
		return true
	}
	return false
}

// hasLine reports whether a line of b is line (blanks around it aside).
func hasLine(b []byte, line string) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == line {
			return true
		}
	}
	return false
}

// setsMergeAttr reports whether an attributes file gives the repository's
// root xbin.json a merge attribute (merge, -merge, !merge, merge=…, or the
// binary macro) on a line other than skip: a merge rule of the builder's
// own, which the driver's line (in .git/info/attributes, which git reads
// last) would override.
func setsMergeAttr(attrs []byte, skip string) bool {
	sc := bufio.NewScanner(bytes.NewReader(attrs))
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || ln[0] == '#' || ln == skip || strings.HasPrefix(ln, "[attr]") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 || !rootPatternMatches(f[0], "xbin.json") {
			continue
		}
		for _, a := range f[1:] {
			if a == "binary" || strings.TrimLeft(a, "-!") == "merge" || strings.HasPrefix(a, "merge=") {
				return true
			}
		}
	}
	return false
}

// rootPatternMatches reports whether a gitattributes pattern in the
// repository's root matches the root file name: a pattern without a slash
// matches at any depth, one with a leading slash or leading **/ at the root.
// A quoted pattern counts as matching (xbind doesn't read those; it steps
// aside rather than guess).
func rootPatternMatches(pat, name string) bool {
	if strings.HasPrefix(pat, `"`) {
		return true
	}
	pat = strings.TrimPrefix(pat, "/")
	for strings.HasPrefix(pat, "**/") {
		pat = pat[3:]
	}
	if strings.Contains(pat, "/") {
		return false
	}
	ok, _ := path.Match(pat, name)
	return ok
}
