package users

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The persisted user row's decoding: the current shape plus the legacy
// tiles/terminal one (D15-style load-and-rewrite).

// UnmarshalJSON accepts both the current shape (tiles as a path→level map) and
// the legacy one (tiles as a []string allow-list + a global "terminal" bool):
// legacy entries load as `write`, or `terminal` when the flag was set — the
// exact power they had under the old model. Rewritten to the new shape on the
// next save (D15-style).
func (u *User) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID            string          `json:"id"`
		Name          string          `json:"name"`
		Email         string          `json:"email"`
		Role          string          `json:"role"`
		Tiles         json.RawMessage `json:"tiles"`
		Terminal      bool            `json:"terminal"` // legacy global flag
		CanCreate     []string        `json:"canCreate"`
		TermAPI       bool            `json:"termApi"`
		TermNet       bool            `json:"termNet"`
		NoPersonal    bool            `json:"noPersonalTiles"`
		NoTerminal    bool            `json:"noTerminal"`
		Sets          []string        `json:"sets"`
		NetSets       []string        `json:"netSets"`
		Disabled      bool            `json:"disabled"`
		PassHash      string          `json:"passHash"`
		InviteHash    string          `json:"inviteHash"`
		InviteExpires int64           `json:"inviteExpires"`
		Created       int64           `json:"created"`
		RoleVia       string          `json:"roleVia"`
		LastLogin     int64           `json:"lastLogin"`
		LastLoginVia  string          `json:"lastLoginVia"`
		LastSSO       int64           `json:"lastSSO"`
		SSOGroups     []string        `json:"ssoGroups"`
		SSOSyncError  string          `json:"ssoSyncError"`
		Devices       []Device        `json:"devices"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	tiles, err := ParseTiles(raw.Tiles, raw.Terminal)
	if err != nil {
		return fmt.Errorf("user %q: %w", raw.ID, err)
	}
	// Every persisted field must be listed here — a field missing from this
	// literal is silently dropped on reload (the 2026-09-05 email incident).
	*u = User{
		ID: raw.ID, Name: raw.Name, Email: raw.Email, Role: raw.Role, Tiles: tiles,
		CanCreate: raw.CanCreate, TermAPI: raw.TermAPI, TermNet: raw.TermNet,
		NoPersonalTiles: raw.NoPersonal, NoTerminal: raw.NoTerminal, Sets: raw.Sets, NetSets: raw.NetSets,
		Disabled: raw.Disabled, PassHash: raw.PassHash, InviteHash: raw.InviteHash,
		InviteExpires: raw.InviteExpires, Created: raw.Created,
		RoleVia: raw.RoleVia, LastLogin: raw.LastLogin, LastLoginVia: raw.LastLoginVia, LastSSO: raw.LastSSO,
		SSOGroups: raw.SSOGroups, SSOSyncError: raw.SSOSyncError, Devices: raw.Devices,
	}
	return nil
}

// ParseTiles decodes a tiles field that may be either shape (see
// User.UnmarshalJSON); the API uses it too, so old clients that still POST
// {tiles: [...], terminal: bool} keep working. nil/absent ⇒ empty map.
func ParseTiles(raw json.RawMessage, legacyTerminal bool) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]string{}, nil
	}
	if raw[0] == '[' { // legacy allow-list
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("tiles: %w", err)
		}
		level := LevelWrite
		if legacyTerminal {
			level = LevelTerminal
		}
		tiles := make(map[string]string, len(list))
		for _, t := range list {
			if t = strings.TrimSpace(t); t != "" {
				tiles[t] = level
			}
		}
		return tiles, nil
	}
	var tiles map[string]string
	if err := json.Unmarshal(raw, &tiles); err != nil {
		return nil, fmt.Errorf("tiles: %w", err)
	}
	for k, v := range tiles {
		if levelRank(v) == 0 && v != LevelNone {
			return nil, fmt.Errorf("tiles[%q]: unknown level %q (want read|write|terminal, or none to exclude)", k, v)
		}
	}
	if tiles == nil {
		tiles = map[string]string{}
	}
	return tiles, nil
}
