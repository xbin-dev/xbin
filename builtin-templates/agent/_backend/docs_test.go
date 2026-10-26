package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures, made here ---------------------------------------------------------

// tinyPDF writes a minimal PDF: one page per text, Helvetica, offsets right.
func tinyPDF(pages ...string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(body string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	b.WriteString("%PDF-1.4\n")
	n := len(pages)
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, text := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i))
		stream := fmt.Sprintf("BT /F1 18 Tf 30 100 Td (%s) Tj ET", text)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

func tinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{31, 61, 255, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// zipOf packs name → content in order.
func zipOf(t testing.TB, parts ...[2]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, p := range parts {
		w, err := zw.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(p[1]))
	}
	zw.Close()
	return b.Bytes()
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Fatalf("%s: missing %q in:\n%s", what, w, got)
		}
	}
}

// --- Markdown -----------------------------------------------------------------------

func TestDocsMarkdownBlocks(t *testing.T) {
	md := "# Title\n\nIntro with **bold**, *it*, `code` and [a link](https://x.dev).\n\n" +
		"- one\n- two\n  - two.a\n  - two.b\n- three\n\n3. third\n4. fourth\n\n> a quote\n\n```go\nfunc x() {}\n```\n\n---\n\n" +
		"| A | B |\n|:--|--:|\n| 1 | 2 |\n| x \\| y | z |\n\n![Chart](chart.png)\n"
	bs := parseMarkdown(md)
	var kinds []string
	for _, b := range bs {
		kinds = append(kinds, b.Kind)
	}
	if got := strings.Join(kinds, " "); got != "h p ul ol quote code hr table img" {
		t.Fatalf("kinds: %s", got)
	}
	if bs[2].Items[1].Sub == nil || len(bs[2].Items[1].Sub.Items) != 2 {
		t.Fatalf("nested list: %+v", bs[2].Items)
	}
	if bs[3].Start != 3 || len(bs[3].Items) != 2 {
		t.Fatalf("ordered list: %+v", bs[3])
	}
	if bs[5].Lang != "go" || bs[5].Text != "func x() {}" {
		t.Fatalf("fence: %+v", bs[5])
	}
	tb := bs[7]
	if len(tb.Rows) != 2 || tb.Rows[1][0] != "x | y" || tb.Align[0] != "left" || tb.Align[1] != "right" {
		t.Fatalf("table: %+v", tb)
	}
	if bs[8].Src != "chart.png" || bs[8].Alt != "Chart" {
		t.Fatalf("figure: %+v", bs[8])
	}
	runs := parseInline("a **b** *c* `d` [e](https://f)")
	var desc []string
	for _, r := range runs {
		desc = append(desc, fmt.Sprintf("%s:%v%v%v%s", r.Text, r.Bold, r.Italic, r.Code, r.Link))
	}
	if got := strings.Join(desc, "|"); got != "a :falsefalsefalse|b:truefalsefalse| :falsefalsefalse|c:falsetruefalse| :falsefalsefalse|d:falsefalsetrue| :falsefalsefalse|e:falsefalsefalsehttps://f" {
		t.Fatalf("inline: %s", got)
	}
	// snake_case words are not italics
	if r := parseInline("a_b_c"); len(r) != 1 || r[0].Italic {
		t.Fatalf("snake_case: %+v", r)
	}
}

// --- Word ---------------------------------------------------------------------------

