package agent

// ATTACHMENTS: files sent with a prompt (POST /term/sessions/<id>/prompt
// {text, attachments}, plans/native.md §13 — a photo, a screenshot, a log,
// a PDF from the phone). The driver decides how each reaches the agent (the
// ACP one: every file is also dropped inside the sandbox; images go inline
// as image blocks, small text inline as an embedded resource, the rest as a
// link to the dropped file). Here: the one model of an attachment, the
// limits, and the normalisation every driver relies on — a safe, unique
// file name and a media type the bytes agree with.

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Attachment is one file of a prompt.
type Attachment struct {
	Name string // a plain file name (PrepareAttachments makes it so)
	Mime string // its media type, no parameters
	Data []byte
}

// Prompt is one user turn: the text and its attachments (either may be
// empty, not both).
type Prompt struct {
	Text        string
	Attachments []Attachment
}

// AttachmentInfo is what the transcript keeps of an attachment (the user's
// message.delta `attachments`): never the bytes. Inline: the model got it
// with the prompt (an image block, an embedded text) — else it is a file
// the agent was pointed at.
type AttachmentInfo struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int    `json:"size"`
	Inline bool   `json:"inline,omitempty"`
}

// Info is the attachment's transcript entry.
func (a Attachment) Info() AttachmentInfo {
	return AttachmentInfo{Name: a.Name, Mime: a.Mime, Size: len(a.Data)}
}

// Limits (decoded bytes). Every attachment is a file for the agent (≤
// MaxFileBytes). An image also goes to the model inline, within the model
// APIs' limits: an image block counts its base64 (4/3 of the bytes), and
// Anthropic's cap on Bedrock and Vertex is 5 MB of base64 (10 MB on its own
// API) — MaxImageBytes is that 5 MB decoded. The images of a prompt stay in
// the agent's conversation and ride every later request (32 MB on the
// Anthropic API), so a prompt sends at most MaxInlineImagesBytes of them
// inline; a bigger image, or one past that, is a file only (the agent opens
// it with its own tools). Clients should downscale photos.
const (
	MaxAttachments       = 10
	MaxImageBytes        = 15 << 18 // 3.75 MiB: 5 MiB as base64
	MaxInlineImagesBytes = 4 << 20  // inline image bytes per prompt
	MaxFileBytes         = 10 << 20
	MaxAttachmentsBytes  = 20 << 20 // one prompt's attachments together
	// MaxInlineText is the largest text file sent inline (as an embedded
	// resource the model reads with the prompt); a bigger one is a file the
	// agent reads with its tools.
	MaxInlineText = 128 << 10
	maxNameBytes  = 100
)

// Errors PrepareAttachments returns (wrapped): ErrAttachmentTooLarge is a
// 413, ErrBadAttachment a 400. ErrUnsupportedContent is a driver's: the
// agent cannot take this kind of content (a 400).
var (
	ErrAttachmentTooLarge = errors.New("attachment too large")
	ErrBadAttachment      = errors.New("bad attachment")
	ErrUnsupportedContent = errors.New("the agent does not accept this content")
)

// inlineImages are the image types every model API takes inline.
var inlineImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// InlineImage reports whether a media type is an image the model sees
// itself (png, jpeg, gif, webp). Other images (HEIC, TIFF, SVG, …) are
// files: a client converts a photo to JPEG first.
func InlineImage(mediaType string) bool { return inlineImages[mediaType] }

// IsText reports whether data reads as text (valid UTF-8, no NUL).
func IsText(data []byte) bool {
	return utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
}

// PrepareAttachments checks a prompt's attachments against the limits and
// normalises them: each name becomes a plain, unique file name ("" gets
// one), and each media type is what the bytes are — an image is an inline
// image only when its bytes say so (a declared type is kept for everything
// else, else guessed from the name, else sniffed).
func PrepareAttachments(in []Attachment) ([]Attachment, error) {
	if len(in) > MaxAttachments {
		return nil, fmt.Errorf("%w: %d attachments (at most %d)", ErrBadAttachment, len(in), MaxAttachments)
	}
	out := make([]Attachment, 0, len(in))
	used := map[string]bool{}
	total := 0
	for i, a := range in {
		a.Mime = mediaType(a)
		a.Name = uniqueName(safeName(a.Name, i, a.Mime), used)
		total += len(a.Data)
		switch {
		case len(a.Data) > MaxFileBytes:
			return nil, fmt.Errorf("%w: %s is %s — the limit for a file is %s", ErrAttachmentTooLarge, a.Name, size(len(a.Data)), size(MaxFileBytes))
		case total > MaxAttachmentsBytes:
			return nil, fmt.Errorf("%w: the attachments are over %s together", ErrAttachmentTooLarge, size(MaxAttachmentsBytes))
		}
		out = append(out, a)
	}
	return out, nil
}

// mediaType decides an attachment's type: the sniffed one when the bytes
// are an inline image; else the declared one (parameters dropped); else by
// the name's extension; else sniffed.
func mediaType(a Attachment) string {
	sniffed := bare(http.DetectContentType(a.Data))
	if InlineImage(sniffed) {
		return sniffed
	}
	if d := bare(a.Mime); d != "" && !InlineImage(d) { // a declared image the bytes deny is not one
		return d
	}
	if t := bare(mime.TypeByExtension(strings.ToLower(filepath.Ext(a.Name)))); t != "" && !InlineImage(t) {
		return t
	}
	return sniffed
}

// bare is a media type without parameters, lower-cased; "" when unparsable.
func bare(t string) string {
	mt, _, err := mime.ParseMediaType(strings.TrimSpace(t))
	if err != nil {
		return ""
	}
	return mt
}

// safeName is a plain file name: the last path element, no control
// characters, not "." or "..", at most maxNameBytes (the extension kept).
// An empty one becomes attachment-<n> with an extension for its type.
func safeName(name string, i int, mediaType string) string {
	if j := strings.LastIndexAny(name, `/\`); j >= 0 {
		name = name[j+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "attachment-" + strconv.Itoa(i+1)
		if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
			name += preferredExt(mediaType, exts)
		}
	}
	if len(name) > maxNameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := name[:maxNameBytes-len(ext)]
		for !utf8.ValidString(stem) { // never cut a rune in half
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}

// preferredExt picks the usual extension of a type (mime lists .jpe before
// .jpg).
func preferredExt(mediaType string, exts []string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "text/plain":
		return ".txt"
	}
	return exts[0]
}

// uniqueName suffixes a name already used in this prompt: a.png, a-2.png.
func uniqueName(name string, used map[string]bool) string {
	out := name
	ext := filepath.Ext(name)
	for n := 2; used[out]; n++ {
		out = strings.TrimSuffix(name, ext) + "-" + strconv.Itoa(n) + ext
	}
	used[out] = true
	return out
}

func size(n int) string {
	if n >= 1<<20 {
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	}
	return strconv.Itoa(n>>10) + " KiB"
}
