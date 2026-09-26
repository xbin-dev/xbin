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
//     sent something it would drop. Inline only up to agent.MaxImageBytes
//     each and agent.MaxInlineImagesBytes per prompt (the model APIs' own
//     limits, and every later turn carries them): past that an image is a
//     link only, a file the agent opens with its tools;
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

// Prompt starts a turn. One at a time: the slot is taken before the files
// move (preparing), so a second prompt is refused at once instead of
// pushing its files too, and Cancel can abort the hand-off (ErrCancelled —
// no turn starts).
func (c *Client) Prompt(ctx context.Context, p agent.Prompt) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return agent.ErrEnded
	}
	if c.busy || c.preparing {
		c.mu.Unlock()
		return agent.ErrBusy
	}
	caps := c.promptCaps
	for _, a := range p.Attachments {
		if agent.InlineImage(a.Mime) && !caps.Image {
			c.mu.Unlock()
			return fmt.Errorf("%w: %s — this agent does not take images (it did not advertise promptCapabilities.image)", agent.ErrUnsupportedContent, a.Name)
		}
	}
	pctx, cancel := context.WithCancelCause(ctx)
	c.preparing, c.prepCancel = true, cancel
	c.mu.Unlock()
	blocks, infos, err := c.promptBlocks(pctx, p, caps)
	c.mu.Lock()
	c.preparing, c.prepCancel = false, nil
	cancelled := errors.Is(context.Cause(pctx), agent.ErrCancelled)
	cancel(nil)
	switch {
	case cancelled:
		c.mu.Unlock()
		return agent.ErrCancelled
	case err != nil:
		c.mu.Unlock()
		return err
	case c.closed:
		c.mu.Unlock()
		return agent.ErrEnded
	}
	c.busy = true
	c.turn++
	turn := c.turn
	c.tools = map[string]string{}
	c.usage = nil
	sid := c.sessionID
	c.mu.Unlock()
	echo := map[string]any{"role": "user", "text": p.Text}
	if len(infos) > 0 { // the transcript names them; the bytes are not logged
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
// attachment (dropped in the sandbox first — see the file comment) — and
// the transcript's entry for each (inline: the model sees it with the
// prompt).
func (c *Client) promptBlocks(ctx context.Context, p agent.Prompt, caps PromptCapabilities) ([]ContentBlock, []agent.AttachmentInfo, error) {
	var blocks []ContentBlock
	if p.Text != "" || len(p.Attachments) == 0 {
		blocks = append(blocks, ContentBlock{Type: "text", Text: p.Text})
	}
	var infos []agent.AttachmentInfo
	imageBudget := agent.MaxInlineImagesBytes
	for _, a := range p.Attachments {
		path, err := c.dropFile(ctx, a)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				err = cause
			}
			return nil, nil, fmt.Errorf("could not hand %s to the agent's sandbox: %w", a.Name, err)
		}
		uri := (&url.URL{Scheme: "file", Path: path}).String()
		link := ContentBlock{Type: "resource_link", URI: uri, Name: a.Name, MimeType: a.Mime, Size: int64(len(a.Data))}
		info := a.Info()
		switch {
		case agent.InlineImage(a.Mime) && len(a.Data) <= agent.MaxImageBytes && len(a.Data) <= imageBudget:
			imageBudget -= len(a.Data)
			info.Inline = true
			blocks = append(blocks, ContentBlock{Type: "image", Data: base64.StdEncoding.EncodeToString(a.Data), MimeType: a.Mime}, link)
		case caps.EmbeddedContext && len(a.Data) <= agent.MaxInlineText && agent.IsText(a.Data):
			info.Inline = true
			blocks = append(blocks, ContentBlock{Type: "resource", Resource: &EmbeddedResource{URI: uri, MimeType: a.Mime, Text: string(a.Data)}})
		default: // a file the agent opens itself — an image too big to go inline among them
			blocks = append(blocks, link)
		}
		infos = append(infos, info)
	}
	return blocks, infos, nil
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
