package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func encPNG(t *testing.T, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// halfClear is w×h, opaque red on the left half, fully transparent on the right.
func halfClear(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/2 {
				m.Set(x, y, color.NRGBA{R: 255, A: 255})
			}
		}
	}
	return m
}

// pngHeaderOnly is a PNG whose IHDR claims w×h (valid CRC) and no pixels —
// enough for DecodeConfig, which is all a size cap may look at.
func pngHeaderOnly(w, h uint32, depth byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = depth, 6 // RGBA
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return b.Bytes()
}

func thumbGet(t *testing.T, id int64, query string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", fmt.Sprintf("/runs/%d/thumb%s", id, query), nil)
	r.SetPathValue("id", fmt.Sprint(id))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handleThumb(w, r)
	return w
}

func decodeAs(t *testing.T, w *httptest.ResponseRecorder, wantType string) image.Image {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("thumb: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != wantType {
		t.Fatalf("Content-Type %q, want %q", ct, wantType)
	}
	m, _, err := image.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestThumbScalesImages(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)

	// PNG with alpha: fitted into w, kept PNG, transparency survives
	upload(t, ag, id, "clear.png", "image/png", encPNG(t, halfClear(400, 300)))
	m := decodeAs(t, thumbGet(t, id, "?path=clear.png&w=100", nil), "image/png")
	if b := m.Bounds(); b.Dx() != 100 || b.Dy() != 75 {
		t.Fatalf("clear.png → %v", b)
	}
	if _, _, _, a := m.At(90, 30).RGBA(); a != 0 {
		t.Fatal("the transparent half is no longer transparent")
	}
	if r, _, _, a := m.At(10, 30).RGBA(); r>>8 != 255 || a>>8 != 255 {
		t.Fatal("the red half is no longer red")
	}
	// …and as JPEG, flattened on white
	m = decodeAs(t, thumbGet(t, id, "?path=clear.png&w=100&fmt=jpeg", nil), "image/jpeg")
	if r, g, b, _ := m.At(90, 30).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("transparent → JPEG should be white, got %d %d %d", r>>8, g>>8, b>>8)
	}

	// JPEG: stays JPEG; the default width is 320
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for i := range src.Pix {
		src.Pix[i] = byte(i)
	}
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, src, nil)
	upload(t, ag, id, "photo.jpg", "image/jpeg", jb.Bytes())
	if b := decodeAs(t, thumbGet(t, id, "?path=photo.jpg", nil), "image/jpeg").Bounds(); b.Dx() != 320 || b.Dy() != 160 {
		t.Fatalf("photo.jpg default → %v", b)
	}
	// already small enough: the original bytes
	if w := thumbGet(t, id, "?path=photo.jpg&w=99999", nil); !bytes.Equal(w.Body.Bytes(), jb.Bytes()) {
		t.Fatal("a JPEG that fits should be served as it is")
	}
	// a bounding height: 1000×500 into 320×40 → 80×40
	if b := decodeAs(t, thumbGet(t, id, "?path=photo.jpg&h=40", nil), "image/jpeg").Bounds(); b.Dx() != 80 || b.Dy() != 40 {
		t.Fatalf("photo.jpg h=40 → %v", b)
	}
	// tiny w clamps up to 16
	if b := decodeAs(t, thumbGet(t, id, "?path=photo.jpg&w=1", nil), "image/jpeg").Bounds(); b.Dx() != 16 {
		t.Fatalf("w=1 → %v", b)
	}

	// GIF: the first frame, as PNG
	pal := image.NewPaletted(image.Rect(0, 0, 60, 40), color.Palette{color.Black, color.White})
	var gb bytes.Buffer
	_ = gif.Encode(&gb, pal, nil)
	upload(t, ag, id, "anim.gif", "image/gif", gb.Bytes())
	if b := decodeAs(t, thumbGet(t, id, "?path=anim.gif&w=30", nil), "image/png").Bounds(); b.Dx() != 30 || b.Dy() != 20 {
		t.Fatalf("anim.gif → %v", b)
	}
}

