package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestPrepareAttachmentsNamesAndTypes(t *testing.T) {
	out, err := PrepareAttachments([]Attachment{
		{Name: "shot.png", Mime: "image/png", Data: pngHeader},                   // an image, as declared
		{Name: "../../etc/passwd", Mime: "", Data: []byte("root:x:0:0\n")},       // a path: its last element
		{Name: "photo.png", Mime: "image/png", Data: []byte("not a png at all")}, // the bytes deny it: a file
		{Name: "", Mime: "application/octet-stream", Data: pngHeader},            // the bytes say image
		{Name: "shot.png", Mime: "image/png", Data: pngHeader},                   // a second shot.png
		{Name: "notes.txt", Mime: "text/plain; charset=utf-8", Data: []byte("hi")},
		{Name: "IMG_1.HEIC", Mime: "image/heic", Data: []byte("\x00\x00\x00\x18ftypheic")}, // not an inline image
		{Name: "a\x00b\nc.txt", Mime: "", Data: []byte("x")},
		{Name: "..", Mime: "text/plain", Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, mime string }{
		{"shot.png", "image/png"}, {"passwd", ""}, {"photo.png", "text/plain"}, {"attachment-4.png", "image/png"},
		{"shot-2.png", "image/png"}, {"notes.txt", "text/plain"}, {"IMG_1.HEIC", "image/heic"}, {"a_b_c.txt", "text/plain"}, {"attachment-9.txt", "text/plain"},
	}
	for i, w := range want {
		if out[i].Name != w.name || (w.mime != "" && out[i].Mime != w.mime) {
			t.Errorf("%d: %q %q, want %q %q", i, out[i].Name, out[i].Mime, w.name, w.mime)
		}
	}
	if !InlineImage(out[0].Mime) || InlineImage(out[2].Mime) || InlineImage(out[6].Mime) {
		t.Fatal("inline images: the bytes decide")
	}
	if i := out[1].Info(); i.Name != "passwd" || i.Size != len("root:x:0:0\n") {
		t.Fatalf("info: %+v", i)
	}
	long := strings.Repeat("é", 80) + ".markdown"
	out, _ = PrepareAttachments([]Attachment{{Name: long, Data: []byte("x")}})
	if n := out[0].Name; len(n) > maxNameBytes || !strings.HasSuffix(n, ".markdown") || !strings.HasPrefix(n, "é") {
		t.Fatalf("a long name keeps its extension and whole runes: %q (%d)", n, len(n))
	}
}

func TestPrepareAttachmentsLimits(t *testing.T) {
	big := func(n int, head []byte) []byte {
		return append(append([]byte(nil), head...), bytes.Repeat([]byte{'a'}, n-len(head))...)
	}
	cases := []struct {
		name string
		in   []Attachment
		want error
	}{
		{"too many", make([]Attachment, MaxAttachments+1), ErrBadAttachment},
		{"an image too big to go inline is a file", []Attachment{{Name: "a.png", Data: big(MaxImageBytes+1, pngHeader)}}, nil},
		{"an image over the file limit", []Attachment{{Name: "a.png", Data: big(MaxFileBytes+1, pngHeader)}}, ErrAttachmentTooLarge},
		{"a big file", []Attachment{{Name: "a.log", Data: big(MaxFileBytes+1, nil)}}, ErrAttachmentTooLarge},
		{"too much together", []Attachment{{Name: "a", Data: big(MaxFileBytes, nil)}, {Name: "b", Data: big(MaxFileBytes, nil)}, {Name: "c", Data: []byte("x")}}, ErrAttachmentTooLarge},
		{"an image at the limit", []Attachment{{Name: "a.png", Data: big(MaxImageBytes, pngHeader)}}, nil},
		{"a file at the limit", []Attachment{{Name: "a.log", Data: big(MaxFileBytes, nil)}}, nil},
	}
	for _, c := range cases {
		_, err := PrepareAttachments(c.in)
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("héllo\n")) || IsText([]byte("a\x00b")) || IsText([]byte{0xff, 0xfe}) || !IsText(nil) {
		t.Fatal("IsText")
	}
}

// MaxImageBytes is what the strictest model API takes inline: 5 MiB of
// base64 (Anthropic on Bedrock and Vertex).
func TestInlineImageLimit(t *testing.T) {
	if b64 := (MaxImageBytes + 2) / 3 * 4; b64 > 5<<20 {
		t.Fatalf("an image at the limit is %d bytes of base64", b64)
	}
	if MaxInlineImagesBytes < MaxImageBytes || MaxInlineImagesBytes > 8<<20 {
		t.Fatal("the per-prompt inline budget must take one image at the limit and stay far below the 32 MB request")
	}
}
