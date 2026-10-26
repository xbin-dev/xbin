// docs.go — Office documents for the agent (D201): reading PDFs, Word,
// Excel, PowerPoint and CSV files, and writing spreadsheets, Word documents
// and slide decks — with no coding sandbox. Every decoder runs inside this
// backend as type-safe code: Go for the Office formats (archive/zip +
// encoding/xml, bounded in docs_zip.go; excelize for workbooks), PDFium as
// WebAssembly on wazero for PDFs (docs_pdf.go).
//
//	doc_read(path, pages?, slides?, sheet?, range?, offset?, limit?)
//	    any of them as Markdown: a PDF's text per page, a deck per slide
//	    with its notes, a workbook as tables with cell addresses and
//	    formulas, a Word document as headings, lists and tables, a CSV
//	    by rows
//	doc_view(path, page)        a PDF page as a picture the model sees
//	sheet_write(path, ops)      change (or create) an .xlsx in place
//	doc_write(path, markdown)   write a .docx from Markdown
//	slides_write(path, deck)    write a .pptx from a deck
//
// What a write makes is a session file (a new version when it exists), and
// a preview — <path>.html, a static snapshot — opens in the render pane the
// way render_html's does, with a link to download the file itself. The
// tools are file tools: zero egress, nothing outside this run's files, in
// every class that has files (both lanes), switched by the "docs" feature.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// docEpoch: the timestamp in the zip entries of a package we write, so the
// same input gives the same bytes.
var docEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const docReadMaxChars = 48 << 10

var docToolNames = map[string]bool{
	"doc_read": true, "doc_view": true, "sheet_write": true, "doc_write": true, "slides_write": true,
}

const (
	mimeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimePptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimePDF  = "application/pdf"
)

