package broker

// accessreach.go — the power rule of a raise on tile T (D246, after two
// reviews): a change that would let someone run or change CODE THAT HOLDS
// power over the workspace or the host is refused — an admin makes it by
// hand; a change that only brings someone nearer to power is warned, with
// what it brings listed.
//
// Refused (an access.set that raises someone to write or terminal, an
// org.share that raises a share) when power is held by code T's writers
// change:
//
//   - T itself, and every tile nested in T — a tile's writers write its
//     nested tiles too (writersCover; a terminal on T binds T's directory
//     read-write, nested tiles included; T's writers decide their PRs);
//   - a tile T lies inside — its build, its pages and its scope.json may
//     take in files beneath it, T's among them;
//   - a tile that builds or loads T's code: its backend's build workspace
//     takes a module of T's subtree (BuildsWith: the runner's D166 rule —
//     a deps entry, a replace, a v0.0.0 or dotless require — transitively),
//     its manifest's deps reach T's subtree (transitively), or its pages'
//     import map points into it.
//
// A `code:` grant is read-only (docs/auth.md): no tile's code is changed
// through one, so it ties nothing here. A tile T's code merely CALLS — a
// call or code: grant, a binding to a provider tile, a person's personal
// bind — is the callee's to mediate: listed and warned, never refused.
//
// What power is (targetPower): the admin API (xbin, xbin:*), a host device
// (gpu:*), all source (code), the host's network, and each cap: as
// capClasses says — a cap no row names counts as power.
//
// The subtree, the ancestors, the deps and the callees are each walked once
// over the registry; the one costly question — which tiles a build takes —
// is asked only of the tiles that hold power, at most accessHoldersMax of
// them: past that the raise is refused (fail closed), which no ordinary
// workspace reaches.

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// accessHoldersMax bounds the tiles holding power whose builds a raise
// checks.
const accessHoldersMax = 64

// accessNamesShown is how many names an effect lists before "and N more".
const accessNamesShown = 5

// capClass is what code holding a cap: can do, as an access line weighs it.
type capClass struct {
	power bool
	what  string
}

// capClasses weighs every cap: target xbind serves (docs/auth.md). Power is
// a cap that lets code act on the host, on other tiles or on people's
// sessions; anything less is listed only. TestEveryCapIsClassified fails
// when xbind's code or docs name a cap: without a row here.
var capClasses = map[string]capClass{
	NetAdminCap:        {true, "a network provider: its dataplane carries other tiles' traffic"},
	ContainersCap:      {true, "a container host: its user-namespace capabilities and a minimal seccomp floor face the host's kernel"},
	users.SandboxesCap: {true, "a sandbox manager: its backend drives xbind's sandboxes on this host"},
	OpenLinksCap:       {false, "its pages may open windows that leave their sandbox"},
}

// targetPower weighs a grant target: power, and in words; listed says it is
// one an access line names at all (a cap below power is listed only).
func targetPower(target string) (power bool, what string, listed bool) {
	switch {
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		return true, "the admin API", true
	case strings.HasPrefix(target, "gpu:"):
		return true, "a device of the host", true
	case target == "code":
		return true, "every tile's source", true
	case strings.HasPrefix(target, "cap:"):
		if c, ok := capClasses[target]; ok {
			return c.power, c.what, true
		}
		return true, "a capability xbind doesn't weigh", true
	}
	return false, "", false
}

// accessReach is what a raise on a tile brings (the file comment).
type accessReach struct {
	refuse  []powerTie // power held by code the raise lets someone change
	far     bool       // more tiles hold power than a raise checks
	nested  []string
	weight  []string            // a cap below power in the subtree: "cap:open-links — … (tile)"
	calls   []string            // tiles outside the subtree its code calls or reads
	callPow map[string][]string // a called tile's power
	nets    []string
	llm     []string
	vaults  []string
}

// powerTie is power held by code tied to the raised tile.
type powerTie struct {
	holder, what, tie string
}

// heldPower is what each tile holds: its power, and the caps it holds below
// power (listed only).
func (b *Broker) heldPower() (power, weight map[string][]string) {
	power, weight = map[string][]string{}, map[string][]string{}
	add := func(m map[string][]string, tile, e string) {
		if !slices.Contains(m[tile], e) {
			m[tile] = append(m[tile], e)
		}
	}
	ws := b.Reg.Workspace()
	for _, g := range ws.Grants {
		if p, what, ok := targetPower(g.Target); ok && p {
			add(power, g.From, g.Target+" — "+what)
		} else if ok {
			add(weight, g.From, g.Target+" — "+what)
		}
	}
	for comp, slots := range ws.Bindings {
		for _, bind := range slots {
			for _, e := range bind {
				if prov, _ := splitRef(e.Ref); b.refIsHostNet(comp, prov) {
					add(power, comp, "the host's network")
				}
			}
		}
	}
	for _, c := range b.Reg.Components() {
		if b.NetHostShare(c) {
			add(power, c.Path, "the host's network")
		}
	}
	return power, weight
}

