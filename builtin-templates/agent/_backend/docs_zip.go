// docs_zip.go — reading Office files' zip packages and XML parts without
// trusting them (D201): an uploaded .docx/.pptx is a stranger's archive, so
// every read is bounded before it is decompressed or parsed.
//
//   - the archive: at most docMaxEntries entries, each part at most
//     docMaxPart bytes and all of them together at most docMaxUnzipped bytes
//     once DECOMPRESSED — counted while reading, never taken from the headers
//     a zip bomb lies in;
//   - an XML part: at most docMaxDepth nested elements and docMaxElements
//     elements, and the read gives up once the call's context is done.
//
// Everything here is pure Go (archive/zip, encoding/xml): no cgo, no
// subprocess, nothing outside the tile backend's own sandbox.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	docMaxEntries  = 4000
	docMaxPart     = 32 << 20
	docMaxUnzipped = 96 << 20
	docMaxDepth    = 256
	docMaxElements = 2_000_000
)

var errDocTooBig = errors.New("the document is too large or too deeply nested to read")

// ooxPkg is an opened Office package: its parts by name, read lazily and
// counted against the archive's budget.
type ooxPkg struct {
	ctx   context.Context
	files map[string]*zip.File
	used  int64
}

func openOOX(ctx context.Context, data []byte) (*ooxPkg, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not an Office document (the zip package doesn't open: %v)", err)
	}
	if len(zr.File) > docMaxEntries {
		return nil, errDocTooBig
	}
	p := &ooxPkg{ctx: ctx, files: map[string]*zip.File{}}
	for _, f := range zr.File {
		p.files[strings.TrimPrefix(f.Name, "/")] = f
	}
	return p, nil
}

func (p *ooxPkg) has(name string) bool { _, ok := p.files[name]; return ok }

// read returns one part's bytes, counting what it decompresses.
func (p *ooxPkg) read(name string) ([]byte, error) {
	f, ok := p.files[strings.TrimPrefix(name, "/")]
	if !ok {
		return nil, fmt.Errorf("the document has no %s", name)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	left := int64(docMaxUnzipped) - p.used
	if left > docMaxPart {
		left = docMaxPart
	}
	b, err := io.ReadAll(io.LimitReader(rc, left+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > left {
		return nil, errDocTooBig
	}
	p.used += int64(len(b))
	return b, nil
}

// rels reads a part's relationships: id → target, resolved against the
// part's directory (an External target is kept as written).
func (p *ooxPkg) rels(part string) map[string]ooxRel {
	dir, base := path.Split(part)
	b, err := p.read(dir + "_rels/" + base + ".rels")
	out := map[string]ooxRel{}
	if err != nil {
		return out
	}
	_ = walkXML(p.ctx, b, func(d *xmlWalk, se xml.StartElement) error {
		if se.Name.Local != "Relationship" {
			return nil
		}
		r := ooxRel{Type: attr(se, "Type"), Target: attr(se, "Target"), External: attr(se, "TargetMode") == "External"}
		if !r.External {
			if strings.HasPrefix(r.Target, "/") {
				r.Target = strings.TrimPrefix(r.Target, "/")
			} else {
				r.Target = path.Clean(dir + r.Target)
			}
		}
		out[attr(se, "Id")] = r
		return nil
	}, nil, nil)
	return out
}

type ooxRel struct {
	Type, Target string
	External     bool
}

// xmlWalk is the state a walk hands its callbacks: the open elements, by
// local name, outermost first.
type xmlWalk struct {
	stack []string
}

func (w *xmlWalk) in(local string) bool {
	for _, s := range w.stack {
		if s == local {
			return true
		}
	}
	return false
}

// walkXML streams an XML part through start/end/text callbacks (any may be
// nil), refusing one nested past docMaxDepth or with more than
// docMaxElements elements, and stopping when ctx is done. A callback's
// error stops the walk and is returned.
func walkXML(ctx context.Context, b []byte, start func(*xmlWalk, xml.StartElement) error,
	end func(*xmlWalk, xml.EndElement) error, text func(*xmlWalk, []byte) error) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	w := &xmlWalk{}
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the document's XML doesn't parse: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n++
			if n > docMaxElements || len(w.stack) >= docMaxDepth {
				return errDocTooBig
			}
			if n%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			w.stack = append(w.stack, t.Name.Local)
			if start != nil {
				if err := start(w, t); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if end != nil {
				if err := end(w, t); err != nil {
					return err
				}
			}
			if len(w.stack) > 0 {
				w.stack = w.stack[:len(w.stack)-1]
			}
		case xml.CharData:
			if text != nil {
				if err := text(w, t); err != nil {
					return err
				}
			}
		}
	}
}

// relID is an element's relationship id (r:id — the namespaced one: a
// <p:sldId> also has a plain numeric id).
func relID(se xml.StartElement) string {
	for _, a := range se.Attr {
		if a.Name.Local == "id" && strings.Contains(a.Name.Space, "relationships") {
			return a.Value
		}
	}
	return ""
}

// attr is an attribute's value by local name ("" when absent).
func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// xmlEsc escapes text for an XML part we write.
func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(cleanXMLText(s)))
	return b.String()
}

// cleanXMLText drops the characters XML 1.0 can't carry (control
// characters other than tab, newline and carriage return), which a model
// or a pasted table may well produce.
func cleanXMLText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0xFFFE && r != 0xFFFF) {
			return r
		}
		return -1
	}, s)
}

// zipWriter assembles a package we write: parts in order, deflated, with
// fixed timestamps so the same input gives the same bytes.
type zipWriter struct {
	buf bytes.Buffer
	zw  *zip.Writer
	err error
}

func newZipWriter() *zipWriter {
	z := &zipWriter{}
	z.zw = zip.NewWriter(&z.buf)
	return z
}

func (z *zipWriter) add(name string, data []byte) {
	if z.err != nil {
		return
	}
	w, err := z.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: docEpoch})
	if err != nil {
		z.err = err
		return
	}
	_, z.err = w.Write(data)
}

func (z *zipWriter) addString(name, s string) { z.add(name, []byte(s)) }

func (z *zipWriter) bytes() ([]byte, error) {
	if z.err != nil {
		return nil, z.err
	}
	if err := z.zw.Close(); err != nil {
		return nil, err
	}
	return z.buf.Bytes(), nil
}
