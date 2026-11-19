package broker

// builtinline.go — builtin.update {id}: a builtin's simple update as a
// line of a change set or a plan (D248; plans/wsagent/updates.md §2;
// docs/protocol/delegation.md §Change sets).
//
// It applies as POST /builtins/update {id, mode: "safe"} — the by-hand
// route, its handler, its requireWriter gate — as the approving person:
// merged when every file merges cleanly, installed when it is a missing
// essential page, and otherwise filed as a change proposal on its tile
// (mode "pr"), nothing written. Nothing here writes; this file describes
// the line as the person would apply it now:
//
//   - ALWAYS AN ADMIN'S: xbin:writer is a workspace admin's (or the Tile
//     Manager's, a component's — never a person's). Anyone else reads the
//     line and "needs an admin" (the server's describePlan asks for an
//     admin for any person who is none: an org admin too);
//   - the agent chose WHICH builtin, nothing more: the kind takes {id} and
//     refuses any other key; the files are the binary's embedded copy;
//   - the line is xbind's — the builtin, its path, the versions, how many
//     files change, and the dry run's verdict: "your local changes are
//     kept", or "conflicts with your changes: filed as a proposal for
//     review instead". A scaffold unit is warned (everyone sees it), so it
//     starts unticked;
//   - pinned, no update, not installed, a missing page's path taken: a
//     blocked line with the builtin's own reason (delegation.Builtin*);
//     an id that is no builtin, a sealed path, a tile a plan still makes:
//     no line at all (400);
//   - PINNED to what it was described from (the installed files, the
//     embedded copy, the verdict): an edit before Apply makes it stale.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// ChangeBuiltinUpdate is a builtin's simple update.
const ChangeBuiltinUpdate = delegation.KindBuiltinUpdate

// builtinWarnScaffold: a scaffold unit is the workspace's own chrome.
const builtinWarnScaffold = "SCAFFOLD: %s is part of this workspace's own chrome — the shell, a console or a page everyone has: updating it changes what everyone here sees"

// builtinWhat names a unit in a line: "the builtin tile llm-gw (apps/llm-gw)",
// "the workspace's own shell", "the workspace's own page tiles/apidocs".
func builtinWhat(u builtins.Unit) string {
	switch {
	case u.Kind == "tile":
		return fmt.Sprintf("the builtin tile %s (%s)", u.Name, u.InstallPath)
	case u.InstallPath == "shell":
		return "the workspace's own shell"
	}
	return "the workspace's own page " + u.InstallPath
}

// builtinVersions is "from v3 to v4" for a tile; a scaffold unit's copies
// carry no version number of their own.
func builtinVersions(u builtins.Unit) (from, to, span string) {
	if u.Kind != "tile" {
		return "this workspace's copy", "this xbin's copy", "to this xbin's copy"
	}
	return fmt.Sprintf("v%d", u.From), fmt.Sprintf("v%d", u.To), fmt.Sprintf("from v%d to v%d", u.From, u.To)
}

// filesWords lists files for an effect: at most eight by name.
func filesWords(files []string) string {
	if len(files) <= 8 {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:8], ", ") + fmt.Sprintf(" and %d more", len(files)-8)
}