func docToolSpecs(cfg Config) []toolSpec {
	anyPath := "a session file key ('report.pdf'), as file_read takes"
	if sandboxToolsOn(cfg) {
		anyPath += ", or a sandbox path (copied into the session files first)"
	}
	pageSpec := map[string]any{"type": "string", "description": "which ones, e.g. '1-3,7' (default: all, as far as the output limit goes)"}
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "doc_read",
			Description: "Read a document as Markdown: PDF (text per page), Word .docx (headings, lists, tables), Excel .xlsx (each sheet as a table with column letters and row numbers, its formulas listed with their results), PowerPoint .pptx (each slide's title, text, tables and speaker notes), CSV/TSV (as a table). " +
				"Long documents come in parts: pass pages (PDF), slides (pptx), sheet+range (xlsx) or offset/limit (lines of a docx, rows of a CSV) for the rest.",
			Parameters: obj([]string{"path"}, map[string]any{
				"path":   strProp(anyPath),
				"pages":  pageSpec,
				"slides": pageSpec,
				"sheet":  strProp("xlsx: one sheet by name (default: all)"),
				"range":  strProp("xlsx: cells to show, e.g. 'A1:H60' (default: the first 200 rows × 40 columns)"),
				"offset": map[string]any{"type": "integer", "description": "docx: first line; CSV: first data row (1-based)"},
				"limit":  map[string]any{"type": "integer", "description": "docx: lines; CSV: rows (default 200)"},
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "sheet_write",
			Description: "Change an Excel workbook (.xlsx) in place — creating it when the path is new — with a list of operations; formatting you don't touch is kept. " +
				"Values starting with '=' are formulas (=SUM(B2:B9)); their results are computed and reported. A preview of the workbook opens for the human. Ops: " +
				`{"op":"set","sheet":"Sales","cell":"A1","values":[["Region","Q1"],["EU",120]]} (a block from cell; null clears) · ` +
				`{"op":"formula","cell":"C2","formula":"=B2*1.2"} · ` +
				`{"op":"style","range":"A1:C1","bold":true,"fill":"#E8E9EE","color":"#0B0C12","numFmt":"#,##0.00","align":"center","wrap":true,"border":true,"size":12} · ` +
				`{"op":"width","cols":"A:C","width":18} · {"op":"height","rows":"1:1","height":24} · {"op":"freeze","cell":"A2"} · {"op":"merge","range":"A1:D1"} · ` +
				`{"op":"clear","range":"A10:D20"} · {"op":"filter","range":"A1:D50"} · {"op":"add_sheet","name":"Summary"} · {"op":"rename_sheet","from":"Sheet1","to":"Data"} · {"op":"delete_sheet","name":"Old"} · ` +
				`{"op":"chart","cell":"F2","type":"col|bar|line|pie|area|scatter|doughnut|col_stacked|bar_stacked","title":"…","categories":"A2:A9","series":[{"name":"Q1","values":"B2:B9"}]} (a series is named by the header cell above its values). ` +
				"An op's sheet defaults to the one the op before used (a new workbook's first sheet takes the first name you give).",
			Parameters: obj([]string{"path", "ops"}, map[string]any{
				"path": strProp("the .xlsx session file to change or create"),
				"ops":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "the operations, in order"},
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "doc_write",
			Description: "Write a Word document (.docx) from Markdown — # headings, paragraphs, **bold**, *italic*, `code`, [links](https://…), - and 1. lists (nested by indenting), > quotes, ``` code blocks, --- rules, pipe tables, and ![caption](chart.png) images from the session files. " +
				"Overwrites the file (its earlier version is kept). A preview opens for the human. To revise a document, doc_read it, edit the Markdown, and write it again.",
			Parameters: obj([]string{"path", "markdown"}, map[string]any{
				"path":     strProp("the .docx session file to write"),
				"markdown": strProp("the whole document"),
				"title":    strProp("the document's title in its properties (default: its first heading)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "slides_write",
			Description: "Write a PowerPoint deck (.pptx), 16:9, from slides. Layouts: title {title, subtitle} · content {title, bullets, and optionally a table {header, rows} or an image beside them} · two {title, left, right} (two bullet columns) · image {title, image, caption} · section {title, subtitle}. " +
				"bullets are strings with **bold**/*italic*; an array right after a bullet is its sub-bullets. image names a session PNG/JPEG. notes are speaker notes. theme {accent:'#RRGGBB', font, dark}. " +
				"Keep slides short: a title and 3–6 bullets. Overwrites the file (its earlier version is kept); a preview opens for the human. To revise a deck, doc_read it and write it again.",
			Parameters: obj([]string{"path", "slides"}, map[string]any{
				"path":   strProp("the .pptx session file to write"),
				"title":  strProp("the deck's title (its properties)"),
				"theme":  map[string]any{"type": "object", "description": "{accent, font, dark}"},
				"slides": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "the slides, in order"},
			}),
		}},
	}
	if cfg.feature("vision") {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "doc_view",
			Description: "Look at one page of a PDF as a picture — for charts, figures, scanned pages and layout that its text (doc_read) doesn't carry. " +
				"The picture is attached after this tool's result; only the few most recent pictures stay visible.",
			Parameters: obj([]string{"path", "page"}, map[string]any{
				"path": strProp(anyPath),
				"page": map[string]any{"type": "integer", "description": "the page, 1-based"},
			}),
		}})
	}
	return specs
}

// --- kinds ---------------------------------------------------------------------

// docKind is what doc_read can read a file as ("" when nothing).
func docKind(name, mime string, head []byte) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".docx", ".docm", ".dotx":
		return "docx"
	case ".xlsx", ".xlsm", ".xltx":
		return "xlsx"
	case ".pptx", ".pptm", ".potx":
		return "pptx"
	case ".csv", ".tsv":
		return "csv"
	}
	switch {
	case mime == mimePDF || bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf"
	case mime == mimeDocx:
		return "docx"
	case mime == mimeXlsx:
		return "xlsx"
	case mime == mimePptx:
		return "pptx"
	case mime == "text/csv" || mime == "application/csv" || mime == "text/tab-separated-values":
		return "csv"
	}
	return ""
}

var kindName = map[string]string{"pdf": "PDF", "docx": "Word document", "xlsx": "Excel workbook", "pptx": "PowerPoint deck", "csv": "CSV"}

