package acp

// abort ends a Start that failed before its read loop existed (no spawner,
// the spawn itself failing — e.g. xbind's host process killed while starting):
// the loop is what closes the event channel, and the session's pump waits on
// that close to tear down — without it the session never ended, never left
// the directory, and held its layer forever. Error status first, so the
// clients see why.
func (c *Client) abort(err error) error {
	c.setStatus(StatusError, err.Error())
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	close(c.stop)
	c.emu.Lock()
	close(c.events)
	c.emu.Unlock()
	close(c.done)
	return err
}
