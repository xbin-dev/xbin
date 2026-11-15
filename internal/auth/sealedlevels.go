package auth

import (
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// A sealed component (internal/sealed) has no source and no dev sandbox:
// the write and terminal levels don't exist there, for admins and the root
// token too — no terminal, no agent session, no "*" pattern reaches one
// (CanTerminalTile, auth.go). Its logs are the admins' (obs.canReadLogs).
func (p Principal) CanWriteTile(path string) bool {
	return !util.SealedPath(path) && p.tileLevel(path, users.LevelWrite)
}