// previewBuiltinUpdate describes builtin.update (the file comment).
func (b *Broker) previewBuiltinUpdate(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error) {
	var a delegation.BuiltinArgs
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	a.ID = strings.TrimSpace(a.ID)
	if err := lineNames("id", a.ID); err != nil {
		return delegation.ChangePreview{}, err
	}
	switch {
	case a.ID == "":
		return delegation.ChangePreview{}, errors.New("builtin.update names a builtin by its id (updates lists them: tile:llm-gw, scaffold:shell)")
	case b.updater == nil:
		return delegation.ChangePreview{}, errors.New("this xbind tracks no builtin updates")
	}
	if no := sealedRefusal(sealedNone(a.ID, "it is updated with xbin itself")); no != nil { // the by-hand route's own refusal
		return delegation.ChangePreview{}, errors.New(no.msg)
	}
	id := b.updater.ResolveID(a.ID)
	u, ok := b.updater.Unit(id)
	if !ok {
		return delegation.ChangePreview{}, fmt.Errorf("no builtin %q: updates lists the builtins that have one", a.ID)
	}
	if _, ok := plannedTile(planned, u.InstallPath); ok {
		return delegation.ChangePreview{}, fmt.Errorf("%s is a tile this plan still makes: a builtin is updated where it is installed", u.InstallPath)
	}
	what := builtinWhat(u)
	from, to, span := builtinVersions(u)
	pv := delegation.ChangePreview{Tiles: []string{u.InstallPath}, Admin: true,
		Call:       delegation.AdminCall{Method: http.MethodPost, Path: "/builtins/update", Body: mustJSON(map[string]string{"id": id, "mode": "safe"})},
		Line:       fmt.Sprintf("Update %s %s", what, span),
		Before:     mustJSON(map[string]any{"version": u.From}),
		After:      mustJSON(map[string]any{"version": u.To}),
		BeforeText: from, AfterText: to,
		UndoByHand: "xbin doesn't undo an update: what it wrote is a change in " + u.InstallPath + "'s files, which a terminal there shows and restores with git (git diff, git checkout -- <file>)"}
	if u.Kind == "scaffold" {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(builtinWarnScaffold, u.InstallPath))
	}
	sp, err := b.updater.SafePlan(id)
	switch {
	case errors.Is(err, builtins.ErrPinned):
		pv.Blocked = delegation.BuiltinPinned
	case errors.Is(err, builtins.ErrUpToDate):
		pv.Blocked = delegation.BuiltinUpToDate
	case errors.Is(err, builtins.ErrNotInstalled):
		pv.Blocked = delegation.BuiltinNotInstalled
	case errors.Is(err, builtins.ErrOccupied):
		pv.Blocked = delegation.BuiltinOccupied
	case err != nil:
		pv.Blocked = "xbin can't tell what the update would do: " + err.Error()
	}
	if err == nil {
		pv.Pin = sp.Pin
		backend := false
		if c, ok := b.Reg.Component(u.InstallPath); ok {
			backend = c.HasBackend()
		}
		n := len(sp.Changes)
		switch sp.Mode {
		case builtins.SafeInstall:
			pv.Line = fmt.Sprintf("Install %s, which this workspace predates: %s", what, plural(n, "file"))
			pv.BeforeText, pv.AfterText = "not installed", to
			pv.Effects = append(pv.Effects, "files written in "+u.InstallPath+": "+filesWords(sp.Changes))
		case builtins.SafePropose:
			pv.Line = fmt.Sprintf("Update %s %s: %s change — conflicts with your changes: filed as a proposal for review instead", what, span, plural(n, "file"))
			pv.AfterText = "unchanged until the proposal is merged"
			pv.After = mustJSON(map[string]any{"version": u.From, "proposal": true})
			pv.Effects = append(pv.Effects,
				"nothing in "+u.InstallPath+" changes now: xbin files the update as a change proposal on "+u.InstallPath+", which that tile's agent (or a terminal there) merges — closing it merged completes the update",
				"the files that conflict with changes made here: "+filesWords(sp.Conflicts))
		default: // merge, or an adopted copy that only gains files
			kept := "no file here was edited"
			if len(sp.Kept)+len(sp.Merged) > 0 {
				kept = "your local changes are kept"
			}
			pv.Line = fmt.Sprintf("Update %s %s: %s change — %s", what, span, plural(n, "file"), kept)
			if n > 0 {
				pv.Effects = append(pv.Effects, "files written or removed in "+u.InstallPath+": "+filesWords(sp.Changes))
			}
			if len(sp.Merged) > 0 {
				pv.Effects = append(pv.Effects, plural(len(sp.Merged), "file")+" changed both here and upstream "+map[bool]string{true: "is", false: "are"}[len(sp.Merged) == 1]+" merged line by line, without a conflict: "+filesWords(sp.Merged))
			}
			if len(sp.Kept) > 0 {
				pv.Effects = append(pv.Effects, plural(len(sp.Kept), "file")+" edited here that upstream didn't change "+map[bool]string{true: "stays", false: "stay"}[len(sp.Kept) == 1]+" as "+map[bool]string{true: "it is", false: "they are"}[len(sp.Kept) == 1]+": "+filesWords(sp.Kept))
			}
		}
		if backend && sp.Mode != builtins.SafePropose {
			pv.Effects = append(pv.Effects, u.InstallPath+"'s backend rebuilds and restarts")
		}
		for _, n := range sp.Notes {
			pv.Effects = append(pv.Effects, n)
		}
	}
	// the by-hand route's gate: a person who may not apply it reads the line
	// and the refusal (an admin's line: "needs an admin" for them)
	if !b.writerOK(p) {
		pv.Blocked, pv.Forbidden = writerRefusal, true
	}
	return pv, nil
}
