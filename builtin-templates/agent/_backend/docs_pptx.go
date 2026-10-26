// docs_pptx.go — slide decks (D201): a .pptx read into Markdown, a slide
// per section (doc_read), and a deck written as a .pptx (slides_write) by
// our own small PresentationML writer. A written deck is 16:9 in Base Two
// (ink #0B0C12, accent #1F3DFF, panel #E8E9EE) unless the deck's theme
// says otherwise, its slides from five layouts — title, content (bullets,
// optionally a table or an image beside them), two (columns), image and
// section — with speaker notes. Every shape is placed explicitly, so the
// file looks the same in PowerPoint, Keynote, LibreOffice and Google Slides.
package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- reading ---------------------------------------------------------------

// readPptx renders a deck as Markdown: "## Slide N: title", then its text
// (bullets keep their level), tables, picture names and speaker notes.
// slides limits it to some (1-based; nil: all).
func readPptx(ctx context.Context, data []byte, want func(int) bool) (string, int, error) {
	p, err := openOOX(ctx, data)
	if err != nil {
		return "", 0, err
	}
	pres := "ppt/presentation.xml"
	for _, r := range p.rels("") {
		if strings.HasSuffix(r.Type, "/officeDocument") && !r.External {
			pres = r.Target
		}
	}
	pb, err := p.read(pres)
	if err != nil {
		return "", 0, err
	}
	prels := p.rels(pres)
	var order []string
	_ = walkXML(ctx, pb, func(w *xmlWalk, se xml.StartElement) error {
		if se.Name.Local == "sldId" {
			if r, ok := prels[relID(se)]; ok {
				order = append(order, r.Target)
			}
		}
		return nil
	}, nil, nil)
	var out strings.Builder
	for i, part := range order {
		if want != nil && !want(i+1) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		sb, err := p.read(part)
		if err != nil {
			fmt.Fprintf(&out, "## Slide %d\n\n(unreadable: %v)\n\n", i+1, err)
			continue
		}
		title, body := slideText(ctx, sb, false)
		if title != "" {
			fmt.Fprintf(&out, "## Slide %d: %s\n\n", i+1, title)
		} else {
			fmt.Fprintf(&out, "## Slide %d\n\n", i+1)
		}
		if body != "" {
			out.WriteString(body + "\n\n")
		}
		for _, r := range p.rels(part) {
			if strings.HasSuffix(r.Type, "/notesSlide") && !r.External {
				if nb, err := p.read(r.Target); err == nil {
					if _, notes := slideText(ctx, nb, true); strings.TrimSpace(notes) != "" {
						out.WriteString("Notes: " + strings.ReplaceAll(strings.TrimSpace(notes), "\n", " ") + "\n\n")
					}
				}
			}
		}
	}
	return tidyMarkdown(out.String()), len(order), nil
}

// slideText reads one slide's (or notes page's) shapes: the title
// placeholder's text, and everything else as Markdown lines. notes keeps
// only the notes body.
func slideText(ctx context.Context, b []byte, notes bool) (title, body string) {
	var lines []string
	var para strings.Builder
	var lvl int
	var bullet bool
	var ph string
	var titleParts []string
	var rows [][]string
	var cell strings.Builder
	inTable := false
	_ = walkXML(ctx, b, func(w *xmlWalk, se xml.StartElement) error {
		switch se.Name.Local {
		case "sp":
			ph = ""
		case "ph":
			ph = attr(se, "type")
			if ph == "" {
				ph = "body"
			}
		case "pPr":
			lvl, _ = strconv.Atoi(attr(se, "lvl"))
			lvl = max(0, min(lvl, 8))
		case "buChar", "buAutoNum":
			bullet = true
		case "br":
			para.WriteString(" ")
		case "tbl":
			inTable, rows = true, nil
		case "tr":
			rows = append(rows, nil)
		case "tc":
			cell.Reset()
		case "cNvPr":
			if w.in("pic") && !notes {
				name := attr(se, "descr")
				if name == "" {
					name = attr(se, "name")
				}
				lines = append(lines, "[image: "+name+"]")
			}
		case "graphicData":
			if strings.HasSuffix(attr(se, "uri"), "/chart") && !notes {
				lines = append(lines, "[chart]")
			}
		}
		return nil
	}, func(w *xmlWalk, ee xml.EndElement) error {
		switch ee.Name.Local {
		case "p":
			t := strings.TrimSpace(para.String())
			para.Reset()
			if inTable {
				if cell.Len() > 0 && t != "" {
					cell.WriteString(" ")
				}
				cell.WriteString(t)
			} else if t != "" {
				switch {
				case notes:
					if ph == "body" {
						lines = append(lines, t)
					}
				case ph == "title" || ph == "ctrTitle":
					titleParts = append(titleParts, t)
				case bullet || lvl > 0 || ph == "body" || ph == "obj":
					lines = append(lines, strings.Repeat("  ", lvl)+"- "+t)
				default:
					lines = append(lines, t)
				}
			}
			lvl, bullet = 0, false
		case "tc":
			if n := len(rows); n > 0 {
				rows[n-1] = append(rows[n-1], cell.String())
			}
		case "tbl":
			inTable = false
			if !notes {
				lines = append(lines, strings.TrimSpace(mdTable(rows)))
			}
		case "sp":
			ph = ""
		}
		return nil
	}, func(w *xmlWalk, cd []byte) error {
		if w.in("t") && (w.in("r") || w.in("fld")) {
			para.Write(cd)
		}
		return nil
	})
	return strings.Join(titleParts, " — "), strings.Join(lines, "\n")
}

