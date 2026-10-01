package main

// doctor_partitions.go — bx doctor's partition checks (plans/partitions/06
// §7; C23: bx doctor is the operator surface that ships with xbind): tiles
// whose partition mode waits for a manager, was kept, or is invalid;
// partitioned tiles without isolation; who can change a partitioned tile's
// code while it runs live (the trust warnings); global binds on partitioned
// tiles, for review; tiles bound to a partitioned tile without a global
// instance; sandbox managers that don't keep people apart; untracked files
// in partitioned tiles' directories (the directory is shared code: a file a
// person leaves there is everyone's — xbind lists them with a confined git
// in each tile's own repository, ?untracked=1); caps hit recently; orphaned
// partitions. Admin credentials only for the workspace-wide rows; nothing
// is printed against an xbind without partitions.

import (
	"fmt"
	"sort"
	"strings"
)

// doctorPartitions runs the checks; ws is the workspace root (unused: xbind
// answers every check).
func doctorPartitions(_ string, warn, ok func(string, ...any)) {
	var out struct {
		Isolated *bool `json:"isolated"`
		Tiles    []struct {
			Tile    string `json:"tile"`
			State   string `json:"state"`
			Error   string `json:"error"`
			Request *struct {
				Declined bool `json:"declined"`
			} `json:"request"`
			Spec struct {
				User, Global bool
			} `json:"spec"`
			Trust           []string            `json:"trust"`
			GlobalBinds     map[string][]string `json:"globalBinds"`
			BoundNoGlobal   []string            `json:"boundWithoutGlobal"`
			ManagersLacking []string            `json:"managersLacking"`
			Untracked       []string            `json:"untracked"`
			UntrackedCount  int                 `json:"untrackedCount"`
			UntrackedError  string              `json:"untrackedError"`
			CapsHit         *struct {
				At    string `json:"at"`
				Kind  string `json:"kind"`
				Count int    `json:"count"`
			} `json:"capsHit"`
		} `json:"tiles"`
		Orphans []struct {
			Tile, User, Reason, Since, Partition string
		} `json:"orphans"`
	}
	if apiJSON("GET", "/api/xbin/partitions?untracked=1", nil, &out) != nil {
		return // an xbind without partitions, or no access
	}
	partitioned := 0
	for _, t := range out.Tiles {
		switch {
		case t.State == "pending":
			warn("%s: a partition mode switch is requested and nothing of it runs until a manager decides — bx partition switch|keep %s", t.Tile, t.Tile)
		case t.State == "invalid":
			warn("%s: its partition request is invalid, so it doesn't run: %s", t.Tile, t.Error)
		case t.Request != nil && t.Request.Declined:
			fmt.Printf("  · %s keeps its partition mode though its code asks for another (declined; bx partition switch %s takes it)\n", t.Tile, t.Tile)
		}
		if !t.Spec.User {
			continue
		}
		partitioned++
		for _, w := range t.Trust {
			warn("%s", w)
		}
		slots := make([]string, 0, len(t.GlobalBinds))
		for slot := range t.GlobalBinds {
			slots = append(slots, slot)
		}
		sort.Strings(slots)
		for _, slot := range slots {
			fmt.Printf("  · %s: global bind %s → %s reaches every person's partition (review)\n", t.Tile, slot, strings.Join(t.GlobalBinds[slot], ", "))
		}
		for _, r := range t.BoundNoGlobal {
			warn("%s is bound to %s, which has no global instance: its calls are refused (403)", r, t.Tile)
		}
		for _, m := range t.ManagersLacking {
			warn("%s: sandbox manager %s doesn't keep people apart (its hello lacks \"partitions\"): every person shares its sandboxes", t.Tile, m)
		}
		switch {
		case t.UntrackedError != "":
			warn("%s: its untracked files couldn't be listed: %s", t.Tile, t.UntrackedError)
		case t.UntrackedCount > 0:
			warn("%s: %d untracked file(s) in its directory (%s): the directory is shared code — a file a person leaves there is everyone's", t.Tile, t.UntrackedCount, strings.Join(firstN(t.Untracked, 3), ", "))
		}
		if h := t.CapsHit; h != nil {
			warn("%s: people's partitions met the running caps recently (%d time(s) since xbind started, last %s: %s) — bx partition limits %s", t.Tile, h.Count, h.At, h.Kind, t.Tile)
		}
	}
	if out.Isolated != nil && !*out.Isolated && partitioned > 0 {
		warn("%d partitioned tile(s), but xbind runs without --isolate: no person's partition starts", partitioned)
	}
	for _, o := range out.Orphans {
		fmt.Printf("  · %s: %s's partition is orphaned (%s since %s); swept 30 days after — bx partition purge %s --partition %s --yes\n", o.Tile, o.User, o.Reason, o.Since, o.Tile, o.Partition)
	}
	if partitioned > 0 {
		ok("%d partitioned tile(s) checked", partitioned)
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], "…")
	}
	return s
}
