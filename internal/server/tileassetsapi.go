package server

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/assetscan"
	"github.com/xbin-dev/xbin/internal/auth"
)

// tileAssetReport is one tile's entry in GET /tile-assets: the scan plus how
// many of its findings fail under each strict mode.
type tileAssetReport struct {
	assetscan.Report
	Breaking map[string]int `json:"breaking"`
}

// apiTileAssets is the tile asset report (plans/tile-asset-auth.md,
// detection): each sandboxed tile's absolute /c/ references, inject:false
// and escaping symlinks — what strict tile asset gating will refuse — with
// the relative rewrite `bx fix assets` would make. Read-filtered like
// /components; ?component= narrows to one tile (and lists it even when
// clean). Chrome runs unsandboxed on the workspace origin with the cookie,
// so strict gating never touches it and it is not scanned.
func (s *Server) apiTileAssets(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	want := r.URL.Query().Get("component")
	out := []tileAssetReport{}
	found := false
	for _, c := range s.Reg.Components() {
		if want != "" && c.Path != want {
			continue
		}
		if !p.CanReadTile(c.Path) && !s.codeGranted(p, c.Path) {
			continue
		}
		found = true
		if !sandboxedFrame(c.Path, c) {
			continue
		}
		rep, err := assetscan.Scan(c.Dir, c.Path, assetscan.Options{Owner: s.owningComponent})
		if err != nil {
			continue // vanished mid-scan
		}
		if want == "" && len(rep.Findings) == 0 {
			continue
		}
		out = append(out, tileAssetReport{Report: rep, Breaking: map[string]int{
			TileAssetsTokens: rep.Breaking(TileAssetsTokens), TileAssetsOrigins: rep.Breaking(TileAssetsOrigins),
		}})
	}
	if want != "" && !found {
		apiErr(w, http.StatusNotFound, "no such component")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"mode": s.assetMode(), "tiles": out})
}
