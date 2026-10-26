// docs_docx.go — Word documents (D201): a .docx read into Markdown
// (doc_read), and Markdown written as a .docx (doc_write) by our own small
// WordprocessingML writer — headings, paragraphs with bold/italic/code/links,
// bullet and numbered lists (nested), quotes, code blocks, rules, tables and
// images from the session files. Pure Go, bounded by docs_zip.go.
package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strconv"
	"strings"
	"time"
)

// --- reading ---------------------------------------------------------------

// readDocx renders a Word document's body as Markdown.
func readDocx(ctx context.Context, data []byte) (string, error) {
	p, err := openOOX(ctx, data)
	if err != nil {
		return "", err
	}
	main := "word/document.xml"
	for _, r := range p.rels("") {
		if strings.HasSuffix(r.Type, "/officeDocument") && !r.External {
			main = r.Target
		}
	}
	body, err := p.read(main)
	if err != nil {
		return "", err
	}
	styles := docxStyles(p)
	nums := docxNumbering(p)
	rels := p.rels(main)

	var out strings.Builder
	var para strings.Builder // the paragraph being read
	var pStyle, numID string
	var ilvl int
	var bold, italic bool
	var linkTarget string
	counters := map[string]int{}   // numId/ilvl → items so far
	lastList := ""                 // the list the last paragraph was an item of
	endList := func(next string) { // a list ends with a blank line
		if lastList != "" && lastList != next {
			out.WriteString("\n")
		}
		lastList = next
	}
	type cell = []string
	var tables [][][]string // nested tables: rows of cells
	var cellParas []string
	inCell := 0

	endPara := func() {
		text := strings.TrimRight(para.String(), " \t")
		para.Reset()
		if inCell > 0 {
			if text != "" {
				cellParas = append(cellParas, text)
			}
			pStyle, numID, ilvl = "", "", 0
			return
		}
		st := styles[pStyle]
		if text != "" {
			if numID != "" && numID != "0" && st.heading == 0 {
				endList(numID)
			} else {
				endList("")
			}
		}
		switch {
		case text == "":
		case st.heading > 0:
			fmt.Fprintf(&out, "%s %s\n\n", strings.Repeat("#", min(st.heading, 6)), strings.TrimSpace(text))
		case numID != "" && numID != "0":
			key := numID + "/" + strconv.Itoa(ilvl)
			counters[key]++
			// a deeper level restarts when a shallower one advances
			for k := range counters {
				if strings.HasPrefix(k, numID+"/") {
					if l, _ := strconv.Atoi(strings.TrimPrefix(k, numID+"/")); l > ilvl {
						delete(counters, k)
					}
				}
			}
			mark := "-"
			if nums.ordered(numID, ilvl) {
				mark = strconv.Itoa(counters[key]) + "."
			}
			fmt.Fprintf(&out, "%s%s %s\n", strings.Repeat("  ", ilvl), mark, strings.TrimSpace(text))
		case st.quote:
			fmt.Fprintf(&out, "> %s\n\n", strings.TrimSpace(text))
		case st.code:
			fmt.Fprintf(&out, "```\n%s\n```\n\n", text)
		default:
			out.WriteString(strings.TrimSpace(text) + "\n\n")
		}
		pStyle, numID, ilvl = "", "", 0
	}
	err = walkXML(ctx, body, func(w *xmlWalk, se xml.StartElement) error {
		switch se.Name.Local {
		case "pStyle":
			pStyle = attr(se, "val")
		case "numId":
			numID = attr(se, "val")
		case "ilvl":
			ilvl, _ = strconv.Atoi(attr(se, "val"))
			ilvl = max(0, min(ilvl, 8))
		case "b":
			if w.in("rPr") && attr(se, "val") != "0" && attr(se, "val") != "false" {
				bold = true
			}
		case "i":
			if w.in("rPr") && attr(se, "val") != "0" && attr(se, "val") != "false" {
				italic = true
			}
		case "r":
			bold, italic = false, false
		case "hyperlink":
			if r, ok := rels[relID(se)]; ok && r.External && safeLink(r.Target) {
				linkTarget = r.Target
				para.WriteString("[")
			}
		case "tab":
			if w.in("r") {
				para.WriteString("\t")
			}
		case "br", "cr":
			if w.in("r") {
				para.WriteString("  \n")
			}
		case "docPr":
			name := attr(se, "descr")
			if name == "" {
				name = attr(se, "name")
			}
			fmt.Fprintf(&para, "[image: %s]", name)
		case "tbl":
			if inCell == 0 && para.Len() > 0 {
				endPara()
			}
			tables = append(tables, nil)
		case "tr":
			if n := len(tables); n > 0 {
				tables[n-1] = append(tables[n-1], cell{})
			}
		case "tc":
			inCell++
			cellParas = nil
		}
		return nil
	}, func(w *xmlWalk, ee xml.EndElement) error {
		switch ee.Name.Local {
		case "p":
			endPara()
		case "hyperlink":
			if linkTarget != "" {
				fmt.Fprintf(&para, "](%s)", linkTarget)
				linkTarget = ""
			}
		case "tc":
			inCell--
			if n := len(tables); n > 0 && len(tables[n-1]) > 0 {
				rows := tables[n-1]
				rows[len(rows)-1] = append(rows[len(rows)-1], strings.Join(cellParas, "<br>"))
			}
			cellParas = nil
		case "tbl":
			n := len(tables)
			if n == 0 {
				return nil
			}
			t := tables[n-1]
			tables = tables[:n-1]
			if len(t) > 0 { // a header row is bold by its nature: no ** marks
				for i, c := range t[0] {
					if strings.HasPrefix(c, "**") && strings.HasSuffix(c, "**") && len(c) > 4 && !strings.Contains(c[2:len(c)-2], "**") {
						t[0][i] = c[2 : len(c)-2]
					}
				}
			}
			md := mdTable(t)
			if n > 1 || inCell > 0 { // a table inside a cell: flattened into it
				cellParas = append(cellParas, strings.ReplaceAll(strings.TrimSpace(md), "\n", "<br>"))
			} else {
				endList("")
				out.WriteString(md + "\n")
			}
		}
		return nil
	}, func(w *xmlWalk, cd []byte) error {
		if w.in("t") && w.in("r") {
			s := string(cd)
			if bold && strings.TrimSpace(s) != "" {
				s = "**" + s + "**"
			}
			if italic && strings.TrimSpace(s) != "" {
				s = "*" + s + "*"
			}
			para.WriteString(s)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return tidyMarkdown(out.String()), nil
}

// mdTable draws rows of cells (the first is the header) as a pipe table.
func mdTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	if w == 0 {
		return ""
	}
	var b strings.Builder
	line := func(r []string) {
		b.WriteString("|")
		for i := 0; i < w; i++ {
			c := ""
			if i < len(r) {
				c = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(r[i]), "\n", " "), "|", `\|`)
			}
			b.WriteString(" " + c + " |")
		}
		b.WriteString("\n")
	}
	line(rows[0])
	b.WriteString("|" + strings.Repeat(" --- |", w) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	return b.String()
}

// tidyMarkdown collapses runs of blank lines and merges **a****b**.
func tidyMarkdown(s string) string {
	s = strings.ReplaceAll(s, "****", "")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}

type docxStyle struct {
	heading     int
	quote, code bool
}

// docxStyles maps style ids to what they mean for Markdown: a heading's
// level (its name "heading N", "Title", or an outline level), a quote, code.
func docxStyles(p *ooxPkg) map[string]docxStyle {
	out := map[string]docxStyle{}
	b, err := p.read("word/styles.xml")
	if err != nil {
		return out
	}
	var id string
	var st docxStyle
	_ = walkXML(p.ctx, b, func(w *xmlWalk, se xml.StartElement) error {
		switch se.Name.Local {
		case "style":
			id, st = attr(se, "styleId"), docxStyle{}
		case "name":
			if w.in("style") {
				n := strings.ToLower(attr(se, "val"))
				switch {
				case n == "title":
					st.heading = 1
				case strings.HasPrefix(n, "heading "):
					if l, err := strconv.Atoi(strings.TrimPrefix(n, "heading ")); err == nil {
						st.heading = l
					}
				case strings.Contains(n, "quote"):
					st.quote = true
				case strings.Contains(n, "code") || n == "html preformatted":
					st.code = true
				}
			}
		case "outlineLvl":
			if w.in("style") && st.heading == 0 {
				if l, err := strconv.Atoi(attr(se, "val")); err == nil && l < 9 {
					st.heading = l + 1
				}
			}
		}
		return nil
	}, func(w *xmlWalk, ee xml.EndElement) error {
		if ee.Name.Local == "style" && id != "" {
			out[id] = st
		}
		return nil
	}, nil)
	// the conventional ids, when styles.xml is missing or terse
	for l := 1; l <= 6; l++ {
		k := "Heading" + strconv.Itoa(l)
		if _, ok := out[k]; !ok {
			out[k] = docxStyle{heading: l}
		}
	}
	if _, ok := out["Title"]; !ok {
		out["Title"] = docxStyle{heading: 1}
	}
	return out
}

// docxNums: which numbering instances count (ordered) and which are bullets.
type docxNums struct {
	abstractOf map[string]string         // numId → abstractNumId
	formats    map[string]map[int]string // abstractNumId → ilvl → numFmt
}

func (n docxNums) ordered(numID string, ilvl int) bool {
	f := n.formats[n.abstractOf[numID]][ilvl]
	return f != "" && f != "bullet" && f != "none"
}

func docxNumbering(p *ooxPkg) docxNums {
	n := docxNums{abstractOf: map[string]string{}, formats: map[string]map[int]string{}}
	b, err := p.read("word/numbering.xml")
	if err != nil {
		return n
	}
	var abs, num string
	lvl := 0
	_ = walkXML(p.ctx, b, func(w *xmlWalk, se xml.StartElement) error {
		switch se.Name.Local {
		case "abstractNum":
			abs = attr(se, "abstractNumId")
			n.formats[abs] = map[int]string{}
		case "lvl":
			lvl, _ = strconv.Atoi(attr(se, "ilvl"))
		case "numFmt":
			if w.in("abstractNum") && n.formats[abs] != nil {
				n.formats[abs][lvl] = attr(se, "val")
			}
		case "num":
			num = attr(se, "numId")
		case "abstractNumId":
			if w.in("num") {
				n.abstractOf[num] = attr(se, "val")
			}
		}
		return nil
	}, nil, nil)
	return n
}

// --- writing ---------------------------------------------------------------

// docImage is an image a document embeds: its bytes and its type.
type docImage struct {
	data []byte
	mime string
}

// docxWriter accumulates the parts of a document being written.
type docxWriter struct {
	body    strings.Builder
	rels    []string // extra relationships of document.xml
	media   map[string][]byte
	nums    []string // <w:num> instances
	images  map[string]docImage
	missing []string // images the Markdown named that weren't there
	nextID  int
}

func (d *docxWriter) rel(typ, target string, external bool) string {
	d.nextID++
	id := "rId" + strconv.Itoa(d.nextID+10)
	mode := ""
	if external {
		mode = ` TargetMode="External"`
	}
	d.rels = append(d.rels, fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/%s" Target="%s"%s/>`,
		id, typ, xmlEsc(target), mode))
	return id
}

