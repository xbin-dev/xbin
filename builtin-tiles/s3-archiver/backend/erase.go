package main

// erase.go — sealed archives and their backup keys
// (docs/overview/14-lifecycle.md §Sealed archives; PD-56). xbind seals every archive
// of a workspace with a vault under a backup subkey, and names the key's
// opaque id on the PUT (X-XBin-Backup-Subkey). This tile never sees key
// material or plaintext: it records the id as an empty marker object,
// <prefix>/.subkeys/<id>/<key>/<version>, because S3 listings carry names,
// sizes and times only. When xbind erases a key (the data sealed under it is
// unreadable from then on), POST /archive/erase deletes every version the
// key's markers name, and the markers.

import (
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"strings"
)

const (
	headerSubkey = "X-XBin-Backup-Subkey"
	sealMagic    = "XBINSEAL" // a sealed archive's first bytes; a plain tar's are "backup.json"
	maxErase     = 1024       // subkeys one erase names
)

var subkeyID = regexp.MustCompile(`^bk-[0-9a-f]{32}$`)

// markerKey is the marker of key's version sealed under subkey id.
func markerKey(prefix, id, key, version string) string {
	return path.Join(prefix, ".subkeys", id, key, version)
}

// eraseSubkeys is POST /archive/erase {"subkeys": ["bk-…"]} → {"deleted": n}:
// every version sealed under those keys, across archive keys, and their
// markers. A version retention already pruned deletes as nothing.
func eraseSubkeys(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	var req struct {
		Subkeys []string `json:"subkeys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.Subkeys) > maxErase {
		fail(w, http.StatusBadRequest, `need {"subkeys": ["bk-…", …]}`)
		return
	}
	deleted := 0
	for _, id := range req.Subkeys {
		if !subkeyID.MatchString(id) {
			fail(w, http.StatusBadRequest, "not a backup subkey id: "+id)
			return
		}
		dir := path.Join(cfg.Prefix, ".subkeys", id) + "/"
		markers, err := s3.List(dir)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, m := range markers {
			key, version, ok := strings.Cut(strings.TrimPrefix(m.Key, dir), "/")
			if !ok || key == "" || version == "" || strings.Contains(version, "/") {
				continue
			}
			if err := s3.Delete(objKey(cfg.Prefix, key, version)); err != nil {
				fail(w, http.StatusBadGateway, err.Error())
				return
			}
			if err := s3.Delete(m.Key); err != nil {
				fail(w, http.StatusBadGateway, err.Error())
				return
			}
			deleted++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
}