func TestDocsDocxRoundTrip(t *testing.T) {
	md := "# Quarterly report\n\nSales grew **12%** in *Q3*. See [the dashboard](https://example.com/d).\n\n" +
		"## Highlights\n\n- EU up\n- US flat\n  - West down\n\n1. Hire\n2. Ship\n\n" +
		"| Region | Q3 |\n|---|---|\n| EU | 120 |\n| US | 98 |\n\n> Keep going.\n\n```\nsum = a + b\n```\n\n![Revenue](rev.png)\n"
	imgs := map[string]docImage{"rev.png": {data: tinyPNG(t, 40, 20), mime: "image/png"}}
	data, missing, err := writeDocx(parseMarkdown(md), "Quarterly report", imgs, docEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing images: %v", missing)
	}
	got, err := readDocx(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "docx", got, "# Quarterly report", "## Highlights", "**12%**", "*Q3*",
		"[the dashboard](https://example.com/d)", "- EU up", "  - West down", "1. Hire", "2. Ship",
		"| Region | Q3 |", "| EU | 120 |", "> Keep going.", "sum = a + b", "[image: Revenue]")
	// the package is a real Word file: the parts a reader looks for are there
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, n := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml", "word/styles.xml", "word/numbering.xml", "word/media/image1.png", "docProps/core.xml"} {
		if !names[n] {
			t.Fatalf("docx lacks %s", n)
		}
	}
	// same input, same bytes
	again, _, _ := writeDocx(parseMarkdown(md), "Quarterly report", imgs, docEpoch)
	if !bytes.Equal(again, data) {
		t.Fatal("writing the same document twice gave different bytes")
	}
	// a missing image is its alt text, and said so
	_, missing, _ = writeDocx(parseMarkdown("![Nope](gone.png)"), "", nil, docEpoch)
	if len(missing) != 1 || missing[0] != "gone.png" {
		t.Fatalf("missing: %v", missing)
	}
}

// A document from another program: style ids that aren't ours, a numbering
// definition, a hyperlink, a tab and a nested table.
func TestDocsDocxForeign(t *testing.T) {
	doc := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>
<w:p><w:pPr><w:pStyle w:val="berschrift1"/></w:pPr><w:r><w:t>Überblick</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="7"/></w:numPr></w:pPr><w:r><w:t>first</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="7"/></w:numPr></w:pPr><w:r><w:t>second</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">a</w:t><w:tab/><w:t>b </w:t></w:r><w:hyperlink r:id="rId9"><w:r><w:t>site</w:t></w:r></w:hyperlink></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>H1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>H2</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>x</w:t></w:r></w:p></w:tc><w:tc><w:tbl><w:tr><w:tc><w:p><w:r><w:t>inner</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p/></w:tc></w:tr></w:tbl>
</w:body></w:document>`
	styles := `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:styleId="berschrift1"><w:name w:val="heading 1"/></w:style></w:styles>`
	numbering := `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="3"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum><w:num w:numId="7"><w:abstractNumId w:val="3"/></w:num></w:numbering>`
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.org" TargetMode="External"/></Relationships>`
	data := zipOf(t, [2]string{"word/document.xml", doc}, [2]string{"word/styles.xml", styles},
		[2]string{"word/numbering.xml", numbering}, [2]string{"word/_rels/document.xml.rels", rels})
	got, err := readDocx(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "foreign docx", got, "# Überblick", "1. first", "2. second", "a\tb [site](https://example.org)", "| H1 | H2 |", "inner")
}

// --- PowerPoint -----------------------------------------------------------------------

func TestDocsPptxRoundTrip(t *testing.T) {
	deck := deckSpec{Title: "Q3", Slides: []slideSpec{
		{Layout: "title", Title: "Q3 review", Subtitle: "Numbers & next steps"},
		{Title: "Highlights", Bullets: []any{"EU **up** 12%", []any{"driven by DE"}, "US flat"}, Notes: "Mention hiring."},
		{Title: "By region", Bullets: []any{"see table"}, Table: &slideTbl{Header: []string{"Region", "Q3"}, Rows: [][]string{{"EU", "120"}, {"US", "98"}}}},
		{Layout: "two", Title: "Pros & cons", Left: []any{"fast"}, Right: []any{"costly"}},
		{Layout: "image", Title: "Chart", Image: "rev.png", Caption: "Revenue"},
		{Layout: "section", Title: "Appendix"},
	}}
	imgs := map[string]docImage{"rev.png": {data: tinyPNG(t, 64, 32), mime: "image/png"}}
	data, missing, err := writePptx(deck, imgs, docEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing: %v", missing)
	}
	got, n, err := readPptx(context.Background(), data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("slides: %d", n)
	}
	mustContain(t, "pptx", got, "## Slide 1: Q3 review", "Numbers & next steps", "## Slide 2: Highlights",
		"- EU up 12%", "  - driven by DE", "Notes: Mention hiring.", "| Region | Q3 |", "| EU | 120 |",
		"## Slide 4: Pros & cons", "fast", "costly", "[image: rev.png]", "## Slide 6: Appendix")
	some, _, _ := readPptx(context.Background(), data, func(i int) bool { return i == 2 })
	if strings.Contains(some, "Slide 1") || !strings.Contains(some, "Slide 2") {
		t.Fatalf("slides filter: %s", some)
	}
	// the deck's preview draws every slide
	html := deckPreviewHTML(deck, imgs)
	if strings.Count(html, `class="slide`) != 6 || !strings.Contains(html, "data:image/png;base64,") {
		t.Fatal("deck preview should draw 6 slides with the image inline")
	}
	if _, _, err := writePptx(deckSpec{}, nil, docEpoch); err == nil {
		t.Fatal("an empty deck should be refused")
	}
}