// writeDocx writes blocks as a Word document. images are the session images
// the Markdown may name; title goes into the document's properties.
func writeDocx(blocks []mdBlock, title string, images map[string]docImage, when time.Time) ([]byte, []string, error) {
	d := &docxWriter{media: map[string][]byte{}, images: images}
	for _, b := range blocks {
		d.block(b, 0)
	}
	d.body.WriteString(`<w:p/>`) // a table may not end the body
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` +
		d.body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`
	z := newZipWriter()
	z.addString("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Default Extension="jpeg" ContentType="image/jpeg"/><Default Extension="gif" ContentType="image/gif"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`)
	z.addString("_rels/.rels", pkgRels("word/document.xml"))
	z.addString("docProps/core.xml", coreProps(title, when))
	z.addString("docProps/app.xml", appProps("xbin agent"))
	z.addString("word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>`+
		strings.Join(d.rels, "")+`</Relationships>`)
	z.addString("word/document.xml", doc)
	z.addString("word/styles.xml", docxStylesXML)
	z.addString("word/numbering.xml", docxNumberingXML(d.nums))
	for _, name := range docKeys(d.media) {
		z.add("word/media/"+name, d.media[name])
	}
	out, err := z.bytes()
	return out, d.missing, err
}

func (d *docxWriter) block(b mdBlock, depth int) {
	switch b.Kind {
	case "h":
		style := "Heading" + strconv.Itoa(min(max(b.Level, 1), 6))
		d.para(style, "", parseInline(b.Text))
	case "p":
		d.para("", "", parseInline(b.Text))
	case "quote":
		d.para("Quote", "", parseInline(b.Text))
	case "code":
		var r strings.Builder
		r.WriteString(`<w:p><w:pPr><w:pStyle w:val="CodeBlock"/></w:pPr><w:r>`)
		for i, ln := range strings.Split(b.Text, "\n") {
			if i > 0 {
				r.WriteString(`<w:br/>`)
			}
			r.WriteString(`<w:t xml:space="preserve">` + xmlEsc(ln) + `</w:t>`)
		}
		r.WriteString(`</w:r></w:p>`)
		d.body.WriteString(r.String())
	case "hr":
		d.body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="CDD0D8"/></w:pBdr></w:pPr></w:p>`)
	case "ul", "ol":
		d.list(b, 0)
	case "table":
		d.table(b)
	case "img":
		d.image(b.Src, b.Alt)
	}
}

func (d *docxWriter) list(b mdBlock, lvl int) {
	abs := 0
	if b.Kind == "ol" {
		abs = 1
	}
	numID := len(d.nums) + 1
	start := ""
	if b.Kind == "ol" {
		s := max(b.Start, 1)
		start = fmt.Sprintf(`<w:lvlOverride w:ilvl="%d"><w:startOverride w:val="%d"/></w:lvlOverride>`, lvl, s)
	}
	d.nums = append(d.nums, fmt.Sprintf(`<w:num w:numId="%d"><w:abstractNumId w:val="%d"/>%s</w:num>`, numID, abs, start))
	for _, it := range b.Items {
		num := fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, min(lvl, 8), numID)
		d.para("ListParagraph", num, parseInline(it.Text))
		if it.Sub != nil {
			d.list(*it.Sub, lvl+1)
		}
	}
}

// para writes one paragraph of runs.
func (d *docxWriter) para(style, extraPPr string, runs []mdRun) {
	d.body.WriteString(`<w:p>`)
	if style != "" || extraPPr != "" {
		d.body.WriteString(`<w:pPr>`)
		if style != "" {
			d.body.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		d.body.WriteString(extraPPr + `</w:pPr>`)
	}
	d.runs(runs)
	d.body.WriteString(`</w:p>`)
}

func (d *docxWriter) runs(runs []mdRun) {
	for i := 0; i < len(runs); i++ {
		r := runs[i]
		switch {
		case r.Break:
			d.body.WriteString(`<w:r><w:br/></w:r>`)
		case r.Image != "":
			d.inlineImage(r.Image, r.Text)
		case r.Link != "" && safeLink(r.Link):
			id := d.rel("hyperlink", r.Link, true)
			d.body.WriteString(`<w:hyperlink r:id="` + id + `">`)
			for ; i < len(runs) && runs[i].Link == r.Link; i++ {
				d.run(runs[i], "Hyperlink")
			}
			i--
			d.body.WriteString(`</w:hyperlink>`)
		default:
			d.run(r, "")
		}
	}
}

func (d *docxWriter) run(r mdRun, charStyle string) {
	var pr strings.Builder
	if charStyle != "" {
		pr.WriteString(`<w:rStyle w:val="` + charStyle + `"/>`)
	} else if r.Code {
		pr.WriteString(`<w:rStyle w:val="CodeChar"/>`)
	}
	if r.Bold {
		pr.WriteString(`<w:b/>`)
	}
	if r.Italic {
		pr.WriteString(`<w:i/>`)
	}
	d.body.WriteString(`<w:r>`)
	if pr.Len() > 0 {
		d.body.WriteString(`<w:rPr>` + pr.String() + `</w:rPr>`)
	}
	d.body.WriteString(`<w:t xml:space="preserve">` + xmlEsc(r.Text) + `</w:t></w:r>`)
}

func (d *docxWriter) table(b mdBlock) {
	cols := len(b.Header)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	const textW = 9026 // A4 minus the margins, in twentieths of a point
	cw := textW / cols
	d.body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="5000" w:type="pct"/><w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for i := 0; i < cols; i++ {
		d.body.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, cw))
	}
	d.body.WriteString(`</w:tblGrid>`)
	row := func(cells []string, head bool) {
		d.body.WriteString(`<w:tr>`)
		if head {
			d.body.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			d.body.WriteString(fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>`, cw))
			if head {
				d.body.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="E8E9EE"/>`)
			}
			d.body.WriteString(`</w:tcPr>`)
			jc := ""
			if i < len(b.Align) && b.Align[i] != "" {
				a := b.Align[i]
				if a == "left" {
					a = "start"
				} else if a == "right" {
					a = "end"
				}
				jc = `<w:jc w:val="` + a + `"/>`
			}
			runs := parseInline(strings.ReplaceAll(c, "<br>", "\n"))
			if head {
				for k := range runs {
					runs[k].Bold = true
				}
			}
			d.para("", jc, runs)
			d.body.WriteString(`</w:tc>`)
		}
		d.body.WriteString(`</w:tr>`)
	}
	row(b.Header, true)
	for _, r := range b.Rows {
		row(r, false)
	}
	d.body.WriteString(`</w:tbl>`)
}

// image writes a figure: the picture in a paragraph of its own, its alt
// text as the caption.
func (d *docxWriter) image(src, alt string) {
	d.body.WriteString(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr>`)
	d.inlineImage(src, alt)
	d.body.WriteString(`</w:p>`)
	if strings.TrimSpace(alt) != "" {
		d.para("Caption", "", []mdRun{{Text: alt}})
	}
}