func TestThumbRefusesWhatItCannotScale(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)
	upload(t, ag, id, "notes.txt", "text/plain", []byte("hello"))
	upload(t, ag, id, "pic.webp", "image/webp", append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...))
	upload(t, ag, id, "blob.bin", "application/octet-stream", []byte{0, 1, 2, 3, 0xff})
	upload(t, ag, id, "huge.png", "image/png", pngHeaderOnly(20000, 20000, 8)) // 400 MP
	upload(t, ag, id, "deep.png", "image/png", pngHeaderOnly(5000, 5000, 16))  // 25 MP × 8 B
	upload(t, ag, id, "broken.png", "image/png", pngHeaderOnly(64, 64, 8))     // no pixel data
	for _, c := range []struct {
		q    string
		want int
	}{
		{"?path=notes.txt", http.StatusUnsupportedMediaType},
		{"?path=pic.webp", http.StatusUnsupportedMediaType},
		{"?path=blob.bin", http.StatusUnsupportedMediaType},
		{"?path=huge.png", http.StatusUnprocessableEntity},
		{"?path=deep.png", http.StatusUnprocessableEntity},
		{"?path=broken.png&w=16", http.StatusUnprocessableEntity},
		{"?path=missing.png", http.StatusNotFound},
		{"?path=../x", http.StatusBadRequest},
		{"?path=notes.txt&w=wide", http.StatusBadRequest},
		{"?path=notes.txt&fmt=webp", http.StatusBadRequest},
	} {
		if w := thumbGet(t, id, c.q, nil); w.Code != c.want {
			t.Fatalf("%s: %d (want %d) %s", c.q, w.Code, c.want, w.Body.String())
		} else if w.Header().Get("ETag") != "" {
			t.Fatalf("%s: an error carries an ETag", c.q)
		}
	}
}

// A client revalidates with the ETag and gets 304; another size is another
// tag. The route has /raw's access.
func TestThumbCachingAndAccess(t *testing.T) {
	ag, mux := accessFixture(t)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	upload(t, ag, id, "clear.png", "image/png", encPNG(t, halfClear(200, 200)))
	w := thumbGet(t, id, "?path=clear.png&w=50", nil)
	tag := w.Header().Get("ETag")
	if w.Code != 200 || tag == "" || w.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("first: %d etag=%q cc=%q", w.Code, tag, w.Header().Get("Cache-Control"))
	}
	again := thumbGet(t, id, "?path=clear.png&w=50", nil)
	if !bytes.Equal(again.Body.Bytes(), w.Body.Bytes()) || again.Header().Get("ETag") != tag {
		t.Fatal("the same request should give the same thumbnail")
	}
	if nm := thumbGet(t, id, "?path=clear.png&w=50", map[string]string{"If-None-Match": tag}); nm.Code != http.StatusNotModified || nm.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d", nm.Code)
	}
	if other := thumbGet(t, id, "?path=clear.png&w=60", nil); other.Header().Get("ETag") == tag {
		t.Fatal("another size must be another ETag")
	}
	if got := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/thumb?path=clear.png", id), nil).Code; got != 404 {
		t.Fatalf("bob thumbs alice's private file: %d", got)
	}
	if got := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/thumb?path=clear.png", id), nil).Code; got != 200 {
		t.Fatalf("alice thumbs her file: %d", got)
	}
}

// The box filter averages each block (premultiplied), and fitInside never
// enlarges and keeps the aspect.
func TestBoxDownscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			if (x+y)%2 == 0 {
				src.Set(x, y, color.White)
			} else {
				src.Set(x, y, color.Transparent)
			}
		}
	}
	dst := boxDownscale(src, 2, 2)
	for i := 0; i < len(dst.Pix); i += 4 {
		if p := dst.Pix[i : i+4]; p[0] != 128 || p[3] != 128 {
			t.Fatalf("a checkerboard block should average to half: %v", p)
		}
	}
	// a sub-image with an offset origin
	sub := src.SubImage(image.Rect(1, 1, 3, 3))
	if d := boxDownscale(sub, 1, 1); d.Pix[3] != 128 {
		t.Fatalf("offset origin: %v", d.Pix)
	}
	for _, c := range []struct{ w, h, bw, bh, ww, wh int }{
		{100, 50, 320, 1280, 100, 50},
		{1000, 500, 320, 1280, 320, 160},
		{500, 5000, 320, 1280, 128, 1280},
		{10000, 1, 16, 64, 16, 1},
		{1, 10000, 16, 64, 1, 64},
	} {
		if w, h := fitInside(c.w, c.h, c.bw, c.bh); w != c.ww || h != c.wh {
			t.Fatalf("fitInside(%d×%d in %d×%d) = %d×%d, want %d×%d", c.w, c.h, c.bw, c.bh, w, h, c.ww, c.wh)
		}
	}
}

