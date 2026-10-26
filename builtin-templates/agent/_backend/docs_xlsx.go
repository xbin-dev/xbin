// docs_xlsx.go — spreadsheets (D201) through excelize (BSD-3, pure Go): a
// workbook read into Markdown tables with its cell addresses and formulas
// (doc_read), changed in place by a list of operations that keeps every
// formatting it doesn't touch (sheet_write), formulas computed by
// excelize's own engine (CalcCellValue), and drawn as an HTML preview.
// excelize's unzip limits bound what a hostile workbook can make it read;
// a panic inside it is an error, never a crashed backend.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	xlsxMaxRows  = 200 // rows doc_read shows per call (range pages through)
	xlsxMaxCols  = 40
	xlsxMaxCells = 200_000 // cells one sheet_write may touch
)

// openXlsx opens a workbook from bytes, bounded.
func openXlsx(data []byte) (f *excelize.File, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, err = nil, fmt.Errorf("the workbook doesn't open: %v", r)
		}
	}()
	f, err = excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		UnzipSizeLimit: docMaxUnzipped, UnzipXMLSizeLimit: docMaxPart,
	})
	if err != nil {
		return nil, fmt.Errorf("the workbook doesn't open: %v", err)
	}
	return f, nil
}

// xlsxRange is a rectangle of cells, 1-based and inclusive.
type xlsxRange struct{ c1, r1, c2, r2 int }

func parseRange(s string) (xlsxRange, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "$", ""))
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		b = a
	}
	c1, r1, err := excelize.CellNameToCoordinates(a)
	if err != nil {
		return xlsxRange{}, fmt.Errorf("bad range %q (like A1:F50)", s)
	}
	c2, r2, err := excelize.CellNameToCoordinates(b)
	if err != nil {
		return xlsxRange{}, fmt.Errorf("bad range %q (like A1:F50)", s)
	}
	return xlsxRange{min(c1, c2), min(r1, r2), max(c1, c2), max(r1, r2)}, nil
}

func cellName(c, r int) string { n, _ := excelize.CoordinatesToCellName(c, r); return n }
func colName(c int) string     { n, _ := excelize.ColumnNumberToName(c); return n }

// readXlsx: each sheet (or the one named) as a table — column letters
// across, row numbers down — its formulas listed below it, at most
// xlsxMaxRows × xlsxMaxCols cells per sheet unless a range asks for others.
func readXlsx(data []byte, sheet, rng string) (out string, err error) {
	f, err := openXlsx(data)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reading the workbook failed: %v", r)
		}
	}()
	sheets := f.GetSheetList()
	var b strings.Builder
	fmt.Fprintf(&b, "Workbook: %d sheet(s): %s\n\n", len(sheets), strings.Join(sheets, ", "))
	for _, sh := range sheets {
		if sheet != "" && !strings.EqualFold(sh, sheet) {
			continue
		}
		b.WriteString(sheetMarkdown(f, sh, rng))
	}
	if sheet != "" && !containsFold(sheets, sheet) {
		return "", fmt.Errorf("no sheet %q (sheets: %s)", sheet, strings.Join(sheets, ", "))
	}
	return b.String(), nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// usedRange: the sheet's filled rectangle (from its dimension, else its rows).
func usedRange(f *excelize.File, sh string) (xlsxRange, [][]string) {
	rows, _ := f.GetRows(sh)
	r := xlsxRange{1, 1, 1, len(rows)}
	for _, row := range rows {
		r.c2 = max(r.c2, len(row))
	}
	if dim, err := f.GetSheetDimension(sh); err == nil && dim != "" {
		if d, err := parseRange(dim); err == nil && d.r2 >= len(rows) {
			r.c2 = max(r.c2, d.c2)
		}
	}
	return r, rows
}

