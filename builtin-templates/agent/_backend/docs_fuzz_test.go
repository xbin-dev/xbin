package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"testing"
	"time"
)

// The document readers take a stranger's bytes: none may panic, hang or
// grow without bound on any input (D201). Seeds are our own writers' output
// and a few broken shapes; `go test -fuzz FuzzDocx -fuzztime 30s` digs.

func fuzzCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func docxSeed(tb testing.TB) []byte {
	d, _, err := writeDocx(parseMarkdown("# T\n\n- a\n  - b\n\n| x | y |\n|---|---|\n| 1 | 2 |\n\n**b** [l](https://e.x)"), "T", nil, docEpoch)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

func FuzzDocxRead(f *testing.F) {
	f.Add(docxSeed(f))
	f.Add([]byte("PK\x03\x04"))
	f.Add(zipOf(f, [2]string{"word/document.xml", "<w:document><w:body><w:p><w:r><w:t>x"}))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = readDocx(fuzzCtx(t), data)
	})
}

func FuzzPptxRead(f *testing.F) {
	deck, _, err := writePptx(deckSpec{Slides: []slideSpec{{Title: "a", Bullets: []any{"b", []any{"c"}}, Notes: "n"},
		{Title: "t", Table: &slideTbl{Header: []string{"h"}, Rows: [][]string{{"r"}}}}}}, nil, docEpoch)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(deck)
	f.Add(zipOf(f, [2]string{"ppt/presentation.xml", `<p:presentation xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId r:id="rId1"/></p:sldIdLst></p:presentation>`},
		[2]string{"ppt/_rels/presentation.xml.rels", `<Relationships><Relationship Id="rId1" Target="../../../../etc/passwd"/></Relationships>`}))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = readPptx(fuzzCtx(t), data, nil)
	})
}

func FuzzXlsxRead(f *testing.F) {
	var ops []sheetOp
	ops = append(ops, sheetOp{Op: "set", Cell: "A1", Values: [][]any{{"a", 1.0}, {"b", "=B1*2"}}})
	x, _, err := writeSheet(nil, ops)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(x)
	f.Add([]byte("PK\x03\x04garbage"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = readXlsx(data, "", "")
	})
}

func FuzzCSVRead(f *testing.F) {
	f.Add([]byte("a,b\n1,2\n"), 0, 0)
	f.Add([]byte("x;y\n\"q\"\"\";2\n"), 2, 1)
	f.Add([]byte("\"unterminated"), 1, 1)
	f.Fuzz(func(t *testing.T, data []byte, off, lim int) {
		_, _ = readCSV(data, "f.csv", off%1000, lim%1000)
	})
}

// Whatever Markdown a model writes becomes a well-formed .docx that reads back.
func FuzzMarkdownDocx(f *testing.F) {
	for _, s := range []string{"# h\n\ntext **b** *i* `c`", "- a\n  - b\n1. c", "| a |\n|---|\n| b |", "```\ncode\n", "> q\n\n---\n\n![x](y.png)", "[l](javascript:alert(1)) \x00\x01 <w:t>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		data, _, err := writeDocx(parseMarkdown(md), "t", nil, docEpoch)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, zf := range zr.File {
			if zf.Name != "word/document.xml" {
				continue
			}
			rc, _ := zf.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			dec := xml.NewDecoder(bytes.NewReader(b))
			for {
				if _, err := dec.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("document.xml is not well-formed for %q: %v", md, err)
				}
			}
		}
		if _, err := readDocx(fuzzCtx(t), data); err != nil {
			t.Fatalf("our own .docx doesn't read back: %v", err)
		}
	})
}
