package users

// The workspace's native-runtime switch (docs/elements.md §Native app UI):
// an admin can turn the xbin app's native tile UIs off for the whole
// workspace. whoami then reports native.runtime 0 — the app opens every
// tile as its web page — and xbind refuses the runtime documents
// (/c/<tile>/?native=1) the app would load. Off by default; kept in
// users.json with the rest of the workspace policy.

// NativeRuntimeDisabled reports whether an admin turned native tile UIs off.
func (s *Store) NativeRuntimeDisabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nativeRuntimeOff
}

// SetNativeRuntimeDisabled flips the switch (admin; the caller checks).
func (s *Store) SetNativeRuntimeDisabled(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeRuntimeOff = v
	return s.persistLocked()
}