// withOrientation puts an EXIF APP1 segment carrying orientation o right
// after a JPEG's SOI, the way cameras write it.
func withOrientation(jpg []byte, o uint16, bigEndian bool) []byte {
	var tiff bytes.Buffer
	var bo binary.ByteOrder = binary.LittleEndian
	tiff.WriteString("II")
	if bigEndian {
		bo = binary.BigEndian
		tiff.Reset()
		tiff.WriteString("MM")
	}
	_ = binary.Write(&tiff, bo, uint16(42))
	_ = binary.Write(&tiff, bo, uint32(8))
	_ = binary.Write(&tiff, bo, uint16(2)) // two entries: another tag, then orientation
	for _, e := range [][3]uint16{{0x010F, 2, 0}, {0x0112, 3, o}} {
		_ = binary.Write(&tiff, bo, e[0])
		_ = binary.Write(&tiff, bo, e[1])
		_ = binary.Write(&tiff, bo, uint32(1))
		_ = binary.Write(&tiff, bo, e[2])
		_ = binary.Write(&tiff, bo, uint16(0))
	}
	_ = binary.Write(&tiff, bo, uint32(0))
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

// A phone photo stored sideways (EXIF orientation) thumbnails the way it is
// shown: fitted and turned.
func TestThumbFollowsEXIFOrientation(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)
	// stored 80×40: left half red, right half blue
	src := image.NewRGBA(image.Rect(0, 0, 80, 40))
	for y := range 40 {
		for x := range 80 {
			c := color.RGBA{R: 255, A: 255}
			if x >= 40 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, src, &jpeg.Options{Quality: 95})
	red := func(c color.Color) bool { r, _, b, _ := c.RGBA(); return r>>8 > 200 && b>>8 < 60 }
	blue := func(c color.Color) bool { r, _, b, _ := c.RGBA(); return b>>8 > 200 && r>>8 < 60 }
	for _, c := range []struct {
		o     uint16
		be    bool
		w, h  int
		check func(m image.Image) bool
	}{
		// 6: shown turned clockwise — 40×80, the stored left (red) on top
		{6, false, 20, 40, func(m image.Image) bool { return red(m.At(10, 5)) && blue(m.At(10, 35)) }},
		// 8: counter-clockwise — red at the bottom
		{8, true, 20, 40, func(m image.Image) bool { return blue(m.At(10, 5)) && red(m.At(10, 35)) }},
		// 3: upside down — red on the right
		{3, false, 40, 20, func(m image.Image) bool { return blue(m.At(5, 10)) && red(m.At(35, 10)) }},
		// 2: mirrored — red on the right
		{2, true, 40, 20, func(m image.Image) bool { return blue(m.At(5, 10)) && red(m.At(35, 10)) }},
	} {
		name := fmt.Sprintf("o%d.jpg", c.o)
		upload(t, ag, id, name, "image/jpeg", withOrientation(jb.Bytes(), c.o, c.be))
		m := decodeAs(t, thumbGet(t, id, fmt.Sprintf("?path=%s&w=%d", name, c.w), nil), "image/jpeg")
		if b := m.Bounds(); b.Dx() != c.w || b.Dy() != c.h {
			t.Fatalf("orientation %d: %v, want %d×%d", c.o, b, c.w, c.h)
		}
		if !c.check(m) {
			t.Fatalf("orientation %d: turned the wrong way", c.o)
		}
	}
	if o := jpegOrientation(jb.Bytes()); o != 1 {
		t.Fatalf("no EXIF: %d", o)
	}
	for _, junk := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x40}, []byte("\xFF\xD8\xFF\xE1\x00\x0cExif\x00\x00MM\x00*")} {
		if o := jpegOrientation(junk); o != 1 {
			t.Fatalf("junk %q: %d", junk, o)
		}
	}
	// all eight turn a 3×2 image into the right shape and keep its pixels
	m := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range m.Pix {
		m.Pix[i] = byte(i)
	}
	for o := 1; o <= 8; o++ {
		out := orientRGBA(m, o)
		w, h := 3, 2
		if o >= 5 {
			w, h = 2, 3
		}
		if out.Rect.Dx() != w || out.Rect.Dy() != h {
			t.Fatalf("orientation %d: %v", o, out.Rect)
		}
		sum := 0
		for _, p := range out.Pix {
			sum += int(p)
		}
		if sum != 276 { // 0+1+…+23
			t.Fatalf("orientation %d lost pixels", o)
		}
	}
}
