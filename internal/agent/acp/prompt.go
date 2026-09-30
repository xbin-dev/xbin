package acp

// A prompt's files, xbind's way: sdk/acp's client builds the content blocks
// (images inline, small text embedded, the rest linked); every file is
// first handed to the agent host, which drops it inside the sandbox
// (_xbin/attach → a path under the sandbox's private /tmp), so the agent
// can read it with its own tools, copy it into the tile, attach it to a
// commit.

import (
	"context"
	"errors"

	"github.com/xbin-dev/xbin/internal/agent"
)

// dropFile asks the agent host to write one attachment inside the sandbox;
// the path is where the agent finds it. (The client bounds each call.)
func dropFile(ctx context.Context, conn *Conn, a agent.Attachment) (string, error) {
	var res AttachResult
	if err := conn.CallCtx(ctx, MXbinAttach, AttachParams{Name: a.Name, Data: a.Data}, &res); err != nil {
		return "", err
	}
	if res.Path == "" {
		return "", errors.New("the host gave no path")
	}
	return res.Path, nil
}