// isDocFile: a session file doc_read reads.
func isDocFile(f *ReplFile) bool { return docKind(f.Path, f.Mime, nil) != "" }

// fileBytes is a session file's content, text or binary.
func (ag *Agent) fileBytes(ctx context.Context, f *ReplFile) ([]byte, error) {
	if !f.Binary {
		return []byte(f.Content), nil
	}
	return ag.readBlob(ctx, f.Blob)
}

// --- dispatch ---------------------------------------------------------------------

func (ag *Agent) runDocTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	if !cfg.feature("docs") {
		return "", fmt.Errorf("document tools are turned off for this conversation (the docs feature)")
	}
	switch name {
	case "doc_read":
		return ag.toolDocRead(ctx, run, cfg, args)
	case "doc_view":
		return ag.toolDocView(ctx, run, cfg, args)
	case "sheet_write":
		return ag.toolSheetWrite(ctx, run, cfg, args)
	case "doc_write":
		return ag.toolDocWrite(ctx, run, cfg, args)
	case "slides_write":
		return ag.toolSlidesWrite(ctx, run, cfg, args)
	}
	return "", fmt.Errorf("unknown document tool %q", name)
}

// docFile resolves a reading tool's path (copying a sandbox file in) and
// loads its bytes.
func (ag *Agent) docFile(ctx context.Context, run *Run, cfg Config, args map[string]any, tool string) (*ReplFile, []byte, string, error) {
	r, err := fileArg(cfg, args, "path")
	if err != nil {
		return nil, nil, "", err
	}
	var f *ReplFile
	note := ""
	if r.sandbox {
		f, note, err = ag.sessionCopy(ctx, run, cfg, r, tool)
	} else {
		f, err = ag.sessionFile(ctx, run, cfg, r)
	}
	if err != nil {
		return nil, nil, "", err
	}
	data, err := ag.fileBytes(ctx, f)
	if err != nil {
		return nil, nil, "", fmt.Errorf("loading %s failed: %v", f.Path, err)
	}
	return f, data, note, nil
}

func (ag *Agent) toolDocRead(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	f, data, note, err := ag.docFile(ctx, run, cfg, args, "doc_read")
	if err != nil {
		return "", err
	}
	out, err := readDocument(ctx, f.Path, f.Mime, data, args)
	if err != nil {
		return "", err
	}
	if note != "" {
		out = note + "\n" + out
	}
	return out, nil
}