func sheetMarkdown(f *excelize.File, sh, rng string) string {
	used, rows := usedRange(f, sh)
	view := xlsxRange{1, 1, min(used.c2, xlsxMaxCols), min(used.r2, xlsxMaxRows)}
	if rng != "" {
		if r, err := parseRange(rng); err == nil {
			view = xlsxRange{r.c1, r.r1, min(r.c2, r.c1+xlsxMaxCols-1), min(r.r2, r.r1+xlsxMaxRows-1)}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Sheet: %s (used %s:%s, %d rows × %d columns)\n\n", sh,
		cellName(1, 1), cellName(max(used.c2, 1), max(used.r2, 1)), used.r2, used.c2)
	if used.r2 == 0 {
		b.WriteString("(empty)\n\n")
		return b.String()
	}
	b.WriteString("| |")
	for c := view.c1; c <= view.c2; c++ {
		b.WriteString(" " + colName(c) + " |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", view.c2-view.c1+1) + "\n")
	var formulas []string
	for r := view.r1; r <= view.r2; r++ {
		fmt.Fprintf(&b, "| %d |", r)
		for c := view.c1; c <= view.c2; c++ {
			v := ""
			if r-1 < len(rows) && c-1 < len(rows[r-1]) {
				v = rows[r-1][c-1]
			}
			name := cellName(c, r)
			if fx, _ := f.GetCellFormula(sh, name); fx != "" {
				if v == "" {
					v, _ = f.CalcCellValue(sh, name)
				}
				if len(formulas) < 200 {
					formulas = append(formulas, fmt.Sprintf("%s =%s → %s", name, fx, v))
				}
			}
			b.WriteString(" " + strings.ReplaceAll(strings.ReplaceAll(v, "|", `\|`), "\n", " ") + " |")
		}
		b.WriteString("\n")
	}
	if view.r2 < used.r2 || view.c2 < used.c2 {
		fmt.Fprintf(&b, "\n(showing %s:%s — pass range to read others, e.g. %s:%s)\n",
			cellName(view.c1, view.r1), cellName(view.c2, view.r2),
			cellName(view.c1, view.r2+1), cellName(view.c2, min(view.r2+xlsxMaxRows, used.r2)))
	}
	if len(formulas) > 0 {
		b.WriteString("\nFormulas:\n")
		for _, l := range formulas {
			b.WriteString("- " + l + "\n")
		}
	}
	if m, _ := f.GetMergeCells(sh, true); len(m) > 0 {
		var ms []string
		for _, x := range m[:min(len(m), 50)] {
			ms = append(ms, x.GetStartAxis()+":"+x.GetEndAxis())
		}
		b.WriteString("\nMerged: " + strings.Join(ms, ", ") + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// --- sheet_write -------------------------------------------------------------

// sheetOp is one operation of sheet_write.
type sheetOp struct {
	Op      string          `json:"op"`
	Sheet   string          `json:"sheet"`
	Cell    string          `json:"cell"`
	Range   string          `json:"range"`
	Values  [][]any         `json:"values"`
	Value   json.RawMessage `json:"value"`
	Formula string          `json:"formula"`
	// style
	Bold   *bool   `json:"bold"`
	Italic *bool   `json:"italic"`
	Color  string  `json:"color"`
	Fill   string  `json:"fill"`
	NumFmt string  `json:"numFmt"`
	Align  string  `json:"align"`
	Wrap   *bool   `json:"wrap"`
	Border *bool   `json:"border"`
	Size   float64 `json:"size"`
	// layout
	Cols   string  `json:"cols"`
	Rows   string  `json:"rows"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Name   string  `json:"name"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	// chart
	Type       string        `json:"type"`
	Title      string        `json:"title"`
	Categories string        `json:"categories"`
	Series     []chartSeries `json:"series"`
}

type chartSeries struct {
	Name   string `json:"name"`
	Values string `json:"values"`
}

// sheetResult is what sheet_write reports.
type sheetResult struct {
	done    []string // one line per op
	results []string // computed formula cells
}

// writeSheet applies ops to the workbook (nil data: a new one) and returns
// its bytes.
func writeSheet(data []byte, ops []sheetOp) (out []byte, res sheetResult, err error) {
	var f *excelize.File
	if data == nil {
		f = excelize.NewFile()
	} else if f, err = openXlsx(data); err != nil {
		return nil, res, err
	}
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the change failed inside the spreadsheet library: %v", r)
		}
	}()
	fresh := data == nil
	cur := f.GetSheetName(f.GetActiveSheetIndex())
	touched := 0
	var formulaCells [][2]string
	for i, op := range ops {
		sh := cur
		if op.Sheet != "" {
			sh = op.Sheet
			// a new workbook's first named sheet takes the place of Sheet1
			if fresh && i == 0 && op.Op != "add_sheet" && !containsFold(f.GetSheetList(), sh) {
				if err := f.SetSheetName(cur, sh); err != nil {
					return nil, res, err
				}
			}
			if !containsFold(f.GetSheetList(), sh) && op.Op != "add_sheet" {
				if _, err := f.NewSheet(sh); err != nil {
					return nil, res, fmt.Errorf("op %d: %v", i+1, err)
				}
				res.done = append(res.done, "added sheet "+sh)
			}
		}
		fail := func(e error) ([]byte, sheetResult, error) {
			return nil, res, fmt.Errorf("op %d (%s): %v", i+1, op.Op, e)
		}
		switch strings.ToLower(op.Op) {
		case "set":
			start := strings.ToUpper(orStr(op.Cell, "A1"))
			c0, r0, err := excelize.CellNameToCoordinates(start)
			if err != nil {
				return fail(err)
			}
			vals := op.Values
			if vals == nil && len(op.Value) > 0 {
				var v any
				if err := json.Unmarshal(op.Value, &v); err != nil {
					return fail(err)
				}
				vals = [][]any{{v}}
			}
			for dr, row := range vals {
				for dc, v := range row {
					if touched++; touched > xlsxMaxCells {
						return fail(fmt.Errorf("at most %d cells per call", xlsxMaxCells))
					}
					name := cellName(c0+dc, r0+dr)
					if s, ok := v.(string); ok && strings.HasPrefix(s, "=") && len(s) > 1 {
						if err := f.SetCellFormula(sh, name, s[1:]); err != nil {
							return fail(err)
						}
						formulaCells = append(formulaCells, [2]string{sh, name})
						continue
					}
					if err := setCell(f, sh, name, v); err != nil {
						return fail(err)
					}
				}
			}
			end := start
			if len(vals) > 0 {
				w := 0
				for _, r := range vals {
					w = max(w, len(r))
				}
				end = cellName(c0+max(w, 1)-1, r0+len(vals)-1)
			}
			res.done = append(res.done, fmt.Sprintf("set %s!%s:%s", sh, start, end))
		case "formula":
			fx := strings.TrimPrefix(strings.TrimSpace(op.Formula), "=")
			if fx == "" || op.Cell == "" {
				return fail(fmt.Errorf("formula needs cell and formula"))
			}
			if err := f.SetCellFormula(sh, strings.ToUpper(op.Cell), fx); err != nil {
				return fail(err)
			}
			formulaCells = append(formulaCells, [2]string{sh, strings.ToUpper(op.Cell)})
			res.done = append(res.done, fmt.Sprintf("formula %s!%s =%s", sh, strings.ToUpper(op.Cell), fx))
		case "style":
			r, err := parseRange(orStr(op.Range, op.Cell))
			if err != nil {
				return fail(err)
			}
			st := &excelize.Style{}
			if op.Bold != nil || op.Italic != nil || op.Color != "" || op.Size > 0 {
				st.Font = &excelize.Font{Color: hexOf(op.Color)}
				if op.Bold != nil {
					st.Font.Bold = *op.Bold
				}
				if op.Italic != nil {
					st.Font.Italic = *op.Italic
				}
				if op.Size > 0 && op.Size <= 96 {
					st.Font.Size = op.Size
				}
			}
			if h := hexOf(op.Fill); h != "" {
				st.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{h}}
			}
			if op.NumFmt != "" {
				nf := op.NumFmt
				st.CustomNumFmt = &nf
			}
			if op.Align != "" || (op.Wrap != nil && *op.Wrap) {
				st.Alignment = &excelize.Alignment{Horizontal: strings.ToLower(op.Align), WrapText: op.Wrap != nil && *op.Wrap}
			}
			if op.Border != nil && *op.Border {
				for _, side := range []string{"left", "right", "top", "bottom"} {
					st.Border = append(st.Border, excelize.Border{Type: side, Color: "CDD0D8", Style: 1})
				}
			}
			id, err := f.NewStyle(st)
			if err != nil {
				return fail(err)
			}
			if err := f.SetCellStyle(sh, cellName(r.c1, r.r1), cellName(r.c2, r.r2), id); err != nil {
				return fail(err)
			}
			res.done = append(res.done, fmt.Sprintf("styled %s!%s:%s", sh, cellName(r.c1, r.r1), cellName(r.c2, r.r2)))
		case "width":
			a, b, _ := strings.Cut(strings.ToUpper(orStr(op.Cols, op.Range)), ":")
			if b == "" {
				b = a
			}
			if err := f.SetColWidth(sh, a, b, math.Max(1, math.Min(op.Width, 255))); err != nil {
				return fail(err)
			}
			res.done = append(res.done, fmt.Sprintf("width %s:%s = %g", a, b, op.Width))
		case "height":
			a, b, _ := strings.Cut(orStr(op.Rows, op.Range), ":")
			r1, err1 := strconv.Atoi(strings.TrimSpace(a))
			r2, err2 := strconv.Atoi(strings.TrimSpace(orStr(b, a)))
			if err1 != nil || err2 != nil {
				return fail(fmt.Errorf("rows like \"1:3\""))
			}
			for r := min(r1, r2); r <= max(r1, r2) && r-min(r1, r2) < 10000; r++ {
				if err := f.SetRowHeight(sh, r, math.Max(1, math.Min(op.Height, 409))); err != nil {
					return fail(err)
				}
			}
			res.done = append(res.done, fmt.Sprintf("height rows %s = %g", orStr(op.Rows, op.Range), op.Height))
		case "freeze":
			c, r, err := excelize.CellNameToCoordinates(strings.ToUpper(orStr(op.Cell, "A2")))
			if err != nil {
				return fail(err)
			}
			pane := "bottomLeft"
			if c > 1 && r > 1 {
				pane = "bottomRight"
			} else if c > 1 {
				pane = "topRight"
			}
			if err := f.SetPanes(sh, &excelize.Panes{Freeze: true, XSplit: c - 1, YSplit: r - 1, TopLeftCell: cellName(c, r), ActivePane: pane}); err != nil {
				return fail(err)
			}
			res.done = append(res.done, "froze above/left of "+cellName(c, r))
		case "merge":
			r, err := parseRange(op.Range)
			if err != nil {
				return fail(err)
			}
			if err := f.MergeCell(sh, cellName(r.c1, r.r1), cellName(r.c2, r.r2)); err != nil {
				return fail(err)
			}
			res.done = append(res.done, "merged "+op.Range)
		case "clear":
			r, err := parseRange(orStr(op.Range, op.Cell))
			if err != nil {
				return fail(err)
			}
			if (r.c2-r.c1+1)*(r.r2-r.r1+1) > xlsxMaxCells {
				return fail(fmt.Errorf("at most %d cells per call", xlsxMaxCells))
			}
			for rr := r.r1; rr <= r.r2; rr++ {
				for cc := r.c1; cc <= r.c2; cc++ {
					_ = f.SetCellFormula(sh, cellName(cc, rr), "")
					_ = f.SetCellValue(sh, cellName(cc, rr), nil)
				}
			}
			res.done = append(res.done, "cleared "+orStr(op.Range, op.Cell))
		case "filter":
			r, err := parseRange(op.Range)
			if err != nil {
				return fail(err)
			}
			if err := f.AutoFilter(sh, cellName(r.c1, r.r1)+":"+cellName(r.c2, r.r2), nil); err != nil {
				return fail(err)
			}
			res.done = append(res.done, "filter on "+op.Range)
		case "add_sheet":
			name := orStr(op.Name, op.Sheet)
			if name == "" {
				return fail(fmt.Errorf("add_sheet needs name"))
			}
			if _, err := f.NewSheet(name); err != nil {
				return fail(err)
			}
			res.done = append(res.done, "added sheet "+name)
			cur = name
		case "rename_sheet":
			if err := f.SetSheetName(orStr(op.From, sh), op.To); err != nil {
				return fail(err)
			}
			res.done = append(res.done, fmt.Sprintf("renamed sheet %s → %s", orStr(op.From, sh), op.To))
			if strings.EqualFold(cur, orStr(op.From, sh)) {
				cur = op.To
			}
		case "delete_sheet":
			name := orStr(op.Name, op.Sheet)
			if len(f.GetSheetList()) <= 1 {
				return fail(fmt.Errorf("a workbook keeps at least one sheet"))
			}
			if err := f.DeleteSheet(name); err != nil {
				return fail(err)
			}
			res.done = append(res.done, "deleted sheet "+name)
			cur = f.GetSheetName(f.GetActiveSheetIndex())
		case "chart":
			if err := addChart(f, sh, op); err != nil {
				return fail(err)
			}
			res.done = append(res.done, fmt.Sprintf("%s chart at %s!%s", orStr(op.Type, "col"), sh, strings.ToUpper(orStr(op.Cell, "H2"))))
		default:
			return fail(fmt.Errorf("unknown op (set, formula, style, width, height, freeze, merge, clear, filter, add_sheet, rename_sheet, delete_sheet, chart)"))
		}
		if op.Sheet != "" && strings.ToLower(op.Op) != "delete_sheet" && strings.ToLower(op.Op) != "rename_sheet" {
			cur = sh
		}
	}
	// Excel and LibreOffice recompute on open: a workbook written here has
	// no cached results for its formulas
	t := true
	_ = f.SetCalcProps(&excelize.CalcPropsOptions{FullCalcOnLoad: &t})
	for _, fc := range formulaCells[:min(len(formulaCells), 100)] {
		v, err := f.CalcCellValue(fc[0], fc[1])
		if err != nil {
			res.results = append(res.results, fmt.Sprintf("%s!%s → error: %v", fc[0], fc[1], err))
		} else {
			res.results = append(res.results, fmt.Sprintf("%s!%s → %s", fc[0], fc[1], v))
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, res, err
	}
	return buf.Bytes(), res, nil
}

func setCell(f *excelize.File, sh, name string, v any) error {
	switch x := v.(type) {
	case nil:
		_ = f.SetCellFormula(sh, name, "")
		return f.SetCellValue(sh, name, nil)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return f.SetCellValue(sh, name, int64(x))
		}
		return f.SetCellValue(sh, name, x)
	case bool:
		return f.SetCellValue(sh, name, x)
	case string:
		return f.SetCellStr(sh, name, x)
	default:
		b, _ := json.Marshal(x)
		return f.SetCellStr(sh, name, string(b))
	}
}

func hexOf(s string) string {
	if m := hexColorRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

func addChart(f *excelize.File, sh string, op sheetOp) error {
	types := map[string]excelize.ChartType{
		"col": excelize.Col, "column": excelize.Col, "bar": excelize.Bar, "line": excelize.Line,
		"pie": excelize.Pie, "area": excelize.Area, "scatter": excelize.Scatter, "doughnut": excelize.Doughnut,
		"col_stacked": excelize.ColStacked, "bar_stacked": excelize.BarStacked,
	}
	t, ok := types[strings.ToLower(orStr(op.Type, "col"))]
	if !ok {
		return fmt.Errorf("chart type: col, bar, line, pie, area, scatter, doughnut, col_stacked, bar_stacked")
	}
	if len(op.Series) == 0 {
		return fmt.Errorf("a chart needs series [{name, values}]")
	}
	ref := func(s string) string {
		if s == "" || strings.Contains(s, "!") {
			return s
		}
		r, err := parseRange(s)
		if err != nil {
			return s
		}
		q := "'" + strings.ReplaceAll(sh, "'", "''") + "'"
		return fmt.Sprintf("%s!$%s$%d:$%s$%d", q, colName(r.c1), r.r1, colName(r.c2), r.r2)
	}
	// a series' name in a chart is a cell: the header above its values when
	// it says the name (or no name was given), a cell named outright, else none
	name := func(s chartSeries) string {
		if strings.Contains(s.Name, "!") {
			return s.Name
		}
		if r, err := parseRange(s.Values); err == nil && r.r1 > 1 {
			hc := cellName(r.c1, r.r1-1)
			if v, _ := f.GetCellValue(sh, hc); s.Name == "" || strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s.Name)) {
				return ref(hc)
			}
		}
		if _, _, err := excelize.CellNameToCoordinates(strings.ToUpper(s.Name)); err == nil {
			return ref(strings.ToUpper(s.Name))
		}
		return ""
	}
	ch := &excelize.Chart{Type: t, Title: excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: op.Title}}}}
	for _, s := range op.Series[:min(len(op.Series), 16)] {
		ch.Series = append(ch.Series, excelize.ChartSeries{Name: name(s), Categories: ref(op.Categories), Values: ref(s.Values)})
	}
	return f.AddChart(sh, strings.ToUpper(orStr(op.Cell, "H2")), ch)
}

