package agent

// ATTACHMENTS: files sent with a prompt (POST /term/sessions/<id>/prompt
// {text, attachments}, plans/native.md §13 — a photo, a screenshot, a log,
// a PDF from the phone). The model, the limits and the normalisation are
// the ACP client's (sdk/acp attachments.go); the ACP driver drops every
// file inside the sandbox through the host (internal/agent/acp prompt.go).

import (
	"github.com/xbin-dev/xbin/sdk/acp"
)

type (
	// Attachment is one file of a prompt.
	Attachment = acp.Attachment
	// Prompt is one user turn: the text and its attachments (either may be
	// empty, not both).
	Prompt = acp.Prompt
	// AttachmentInfo is what the transcript keeps of an attachment (the
	// user's message.delta `attachments`): never the bytes.
	AttachmentInfo = acp.AttachmentInfo
)

// Limits (decoded bytes): sdk/acp attachments.go says why each is what it is.
const (
	MaxAttachments       = acp.MaxAttachments
	MaxImageBytes        = acp.MaxImageBytes
	MaxInlineImagesBytes = acp.MaxInlineImagesBytes
	MaxFileBytes         = acp.MaxFileBytes
	MaxAttachmentsBytes  = acp.MaxAttachmentsBytes
	MaxInlineText        = acp.MaxInlineText
)

// Errors PrepareAttachments returns (wrapped): ErrAttachmentTooLarge is a
// 413, ErrBadAttachment a 400. ErrUnsupportedContent is a driver's: the
// agent cannot take this kind of content (a 400).
var (
	ErrAttachmentTooLarge = acp.ErrAttachmentTooLarge
	ErrBadAttachment      = acp.ErrBadAttachment
	ErrUnsupportedContent = acp.ErrUnsupportedContent
)

// InlineImage reports whether a media type is an image the model sees
// itself (png, jpeg, gif, webp).
func InlineImage(mediaType string) bool { return acp.InlineImage(mediaType) }

// IsText reports whether data reads as text (valid UTF-8, no NUL).
func IsText(data []byte) bool { return acp.IsText(data) }

// PrepareAttachments checks a prompt's attachments against the limits and
// normalises them: plain, unique file names and the media type the bytes
// are.
func PrepareAttachments(in []Attachment) ([]Attachment, error) { return acp.PrepareAttachments(in) }
