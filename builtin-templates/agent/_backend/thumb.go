// thumb.go — GET /runs/{id}/thumb?path=&w=[&h=][&fmt=]: a sized copy of an
// image session file, for chat previews that should not pull a 12 MB photo.
//
// Same access as /raw (a viewer of the run). Standard library only: PNG, JPEG
// and GIF (its first frame) decode; anything else — WebP, SVG, a text file —
// is 415 and the client falls back to /raw or a file chip. The image is
// fitted inside w × h (never enlarged), box-filtered row by row (no second
// full-size copy), turned as a JPEG's EXIF orientation says (the pixels of a
// phone photo are often stored sideways), and written as the source's format
// — PNG for PNG and GIF, JPEG for JPEG — or as `fmt` asks. A source that
// already fits and is in the wanted format is served as it is (a viewer
// applies its orientation).
//
// Caps: w and h are clamped to [16, 2048] (w defaults to 320, h to 4·w);
// an image whose decoded size would pass thumbMaxDecode (by its header,
// before decoding) is 422; two decodes run at a time. Results are cached in
// memory by the file's blob — blobs are immutable, so a file overwritten at
// the same path is a new blob — and carry an ETag, so a client revalidates
// with If-None-Match and gets 304.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the decoder
	"image/jpeg"
	"image/png"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	thumbMinSide     = 16
	thumbMaxSide     = 2048
	thumbDefaultW    = 320
	thumbMaxDecode   = 100 << 20 // bytes a decoded source may take
	thumbMaxPixels   = 50_000_000
	thumbJPEGQuality = 82
	thumbMaxCached   = 2 << 20 // a bigger result is made again rather than kept
)

var (
	thumbs   = newBlobCache(24 << 20) // key → encoded thumbnail
	thumbSem = make(chan struct{}, 2)

	errThumbFormat = errors.New("not an image this backend can scale (PNG, JPEG or GIF)")
	errThumbLarge  = errors.New("image too large to scale")
)

// thumbReq is one request, normalized.
type thumbReq struct {
	w, h int
	fmt  string // "", "png", "jpeg"
}

func parseThumbReq(r *http.Request) (thumbReq, error) {
	q := r.URL.Query()
	side := func(name string, def int) (int, error) {
		s := q.Get(name)
		if s == "" {
			return def, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%s must be a positive integer", name)
		}
		return max(thumbMinSide, min(n, thumbMaxSide)), nil
	}
	w, err := side("w", thumbDefaultW)
	if err != nil {
		return thumbReq{}, err
	}
	h, err := side("h", min(4*w, thumbMaxSide))
	if err != nil {
		return thumbReq{}, err
	}
	f := strings.ToLower(q.Get("fmt"))
	switch f {
	case "", "png", "jpeg":
	case "jpg":
		f = "jpeg"
	default:
		return thumbReq{}, fmt.Errorf("fmt must be png or jpeg")
	}
	return thumbReq{w: w, h: h, fmt: f}, nil
}

func handleThumb(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	p, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	tr, err := parseThumbReq(r)
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := agent.db.replFile(id, p)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	if !f.Binary || !strings.HasPrefix(f.Mime, "image/") {
		xbin.WriteError(w, http.StatusUnsupportedMediaType, errThumbFormat.Error())
		return
	}
	key := fmt.Sprintf("%s|%d|%d|%s", f.Blob, tr.w, tr.h, tr.fmt)
	sum := sha256.Sum256([]byte(key))
	etag := `"th-` + hex.EncodeToString(sum[:12]) + `"`
	h := w.Header()
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		h.Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	out, ok := thumbs.get(key)
	if !ok {
		src, err := agent.readBlob(r.Context(), f.Blob)
		if err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		select {
		case thumbSem <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		var asIs bool
		out, asIs, err = makeThumb(src, tr)
		<-thumbSem
		switch {
		case errors.Is(err, errThumbFormat):
			xbin.WriteError(w, http.StatusUnsupportedMediaType, err.Error())
			return
		case err != nil:
			xbin.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if !asIs && len(out) <= thumbMaxCached { // the source is in the blob cache already
			thumbs.put(key, out)
		}
	}
	ct := "image/png"
	if bytes.HasPrefix(out, []byte{0xFF, 0xD8, 0xFF}) {
		ct = "image/jpeg"
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(out)
}

// makeThumb scales an encoded image into tr's box. asIs: it already fit, and
// out is src.
func makeThumb(src []byte, tr thumbReq) (out []byte, asIs bool, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return nil, false, errThumbFormat
		}
		return nil, false, fmt.Errorf("undecodable image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, false, fmt.Errorf("undecodable image: %dx%d", cfg.Width, cfg.Height)
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px > thumbMaxPixels || px*decodedBytesPerPixel(format, cfg) > thumbMaxDecode {
		return nil, false, fmt.Errorf("%w: %dx%d", errThumbLarge, cfg.Width, cfg.Height)
	}
	want := tr.fmt
	if want == "" {
		want = "png"
		if format == "jpeg" {
			want = "jpeg"
		}
	}
	// A photo's EXIF orientation says how it is shown (a viewer of the
	// original applies it): fit what is shown, and turn the result so.
	orient := 1
	if format == "jpeg" {
		orient = jpegOrientation(src)
	}
	sw, sh := cfg.Width, cfg.Height
	if orient >= 5 {
		sw, sh = sh, sw
	}
	if dw, dh := fitInside(sw, sh, tr.w, tr.h); dw == sw && dh == sh && want == format {
		return src, true, nil // already small enough, and in the wanted format
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false, fmt.Errorf("undecodable image: %w", err)
	}
	// A GIF's first frame may be smaller than its canvas: scale what decoded.
	b := img.Bounds()
	sw, sh = b.Dx(), b.Dy()
	if orient >= 5 {
		sw, sh = sh, sw
	}
	dw, dh := fitInside(sw, sh, tr.w, tr.h)
	if orient >= 5 {
		dw, dh = dh, dw // scale the stored image, then turn it
	}
	dst := orientRGBA(boxDownscale(img, dw, dh), orient)
	var buf bytes.Buffer
	if want == "jpeg" {
		flattenOnWhite(dst)
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbJPEGQuality})
	} else {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, dst)
	}
	if err != nil {
		return nil, false, err
	}
	return buf.Bytes(), false, nil
}