// readDocument renders one document for doc_read (and for file_read on a
// document, with no arguments).
func readDocument(ctx context.Context, name, mime string, data []byte, args map[string]any) (string, error) {
	head := data
	if len(head) > 8 {
		head = head[:8]
	}
	kind := docKind(name, mime, head)
	if kind == "" {
		return "", fmt.Errorf("%s isn't a document doc_read reads (PDF, .docx, .xlsx, .pptx, CSV) — file_read reads text, file_view images", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	lead := fmt.Sprintf("%s — %s, %s\n\n", name, kindName[kind], humanBytes(len(data)))
	var body string
	var err error
	switch kind {
	case "pdf":
		want, label := pickPages(str(args["pages"]))
		var n int
		body, n, err = readPDF(ctx, data, want)
		lead = fmt.Sprintf("%s — PDF, %d page(s)%s, %s\n\n", name, n, label, humanBytes(len(data)))
	case "pptx":
		want, label := pickPages(str(args["slides"]))
		var n int
		body, n, err = readPptx(ctx, data, want)
		lead = fmt.Sprintf("%s — PowerPoint deck, %d slide(s)%s, %s\n\n", name, n, label, humanBytes(len(data)))
	case "xlsx":
		body, err = readXlsx(data, str(args["sheet"]), str(args["range"]))
	case "docx":
		body, err = readDocx(ctx, data)
		if err == nil {
			body = sliceLines(body, toInt(args["offset"]), toInt(args["limit"]))
		}
	case "csv":
		body, err = readCSV(data, name, toInt(args["offset"]), toInt(args["limit"]))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %v", name, err)
	}
	if len(body) > docReadMaxChars {
		cut := strings.LastIndex(body[:docReadMaxChars], "\n")
		if cut < docReadMaxChars/2 {
			cut = docReadMaxChars
		}
		more := "pass pages/slides/range/offset to read the rest"
		body = body[:cut] + "\n\n…[cut at " + humanBytes(docReadMaxChars) + " — " + more + "]"
	}
	return lead + body, nil
}

// pickPages reads "1-3,7" into a predicate (nil: all) and a label.
func pickPages(spec string) (func(int) bool, string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, ""
	}
	type span struct{ a, b int }
	var spans []span
	for _, part := range strings.Split(spec, ",") {
		a, b, ok := strings.Cut(strings.TrimSpace(part), "-")
		x, err1 := strconv.Atoi(strings.TrimSpace(a))
		y := x
		var err2 error
		if ok {
			y, err2 = strconv.Atoi(strings.TrimSpace(b))
		}
		if err1 != nil || err2 != nil || x < 1 {
			continue
		}
		spans = append(spans, span{min(x, y), max(x, y)})
	}
	if len(spans) == 0 {
		return nil, ""
	}
	return func(n int) bool {
		for _, s := range spans {
			if n >= s.a && n <= s.b {
				return true
			}
		}
		return false
	}, " (showing " + spec + ")"
}

// readCSV draws rows [offset, offset+limit) of a CSV (or TSV) as a table, the
// first row as its header.
func readCSV(data []byte, name string, offset, limit int) (string, error) {
	if limit <= 0 {
		limit = 200
	}
	if offset <= 0 {
		offset = 1
	}
	delim := ','
	first := data[:min(len(data), 4096)]
	if i := bytes.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	firstLine := string(first)
	if strings.HasSuffix(strings.ToLower(name), ".tsv") || strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") {
		delim = '\t'
	} else if strings.Count(firstLine, ";") > strings.Count(firstLine, ",") {
		delim = ';'
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = delim
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = false
	var header []string
	var rows [][]string
	n := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("the CSV doesn't parse at row %d: %v", n+1, err)
		}
		if header == nil {
			header = rec
			continue
		}
		n++
		if n >= offset && n < offset+limit {
			rows = append(rows, rec)
		}
	}
	if header == nil {
		return "(empty)\n", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d data row(s), %d column(s); showing rows %d–%d\n\n", n, len(header), offset, min(n, offset+limit-1))
	for i := range header {
		header[i] = clip(header[i], 80)
	}
	for _, r := range rows {
		for i := range r {
			r[i] = clip(r[i], 200)
		}
	}
	b.WriteString(mdTable(append([][]string{header}, rows...)))
	if offset+limit-1 < n {
		fmt.Fprintf(&b, "\n(more: offset %d)\n", offset+limit)
	}
	return b.String(), nil
}

// --- doc_view ---------------------------------------------------------------------

func (ag *Agent) toolDocView(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	if !cfg.feature("vision") {
		return "", fmt.Errorf("vision is turned off for this run, so pages cannot be shown")
	}
	f, data, note, err := ag.docFile(ctx, run, cfg, args, "doc_view")
	if err != nil {
		return "", err
	}
	head := data[:min(len(data), 8)]
	if docKind(f.Path, f.Mime, head) != "pdf" {
		if visionMimes[f.Mime] {
			return "", fmt.Errorf("%s is an image — file_view shows it", f.Path)
		}
		return "", fmt.Errorf("doc_view shows PDF pages; %s isn't a PDF — doc_read reads it as text", f.Path)
	}
	page := max(toInt(args["page"]), 1)
	img, total, err := renderPDFPage(ctx, data, page)
	if err != nil {
		return "", fmt.Errorf("%s: %v", f.Path, err)
	}
	if len(img) > maxInlineImageBytes {
		return "", fmt.Errorf("page %d of %s draws to %s, over the %s limit for showing a picture", page, f.Path, humanBytes(len(img)), humanBytes(maxInlineImageBytes))
	}
	key := pageImageKey(f.Path, page)
	pf, _, _, err := ag.putFileData(ctx, run.ID, key, img, "image/png", toolSource(ctx, "doc_view"))
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("%s%s (%s, %s) — page %d of %d of %s; the picture is attached after these tool results.",
		fileViewOK, pf.Path, pf.Mime, humanBytes(pf.Bytes), page, total, f.Path)
	if note != "" {
		out += "\n(" + note + ")"
	}
	return out, nil
}

