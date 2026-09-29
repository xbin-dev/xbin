package broker

// templatepartition.go — the partition mode a new template instance starts
// in (plans/partitions/01-manifest-registry.md §2.7; PD-35, PD-52, PD-19;
// docs/partitions.md). A template names it in its "template" block; the
// instance gets it as its own top-level "partition" (internal/builtins
// partition.go) unless the request says "partition": false or xbind runs
// without --isolate. The fresh instance holds no data, so the first rescan
// records the mode at once (an "auto" entry, partitionmode.go), naming the
// person who instantiated it.

import (
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
)

// templatesIsolated reports whether xbind runs with --isolate: user
// partitions need it (PD-19), so without it an instance starts
// unpartitioned. Tests pin it.
var templatesIsolated = confine.Isolated

// Why a template's default mode wasn't written (templateItem and the
// instantiation's answer: partitionSkipped).
const (
	partitionSkippedIsolate = "needs --isolate"
	partitionSkippedOptOut  = "opted out"
)

// instanceOpts is how an instance of a template whose default mode is def
// (nil: none) is made, given the request's "partition" (nil: absent), and
// why def isn't written ("" when it is, or there is none). Without the
// default being written the instance asks for no mode at all.
func instanceOpts(def []string, asked *bool) (builtins.InstanceOpts, string) {
	why := ""
	switch {
	case asked != nil && !*asked:
		why = partitionSkippedOptOut
	case !templatesIsolated():
		why = partitionSkippedIsolate
	default:
		return builtins.InstanceOpts{Partition: true}, ""
	}
	if def == nil {
		why = ""
	}
	return builtins.InstanceOpts{}, why
}

// workspaceTemplateDefault is a workspace template's default mode as words,
// nil when it names none (or one the registry can't read: the instance
// carries it as written, and its request is invalid until fixed).
func workspaceTemplateDefault(c *registry.Component) []string {
	if c == nil || c.Manifest.Template == nil {
		return nil
	}
	spec, err := registry.ParsePartition(c.Manifest.Template.Partition)
	if err != nil || spec == nil {
		return nil
	}
	words := []string{registry.PartitionWordUser}
	if spec.Global {
		words = append(words, registry.PartitionWordGlobal)
	}
	return words
}

// instantiators names who is instantiating a tile while apiTemplatesNew
// runs (workspace root + "\x00" + tile → person): the tile's first auto
// mode record carries them as `by` (stampAutoBy).
var instantiators sync.Map

// noteInstantiator records p as tile's instantiator until the returned
// func runs.
func (b *Broker) noteInstantiator(tile string, p auth.Principal) func() {
	by := p.UserID
	switch {
	case by != "":
	case p.Owner:
		by = "owner"
	default:
		by = p.Component
	}
	key := b.Reg.Root + "\x00" + tile
	instantiators.Store(key, by)
	return func() { instantiators.CompareAndDelete(key, by) }
}

// stampAutoBy names the instantiator on the "auto" history entry next adds
// to tile's mode record (partitionmode.go's decide calls it before writing).
func (b *Broker) stampAutoBy(tile string, rec, next *modeRecord) {
	if next == nil {
		return
	}
	h, ok := newEntry(rec, next)
	if !ok || h.Op != modeOpAuto || h.By != "" {
		return
	}
	if by, ok := instantiators.Load(b.Reg.Root + "\x00" + tile); ok {
		next.History[len(next.History)-1].By = by.(string)
	}
}
