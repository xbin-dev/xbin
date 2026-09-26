package acp

// Prompts: a turn's text and its attachments, as ACP content blocks.
//
// Every attachment is first handed to the agent host, which drops it inside
// the sandbox (_xbin/attach → a path under the sandbox's private /tmp), so
// the agent can read it with its own tools, copy it into the tile, attach it
// to a commit. Then, by what the agent advertised (promptCapabilities):
//
//   - an inline image (png, jpeg, gif, webp) → an image block the model sees,
//     plus a resource_link naming the dropped file; an agent without the
//     image capability is refused (agent.ErrUnsupportedContent) rather than
//     sent something it would drop;
//   - a small text file (≤ agent.MaxInlineText) → an embedded resource (the
//     model reads it with the prompt; its uri is the dropped file) when the
//     agent takes embedded context;
//   - anything else → a resource_link to the dropped file (every agent takes
//     one: the baseline). No blob resources: claude-agent-acp ignores them.
//
// A text-only prompt is exactly the one text block it always was.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// attachTimeout bounds one file's hand-off to the host.
const attachTimeout = 30 * time.Second

// Send starts a turn with text only.
func (c *Client) Send(ctx context.Context, text string) error {
	return c.Prompt(ctx, agent.Prompt{Text: text})
}

// Prompt starts a turn. One at a time.
func (c *Client) Prompt(ctx context.Context, p agent.Prompt) error {
	c.mu.Lock()
	closed, busy, caps := c.closed, c.busy, c.promptCaps
	c.mu.Unlock()
	if closed {
		return agent.ErrEnded
	}
	if busy {
		return agent.ErrBusy
	}
	for _, a := range p.Attachments {
		if agent.InlineImage(a.Mime) && !caps.Image {
			return fmt.Errorf("%w: %s — this agent does not take images (it did not advertise promptCapabilities.image)", agent.ErrUnsupportedContent, a.Name)
		}
	}
	blocks, err := c.promptBlocks(ctx, p, caps)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return agent.ErrEnded
	}
	if c.busy {
		c.mu.Unlock()
		return agent.ErrBusy
	}
	c.busy = true
	c.turn++
	turn := c.turn
	c.tools = map[string]string{}
	c.usage = nil
	sid := c.sessionID
	c.mu.Unlock()
	echo := map[string]any{"role": "user", "text": p.Text}
	if len(p.Attachments) > 0 { // the transcript names them; the bytes are not logged
		infos := make([]agent.AttachmentInfo, len(p.Attachments))
		for i, a := range p.Attachments {
			infos[i] = a.Info()
		}
		echo["attachments"] = infos
	}
	c.emit(agent.New(agent.EvMessageDelta, echo))
	c.setStatus(agent.StatusRunning, "")
	go func() {
		var res PromptResult
		err := c.conn.Call(MSessionPrompt, PromptParams{SessionID: sid, Prompt: blocks}, &res)
		c.mu.Lock()
		c.busy = false
		usage := c.usage
		c.mu.Unlock()
		if err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				return // the exit status says it
			}
			var re *Error
			if errors.As(err, &re) && re.Code == ErrAuthRequired {
				c.mu.Lock()
				c.authNeeded = true
				c.mu.Unlock()
			}
			c.emit(agent.New(agent.EvTurnEnd, map[string]any{"turn": turn, "stopReason": "error", "error": authHint(err, c.cfg).Error()}))
			c.setStatus(agent.StatusError, authHint(err, c.cfg).Error())
			return
		}
		c.setAuthNeeded(false)
		end := map[string]any{"turn": turn, "stopReason": res.StopReason}
		if usage != nil {
			end["usage"] = usage
		}
		c.emit(agent.New(agent.EvTurnEnd, end))
		c.setStatus(agent.StatusIdle, "")
	}()
	return nil
}

// promptBlocks builds the session/prompt content: the text, then each
// attachment (dropped in the sandbox first — see the file comment).
func (c *Client) promptBlocks(ctx context.Context, p agent.Prompt, caps PromptCapabilities) ([]ContentBlock, error) {
	var blocks []ContentBlock
	if p.Text != "" || len(p.Attachments) == 0 {
		blocks = append(blocks, ContentBlock{Type: "text", Text: p.Text})
	}
	for _, a := range p.Attachments {
		path, err := c.dropFile(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("could not hand %s to the agent's sandbox: %w", a.Name, err)
		}
		uri := (&url.URL{Scheme: "file", Path: path}).String()
		link := ContentBlock{Type: "resource_link", URI: uri, Name: a.Name, MimeType: a.Mime, Size: int64(len(a.Data))}
		switch {
		case agent.InlineImage(a.Mime):
			blocks = append(blocks, ContentBlock{Type: "image", Data: base64.StdEncoding.EncodeToString(a.Data), MimeType: a.Mime}, link)
		case caps.EmbeddedContext && len(a.Data) <= agent.MaxInlineText && agent.IsText(a.Data):
			blocks = append(blocks, ContentBlock{Type: "resource", Resource: &EmbeddedResource{URI: uri, MimeType: a.Mime, Text: string(a.Data)}})
		default:
			blocks = append(blocks, link)
		}
	}
	return blocks, nil
}

// dropFile asks the agent host to write one attachment inside the sandbox;
// the path is where the agent finds it.
func (c *Client) dropFile(ctx context.Context, a agent.Attachment) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	var res AttachResult
	if err := c.conn.CallCtx(ctx, MXbinAttach, AttachParams{Name: a.Name, Data: a.Data}, &res); err != nil {
		return "", err
	}
	if res.Path == "" {
		return "", errors.New("the host gave no path")
	}
	return res.Path, nil
}