// decodedBytesPerPixel estimates what the decoder will allocate, from the
// header alone.
func decodedBytesPerPixel(format string, cfg image.Config) int64 {
	if format == "jpeg" {
		return 2 // YCbCr, subsampled (4:4:4 is 3; CMYK JPEGs are rare)
	}
	if _, ok := cfg.ColorModel.(color.Palette); ok {
		return 1
	}
	switch cfg.ColorModel {
	case color.GrayModel:
		return 1
	case color.Gray16Model:
		return 2
	case color.RGBAModel, color.NRGBAModel:
		return 4
	}
	return 8
}

// fitInside scales w×h down (never up) to fit in bw×bh, keeping the aspect.
func fitInside(w, h, bw, bh int) (int, int) {
	if w <= bw && h <= bh {
		return w, h
	}
	// scale = min(bw/w, bh/h), in integers
	if int64(bw)*int64(h) <= int64(bh)*int64(w) {
		return bw, max(1, int(int64(h)*int64(bw)/int64(w)))
	}
	return max(1, int(int64(w)*int64(bh)/int64(h))), bh
}

// boxDownscale averages each destination pixel's source box (premultiplied,
// so transparency averages right). One source row is converted at a time.
func boxDownscale(src image.Image, dw, dh int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	col := make([]int, sw)   // source x → destination x
	width := make([]int, dw) // source columns per destination column
	for x := range sw {
		col[x] = x * dw / sw
		width[col[x]]++
	}
	acc := make([]uint64, dw*4)
	row := image.NewRGBA(image.Rect(0, 0, sw, 1))
	flush := func(dy, rows int) {
		o := dst.PixOffset(0, dy)
		for dx := range dw {
			n := uint64(width[dx] * rows)
			for c := range 4 {
				dst.Pix[o+dx*4+c] = uint8((acc[dx*4+c] + n/2) / n)
				acc[dx*4+c] = 0
			}
		}
	}
	cur, rows := 0, 0
	for sy := range sh {
		if dy := sy * dh / sh; dy != cur {
			flush(cur, rows)
			cur, rows = dy, 0
		}
		draw.Draw(row, row.Bounds(), src, image.Pt(b.Min.X, b.Min.Y+sy), draw.Src)
		p := row.Pix
		for x := range sw {
			i, j := col[x]*4, x*4
			acc[i] += uint64(p[j])
			acc[i+1] += uint64(p[j+1])
			acc[i+2] += uint64(p[j+2])
			acc[i+3] += uint64(p[j+3])
		}
		rows++
	}
	flush(cur, rows)
	return dst
}

// flattenOnWhite composites a premultiplied image over white (JPEG has no
// alpha; left alone, transparent pixels would turn black).
func flattenOnWhite(m *image.RGBA) {
	for i := 0; i+3 < len(m.Pix); i += 4 {
		a := m.Pix[i+3]
		if a == 0xFF {
			continue
		}
		k := 0xFF - a
		m.Pix[i] += k
		m.Pix[i+1] += k
		m.Pix[i+2] += k
		m.Pix[i+3] = 0xFF
	}
}

// jpegOrientation reads the EXIF orientation (1–8) from a JPEG's APP1
// segment; 1 when there is none or it cannot be read.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		marker, n := b[i+1], int(b[i+2])<<8|int(b[i+3])
		if marker == 0xDA || n < 2 || i+2+n > len(b) { // start of scan: no more headers
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

// tiffOrientation finds tag 0x0112 in IFD0 of a TIFF header.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 }
		u32 = func(p []byte) int { return u16(p) | u16(p[2:])<<16 }
	case "MM":
		u16 = func(p []byte) int { return int(p[0])<<8 | int(p[1]) }
		u32 = func(p []byte) int { return u16(p)<<16 | u16(p[2:]) }
	default:
		return 1
	}
	off := u32(t[4:])
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := u16(t[off:])
	for k := range count {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if u16(t[e:]) == 0x0112 && u16(t[e+2:]) == 3 { // orientation, SHORT
			if o := u16(t[e+8:]); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orientRGBA turns a stored image into how EXIF orientation o shows it.
func orientRGBA(m *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return m
	}
	rw, rh := m.Rect.Dx(), m.Rect.Dy()
	ow, oh := rw, rh
	if o >= 5 {
		ow, oh = rh, rw
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := range oh {
		for x := range ow {
			var sx, sy int
			switch o {
			case 2: // mirrored
				sx, sy = rw-1-x, y
			case 3: // upside down
				sx, sy = rw-1-x, rh-1-y
			case 4: // mirrored, upside down
				sx, sy = x, rh-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // turned 90° clockwise to show
				sx, sy = y, rh-1-x
			case 7: // transversed
				sx, sy = rw-1-y, rh-1-x
			case 8: // turned 90° counter-clockwise to show
				sx, sy = rw-1-y, x
			}
			copy(out.Pix[out.PixOffset(x, y):][:4], m.Pix[m.PixOffset(sx, sy):][:4])
		}
	}
	return out
}