// --- the preview ---------------------------------------------------------------

// xlsxPreviewHTML draws each sheet (at most 6, 120 rows × 30 columns) as a
// table: bold and fills as the cells have them, numbers to the right.
func xlsxPreviewHTML(data []byte, title string) (out string, err error) {
	f, err := openXlsx(data)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("drawing the preview failed: %v", r)
		}
	}()
	var b strings.Builder
	b.WriteString(previewHead(title, `
table{border-collapse:collapse;font:13px/1.35 Arial,sans-serif;margin:0 0 28px;background:#fff}
th,td{border:1px solid #CDD0D8;padding:3px 8px;white-space:nowrap;max-width:320px;overflow:hidden;text-overflow:ellipsis}
th{background:#E8E9EE;color:#4B4D5C;font-weight:600;position:sticky;top:0}
td.n{text-align:right;font-variant-numeric:tabular-nums}
th.r{text-align:right;min-width:28px}
td.fx{background:#F4F6FF}
h2{font:600 15px Arial,sans-serif;margin:18px 0 8px}
.wrap{overflow:auto}
.more{color:#4B4D5C;font:12px Arial,sans-serif;margin:-20px 0 24px}`))
	sheets := f.GetSheetList()
	styleCache := map[int]*excelize.Style{}
	for si, sh := range sheets {
		if si >= 6 {
			fmt.Fprintf(&b, `<p class="more">… and %d more sheet(s)</p>`, len(sheets)-si)
			break
		}
		used, rows := usedRange(f, sh)
		fmt.Fprintf(&b, "<h2>%s</h2>", html.EscapeString(sh))
		if used.r2 == 0 {
			b.WriteString(`<p class="more">(empty)</p>`)
			continue
		}
		nr, nc := min(used.r2, 120), min(used.c2, 30)
		b.WriteString(`<div class="wrap"><table><thead><tr><th class="r"></th>`)
		for c := 1; c <= nc; c++ {
			b.WriteString("<th>" + colName(c) + "</th>")
		}
		b.WriteString("</tr></thead><tbody>")
		for r := 1; r <= nr; r++ {
			fmt.Fprintf(&b, `<tr><th class="r">%d</th>`, r)
			for c := 1; c <= nc; c++ {
				name := cellName(c, r)
				v := ""
				if r-1 < len(rows) && c-1 < len(rows[r-1]) {
					v = rows[r-1][c-1]
				}
				fx, _ := f.GetCellFormula(sh, name)
				if fx != "" && v == "" {
					v, _ = f.CalcCellValue(sh, name)
				}
				var cls []string
				if _, err := strconv.ParseFloat(strings.NewReplacer(",", "", "%", "", "$", "", "€", "", "£", "").Replace(strings.TrimSpace(v)), 64); err == nil && v != "" {
					cls = append(cls, "n")
				}
				if fx != "" {
					cls = append(cls, "fx")
				}
				st := ""
				if id, err := f.GetCellStyle(sh, name); err == nil && id > 0 {
					s, ok := styleCache[id]
					if !ok {
						s, _ = f.GetStyle(id)
						styleCache[id] = s
					}
					if s != nil {
						if s.Font != nil && s.Font.Bold {
							st += "font-weight:700;"
						}
						if s.Font != nil && s.Font.Italic {
							st += "font-style:italic;"
						}
						if len(s.Fill.Color) > 0 && hexOf(s.Fill.Color[0]) != "" {
							st += "background:#" + hexOf(s.Fill.Color[0]) + ";"
						}
						if s.Font != nil && hexOf(s.Font.Color) != "" {
							st += "color:#" + hexOf(s.Font.Color) + ";"
						}
					}
				}
				attrs := ""
				if len(cls) > 0 {
					attrs += ` class="` + strings.Join(cls, " ") + `"`
				}
				if st != "" {
					attrs += ` style="` + st + `"`
				}
				if fx != "" {
					attrs += ` title="=` + html.EscapeString(fx) + `"`
				}
				b.WriteString("<td" + attrs + ">" + html.EscapeString(v) + "</td>")
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</tbody></table></div>")
		if nr < used.r2 || nc < used.c2 {
			fmt.Fprintf(&b, `<p class="more">showing %s:%s of %s:%s</p>`, "A1", cellName(nc, nr), "A1", cellName(used.c2, used.r2))
		}
	}
	b.WriteString("</body></html>")
	return b.String(), nil
}
