package xbin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// UserAccess is what a person may do on this tile, as xbind says now
// (AccessOf).
type UserAccess struct {
	User string `json:"user"` // the id, as xbind keeps it (lower case)
	// Level is the person's access level on this tile: none | read | write
	// | terminal — what CallerInfo.UserLevel would say if they called now.
	// none for a disabled or unknown account.
	Level string `json:"level"`
	// Active: the id is an account that can sign in (it exists and isn't
	// disabled).
	Active bool `json:"active"`
}

// CanRead reports read access or more, on an active account: whether the
// person may use this tile at all.
func (a UserAccess) CanRead() bool {
	return a.Active && (a.Level == "read" || a.Level == "write" || a.Level == "terminal")
}

// CanWrite reports write access or more, on an active account.
func (a UserAccess) CanWrite() bool {
	return a.Active && (a.Level == "write" || a.Level == "terminal")
}

// AccessOf asks xbind what a person may do on this tile now (GET
// /api/xbin/access/<user>, from the backend: its instance token).
// CallerInfo.UserLevel tells a tile about the person calling it; AccessOf is
// for a credential the tile keeps past that call — an SSH key or an API
// token a person registered on its page — to check at each use that its
// person hasn't been removed from the workspace or from the tile since. It
// answers only about this tile. An unknown id is {Level: "none", Active:
// false}, not an error; an error means xbind didn't answer (fail closed).
//
//	a, err := xbin.AccessOf(ctx, key.User)
//	if err != nil || !a.CanRead() { refuse }
func AccessOf(ctx context.Context, user string) (UserAccess, error) {
	user = strings.TrimPrefix(strings.TrimSpace(user), "user:")
	if user == "" {
		return UserAccess{}, fmt.Errorf("access: no user")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://xbin/api/xbin/access/"+url.PathEscape(user), nil)
	if err != nil {
		return UserAccess{}, err
	}
	resp, err := Client().Do(req)
	if err != nil {
		return UserAccess{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return UserAccess{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		text := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			text = e.Error
		}
		return UserAccess{}, fmt.Errorf("access: %s: %s", resp.Status, text)
	}
	var a UserAccess
	if err := json.Unmarshal(body, &a); err != nil {
		return UserAccess{}, fmt.Errorf("access: a garbled answer: %w", err)
	}
	if a.Level == "" {
		a.Level = "none"
	}
	return a, nil
}