// --- Excel -------------------------------------------------------------------------

func TestDocsSheetWriteAndRead(t *testing.T) {
	var ops []sheetOp
	if err := json.Unmarshal([]byte(`[
		{"op":"set","sheet":"Sales","cell":"A1","values":[["Region","Q1","Q2","Total"],["EU",120,130,"=B2+C2"],["US",98.5,100,"=B3+C3"]]},
		{"op":"formula","cell":"D4","formula":"=SUM(D2:D3)"},
		{"op":"style","range":"A1:D1","bold":true,"fill":"#E8E9EE"},
		{"op":"width","cols":"A:D","width":14},
		{"op":"freeze","cell":"A2"},
		{"op":"chart","cell":"F2","type":"col","title":"By region","categories":"A2:A3","series":[{"name":"Q1","values":"B2:B3"}]}
	]`), &ops); err != nil {
		t.Fatal(err)
	}
	data, res, err := writeSheet(nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.results, "\n")
	mustContain(t, "computed", joined, "Sales!D2 → 250", "Sales!D3 → 198.5", "Sales!D4 → 448.5")
	got, err := readXlsx(data, "", "")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "xlsx", got, "Workbook: 1 sheet(s): Sales", "## Sheet: Sales", "| 1 | Region | Q1 | Q2 | Total |",
		"| 2 | EU | 120 | 130 | 250 |", "D4 =SUM(D2:D3) → 448.5")
	// a second write keeps what was there
	var more []sheetOp
	_ = json.Unmarshal([]byte(`[{"op":"set","cell":"A5","values":[["JP",10,20,"=B5+C5"]]},{"op":"add_sheet","name":"Notes"},{"op":"set","sheet":"Notes","cell":"A1","value":"hello"}]`), &more)
	data2, _, err := writeSheet(data, more)
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := readXlsx(data2, "", "")
	mustContain(t, "xlsx v2", got2, "Sales, Notes", "| 2 | EU | 120 | 130 | 250 |", "| 5 | JP | 10 | 20 | 30 |", "## Sheet: Notes", "hello")
	only, _ := readXlsx(data2, "notes", "")
	if strings.Contains(only, "## Sheet: Sales") {
		t.Fatal("sheet filter")
	}
	if _, err := readXlsx(data2, "nope", ""); err == nil {
		t.Fatal("an unknown sheet should say so")
	}
	part, _ := readXlsx(data2, "Sales", "B2:C3")
	mustContain(t, "range", part, "| | B | C |", "| 2 | 120 | 130 |")
	html, err := xlsxPreviewHTML(data2, "s.xlsx")
	if err != nil || !strings.Contains(html, "<h2>Sales</h2>") || !strings.Contains(html, "font-weight:700") {
		t.Fatalf("preview: %v", err)
	}
	// bad ops fail with the op's number
	var bad []sheetOp
	_ = json.Unmarshal([]byte(`[{"op":"set","cell":"A1","value":1},{"op":"explode"}]`), &bad)
	if _, _, err := writeSheet(nil, bad); err == nil || !strings.Contains(err.Error(), "op 2") {
		t.Fatalf("bad op: %v", err)
	}
	if _, err := openXlsx([]byte("not a zip")); err == nil {
		t.Fatal("garbage should not open")
	}
}

// --- CSV ------------------------------------------------------------------------