// inlineImage writes a drawing run for a session image (or, when it's not
// there or not a picture, its alt text in brackets).
func (d *docxWriter) inlineImage(src, alt string) {
	img, ok := d.images[src]
	var cfg image.Config
	var format string
	var err error
	if ok {
		cfg, format, err = image.DecodeConfig(bytes.NewReader(img.data))
	}
	if !ok || err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		d.missing = append(d.missing, src)
		d.run(mdRun{Text: "[" + orStr(alt, src) + "]", Italic: true}, "")
		return
	}
	ext := map[string]string{"png": "png", "jpeg": "jpeg", "gif": "gif"}[format]
	if ext == "" {
		d.missing = append(d.missing, src)
		d.run(mdRun{Text: "[" + orStr(alt, src) + "]", Italic: true}, "")
		return
	}
	n := len(d.media) + 1
	name := fmt.Sprintf("image%d.%s", n, ext)
	d.media[name] = img.data
	id := d.rel("image", "media/"+name, false)
	// 96 dpi, at most the text width (6.27in)
	cx, cy := int64(cfg.Width)*9525, int64(cfg.Height)*9525
	if maxW := int64(5731510); cx > maxW {
		cy = cy * maxW / cx
		cx = maxW
	}
	d.body.WriteString(fmt.Sprintf(`<w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Picture %d" descr="%s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="%s"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`,
		cx, cy, n, n, xmlEsc(alt), n, xmlEsc(path.Base(src)), id, cx, cy))
}