// accessReachOf weighs a raise on tile (the file comment).
func (b *Broker) accessReachOf(tile string) accessReach {
	r := accessReach{callPow: map[string][]string{}}
	subtree := map[string]bool{tile: true}
	var ancestors []string
	for _, c := range b.Reg.Components() {
		switch {
		case strings.HasPrefix(c.Path, tile+"/"):
			subtree[c.Path] = true
			r.nested = append(r.nested, c.Path)
		case strings.HasPrefix(tile, c.Path+"/"):
			ancestors = append(ancestors, c.Path)
		}
	}
	sort.Strings(r.nested)
	power, weight := b.heldPower()
	// the power tied to the subtree's code
	holders := make([]string, 0, len(power))
	for h := range power {
		holders = append(holders, h)
	}
	sort.Strings(holders)
	checked := 0
	for _, h := range holders {
		tie := ""
		switch {
		case h == tile:
			tie = "the tile itself"
		case subtree[h]:
			tie = "inside " + tile + ": its writers write it"
		case slices.Contains(ancestors, h):
			tie = tile + " lies inside it: its build, pages and scope may take in " + tile + "'s files"
		default:
			if checked++; checked > accessHoldersMax {
				r.far = true
				continue
			}
			tie = b.takesCodeOf(h, subtree)
		}
		if tie == "" {
			continue
		}
		for _, what := range power[h] {
			r.refuse = append(r.refuse, powerTie{holder: h, what: what, tie: tie})
		}
	}
	// what the subtree holds below power, its networks, its LLM binding, its
	// vaults, and what its code calls
	ws := b.Reg.Workspace()
	for _, t := range append([]string{tile}, r.nested...) {
		for _, w := range weight[t] {
			r.weight = append(r.weight, w+" ("+t+")")
		}
		c, _ := b.Reg.Component(t)
		for slot, bind := range ws.Bindings[t] {
			for _, e := range bind {
				prov, _ := splitRef(e.Ref)
				switch {
				case b.refIsHostNet(t, prov): // power: above
				case prov == LLMRef:
					addOnce(&r.llm, t)
				case c != nil && isNetSlot(c.Manifest.Interfaces[slot].Kind):
					if prov != "" && prov != NetRefNone {
						addOnce(&r.nets, prov)
					}
				default:
					r.call(b, subtree, prov, power)
				}
			}
		}
		for _, g := range ws.Grants {
			if g.From != t || strings.HasPrefix(g.Target, "net:") || strings.HasPrefix(g.Target, "res:") {
				continue
			}
			if _, _, listed := targetPower(g.Target); listed {
				continue
			}
			callee, _, _ := strings.Cut(strings.TrimPrefix(g.Target, "code:"), "#")
			r.call(b, subtree, callee, power)
		}
		if _, err := os.Stat(b.vaultPath(t)); err == nil {
			addOnce(&r.vaults, t)
		}
	}
	// a person's personal bind of a partition of the subtree: a call to a
	// user-owned provider on an http slot (personalbind.go) — never the
	// host's network, never code that runs in another tile
	_ = b.eachPersonalBinds(func(f *personalBindsFile) {
		for _, pb := range f.Binds {
			if subtree[pb.Requester] {
				r.call(b, subtree, pb.Provider, power)
			}
		}
	})
	sort.Strings(r.weight)
	sort.Strings(r.calls)
	sort.Strings(r.nets)
	sort.Strings(r.llm)
	sort.Strings(r.vaults)
	return r
}

// call notes callee, a tile outside the subtree the subtree's code calls or
// reads, with its power (the callee's to mediate: listed, never refused).
func (r *accessReach) call(b *Broker, subtree map[string]bool, callee string, power map[string][]string) {
	if callee == "" || subtree[callee] || slices.Contains(r.calls, callee) {
		return
	}
	if _, ok := b.Reg.Component(callee); !ok {
		return
	}
	r.calls = append(r.calls, callee)
	if p := power[callee]; len(p) > 0 {
		r.callPow[callee] = p
	}
}

func addOnce(list *[]string, s string) {
	if !slices.Contains(*list, s) {
		*list = append(*list, s)
	}
}

