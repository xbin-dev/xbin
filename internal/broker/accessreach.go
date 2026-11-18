package broker

// accessreach.go — what a person's level on a tile brings beyond the tile
// itself, for an access.set line (D246; accessline.go): a tile's writers
// change its code and every nested tile's (writersCover; a terminal on it
// binds its whole directory read-write, nested tiles included —
// term.scopedBinds; its PRs are decided by its writers), and that code runs
// with everything the tiles reach. So the line is judged on the tile's
// SUBTREE and on what those tiles reach through their grants and bindings:
//
//   - power: a grant of the admin API, a host capability, a device or all
//     source (grantTargetRefusal), the host's own network (refIsHostNet), a
//     call to a system component — held by a tile of the subtree or by one
//     it reaches. A raise there is never a change set's (accessline.go);
//   - reached tiles: the callees of its call and code: grants and the
//     providers its slots are bound to, followed the same way — bounded
//     (accessReachMax tiles) and cycle-safe (each tile once). A walk that
//     stops at its bound counts as power: what lies beyond isn't known;
//   - what the line lists: the nested tiles, the reached ones, the networks
//     its tiles are bound to, the LLM connector, the tiles with vault
//     secrets.

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// accessReachMax bounds the walk: the tiles looked at, the subtree's
// included.
const accessReachMax = 64

// accessNamesShown is how many names an effect lists before "and N more".
const accessNamesShown = 5

// accessReach is what a level on a tile brings (the file comment).
type accessReach struct {
	power   []string // "<what> (<which tile holds it>)"
	nested  []string // the registered tiles inside it
	reached []string // tiles outside its subtree its tiles reach
	nets    []string // the network refs its tiles' net slots are bound to (the host's aside: power)
	llm     []string // its tiles bound to the workspace's LLM connector
	vaults  []string // its tiles with vault secrets
	far     bool     // the walk stopped at accessReachMax
}

// accessReachOf walks tile's subtree and what it reaches (the file comment).
func (b *Broker) accessReachOf(tile string) accessReach {
	var r accessReach
	ws := b.Reg.Workspace()
	seen := map[string]bool{tile: true}
	queue := []string{tile}
	for _, c := range b.Reg.Components() {
		if strings.HasPrefix(c.Path, tile+"/") && !seen[c.Path] {
			seen[c.Path] = true
			r.nested = append(r.nested, c.Path)
			queue = append(queue, c.Path)
		}
	}
	sort.Strings(r.nested)
	add := func(list *[]string, s string) {
		if !slices.Contains(*list, s) {
			*list = append(*list, s)
		}
	}
	visit := func(callee string) {
		if callee == "" || seen[callee] {
			return
		}
		if _, ok := b.Reg.Component(callee); !ok {
			return
		}
		seen[callee] = true
		if util.SealedPath(callee) {
			add(&r.power, "a call to the system component "+callee)
			return
		}
		r.reached = append(r.reached, callee)
		queue = append(queue, callee)
	}
	for i := 0; i < len(queue); i++ {
		if i >= accessReachMax {
			r.far = true
			break
		}
		t := queue[i]
		for _, g := range ws.Grants {
			if g.From != t {
				continue
			}
			switch {
			case strings.HasPrefix(g.Target, "net:"), strings.HasPrefix(g.Target, "res:"):
			case grantTargetRefusal(g.Target) != "":
				add(&r.power, g.Target+" ("+t+")")
			default:
				callee, _, _ := strings.Cut(strings.TrimPrefix(g.Target, "code:"), "#")
				visit(callee)
			}
		}
		c, _ := b.Reg.Component(t)
		for slot, bind := range ws.Bindings[t] {
			for _, e := range bind {
				prov, _ := splitRef(e.Ref)
				switch {
				case b.refIsHostNet(t, prov):
					add(&r.power, "the host's network ("+t+")")
				case prov == LLMRef:
					add(&r.llm, t)
				case c != nil && isNetSlot(c.Manifest.Interfaces[slot].Kind):
					if prov != "" && prov != NetRefNone {
						add(&r.nets, prov)
					}
				default:
					visit(prov)
				}
			}
		}
		if _, err := os.Stat(b.vaultPath(t)); err == nil {
			add(&r.vaults, t)
		}
	}
	sort.Strings(r.power)
	sort.Strings(r.nets)
	sort.Strings(r.llm)
	sort.Strings(r.vaults)
	sort.Strings(r.reached)
	return r
}

func isNetSlot(kind string) bool { return kind == "net" || kind == "sandbox-net" }

// holdsPower reports a reach a raise is refused on.
func (r accessReach) holdsPower() bool { return len(r.power) > 0 || r.far }

// accessPowerRefusal is the refusal of a raise on tile, which holds power:
// the same words for everyone, the holders named only to a workspace admin
// (the grants of a tile the person reaches through another aren't theirs
// to read).
func (b *Broker) accessPowerRefusal(p auth.Principal, tile string, r accessReach) error {
	msg := tile + ", a tile inside it or one it reaches holds power over the workspace or the host (the admin API, a host capability, a device, all source, the host's network, a system component): who may use it is decided on its Access page, by an admin — never by a change set"
	if b.IsAdmin(p) {
		held := slices.Clone(r.power)
		if r.far {
			held = append(held, fmt.Sprintf("more than %d tiles reached, which xbin doesn't follow for a change set", accessReachMax))
		}
		msg += " (" + strings.Join(held, "; ") + ")"
	}
	return fmt.Errorf("%s", msg)
}

// namesCapped lists names, at most accessNamesShown of them, and how many more.
func namesCapped(names []string) string {
	if len(names) <= accessNamesShown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:accessNamesShown], ", "), len(names)-accessNamesShown)
}

// reachEffects are the lines' words for what a level of write or terminal
// brings (r, of tile): its nested tiles by name, the tiles it reaches —
// named where p may read them, counted otherwise — its networks, the LLM
// connector, its vault secrets.
func (r accessReach) effects(p auth.Principal, tile string) []string {
	var out []string
	if len(r.nested) > 0 {
		out = append(out, fmt.Sprintf("the %d tile(s) inside %s, whose code its writers change too: %s", len(r.nested), tile, namesCapped(r.nested)))
	}
	if len(r.reached) > 0 {
		var named []string
		other := 0
		for _, t := range r.reached {
			if p.CanReadTile(t) {
				named = append(named, t)
			} else {
				other++
			}
		}
		what := namesCapped(named)
		if other > 0 {
			what = strings.TrimPrefix(what+fmt.Sprintf(", %d other tile(s)", other), ", ")
		}
		out = append(out, "the tiles its code calls or reads through its grants and bindings: "+what)
	}
	if len(r.nets) > 0 {
		out = append(out, "the networks its tiles are bound to: "+namesCapped(r.nets))
	}
	if len(r.llm) > 0 {
		out = append(out, "the workspace's LLM connector (bound for "+namesCapped(r.llm)+"): its quota is spent by their code")
	}
	if len(r.vaults) > 0 {
		out = append(out, "the vault secrets of "+namesCapped(r.vaults))
	}
	return out
}
