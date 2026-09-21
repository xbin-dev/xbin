package auth

// CanTerminalTileVia is CanTerminalTile for the session API (D74): a
// terminal token passes for its OWN tile while the driving user still holds
// terminal level there (an owner-driven one always); other tiles and every
// other element are refused, as CanTerminalTile refuses them.
func (p Principal) CanTerminalTileVia(path string) bool {
	if p.Component == "" || p.Via != "terminal" {
		return p.CanTerminalTile(path)
	}
	if path != p.Component {
		return false
	}
	if p.UserID == "" {
		return true
	}
	if p.Access != nil {
		return p.Access.CanTerminalTile(path)
	}
	return p.User != nil && p.User.CanTerminalTile(path)
}