// --- writing ---------------------------------------------------------------

// deckSpec is what slides_write takes.
type deckSpec struct {
	Title  string      `json:"title"`
	Theme  deckTheme   `json:"theme"`
	Slides []slideSpec `json:"slides"`
}

type deckTheme struct {
	Accent string `json:"accent"` // #RRGGBB
	Font   string `json:"font"`
	Dark   bool   `json:"dark"`
}

type slideSpec struct {
	Layout   string    `json:"layout"` // title | content | two | image | section
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle"`
	Bullets  []any     `json:"bullets"` // strings; a nested array is the item before's sub-bullets
	Left     []any     `json:"left"`
	Right    []any     `json:"right"`
	Table    *slideTbl `json:"table"`
	Image    string    `json:"image"`
	Caption  string    `json:"caption"`
	Notes    string    `json:"notes"`
}

type slideTbl struct {
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
}

var hexColorRE = regexp.MustCompile(`^#?([0-9A-Fa-f]{6})$`)

// colors of a deck: background, text, muted text, accent, panel.
type deckColors struct{ bg, ink, muted, accent, panel, font string }

func (t deckTheme) colors() deckColors {
	c := deckColors{bg: "FFFFFF", ink: "0B0C12", muted: "4B4D5C", accent: "1F3DFF", panel: "E8E9EE", font: "Arial"}
	if t.Dark {
		c = deckColors{bg: "0B0C12", ink: "E9EAF0", muted: "A3A6B6", accent: "8C9BFF", panel: "1F2028", font: "Arial"}
	}
	if m := hexColorRE.FindStringSubmatch(strings.TrimSpace(t.Accent)); m != nil {
		c.accent = strings.ToUpper(m[1])
	}
	if f := strings.TrimSpace(t.Font); f != "" && len(f) <= 64 {
		c.font = f
	}
	return c
}

// EMU on a 16:9 slide (13.333in × 7.5in).
const (
	slideW = 12192000
	slideH = 6858000
	emuIn  = 914400
)

type pptxWriter struct {
	c       deckColors
	media   map[string][]byte
	images  map[string]docImage
	missing []string
}