// pageImageKey: where a page's picture is kept — beside the PDF, one per
// page (a second look at it is a new version).
func pageImageKey(doc string, page int) string {
	stem := strings.TrimSuffix(doc, path.Ext(doc))
	k := fmt.Sprintf("%s.p%d.png", stem, page)
	if len(k) > 120 {
		k = fmt.Sprintf("page-%d-%s.png", page, shaHex([]byte(doc))[:12])
	}
	return k
}

// --- the writers ---------------------------------------------------------------------

// writeTarget checks a writing tool's path: a session key with the right
// extension.
func writeTarget(args map[string]any, ext string) (string, error) {
	p, err := normReplPath(str(args["path"]))
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(path.Ext(p), ext) {
		return "", fmt.Errorf("the path must end in %s (got %q)", ext, p)
	}
	return p, nil
}

// sessionImages loads the session images a document names (PNG, JPEG, GIF).
func (ag *Agent) sessionImages(ctx context.Context, run *Run, names []string) map[string]docImage {
	out := map[string]docImage{}
	for _, n := range names {
		if _, done := out[n]; done || len(out) >= 40 {
			continue
		}
		key, err := normReplPath(strings.TrimPrefix(n, "session:"))
		if err != nil {
			continue
		}
		f, err := ag.db.replFile(run.ID, key)
		if err != nil || !f.Binary || !visionMimes[f.Mime] || f.Bytes > 8<<20 {
			continue
		}
		if b, err := ag.readBlob(ctx, f.Blob); err == nil {
			out[n] = docImage{data: b, mime: f.Mime}
		}
	}
	return out
}

// showDoc stores what a writer made and its preview, and opens the preview
// in the render pane (a render step naming the document, so the chat
// offers its download).
func (ag *Agent) showDoc(ctx context.Context, run *Run, tool, key, mime string, data []byte, preview string) (*ReplFile, string, error) {
	f, prev, unchanged, err := ag.putFileData(ctx, run.ID, key, data, mime, toolSource(ctx, tool))
	if err != nil {
		return nil, "", err
	}
	what := fmt.Sprintf("wrote %s (%s, v%d)", f.Path, humanBytes(f.Bytes), f.Version)
	if unchanged {
		what = fmt.Sprintf("%s is unchanged (%s, v%d)", f.Path, humanBytes(f.Bytes), f.Version)
	} else if prev != nil {
		what += fmt.Sprintf(" — v%d is kept", prev.Version)
	}
	if preview == "" {
		return f, what, nil
	}
	pkey := key + ".html"
	if len(pkey) > 128 {
		pkey = "preview-" + shaHex([]byte(key))[:12] + ".html"
	}
	if len(preview) > maxRenderBytes {
		preview = preview[:maxRenderBytes-64] + "</body></html>"
	}
	pf, _, _, err := ag.putFileData(ctx, run.ID, pkey, []byte(preview), "text/html", toolSource(ctx, tool))
	if err != nil {
		return f, what + "; the preview couldn't be stored: " + err.Error(), nil
	}
	st := ag.db.journal(run.ID, "render", map[string]any{
		"path": pf.Path, "version": pf.Version, "bytes": pf.Bytes,
		"doc": f.Path, "docVersion": f.Version, "docBytes": f.Bytes, "docMime": mime,
	})
	if ag.eng != nil {
		ag.eng.emitStep(ag.db, rootOf(run), st)
	}
	return f, what + fmt.Sprintf(" — its preview (%s) is shown to the human, with a download of the file", pf.Path), nil
}

