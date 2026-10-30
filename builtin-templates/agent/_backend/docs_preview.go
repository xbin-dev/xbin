// docs_preview.go — what a written document looks like, as the static HTML
// snapshot the render pane shows (D201): a .docx from the same Markdown
// blocks the writer drew, a deck as 16:9 slides. The pane runs no script and
// loads nothing (its CSP allows only inline styles and data: images), so the
// pictures a document embeds ride in as data: URIs, at most a few MB.
package main

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"
)

const previewImageBudget = 1536 << 10 // data: bytes one preview may embed

func previewHead(title, css string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title><style>
body{margin:0;padding:24px;background:#E8E9EE;color:#0B0C12;font:15px/1.55 Arial,Helvetica,sans-serif}
.page{background:#fff;max-width:800px;margin:0 auto;padding:56px 64px;box-shadow:0 1px 3px rgba(0,0,0,.15)}
h1,h2,h3,h4,h5,h6{line-height:1.25;margin:1.2em 0 .45em}
h1{font-size:28px}h2{font-size:22px}h3{font-size:18px}h4,h5,h6{font-size:15px}
p{margin:0 0 .8em}
a{color:#1F3DFF}
code{font:13px "Courier New",monospace;background:#F2F3F6;padding:0 3px}
pre{font:13px/1.4 "Courier New",monospace;background:#F2F3F6;padding:10px 12px;white-space:pre-wrap}
blockquote{margin:0 0 .8em;padding:2px 14px;border-left:4px solid #1F3DFF;color:#4B4D5C;font-style:italic}
hr{border:0;border-top:1px solid #CDD0D8;margin:1.2em 0}
table{border-collapse:collapse;margin:0 0 1em;width:100%}
th,td{border:1px solid #CDD0D8;padding:4px 8px;text-align:left;vertical-align:top}
th{background:#E8E9EE}
figure{margin:1em 0;text-align:center}figure img{max-width:100%}
figcaption{color:#4B4D5C;font-size:13px;font-style:italic}
.missing{color:#4B4D5C;font-style:italic}
` + css + `</style></head><body>`
}

// previewImages turns session images into data: URIs within the budget.
type previewImages struct {
	images map[string]docImage
	left   int
}

func (p *previewImages) uri(src string) string {
	img, ok := p.images[src]
	if !ok || len(img.data) > p.left || !visionMimes[img.mime] {
		return ""
	}
	p.left -= len(img.data)
	return "data:" + img.mime + ";base64," + base64.StdEncoding.EncodeToString(img.data)
}

// mdHTML draws blocks as HTML.
func mdHTML(blocks []mdBlock, imgs *previewImages) string {
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Kind {
		case "h":
			l := min(max(bl.Level, 1), 6)
			fmt.Fprintf(&b, "<h%d>%s</h%d>", l, inlineHTML(bl.Text, imgs), l)
		case "p":
			b.WriteString("<p>" + inlineHTML(bl.Text, imgs) + "</p>")
		case "quote":
			b.WriteString("<blockquote><p>" + inlineHTML(bl.Text, imgs) + "</p></blockquote>")
		case "code":
			b.WriteString("<pre><code>" + html.EscapeString(bl.Text) + "</code></pre>")
		case "hr":
			b.WriteString("<hr>")
		case "ul", "ol":
			b.WriteString(listHTML(bl, imgs))
		case "table":
			b.WriteString("<table><thead><tr>")
			for i, h := range bl.Header {
				b.WriteString("<th" + alignAttr(bl.Align, i) + ">" + inlineHTML(h, imgs) + "</th>")
			}
			b.WriteString("</tr></thead><tbody>")
			for _, r := range bl.Rows {
				b.WriteString("<tr>")
				for i := 0; i < max(len(bl.Header), len(r)); i++ {
					c := ""
					if i < len(r) {
						c = r[i]
					}
					b.WriteString("<td" + alignAttr(bl.Align, i) + ">" + inlineHTML(strings.ReplaceAll(c, "<br>", "\n"), imgs) + "</td>")
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</tbody></table>")
		case "img":
			b.WriteString("<figure>" + imgHTML(bl.Src, bl.Alt, imgs))
			if bl.Alt != "" {
				b.WriteString("<figcaption>" + html.EscapeString(bl.Alt) + "</figcaption>")
			}
			b.WriteString("</figure>")
		}
	}
	return b.String()
}

func alignAttr(al []string, i int) string {
	if i < len(al) && al[i] != "" {
		return ` style="text-align:` + al[i] + `"`
	}
	return ""
}

func listHTML(bl mdBlock, imgs *previewImages) string {
	tag := "ul"
	start := ""
	if bl.Kind == "ol" {
		tag = "ol"
		if bl.Start > 1 {
			start = fmt.Sprintf(` start="%d"`, bl.Start)
		}
	}
	var b strings.Builder
	b.WriteString("<" + tag + start + ">")
	for _, it := range bl.Items {
		b.WriteString("<li>" + inlineHTML(it.Text, imgs))
		if it.Sub != nil {
			b.WriteString(listHTML(*it.Sub, imgs))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

func imgHTML(src, alt string, imgs *previewImages) string {
	if u := imgs.uri(src); u != "" {
		return `<img src="` + u + `" alt="` + html.EscapeString(alt) + `">`
	}
	return `<span class="missing">[image ` + html.EscapeString(orStr(alt, src)) + `]</span>`
}

func inlineHTML(s string, imgs *previewImages) string {
	var b strings.Builder
	for _, r := range parseInline(s) {
		if r.Break {
			b.WriteString("<br>")
			continue
		}
		if r.Image != "" {
			b.WriteString(imgHTML(r.Image, r.Text, imgs))
			continue
		}
		t := html.EscapeString(r.Text)
		if r.Code {
			t = "<code>" + t + "</code>"
		}
		if r.Italic {
			t = "<em>" + t + "</em>"
		}
		if r.Bold {
			t = "<strong>" + t + "</strong>"
		}
		// a link to a session file stays in the preview, which offers it as a
		// download (D207); the document itself carries web links only
		if r.Link != "" && (safeLink(r.Link) || sessionLinkHref(r.Link)) {
			t = `<a href="` + html.EscapeString(r.Link) + `">` + t + `</a>`
		}
		b.WriteString(t)
	}
	return b.String()
}

// docxPreviewHTML: the document as a page.
func docxPreviewHTML(blocks []mdBlock, title string, images map[string]docImage) string {
	imgs := &previewImages{images: images, left: previewImageBudget}
	return previewHead(title, "") + `<div class="page">` + mdHTML(blocks, imgs) + `</div></body></html>`
}

// deckPreviewHTML: each slide as a 16:9 card, in the deck's colours.
func deckPreviewHTML(d deckSpec, images map[string]docImage) string {
	c := d.Theme.colors()
	imgs := &previewImages{images: images, left: previewImageBudget}
	css := fmt.Sprintf(`
.slide{position:relative;aspect-ratio:16/9;max-width:960px;margin:0 auto 22px;background:#%[1]s;color:#%[2]s;box-shadow:0 1px 3px rgba(0,0,0,.2);padding:4.5%% 5%%;box-sizing:border-box;overflow:hidden;font-family:%[5]s,Arial,sans-serif}
.slide .n{position:absolute;right:14px;bottom:8px;font-size:11px;color:#%[3]s}
.slide h2{margin:0 0 6px;font-size:30px}
.slide .rule{width:72px;height:4px;background:#%[4]s;margin:0 0 22px}
.slide ul{margin:0;padding-left:1.2em;font-size:20px;line-height:1.45}
.slide li::marker{color:#%[4]s}
.slide .sub{color:#%[3]s;font-size:20px}
.slide.title{display:flex;flex-direction:column;justify-content:center}
.slide.title h2{font-size:44px}
.slide.section{background:#%[4]s;color:#fff;display:flex;flex-direction:column;justify-content:center;text-align:center}
.slide.section h2{font-size:40px}
.cols{display:flex;gap:4%%}.cols>div{flex:1;min-width:0}
.slide table{font-size:14px;width:100%%;color:#%[2]s}
.slide th{background:#%[4]s;color:#fff;border-color:#%[4]s}
.slide img{max-width:100%%;max-height:62%%;display:block;margin:0 auto;object-fit:contain}
.cap{text-align:center;color:#%[3]s;font-size:14px;margin-top:8px}
.notes{max-width:960px;margin:-14px auto 26px;color:#4B4D5C;font-size:13px}`, c.bg, c.ink, c.muted, c.accent, c.font)
	var b strings.Builder
	b.WriteString(previewHead(orStr(d.Title, "Slides"), css))
	bul := func(items []any) string {
		var walk func([]any) string
		walk = func(items []any) string {
			var s strings.Builder
			s.WriteString("<ul>")
			for i := 0; i < len(items); i++ {
				switch v := items[i].(type) {
				case string:
					s.WriteString("<li>" + inlineHTML(v, imgs))
					if i+1 < len(items) {
						if sub, ok := items[i+1].([]any); ok {
							s.WriteString(walk(sub))
							i++
						}
					}
					s.WriteString("</li>")
				case []any:
					s.WriteString("<li style=\"list-style:none\">" + walk(v) + "</li>")
				case map[string]any:
					t, _ := v["text"].(string)
					s.WriteString("<li>" + inlineHTML(t, imgs))
					if sub, ok := v["sub"].([]any); ok {
						s.WriteString(walk(sub))
					}
					s.WriteString("</li>")
				default:
					s.WriteString("<li>" + html.EscapeString(fmt.Sprint(v)) + "</li>")
				}
			}
			s.WriteString("</ul>")
			return s.String()
		}
		return walk(items)
	}
	for i, s := range d.Slides {
		layout := strings.ToLower(s.Layout)
		cls := "slide"
		if layout == "title" || layout == "section" {
			cls += " " + layout
		}
		b.WriteString(`<div class="` + cls + `">`)
		b.WriteString("<h2>" + inlineHTML(s.Title, imgs) + "</h2>")
		switch layout {
		case "title", "section":
			if layout == "title" {
				b.WriteString(`<div class="rule"></div>`)
			}
			if s.Subtitle != "" {
				b.WriteString(`<div class="sub">` + inlineHTML(s.Subtitle, imgs) + `</div>`)
			}
		case "two":
			b.WriteString(`<div class="rule"></div><div class="cols"><div>` + bul(s.Left) + `</div><div>` + bul(s.Right) + `</div></div>`)
		case "image":
			b.WriteString(`<div class="rule"></div>` + imgHTML(s.Image, s.Caption, imgs))
			if s.Caption != "" {
				b.WriteString(`<div class="cap">` + inlineHTML(s.Caption, imgs) + `</div>`)
			}
		default:
			b.WriteString(`<div class="rule"></div>`)
			side := s.Table != nil || s.Image != ""
			if side && len(s.Bullets) > 0 {
				b.WriteString(`<div class="cols"><div style="flex:5">` + bul(s.Bullets) + `</div><div style="flex:7">`)
			} else if len(s.Bullets) > 0 {
				b.WriteString(bul(s.Bullets))
			}
			if s.Table != nil {
				b.WriteString(mdHTML([]mdBlock{{Kind: "table", Header: s.Table.Header, Rows: s.Table.Rows}}, imgs))
			} else if s.Image != "" {
				b.WriteString(imgHTML(s.Image, "", imgs))
			}
			if side && len(s.Bullets) > 0 {
				b.WriteString(`</div></div>`)
			}
		}
		fmt.Fprintf(&b, `<span class="n">%d</span></div>`, i+1)
		if strings.TrimSpace(s.Notes) != "" {
			b.WriteString(`<div class="notes"><b>Notes:</b> ` + html.EscapeString(s.Notes) + `</div>`)
		}
	}
	b.WriteString("</body></html>")
	return b.String()
}
