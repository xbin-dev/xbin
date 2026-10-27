package broker

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/limits"
)

// Hits as alerts, and alerts' raise actions decorated per viewer (D203,
// docs/protocol.md GET /alerts). A hit is sticky: its alert stays until a
// decision on the kind or an approver's dismissal. An alert's action — its
// choices' approvable, its approvers, the alert's dismiss — depends on who
// looks: GET /alerts resolves the shell's frame to the person driving it
// and the admin console to its admin driver (limitsViewerOf), and clones
// every alert it decorates.

// hitAlertKind is the /alerts kind of a hit of kind k.
func hitAlertKind(k limits.Kind) string {
	switch k {
	case limits.MemMiB:
		return "oom"
	case limits.DiskGiB:
		return "quota"
	case limits.SbxMemMiB, limits.SbxVCPUs, limits.SbxDiskGiB:
		return "sbx-cap"
	}
	return "limit-cap"
}

// limitHitAlerts are the store's hits p may see, live — each with a raise
// action, and a dismiss for approvers — plus, for admins, the store's own
// trouble (kind tile-limits). others are the alerts already gathered: a
// disk hit whose scope another alert already offers to raise (the disk
// monitor's quota alert) isn't repeated.
func (b *Broker) limitHitAlerts(p auth.Principal, others []Alert) []Alert {
	v := b.limitsViewerOf(p)
	var out []Alert
	if v.admin {
		if err := b.tlUnreadable(); err != nil {
			out = append(out, Alert{Level: "crit", Kind: "tile-limits", Message: tileLimitsRel + " can't be read (" + err.Error() +
				") — every tile has its default limits and no limit decision is saved: fix or remove the file by hand"})
		}
	}
	f := b.tl.cur.Load()
	if f == nil {
		return out
	}
	offered := map[string]bool{}
	for _, a := range others {
		if a.Action != nil && a.Action.Kind == limits.DiskGiB {
			offered[limitScopeKey(a.Action.Scope)] = true
		}
	}
	for _, k := range limits.Kinds {
		books := booksOf(f, k)
		for _, key := range sortedNames(books) {
			h, ok := books[key].Hits[k]
			if !ok || k == limits.DiskGiB && offered[key] {
				continue
			}
			tile := b.limitTileOf(key, k)
			switch {
			case h.Deployment != "" && !v.admin: // a non-primary deployment's: admins only (D127h)
				continue
			case !v.admin && (tile == "" || !v.p.CanReadTile(tile)):
				continue
			}
			a := Alert{Level: "warn", Kind: hitAlertKind(k), Tile: tile, Deployment: h.Deployment, Message: hitMessage(tile, key, k, h),
				Action: &limits.Action{Action: limits.ActionRaise, Tile: tile, Kind: k}}
			if k == limits.DiskGiB {
				a.Level = "crit"
				a.Action.Scope = scopePathOfKey(key)
			}
			if b.mayDecideKind(v, tile, k, b.defaultOf(tile, key, k)) {
				a.Dismiss = dismissRoute(tile, key, k)
			}
			out = append(out, a)
		}
	}
	return out
}

// tileHitAlerts are tile's own hits (its primary's and its people's
// partitions'; a scope's disk on the tile that roots it) for its status
// (GET /tile-status, bx status): the message, no decisions.
func (b *Broker) tileHitAlerts(tile string) []Alert {
	f := b.tl.cur.Load()
	if f == nil || tile == "" {
		return nil
	}
	var out []Alert
	for _, k := range limits.Kinds {
		key := tile
		if k == limits.DiskGiB {
			if c, ok := b.Reg.Component(tile); !ok || c.Scope != tile {
				continue
			}
			key = limitScopeKey(tile)
		}
		if h, ok := hitIn(f, key, k); ok && h.Deployment == "" {
			out = append(out, Alert{Level: "warn", Kind: hitAlertKind(k), Tile: tile, Message: hitMessage(tile, key, k, h)})
		}
	}
	return out
}

// defaultOf is kind k's default for tile (a scope's for disk).
func (b *Broker) defaultOf(tile, key string, k limits.Kind) int64 {
	if k == limits.DiskGiB {
		return b.diskDefault()
	}
	def, _ := b.kindDefault(tile, k)
	return def
}

// scopePathOfKey is the scope path of data key key ("" for the workspace).
func scopePathOfKey(key string) string {
	if key == "workspace" {
		return ""
	}
	return strings.ReplaceAll(key, "~", "/")
}

