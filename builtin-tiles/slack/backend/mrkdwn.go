package main

import (
	"regexp"
	"strings"
)

// maxMessage is where a long reply is split: Slack takes far more, but a
// wall of text reads better as several messages.
const maxMessage = 3500

var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var (
	reHeading = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	reBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+`)
	reLink    = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	reBold    = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reItalic  = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*?)\*([^*\w]|$)`)
	reStrike  = regexp.MustCompile(`~~(.+?)~~`)
)

// toMrkdwn turns the agent's Markdown into Slack's mrkdwn: *bold*, _italic_,
// ~strike~, <url|text> links, • bullets, headings as bold lines, code as is
// (fences lose their language). &, < and > are escaped everywhere, so text
// can never become a mention or a link by accident.
func toMrkdwn(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	inCode := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode
			lines[i] = "```"
			continue
		}
		if inCode {
			lines[i] = slackEscape.Replace(l)
			continue
		}
		quote := ""
		if strings.HasPrefix(l, "> ") || l == ">" {
			quote, l = ">", strings.TrimPrefix(l, ">")
		}
		if m := reHeading.FindStringSubmatch(l); m != nil {
			lines[i] = quote + "*" + inline(m[1]) + "*"
			continue
		}
		if m := reBullet.FindStringSubmatch(l); m != nil {
			l = m[1] + "• " + l[len(m[0]):]
		}
		lines[i] = quote + inline(l)
	}
	return strings.Join(lines, "\n")
}

// inline converts one line's inline Markdown, leaving `code` alone.
func inline(s string) string {
	parts := strings.Split(s, "`")
	for i := range parts {
		p := slackEscape.Replace(parts[i])
		if i%2 == 1 && i < len(parts)-1 {
			parts[i] = p // inside a code span
			continue
		}
		p = reLink.ReplaceAllStringFunc(p, func(m string) string {
			sm := reLink.FindStringSubmatch(m)
			if sm[1] == "" || sm[1] == sm[2] {
				return "<" + sm[2] + ">"
			}
			return "<" + sm[2] + "|" + sm[1] + ">"
		})
		p = reBold.ReplaceAllString(p, "\x00$1$2\x00")
		p = reItalic.ReplaceAllString(p, "${1}_${2}_${3}")
		p = strings.ReplaceAll(p, "\x00", "*")
		parts[i] = reStrike.ReplaceAllString(p, "~$1~")
	}
	return strings.Join(parts, "`")
}

// splitMessage cuts text into pieces of at most max bytes at line breaks
// (mid-line only for a line longer than max); a piece that ends inside a code
// block closes it and the next reopens it.
func splitMessage(text string, max int) []string {
	if len(text) <= max {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	inCode := false
	flush := func() {
		s := cur.String()
		if inCode {
			s += "\n```"
		}
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimRight(s, "\n"))
		}
		cur.Reset()
		if inCode {
			cur.WriteString("```\n")
		}
	}
	for _, line := range strings.Split(text, "\n") {
		for len(line) > max-8 { // a line longer than a piece
			cut := max - 8
			for cut > 0 && !utf8Start(line[cut]) {
				cut--
			}
			if cur.Len() > 0 {
				flush()
			}
			cur.WriteString(line[:cut])
			flush()
			line = line[cut:]
		}
		if cur.Len()+len(line)+1 > max-4 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
	}
	inCode = false
	flush()
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

var reAngle = regexp.MustCompile(`<([^<>]+)>`)

// fromSlack turns Slack's message markup into plain text for the agent:
// <@U1> → @name (the bot's own mention is dropped), <#C1|general> →
// #general, <url|label> → label (url), <!here> → @here; entities unescaped.
func fromSlack(s, bot string, name func(string) string) string {
	s = reAngle.ReplaceAllStringFunc(s, func(m string) string {
		target, label, _ := strings.Cut(m[1:len(m)-1], "|")
		switch {
		case strings.HasPrefix(target, "@"):
			id := target[1:]
			if id == bot {
				return ""
			}
			if label != "" {
				return "@" + strings.TrimPrefix(label, "@")
			}
			if n := name(id); n != "" {
				return "@" + n
			}
			return "@" + id
		case strings.HasPrefix(target, "#"):
			return "#" + orStr(label, target[1:])
		case strings.HasPrefix(target, "!"):
			if label != "" {
				return label
			}
			return "@" + strings.SplitN(target[1:], "^", 2)[0]
		}
		if label != "" && label != target {
			return label + " (" + target + ")"
		}
		return strings.TrimPrefix(target, "mailto:")
	})
	s = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
	return strings.TrimSpace(strings.ReplaceAll(s, "  ", " "))
}