func pkgRels(main string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="` + main + `"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`
}

func coreProps(title string, when time.Time) string {
	ts := when.UTC().Format("2006-01-02T15:04:05Z")
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>` +
		xmlEsc(title) + `</dc:title><dc:creator>xbin agent</dc:creator><dcterms:created xsi:type="dcterms:W3CDTF">` + ts +
		`</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">` + ts + `</dcterms:modified></cp:coreProperties>`
}

func appProps(app string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>` + xmlEsc(app) + `</Application></Properties>`
}

// docxNumberingXML: two abstract lists (bullets; decimal, then a., i.), and
// one instance per list the document has, so each numbered list counts from
// its own start.
func docxNumberingXML(nums []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	bullets := []string{"•", "◦", "▪"}
	b.WriteString(`<w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="hybridMultilevel"/>`)
	for l := 0; l < 9; l++ {
		fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`,
			l, bullets[l%3], 720+360*l)
	}
	b.WriteString(`</w:abstractNum><w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="hybridMultilevel"/>`)
	fmts := []string{"decimal", "lowerLetter", "lowerRoman"}
	for l := 0; l < 9; l++ {
		fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%%%d."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`,
			l, fmts[l%3], l+1, 720+360*l)
	}
	b.WriteString(`</w:abstractNum>`)
	for _, n := range nums {
		b.WriteString(n)
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

// docxStylesXML: Normal, the headings, list, quote, code, caption, the
// hyperlink and inline-code character styles and a gridded table — Base Two
// ink (#0B0C12), muted (#4B4D5C), accent (#1F3DFF), panel (#E8E9EE).
const docxStylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:eastAsia="Arial" w:cs="Arial"/><w:color w:val="0B0C12"/><w:sz w:val="22"/><w:szCs w:val="22"/><w:lang w:val="en-US"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="240"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="52"/><w:szCs w:val="52"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="360" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="280" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="30"/><w:szCs w:val="30"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="26"/><w:szCs w:val="26"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:i/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading5"><w:name w:val="heading 5"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:outlineLvl w:val="4"/></w:pPr><w:rPr><w:b/><w:color w:val="4B4D5C"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading6"><w:name w:val="heading 6"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:outlineLvl w:val="5"/></w:pPr><w:rPr><w:i/><w:color w:val="4B4D5C"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="60"/><w:contextualSpacing/></w:pPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:pBdr><w:left w:val="single" w:sz="18" w:space="8" w:color="1F3DFF"/></w:pBdr><w:ind w:left="360"/></w:pPr><w:rPr><w:i/><w:color w:val="4B4D5C"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="CodeBlock"><w:name w:val="Code Block"/><w:basedOn w:val="Normal"/><w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F2F3F6"/><w:spacing w:after="160" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New"/><w:sz w:val="19"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Caption"><w:name w:val="caption"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:jc w:val="center"/></w:pPr><w:rPr><w:i/><w:color w:val="4B4D5C"/><w:sz w:val="18"/></w:rPr></w:style>` +
	`<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:rPr><w:color w:val="1F3DFF"/><w:u w:val="single"/></w:rPr></w:style>` +
	`<w:style w:type="character" w:styleId="CodeChar"><w:name w:val="Code Char"/><w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New"/><w:shd w:val="clear" w:color="auto" w:fill="F2F3F6"/></w:rPr></w:style>` +
	`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/><w:left w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/><w:right w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="CDD0D8"/></w:tblBorders><w:tblCellMar><w:left w:w="100" w:type="dxa"/><w:right w:w="100" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>` +
	`</w:styles>`