// dismissRoute is the route a POST to which dismisses the hit for everyone.
func dismissRoute(tile, key string, k limits.Kind) string {
	if k == limits.DiskGiB && tile == "" {
		scope := scopePathOfKey(key)
		if scope == "" {
			scope = "workspace"
		}
		return "/limits/dismiss?scope=" + url.QueryEscape(scope) + "&kind=" + string(k)
	}
	return "/limits/dismiss?tile=" + url.QueryEscape(tile) + "&kind=" + string(k)
}

// hitMessage words a hit for its banner; old shells show only this, so it
// names where to raise the limit.
func hitMessage(tile, key string, k limits.Kind, h limits.Hit) string {
	who := tile
	switch {
	case tile == "" && k == limits.DiskGiB:
		who = "the workspace scope"
		if s := scopePathOfKey(key); s != "" {
			who = "scope " + s
		}
	case h.Partition:
		who = "a person's partition of " + tile
	case h.Deployment != "":
		who = tile + " (deployment " + h.Deployment + ")"
	}
	at := ""
	if h.Value > 0 {
		at = " " + limits.Format(k, h.Value)
	}
	var what string
	detail := h.Detail
	switch k {
	case limits.MemMiB:
		what = "was OOM-killed at its" + at + " memory limit"
		// what says it: "(2 processes)", "(1 process) while the backend kept running"
		detail = strings.TrimSpace(strings.TrimPrefix(detail, "OOM-killed"))
	case limits.DiskGiB:
		what = "met its" + at + " disk quota"
	case limits.SbxMemMiB, limits.SbxVCPUs, limits.SbxDiskGiB:
		what = "met its tile sandboxes'" + at + " " + k.Meta().Label + " total"
	default:
		what = "met its" + at + " " + k.Meta().Label + " limit"
	}
	switch {
	case strings.HasPrefix(detail, "("):
		what += " " + detail
	case detail != "":
		what += " (" + detail + ")"
	}
	when := " at " + clock(h.At)
	if h.Count > 1 {
		when = fmt.Sprintf(", %d× since %s", h.Count, clock(h.Since))
	}
	target := tile
	if target == "" {
		target = "--scope " + scopeArg(key)
	}
	return who + " " + what + when + " — raise it: the tile's ⚙ → Limits, /xbin/limits, or `bx limits raise " + target + " " + k.Meta().Alias + "`"
}

func scopeArg(key string) string {
	if s := scopePathOfKey(key); s != "" {
		return s
	}
	return "workspace"
}

// clock is a hit's time as a banner says it — the same words for the same
// hit, whenever it's read (a person's dismissal keys on them).
func clock(t time.Time) string { return t.UTC().Format("Jan 2 15:04 UTC") }

// decorateAlerts fills each alert's raise action for p's viewer: the unit,
// the current limit, who may approve, and the +25/50/100 % choices with
// whether each fits and whether this viewer may approve it. Alerts are
// cloned before they change (their source keeps its own).
func (b *Broker) decorateAlerts(p auth.Principal, alerts []Alert) []Alert {
	var v limitsViewer
	var f *limits.File
	caps := map[limits.Kind]capInput{}
	for i, a := range alerts {
		if a.Action == nil || !a.Action.Kind.Valid() {
			continue
		}
		if f == nil {
			v, f = b.limitsViewerOf(p), b.tlDoc()
		}
		act := *a.Action
		act.Approvers, act.Choices = nil, nil
		k := act.Kind
		if act.Action == "" {
			act.Action = limits.ActionRaise
		}
		act.Unit = k.Meta().Unit
		key, tile := act.Tile, act.Tile
		var kf kindFacts
		if k == limits.DiskGiB {
			key = limitScopeKey(act.Scope)
			if tile == "" {
				tile = b.scopeRootOfKey(key)
			}
			kf = kindFacts{def: b.diskDefault(), max: b.diskMax(), enforced: b.disk != nil, placed: true, ask: b.manifestAskOf(key, k)}
		} else if c, ok := b.rootComponent(tile); ok {
			kf = b.tileFacts(c).kinds[k]
		} else {
			continue
		}
		if !kf.applies() { // not enforced here (yet): nothing to choose from
			a.Action = &act
			alerts[i] = a
			continue
		}
		ci, ok := caps[k]
		if !ok {
			ci = b.capInputs(k)[k]
			caps[k] = ci
		}
		cur := kf.effective(entryIn(f, key, k))
		act.Current = cur
		act.Approvers = b.approversFor(tile, k, limits.Step(k, cur, limits.Pcts[0]))
		act.Choices = b.choicesFor(v, tile, k, cur, ci.at(f, key))
		a.Action = &act
		alerts[i] = a
	}
	return alerts
}
