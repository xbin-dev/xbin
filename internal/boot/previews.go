package boot

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/preview"
)

// enablePreviews sets up the preview service (D234, docs/protocol/
// preview.md) with the limits the configuration names. It is always
// mounted: where this xbind can't render — no --isolate, a rootfs without
// Chromium, XBIN_PREVIEWS=off — the route answers 503 with the reason and
// GET /api/xbin/preview reports it.
func (st *State) enablePreviews() {
	cfg := st.Cfg
	svc := &preview.Service{Rootfs: st.rootfs}
	if v := strings.ToLower(strings.TrimSpace(cfg.Previews)); v == "off" || v == "0" || v == "false" {
		svc.Off = "switched off for this workspace (XBIN_PREVIEWS=off)"
	}
	svc.PerUser = previewInt("XBIN_PREVIEW_PER_USER", cfg.PreviewPerUser, preview.DefaultPerUser, 1, 16)
	svc.Total = max(previewInt("XBIN_PREVIEW_TOTAL", cfg.PreviewTotal, preview.DefaultTotal, 1, 64), svc.PerUser)
	svc.Timeout = preview.DefaultTimeout
	if v := strings.TrimSpace(cfg.PreviewTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 10*time.Second && d <= 2*time.Minute {
			svc.Timeout = d
		} else {
			slog.Warn("XBIN_PREVIEW_TIMEOUT: want a duration from 10s to 120s — using the default", "value", v, "default", preview.DefaultTimeout)
		}
	}
	svc.MemMax = max(parseBytes("XBIN_PREVIEW_MEM", cfg.PreviewMem, preview.DefaultMemMax), 256<<20)
	if st.Run != nil && st.Run.Cgroup != nil {
		svc.Cgroup = st.Run.Cgroup
	}
	base := cfg.ExternalURL
	if base == "" {
		base = "http://" + cfg.Listen
	}
	st.Server.EnablePreview(svc, base)
}

func previewInt(key, v string, def, lo, hi int) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		slog.Warn(key+": want a number in range — using the default", "value", v, "min", lo, "max", hi, "default", def)
		return def
	}
	return n
}
