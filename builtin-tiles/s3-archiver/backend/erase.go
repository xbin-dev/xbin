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
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
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

// sealedSubkey is the backup key id a sealed archive's cleartext header
// names — "XBINSEAL", a big-endian u32 length (at most 4 KiB), the JSON
// header — or "" for a plain tar or anything else. It reads only the
// header.
func sealedSubkey(r io.Reader) string {
	var head [len(sealMagic) + 4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || string(head[:len(sealMagic)]) != sealMagic {
		return ""
	}
	n := binary.BigEndian.Uint32(head[len(sealMagic):])
	if n == 0 || n > 4<<10 {
		return ""
	}
	hb := make([]byte, n)
	if _, err := io.ReadFull(r, hb); err != nil {
		return ""
	}
	var h struct {
		Subkey string `json:"subkey"`
	}
	if json.Unmarshal(hb, &h) != nil || !subkeyID.MatchString(h.Subkey) {
		return ""
	}
	return h.Subkey
}

// eraseSubkeys is POST /archive/erase {"subkeys": ["bk-…"]} → {"deleted": n}:
// every version sealed under those keys, across archive keys, and their
// markers. n counts the versions that were there: a marker whose version
// is already gone (pruned before its marker went with it) is just removed.
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
			obj := objKey(cfg.Prefix, key, version)
			there, err := s3.List(obj)
			if err != nil {
				fail(w, http.StatusBadGateway, err.Error())
				return
			}
			if slices.ContainsFunc(there, func(o s3obj) bool { return o.Key == obj }) {
				if err := s3.Delete(obj); err != nil {
					fail(w, http.StatusBadGateway, err.Error())
					return
				}
				deleted++
			}
			if err := s3.Delete(m.Key); err != nil {
				fail(w, http.StatusBadGateway, err.Error())
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
}