func TestDocsCSV(t *testing.T) {
	var b strings.Builder
	b.WriteString("name;qty\n")
	for i := 1; i <= 450; i++ {
		fmt.Fprintf(&b, "item%d;%d\n", i, i)
	}
	got, err := readCSV([]byte(b.String()), "x.csv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "csv", got, "450 data row(s), 2 column(s); showing rows 1–200", "| name | qty |", "| item1 | 1 |", "(more: offset 201)")
	if strings.Contains(got, "item201") {
		t.Fatal("csv default limit")
	}
	got, _ = readCSV([]byte(b.String()), "x.csv", 440, 50)
	mustContain(t, "csv page", got, "| item440 | 440 |", "| item450 | 450 |")
}

// --- PDF ---------------------------------------------------------------------------

func TestDocsPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the PDFium module (seconds)")
	}
	data := tinyPDF("Hello PDF", "Second page here")
	ctx := context.Background()
	got, n, err := readPDF(ctx, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pages: %d", n)
	}
	mustContain(t, "pdf", got, "## Page 1", "Hello PDF", "## Page 2", "Second page here")
	one, _, _ := readPDF(ctx, data, func(i int) bool { return i == 2 })
	if strings.Contains(one, "Hello PDF") {
		t.Fatal("page filter")
	}
	img, total, err := renderPDFPage(ctx, data, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total %d", total)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width < 300 || cfg.Height < 200 {
		t.Fatalf("page picture: %v %+v", err, cfg)
	}
	if _, _, err := renderPDFPage(ctx, data, 9); err == nil {
		t.Fatal("a page out of range should say so")
	}
	if _, _, err := readPDF(ctx, []byte("%PDF-1.4 broken"), nil); err == nil {
		t.Fatal("a broken PDF should not read")
	}
	// the module mounts no filesystem: a PDF naming a host file reads nothing of it
	// (pdfium never opens paths we don't hand it; FilePath isn't used)
}

// --- hardening -----------------------------------------------------------------------

func TestDocsZipBomb(t *testing.T) {
	// one part that inflates past the per-part cap from a few KB
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("word/document.xml")
	chunk := bytes.Repeat([]byte{' '}, 1<<20)
	for i := 0; i < docMaxPart>>20+2; i++ {
		w.Write(chunk)
	}
	zw.Close()
	if b.Len() > 1<<20 {
		t.Fatalf("the bomb should be small (%d)", b.Len())
	}
	if _, err := readDocx(context.Background(), b.Bytes()); err != errDocTooBig {
		t.Fatalf("zip bomb: %v", err)
	}
	// too deep
	deep := strings.Repeat("<a>", docMaxDepth+5) + strings.Repeat("</a>", docMaxDepth+5)
	if _, err := readDocx(context.Background(), zipOf(t, [2]string{"word/document.xml", deep})); err != errDocTooBig {
		t.Fatalf("deep XML: %v", err)
	}
	// not a zip at all
	if _, err := readDocx(context.Background(), []byte("hello")); err == nil {
		t.Fatal("garbage docx")
	}
	if _, _, err := readPptx(context.Background(), []byte("PK\x03\x04junk"), nil); err == nil {
		t.Fatal("garbage pptx")
	}
	// a cancelled read stops
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, _, _ := writeDocx(parseMarkdown("# x"), "x", nil, docEpoch)
	if _, err := readDocx(ctx, data); err == nil {
		t.Fatal("a cancelled read should stop")
	}
}

// --- the tools ---------------------------------------------------------------------

