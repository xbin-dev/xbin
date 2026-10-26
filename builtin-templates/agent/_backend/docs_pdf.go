// docs_pdf.go — PDFs (D201): their text per page (doc_read) and a page
// drawn as a picture the model looks at (doc_view: charts, scans, layout).
//
// The decoder is PDFium compiled to WebAssembly (go-pdfium's webassembly
// mode, MIT; PDFium BSD-3) run by wazero (Apache-2.0), a pure-Go runtime:
// no cgo, no subprocess. The module gets NO filesystem (go-pdfium mounts
// the host's / by default — we pass an empty FS config), no network (WASI
// has none), a memory ceiling, and every call runs under a deadline that
// closes its module instance when it passes (WithCloseOnContextDone). A
// document is handed in as bytes; each one gets a fresh instance, so
// nothing of one PDF is left in memory for the next.
//
// The runtime and the compiled module are made on first use, once, and
// kept: compiling the ~6 MB module is the slow part (seconds); an instance
// is cheap.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	pdfMemPages    = 6144 // 384 MiB of wasm linear memory, at most
	pdfCallTimeout = 60 * time.Second
	pdfMaxPages    = 2000
	pdfViewDPI     = 110
	pdfViewMaxPx   = 1800 // the longer side of a page picture
)

var (
	pdfOnce sync.Once
	pdfPool pdfium.Pool
	pdfErr  error
	// one PDF at a time: the pool holds one instance, and a second caller
	// waits rather than growing the backend's memory
	pdfMu sync.Mutex
)

func pdfiumPool() (pdfium.Pool, error) {
	pdfOnce.Do(func() {
		rc := wazero.NewRuntimeConfig().
			WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
			WithMemoryLimitPages(pdfMemPages).
			WithCloseOnContextDone(true)
		pdfPool, pdfErr = webassembly.Init(webassembly.Config{
			MinIdle: 0, MaxIdle: 1, MaxTotal: 1,
			RuntimeConfig: rc,
			FSConfig:      wazero.NewFSConfig(), // nothing mounted
			Stdout:        io.Discard,
			Stderr:        io.Discard,
		})
		if pdfErr != nil {
			pdfErr = fmt.Errorf("the PDF reader didn't start: %w", pdfErr)
		}
	})
	return pdfPool, pdfErr
}

// withPDF opens data in a fresh PDFium instance and runs fn on it, under
// ctx and pdfCallTimeout; a call still running at the deadline has its
// instance killed.
func withPDF(ctx context.Context, data []byte, fn func(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) error) error {
	pool, err := pdfiumPool()
	if err != nil {
		return err
	}
	pdfMu.Lock()
	defer pdfMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, pdfCallTimeout)
	defer cancel()
	inst, err := pool.GetInstanceWithContext(ctx)
	if err != nil {
		return fmt.Errorf("the PDF reader is busy: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("the PDF reader failed: %v", r)
			}
		}()
		buf := data
		doc, err := inst.OpenDocument(&requests.OpenDocument{File: &buf})
		if err != nil {
			done <- pdfOpenErr(err)
			return
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		if err != nil {
			done <- err
			return
		}
		done <- fn(inst, doc.Document, pc.PageCount)
	}()
	select {
	case err := <-done:
		_ = inst.Close()
		return err
	case <-ctx.Done():
		_ = inst.Kill()
		return fmt.Errorf("reading the PDF took too long (%v) — try fewer pages", pdfCallTimeout)
	}
}

func pdfOpenErr(err error) error {
	s := err.Error()
	switch {
	case strings.Contains(strings.ToLower(s), "password"):
		return errors.New("the PDF is password-protected")
	case strings.Contains(strings.ToLower(s), "format"), strings.Contains(strings.ToLower(s), "file"):
		return fmt.Errorf("not a readable PDF (%v)", err)
	}
	return fmt.Errorf("the PDF doesn't open: %v", err)
}

// readPDF returns the text of the wanted pages (1-based; nil: all, up to
// pdfMaxPages) as "## Page N" sections, and the page count.
func readPDF(ctx context.Context, data []byte, want func(int) bool) (string, int, error) {
	var b strings.Builder
	total := 0
	err := withPDF(ctx, data, func(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) error {
		total = pages
		empty := 0
		for i := 0; i < min(pages, pdfMaxPages); i++ {
			if want != nil && !want(i+1) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			t, err := inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: i}}})
			if err != nil {
				fmt.Fprintf(&b, "## Page %d\n\n(unreadable: %v)\n\n", i+1, err)
				continue
			}
			text := strings.TrimSpace(strings.ReplaceAll(t.Text, "\r\n", "\n"))
			if text == "" {
				empty++
				fmt.Fprintf(&b, "## Page %d\n\n(no text on this page — a scan or a picture; doc_view shows it)\n\n", i+1)
				continue
			}
			fmt.Fprintf(&b, "## Page %d\n\n%s\n\n", i+1, text)
		}
		return nil
	})
	return b.String(), total, err
}

// renderPDFPage draws page (1-based) as a PNG, its longer side at most
// pdfViewMaxPx.
func renderPDFPage(ctx context.Context, data []byte, page int) ([]byte, int, error) {
	var out []byte
	total := 0
	err := withPDF(ctx, data, func(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) error {
		total = pages
		if page < 1 || page > pages {
			return fmt.Errorf("page %d is out of range (the PDF has %d)", page, pages)
		}
		p := requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: page - 1}}
		sz, err := inst.GetPageSize(&requests.GetPageSize{Page: p})
		if err != nil {
			return err
		}
		dpi := pdfViewDPI
		if long := max(sz.Width, sz.Height) * float64(dpi) / 72; long > pdfViewMaxPx {
			dpi = max(36, int(float64(dpi)*pdfViewMaxPx/long))
		}
		r, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{Page: p, DPI: dpi})
		if err != nil {
			return err
		}
		defer r.Cleanup()
		var img image.Image = r.Result.Image
		if r.Result.RenderedImage != nil {
			img = r.Result.RenderedImage
		}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return err
		}
		out = buf.Bytes()
		return nil
	})
	return out, total, err
}