// writePptx writes a deck. images are the session images it may name.
func writePptx(d deckSpec, images map[string]docImage, when time.Time) ([]byte, []string, error) {
	if len(d.Slides) == 0 {
		return nil, nil, fmt.Errorf("a deck needs at least one slide")
	}
	if len(d.Slides) > 200 {
		return nil, nil, fmt.Errorf("at most 200 slides (got %d)", len(d.Slides))
	}
	w := &pptxWriter{c: d.Theme.colors(), media: map[string][]byte{}, images: images}
	z := newZipWriter()
	n := len(d.Slides)

	var ct strings.Builder
	ct.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Default Extension="jpeg" ContentType="image/jpeg"/><Default Extension="gif" ContentType="image/gif"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>` +
		`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>` +
		`<Override PartName="/ppt/notesMasters/notesMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesMaster+xml"/>` +
		`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
		`<Override PartName="/ppt/theme/theme2.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
		`<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>` +
		`<Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/>` +
		`<Override PartName="/ppt/tableStyles.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&ct, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i)
		if strings.TrimSpace(d.Slides[i-1].Notes) != "" {
			fmt.Fprintf(&ct, `<Override PartName="/ppt/notesSlides/notesSlide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>`, i)
		}
	}
	ct.WriteString(`</Types>`)
	z.addString("[Content_Types].xml", ct.String())
	z.addString("_rels/.rels", pkgRels("ppt/presentation.xml"))
	z.addString("docProps/core.xml", coreProps(d.Title, when))
	z.addString("docProps/app.xml", appProps("xbin agent"))

	var pres, prels strings.Builder
	pres.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" saveSubsetFonts="1"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:notesMasterIdLst><p:notesMasterId r:id="rId3"/></p:notesMasterIdLst><p:sldIdLst>`)
	prels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesMaster" Target="notesMasters/notesMaster1.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/presProps" Target="presProps.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/viewProps" Target="viewProps.xml"/><Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/tableStyles" Target="tableStyles.xml"/>`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&pres, `<p:sldId id="%d" r:id="rId%d"/>`, 255+i, 10+i)
		fmt.Fprintf(&prels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, 10+i, i)
	}
	fmt.Fprintf(&pres, `</p:sldIdLst><p:sldSz cx="%d" cy="%d"/><p:notesSz cx="6858000" cy="9144000"/><p:defaultTextStyle><a:defPPr><a:defRPr lang="en-US"/></a:defPPr></p:defaultTextStyle></p:presentation>`, slideW, slideH)
	prels.WriteString(`</Relationships>`)
	z.addString("ppt/presentation.xml", pres.String())
	z.addString("ppt/_rels/presentation.xml.rels", prels.String())
	z.addString("ppt/presProps.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentationPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)
	z.addString("ppt/viewProps.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:viewPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:normalViewPr><p:restoredLeft sz="15620"/><p:restoredTop sz="94660"/></p:normalViewPr><p:gridSpacing cx="76200" cy="76200"/></p:viewPr>`)
	z.addString("ppt/tableStyles.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:tblStyleLst xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" def="{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}"/>`)
	z.addString("ppt/theme/theme1.xml", w.themeXML("xbin"))
	z.addString("ppt/theme/theme2.xml", w.themeXML("xbin notes"))
	z.addString("ppt/slideMasters/slideMaster1.xml", w.masterXML())
	z.addString("ppt/slideMasters/_rels/slideMaster1.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`)
	z.addString("ppt/slideLayouts/slideLayout1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="obj" preserve="1"><p:cSld name="Title and Content"><p:spTree>`+spTreeHead+
		phShape(2, "Title 1", `<p:ph type="title"/>`, 609600, 365125, slideW-2*609600, 1325563)+
		phShape(3, "Content Placeholder 2", `<p:ph idx="1"/>`, 609600, 1825625, slideW-2*609600, 4351338)+
		`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`)
	z.addString("ppt/slideLayouts/_rels/slideLayout1.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`)
	z.addString("ppt/notesMasters/notesMaster1.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:notesMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree>`+spTreeHead+
		phShape(2, "Slide Image Placeholder 1", `<p:ph type="sldImg" idx="2"/>`, 685800, 685800, 5486400, 3086100)+
		phShape(3, "Notes Placeholder 2", `<p:ph type="body" sz="quarter" idx="3"/>`, 685800, 4400550, 5486400, 3600450)+
		`</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/></p:notesMaster>`)
	z.addString("ppt/notesMasters/_rels/notesMaster1.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme2.xml"/></Relationships>`)

	for i, s := range d.Slides {
		num := i + 1
		xml, rels := w.slideXML(s, num)
		hasNotes := strings.TrimSpace(s.Notes) != ""
		var r strings.Builder
		r.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>`)
		if hasNotes {
			fmt.Fprintf(&r, `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide%d.xml"/>`, num)
		}
		r.WriteString(rels + `</Relationships>`)
		z.addString(fmt.Sprintf("ppt/slides/slide%d.xml", num), xml)
		z.addString(fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", num), r.String())
		if hasNotes {
			z.addString(fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", num), notesXML(s.Notes))
			z.addString(fmt.Sprintf("ppt/notesSlides/_rels/notesSlide%d.xml.rels", num), fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesMaster" Target="../notesMasters/notesMaster1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="../slides/slide%d.xml"/></Relationships>`, num))
		}
	}
	for _, name := range docKeys(w.media) {
		z.add("ppt/media/"+name, w.media[name])
	}
	out, err := z.bytes()
	return out, w.missing, err
}

const spTreeHead = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`

func phShape(id int, name, ph string, x, y, cx, cy int64) string {
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr>%s</p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>`,
		id, name, ph, x, y, cx, cy)
}

func notesXML(text string) string {
	var paras strings.Builder
	for _, ln := range strings.Split(strings.TrimSpace(text), "\n") {
		paras.WriteString(`<a:p><a:r><a:rPr lang="en-US"/><a:t>` + xmlEsc(ln) + `</a:t></a:r></a:p>`)
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree>` + spTreeHead +
		`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Slide Image Placeholder 1"/><p:cNvSpPr><a:spLocks noGrp="1" noRot="1" noChangeAspect="1"/></p:cNvSpPr><p:nvPr><p:ph type="sldImg"/></p:nvPr></p:nvSpPr><p:spPr/></p:sp>` +
		`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Notes Placeholder 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>` +
		paras.String() + `</p:txBody></p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
}

// slideXML draws one slide; rels are its extra relationships (images).
func (w *pptxWriter) slideXML(s slideSpec, num int) (string, string) {
	c := w.c
	var sp strings.Builder
	var rels strings.Builder
	id := 2
	next := func() int { id++; return id }
	const mx, top = 609600, 457200 // side margin, top
	bodyY := int64(1600200)
	bodyH := int64(slideH) - bodyY - 457200
	bg := c.bg
	titleColor := c.ink
	layout := strings.ToLower(strings.TrimSpace(s.Layout))
	if layout == "" {
		switch {
		case num == 1 && len(s.Bullets) == 0 && s.Table == nil && s.Image == "":
			layout = "title"
		case s.Image != "" && len(s.Bullets) == 0:
			layout = "image"
		case len(s.Left) > 0 || len(s.Right) > 0:
			layout = "two"
		default:
			layout = "content"
		}
	}
	switch layout {
	case "title":
		sp.WriteString(w.textShape(next(), "Title", `<p:ph type="ctrTitle"/>`, mx, 2286000, slideW-2*mx, 1371600,
			[]pptPara{{runs: parseInline(s.Title), size: 4400, bold: true, color: c.ink}}, "b"))
		sp.WriteString(w.rule(next(), mx, 3733800, 1219200, c.accent))
		if s.Subtitle != "" {
			sp.WriteString(w.textShape(next(), "Subtitle", `<p:ph type="subTitle" idx="1"/>`, mx, 3886200, slideW-2*mx, 1143000,
				[]pptPara{{runs: parseInline(s.Subtitle), size: 2000, color: c.muted}}, "t"))
		}
	case "section":
		bg = c.accent
		titleColor = "FFFFFF"
		sp.WriteString(w.textShape(next(), "Title", `<p:ph type="title"/>`, mx, 2514600, slideW-2*mx, 1371600,
			[]pptPara{{runs: parseInline(s.Title), size: 4000, bold: true, color: titleColor}}, "ctr"))
		if s.Subtitle != "" {
			sp.WriteString(w.textShape(next(), "Subtitle", "", mx, 3886200, slideW-2*mx, 914400,
				[]pptPara{{runs: parseInline(s.Subtitle), size: 2000, color: "FFFFFF"}}, "t"))
		}
	default:
		sp.WriteString(w.textShape(next(), "Title", `<p:ph type="title"/>`, mx, top, slideW-2*mx, 914400,
			[]pptPara{{runs: parseInline(s.Title), size: 3200, bold: true, color: titleColor}}, "b"))
		sp.WriteString(w.rule(next(), mx, top+960120, 914400, c.accent))
		full := int64(slideW - 2*mx)
		switch layout {
		case "two":
			half := (full - 304800) / 2
			sp.WriteString(w.textShape(next(), "Left", "", mx, bodyY, half, bodyH, w.bullets(s.Left, 2000), "t"))
			sp.WriteString(w.textShape(next(), "Right", "", mx+half+304800, bodyY, half, bodyH, w.bullets(s.Right, 2000), "t"))
		case "image":
			capH := int64(0)
			if s.Caption != "" {
				capH = 457200
			}
			sp.WriteString(w.picture(next(), s.Image, mx, bodyY, full, bodyH-capH, &rels))
			if s.Caption != "" {
				sp.WriteString(w.textShape(next(), "Caption", "", mx, bodyY+bodyH-capH, full, capH,
					[]pptPara{{runs: parseInline(s.Caption), size: 1400, color: c.muted, align: "ctr"}}, "t"))
			}
		default: // content: bullets, and a table or an image beside or below them
			side := s.Table != nil || s.Image != ""
			textW := full
			if side && len(s.Bullets) > 0 {
				textW = full*5/12 - 152400
			}
			x := int64(mx)
			if len(s.Bullets) > 0 {
				sp.WriteString(w.textShape(next(), "Content", `<p:ph idx="1"/>`, x, bodyY, textW, bodyH, w.bullets(s.Bullets, 2000), "t"))
				x += textW + 304800
			}
			rest := full - (x - mx)
			if s.Table != nil {
				sp.WriteString(w.table(next(), s.Table, x, bodyY, rest))
			} else if s.Image != "" {
				sp.WriteString(w.picture(next(), s.Image, x, bodyY, rest, bodyH, &rels))
			}
		}
	}
	bgXML := fmt.Sprintf(`<p:bg><p:bgPr><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>`, bg)
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld>` + bgXML + `<p:spTree>` + spTreeHead +
		sp.String() + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`, rels.String()
}

type pptPara struct {
	runs   []mdRun
	size   int // hundredths of a point
	bold   bool
	color  string
	level  int
	bullet bool
	align  string
}

// bullets turns a bullet list (strings; an array is the item before's
// sub-items) into paragraphs.
func (w *pptxWriter) bullets(items []any, size int) []pptPara {
	var out []pptPara
	var walk func(items []any, lvl int)
	walk = func(items []any, lvl int) {
		for _, it := range items {
			switch v := it.(type) {
			case string:
				out = append(out, pptPara{runs: parseInline(v), size: max(size-200*lvl, 1200), color: w.c.ink, level: lvl, bullet: true})
			case []any:
				if lvl < 4 {
					walk(v, lvl+1)
				}
			case map[string]any: // {"text": …, "sub": […]}
				if t, ok := v["text"].(string); ok {
					out = append(out, pptPara{runs: parseInline(t), size: max(size-200*lvl, 1200), color: w.c.ink, level: lvl, bullet: true})
				}
				if sub, ok := v["sub"].([]any); ok && lvl < 4 {
					walk(sub, lvl+1)
				}
			default:
				out = append(out, pptPara{runs: []mdRun{{Text: fmt.Sprint(v)}}, size: size, color: w.c.ink, level: lvl, bullet: true})
			}
		}
	}
	walk(items, 0)
	return out
}

func (w *pptxWriter) textShape(id int, name, ph string, x, y, cx, cy int64, paras []pptPara, anchor string) string {
	var b strings.Builder
	txBox := ""
	if ph == "" {
		txBox = ` txBox="1"`
	}
	fmt.Fprintf(&b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s %d"/><p:cNvSpPr%s/><p:nvPr>%s</p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/></p:spPr><p:txBody><a:bodyPr wrap="square" lIns="0" tIns="45720" rIns="0" bIns="45720" anchor="%s"><a:normAutofit/></a:bodyPr><a:lstStyle/>`,
		id, name, id, txBox, ph, x, y, cx, cy, anchor)
	if len(paras) == 0 {
		b.WriteString(`<a:p><a:endParaRPr lang="en-US"/></a:p>`)
	}
	for _, p := range paras {
		b.WriteString(`<a:p>`)
		algn := ""
		if p.align != "" {
			algn = ` algn="` + p.align + `"`
		}
		if p.bullet {
			marL := 342900 + 342900*int64(p.level)
			fmt.Fprintf(&b, `<a:pPr marL="%d" lvl="%d" indent="-285750"%s><a:spcBef><a:spcPts val="600"/></a:spcBef><a:buClr><a:srgbClr val="%s"/></a:buClr><a:buFont typeface="Arial"/><a:buChar char="%s"/></a:pPr>`,
				marL, p.level, algn, w.c.accent, []string{"•", "–", "•", "–", "•"}[p.level%5])
		} else {
			fmt.Fprintf(&b, `<a:pPr%s><a:buNone/></a:pPr>`, algn)
		}
		for _, r := range p.runs {
			if r.Break {
				b.WriteString(`<a:br><a:rPr lang="en-US"/></a:br>`)
				continue
			}
			txt := r.Text
			if r.Image != "" {
				txt = "[" + r.Text + "]"
			}
			bold := p.bold || r.Bold
			fmt.Fprintf(&b, `<a:r><a:rPr lang="en-US" sz="%d"%s%s dirty="0"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="%s"/>`,
				p.size, boolAttr(" b", bold), boolAttr(" i", r.Italic), p.color, xmlEsc(fontOf(w.c, r.Code)))
			if r.Link != "" && safeLink(r.Link) {
				// a link's target would need a relationship; the text keeps it readable
				txt = txt + " (" + r.Link + ")"
			}
			b.WriteString(`</a:rPr><a:t>` + xmlEsc(txt) + `</a:t></a:r>`)
		}
		b.WriteString(`</a:p>`)
	}
	b.WriteString(`</p:txBody></p:sp>`)
	return b.String()
}

func fontOf(c deckColors, code bool) string {
	if code {
		return "Courier New"
	}
	return c.font
}

func boolAttr(name string, on bool) string {
	if on {
		return name + `="1"`
	}
	return ""
}

// rule is the short accent bar under a title.
func (w *pptxWriter) rule(id int, x, y, cx int64, color string) string {
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="Rule %d"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="45720"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:ln><a:noFill/></a:ln></p:spPr></p:sp>`,
		id, id, x, y, cx, color)
}

// picture places a session image inside the box, keeping its aspect.
func (w *pptxWriter) picture(id int, src string, x, y, cx, cy int64, rels *strings.Builder) string {
	img, ok := w.images[src]
	var cfg image.Config
	var format string
	var err error
	if ok {
		cfg, format, err = image.DecodeConfig(bytes.NewReader(img.data))
	}
	ext := map[string]string{"png": "png", "jpeg": "jpeg", "gif": "gif"}[format]
	if !ok || err != nil || cfg.Width <= 0 || cfg.Height <= 0 || ext == "" {
		w.missing = append(w.missing, src)
		return w.textShape(id, "Missing image", "", x, y, cx, cy,
			[]pptPara{{runs: []mdRun{{Text: "[image " + src + " not found]", Italic: true}}, size: 1600, color: w.c.muted, align: "ctr"}}, "ctr")
	}
	// fit
	iw, ih := int64(cfg.Width), int64(cfg.Height)
	fw, fh := cx, cx*ih/iw
	if fh > cy {
		fh, fw = cy, cy*iw/ih
	}
	ox, oy := x+(cx-fw)/2, y+(cy-fh)/2
	name := fmt.Sprintf("image%d.%s", len(w.media)+1, ext)
	w.media[name] = img.data
	rid := fmt.Sprintf("rId%d", 100+len(w.media))
	fmt.Fprintf(rels, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/%s"/>`, rid, name)
	return fmt.Sprintf(`<p:pic><p:nvPicPr><p:cNvPr id="%d" name="Picture %d" descr="%s"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></p:blipFill><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr></p:pic>`,
		id, id, xmlEsc(src), rid, ox, oy, fw, fh)
}

// table draws a slide table in the box's width.
func (w *pptxWriter) table(id int, t *slideTbl, x, y, cx int64) string {
	cols := len(t.Header)
	for _, r := range t.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
	}
	rows := min(len(t.Rows), 30)
	colW := cx / int64(cols)
	rowH := int64(370840)
	var b strings.Builder
	fmt.Fprintf(&b, `<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="%d" name="Table %d"/><p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/></p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr firstRow="1" bandRow="1"/><a:tblGrid>`,
		id, id, x, y, colW*int64(cols), rowH*int64(rows+1))
	for i := 0; i < cols; i++ {
		fmt.Fprintf(&b, `<a:gridCol w="%d"/>`, colW)
	}
	b.WriteString(`</a:tblGrid>`)
	line := func(cells []string, head bool, band bool) {
		fmt.Fprintf(&b, `<a:tr h="%d">`, rowH)
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			color, fill := w.c.ink, w.c.bg
			if head {
				color, fill = "FFFFFF", w.c.accent
			} else if band {
				fill = w.c.panel
			}
			fmt.Fprintf(&b, `<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US" sz="1400"%s dirty="0"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="%s"/></a:rPr><a:t>%s</a:t></a:r></a:p></a:txBody><a:tcPr><a:lnL w="6350"><a:solidFill><a:srgbClr val="CDD0D8"/></a:solidFill></a:lnL><a:lnR w="6350"><a:solidFill><a:srgbClr val="CDD0D8"/></a:solidFill></a:lnR><a:lnT w="6350"><a:solidFill><a:srgbClr val="CDD0D8"/></a:solidFill></a:lnT><a:lnB w="6350"><a:solidFill><a:srgbClr val="CDD0D8"/></a:solidFill></a:lnB><a:solidFill><a:srgbClr val="%s"/></a:solidFill></a:tcPr></a:tc>`,
				boolAttr(" b", head), color, xmlEsc(w.c.font), xmlEsc(plainInline(c)), fill)
		}
		b.WriteString(`</a:tr>`)
	}
	line(t.Header, true, false)
	for i, r := range t.Rows[:rows] {
		line(r, false, i%2 == 1)
	}
	b.WriteString(`</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`)
	return b.String()
}

func (w *pptxWriter) masterXML() string {
	c := w.c
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="` + c.bg + `"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree>` + spTreeHead +
		phShape(2, "Title Placeholder 1", `<p:ph type="title"/>`, 609600, 365125, slideW-2*609600, 1325563) +
		phShape(3, "Text Placeholder 2", `<p:ph type="body" idx="1"/>`, 609600, 1825625, slideW-2*609600, 4351338) +
		`</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/><p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>` +
		`<p:txStyles><p:titleStyle><a:lvl1pPr algn="l"><a:defRPr sz="3200" b="1"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/></a:defRPr></a:lvl1pPr></p:titleStyle><p:bodyStyle><a:lvl1pPr marL="342900" indent="-342900"><a:buChar char="•"/><a:defRPr sz="2000"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/></a:defRPr></a:lvl1pPr></p:bodyStyle><p:otherStyle><a:lvl1pPr><a:defRPr sz="1800"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill></a:defRPr></a:lvl1pPr></p:otherStyle></p:txStyles></p:sldMaster>`
}

func (w *pptxWriter) themeXML(name string) string {
	c := w.c
	dk1, lt1 := "0B0C12", "FFFFFF"
	if c.bg != "FFFFFF" {
		dk1, lt1 = c.ink, c.bg
	}
	f := xmlEsc(c.font)
	fill := `<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>`
	ln := `<a:ln w="6350"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln>`
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="` + xmlEsc(name) + `"><a:themeElements><a:clrScheme name="Base Two">` +
		`<a:dk1><a:srgbClr val="` + dk1 + `"/></a:dk1><a:lt1><a:srgbClr val="` + lt1 + `"/></a:lt1><a:dk2><a:srgbClr val="4B4D5C"/></a:dk2><a:lt2><a:srgbClr val="E8E9EE"/></a:lt2>` +
		`<a:accent1><a:srgbClr val="` + c.accent + `"/></a:accent1><a:accent2><a:srgbClr val="3FB5A3"/></a:accent2><a:accent3><a:srgbClr val="F2994A"/></a:accent3><a:accent4><a:srgbClr val="A3CF5E"/></a:accent4><a:accent5><a:srgbClr val="FF7A7A"/></a:accent5><a:accent6><a:srgbClr val="8C9BFF"/></a:accent6>` +
		`<a:hlink><a:srgbClr val="` + c.accent + `"/></a:hlink><a:folHlink><a:srgbClr val="4B4D5C"/></a:folHlink></a:clrScheme>` +
		`<a:fontScheme name="Base Two"><a:majorFont><a:latin typeface="` + f + `"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont><a:minorFont><a:latin typeface="` + f + `"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont></a:fontScheme>` +
		`<a:fmtScheme name="Base Two"><a:fillStyleLst>` + fill + fill + fill + `</a:fillStyleLst><a:lnStyleLst>` + ln + ln + ln + `</a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst>` + fill + fill + fill + `</a:bgFillStyleLst></a:fmtScheme></a:themeElements><a:objectDefaults/><a:extraClrSchemeLst/></a:theme>`
}