func (ag *Agent) toolSheetWrite(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	key, err := writeTarget(args, ".xlsx")
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(args["ops"])
	var ops []sheetOp
	if err := json.Unmarshal(raw, &ops); err != nil || len(ops) == 0 {
		return "", fmt.Errorf("ops must be a non-empty array of operations (%v)", err)
	}
	if len(ops) > 500 {
		return "", fmt.Errorf("at most 500 ops per call")
	}
	var data []byte
	if f, err := ag.db.replFile(run.ID, key); err == nil {
		if data, err = ag.fileBytes(ctx, f); err != nil {
			return "", fmt.Errorf("loading %s failed: %v", key, err)
		}
	}
	out, res, err := writeSheet(data, ops)
	if err != nil {
		return "", err
	}
	preview, perr := xlsxPreviewHTML(out, key)
	if perr != nil {
		preview = ""
	}
	_, what, err := ag.showDoc(ctx, run, "sheet_write", key, mimeXlsx, out, preview)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(what + "\n")
	for _, l := range res.done {
		b.WriteString("- " + l + "\n")
	}
	if len(res.results) > 0 {
		b.WriteString("Computed:\n")
		for _, l := range res.results {
			b.WriteString("- " + l + "\n")
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func (ag *Agent) toolDocWrite(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	key, err := writeTarget(args, ".docx")
	if err != nil {
		return "", err
	}
	md := str(args["markdown"])
	if strings.TrimSpace(md) == "" {
		return "", fmt.Errorf("markdown is required (the whole document)")
	}
	if len(md) > 2<<20 {
		return "", fmt.Errorf("the document is over 2 MB of Markdown")
	}
	blocks := parseMarkdown(md)
	title := str(args["title"])
	if title == "" {
		for _, b := range blocks {
			if b.Kind == "h" {
				title = plainInline(b.Text)
				break
			}
		}
	}
	images := ag.sessionImages(ctx, run, mdImageRefs(blocks))
	out, missing, err := writeDocx(blocks, title, images, time.Now())
	if err != nil {
		return "", err
	}
	_, what, err := ag.showDoc(ctx, run, "doc_write", key, mimeDocx, out, docxPreviewHTML(blocks, orStr(title, key), images))
	if err != nil {
		return "", err
	}
	return what + missingNote(missing), nil
}

func (ag *Agent) toolSlidesWrite(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	key, err := writeTarget(args, ".pptx")
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(map[string]any{"title": args["title"], "theme": args["theme"], "slides": args["slides"]})
	var d deckSpec
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", fmt.Errorf("slides must be an array of slide objects (%v)", err)
	}
	var names []string
	for _, s := range d.Slides {
		if s.Image != "" {
			names = append(names, s.Image)
		}
	}
	images := ag.sessionImages(ctx, run, names)
	out, missing, err := writePptx(d, images, time.Now())
	if err != nil {
		return "", err
	}
	_, what, err := ag.showDoc(ctx, run, "slides_write", key, mimePptx, out, deckPreviewHTML(d, images))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s — %d slide(s)%s", what, len(d.Slides), missingNote(missing)), nil
}

func missingNote(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return "\nnot found or not a PNG/JPEG/GIF in the session files (written as their alt text): " + strings.Join(missing, ", ")
}

// mdImageRefs lists the images a document's Markdown names.
func mdImageRefs(blocks []mdBlock) []string {
	var out []string
	var inline func(string)
	inline = func(s string) {
		for _, r := range parseInline(s) {
			if r.Image != "" {
				out = append(out, r.Image)
			}
		}
	}
	var walk func([]mdBlock)
	walk = func(bs []mdBlock) {
		for _, b := range bs {
			switch b.Kind {
			case "img":
				out = append(out, b.Src)
			case "p", "h", "quote":
				inline(b.Text)
			case "ul", "ol":
				for _, it := range b.Items {
					inline(it.Text)
					if it.Sub != nil {
						walk([]mdBlock{*it.Sub})
					}
				}
			case "table":
				for _, r := range b.Rows {
					for _, c := range r {
						inline(c)
					}
				}
			}
		}
	}
	walk(blocks)
	return out
}

// docKeys: a map's keys, sorted.
func docKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