func TestDocsTools(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	cfg := Config{}

	// specs: offered with the files, off with the docs feature, doc_view only with vision
	has := func(specs []toolSpec, name string) bool {
		for _, s := range specs {
			if s.Function.Name == name {
				return true
			}
		}
		return false
	}
	for _, lane := range []string{"", "web"} {
		for name := range docToolNames {
			if !has(toolSpecs(Config{Toolset: lane}, 0, nil), name) {
				t.Fatalf("lane %q lacks %s", lane, name)
			}
			if sideEffect(name, cfg) || toolsetOf(name) != tsFiles {
				t.Fatalf("%s: a file tool, never gated", name)
			}
		}
	}
	if has(toolSpecs(Config{Features: map[string]bool{"docs": false}}, 0, nil), "doc_read") {
		t.Fatal("docs:false hides the tools")
	}
	if has(toolSpecs(Config{Features: map[string]bool{"vision": false}}, 0, nil), "doc_view") {
		t.Fatal("doc_view needs vision")
	}

	// sheet_write creates the workbook and its preview, and journals a render naming it
	out, err := ag.runDocTool(ctx, run, cfg, "sheet_write", map[string]any{"path": "sales.xlsx",
		"ops": []any{map[string]any{"op": "set", "cell": "A1", "values": []any{[]any{"a", 1.0}, []any{"b", 2.0}, []any{"sum", "=B1+B2"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "sheet_write", out, "wrote sales.xlsx", "Sheet1!B3 → 3", "sales.xlsx.html")
	steps, _ := db.steps(id)
	var render string
	for _, s := range steps {
		if s.Kind == "render" {
			render = s.Detail
		}
	}
	mustContain(t, "render step", render, `"path":"sales.xlsx.html"`, `"doc":"sales.xlsx"`)
	f, err := db.replFile(id, "sales.xlsx")
	if err != nil || !f.Binary || f.Mime != mimeXlsx {
		t.Fatalf("stored workbook: %+v %v", f, err)
	}
	// doc_read reads it back, and file_read does too
	got, err := ag.runDocTool(ctx, run, cfg, "doc_read", map[string]any{"path": "sales.xlsx"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "doc_read xlsx", got, "sales.xlsx — Excel workbook", "| 3 | sum | 3 |")
	got, err = ag.runFileTool(ctx, run, cfg, "file_read", map[string]any{"path": "sales.xlsx"})
	if err != nil || !strings.Contains(got, "Excel workbook") {
		t.Fatalf("file_read on a workbook: %v %s", err, got)
	}

	// doc_write with an image from the session files
	f2, _, _, err := ag.putFileData(ctx, id, "chart.png", tinyPNG(t, 30, 20), "image/png", nil)
	if err != nil || !f2.Binary {
		t.Fatal(err)
	}
	out, err = ag.runDocTool(ctx, run, cfg, "doc_write", map[string]any{"path": "r.docx", "markdown": "# Report\n\nText.\n\n![A chart](chart.png)\n\n![x](nope.png)"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "doc_write", out, "wrote r.docx", "not found", "nope.png")
	got, _ = ag.runDocTool(ctx, run, cfg, "doc_read", map[string]any{"path": "r.docx"})
	mustContain(t, "doc_read docx", got, "# Report", "[image: A chart]")
	// a second write keeps the first as a version
	out, _ = ag.runDocTool(ctx, run, cfg, "doc_write", map[string]any{"path": "r.docx", "markdown": "# Report v2"})
	mustContain(t, "doc_write v2", out, "v2", "v1 is kept")

	// slides_write
	out, err = ag.runDocTool(ctx, run, cfg, "slides_write", map[string]any{"path": "d.pptx", "title": "D",
		"slides": []any{map[string]any{"layout": "title", "title": "Hello"}, map[string]any{"title": "Points", "bullets": []any{"one", "two"}}}})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "slides_write", out, "wrote d.pptx", "2 slide(s)")
	got, _ = ag.runDocTool(ctx, run, cfg, "doc_read", map[string]any{"path": "d.pptx", "slides": "2"})
	mustContain(t, "doc_read pptx", got, "## Slide 2: Points", "- one")

	// wrong extensions, non-documents
	if _, err := ag.runDocTool(ctx, run, cfg, "doc_write", map[string]any{"path": "r.txt", "markdown": "x"}); err == nil {
		t.Fatal("doc_write needs .docx")
	}
	if _, err := db.replPutFile(id, "notes.txt", "hi", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.runDocTool(ctx, run, cfg, "doc_read", map[string]any{"path": "notes.txt"}); err == nil {
		t.Fatal("doc_read on plain text should point at file_read")
	}
	if _, err := ag.runDocTool(ctx, run, Config{Features: map[string]bool{"docs": false}}, "doc_read", map[string]any{"path": "sales.xlsx"}); err == nil {
		t.Fatal("docs:false refuses at run time too")
	}
	// an attachment note says doc_read reads a document
	if n := attachmentNote([]*ReplFile{f}); !strings.Contains(n, "doc_read reads it") {
		t.Fatalf("attachment note: %s", n)
	}
}

func TestDocsViewPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the PDFium module (seconds)")
	}
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	if _, _, _, err := ag.putFileData(ctx, id, "paper.pdf", tinyPDF("One", "Two"), "application/pdf", nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := ag.runDocTool(ctx, run, Config{}, "doc_view", map[string]any{"path": "paper.pdf", "page": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("doc_view (first use compiles PDFium): %v", time.Since(start))
	if !strings.HasPrefix(out, fileViewOK+"paper.p2.png (image/png") {
		t.Fatalf("doc_view result: %s", out)
	}
	// the context assembly shows it like file_view's
	c := toolCall{}
	c.Function.Name = "doc_view"
	if got := shownImages(c, out); len(got) != 1 || got[0] != "paper.p2.png" {
		t.Fatalf("shownImages: %v", got)
	}
	got, err := ag.runDocTool(ctx, run, Config{}, "doc_read", map[string]any{"path": "paper.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "doc_read pdf", got, "paper.pdf — PDF, 2 page(s)", "## Page 1", "One", "## Page 2", "Two")
	if _, err := ag.runDocTool(ctx, run, Config{Features: map[string]bool{"vision": false}}, "doc_view", map[string]any{"path": "paper.pdf", "page": 1.0}); err == nil {
		t.Fatal("doc_view needs vision")
	}
}

// TestDocsDump writes sample files for a look in a real office suite
// (DOCS_DUMP=dir; skipped otherwise).
func TestDocsDump(t *testing.T) {
	dir := os.Getenv("DOCS_DUMP")
	if dir == "" {
		t.Skip("DOCS_DUMP not set")
	}
	imgs := map[string]docImage{"chart.png": {data: tinyPNG(t, 320, 160), mime: "image/png"}}
	md := "# Quarterly report\n\nSales grew **12%** in *Q3*. See [the dashboard](https://example.com/d) and `cfg.yaml`.\n\n" +
		"## Highlights\n\n- EU up\n- US flat\n  - West down\n    - Seattle\n\n1. Hire\n2. Ship\n\n" +
		"| Region | Q3 | Change |\n|---|--:|:-:|\n| EU | 120 | +12% |\n| US | 98 | 0% |\n\n> Keep going.\n\n```\nsum = a + b\n```\n\n---\n\n![Revenue by quarter](chart.png)\n"
	docx, _, err := writeDocx(parseMarkdown(md), "Quarterly report", imgs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	deck := deckSpec{Title: "Q3", Slides: []slideSpec{
		{Layout: "title", Title: "Q3 review", Subtitle: "Numbers & next steps"},
		{Title: "Highlights", Bullets: []any{"EU **up** 12%", []any{"driven by DE", "and FR"}, "US flat", "APAC *new*"}, Notes: "Mention hiring."},
		{Title: "By region", Bullets: []any{"EU leads", "US steady"}, Table: &slideTbl{Header: []string{"Region", "Q3", "Δ"}, Rows: [][]string{{"EU", "120", "+12%"}, {"US", "98", "0%"}, {"APAC", "40", "new"}}}},
		{Layout: "two", Title: "Pros & cons", Left: []any{"fast", "cheap"}, Right: []any{"costly to run", []any{"at scale"}}},
		{Layout: "image", Title: "Chart", Image: "chart.png", Caption: "Revenue by quarter"},
		{Layout: "section", Title: "Appendix", Subtitle: "Details"},
	}}
	pptx, _, err := writePptx(deck, imgs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ops []sheetOp
	_ = json.Unmarshal([]byte(`[
		{"op":"set","sheet":"Sales","cell":"A1","values":[["Region","Q1","Q2","Total"],["EU",120,130,"=B2+C2"],["US",98.5,100,"=B3+C3"],["Sum","=SUM(B2:B3)","=SUM(C2:C3)","=SUM(D2:D3)"]]},
		{"op":"style","range":"A1:D1","bold":true,"fill":"#E8E9EE"},
		{"op":"style","range":"B2:D4","numFmt":"#,##0.00"},
		{"op":"width","cols":"A:D","width":14},{"op":"freeze","cell":"A2"},
		{"op":"chart","cell":"F2","type":"col","title":"By region","categories":"A2:A3","series":[{"name":"Q1","values":"B2:B3"},{"name":"Q2","values":"C2:C3"}]}
	]`), &ops)
	xlsx, _, err := writeSheet(nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	html, _ := xlsxPreviewHTML(xlsx, "sales.xlsx")
	for name, b := range map[string][]byte{"report.docx": docx, "deck.pptx": pptx, "sales.xlsx": xlsx,
		"report.html": []byte(docxPreviewHTML(parseMarkdown(md), "Quarterly report", imgs)),
		"deck.html":   []byte(deckPreviewHTML(deck, imgs)), "sales.html": []byte(html), "tiny.pdf": tinyPDF("Hello PDF", "Page two")} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