// takesCodeOf says how holder's code takes in the code of a tile of
// subtree — its build (BuildsWith, transitively), its manifest's deps
// (transitively), its pages' import map — "" when it doesn't.
func (b *Broker) takesCodeOf(holder string, subtree map[string]bool) string {
	in := func(path string) string {
		path = strings.Trim(path, "/")
		for t := range subtree {
			if path == t || strings.HasPrefix(path, t+"/") {
				return t
			}
		}
		return ""
	}
	c, ok := b.Reg.Component(holder)
	if !ok {
		return ""
	}
	if b.BuildsWith != nil {
		for _, u := range b.BuildsWith(c) {
			if t := in(u); t != "" {
				return "its backend's build takes " + t + "'s code"
			}
		}
	}
	seen := map[string]bool{holder: true}
	queue := []*registry.Component{c}
	for i := 0; i < len(queue); i++ {
		for _, d := range queue[i].Manifest.Deps {
			d = strings.Trim(d, "/")
			if t := in(d); t != "" {
				return "its deps take " + t + "'s code"
			}
			if dc, ok := b.Reg.Component(d); ok && !seen[d] {
				seen[d] = true
				queue = append(queue, dc)
			}
		}
	}
	for _, v := range b.Reg.ImportMapFor(c) {
		if p, ok := strings.CutPrefix(v, "/c/"); ok {
			if t := in(p); t != "" {
				return "its pages' import map loads " + t + "'s code"
			}
		}
	}
	return ""
}

func isNetSlot(kind string) bool { return kind == "net" || kind == "sandbox-net" }

// holdsPower reports a reach a raise is refused on.
func (r accessReach) holdsPower() bool { return len(r.refuse) > 0 || r.far }

// accessPowerRefusal is a refused raise on tile, as xbind's chrome says it
// to p: the holders whose grants p may see named (an admin sees all, a
// person the tiles they write or whose org they administer — GET /grants'
// rows), the rest counted. The agent reads one fixed sentence (the
// server's linePowerForAgent).
func (b *Broker) accessPowerRefusal(p auth.Principal, tile string, r accessReach) string {
	msg := "xbin refuses this one: " + tile + " — or code tied to it — holds power over the workspace or the host, so who may change that code is decided on its Access page, by an admin"
	var named []string
	hidden := map[string]bool{}
	for _, e := range r.refuse {
		if b.IsAdmin(p) || b.wiringVisible(p, e.holder) {
			named = append(named, fmt.Sprintf("%s, held by %s (%s)", e.what, e.holder, e.tie))
		} else {
			hidden[e.holder] = true
		}
	}
	if len(hidden) > 0 {
		named = append(named, fmt.Sprintf("power held by %d tile(s) you can't see", len(hidden)))
	}
	if r.far {
		named = append(named, fmt.Sprintf("more than %d tiles hold power: more than xbin checks for a change set", accessHoldersMax))
	}
	if len(named) > 0 {
		msg += ": " + strings.Join(named, "; ")
	}
	return msg
}

// namesCapped lists names, at most accessNamesShown of them, and how many more.
func namesCapped(names []string) string {
	if len(names) <= accessNamesShown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:accessNamesShown], ", "), len(names)-accessNamesShown)
}

// effects are the line's words for what write or terminal on tile brings:
// its nested tiles by name, the caps below power its tiles hold, the tiles
// its code calls or reads — named, with their power, where p may read
// them; counted otherwise —, its networks, the LLM connector, its vault
// secrets. reachesPower: a tile it calls holds power (the line is warned).
func (r accessReach) effects(p auth.Principal, tile string) (out []string, reachesPower bool) {
	if len(r.nested) > 0 {
		out = append(out, fmt.Sprintf("the %d tile(s) inside %s, whose code its writers change too: %s", len(r.nested), tile, namesCapped(r.nested)))
	}
	out = append(out, r.weight...)
	if len(r.calls) > 0 {
		var named []string
		other := 0
		for _, t := range r.calls {
			pw := r.callPow[t]
			if len(pw) > 0 {
				reachesPower = true
			}
			switch {
			case !p.CanReadTile(t):
				other++
			case len(pw) > 0:
				named = append(named, t+", which holds "+strings.Join(pw, "; "))
			default:
				named = append(named, t)
			}
		}
		what := namesCapped(named)
		if other > 0 {
			what = strings.TrimPrefix(what+fmt.Sprintf(", %d other tile(s)", other), ", ")
		}
		out = append(out, "its code can call or read, through its grants and bindings: "+what)
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
	return out, reachesPower
}
