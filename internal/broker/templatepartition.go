package broker

// templatepartition.go — the partition mode a new template instance starts
// in (plans/partitions/01-manifest-registry.md §2.7; PD-35, PD-52, PD-19;
// docs/partitions.md). A template names it in its "template" block; the
// instance gets it as its own top-level "partition" (internal/builtins
// partition.go) unless the request says "partition": false or xbind runs
// without --isolate. The fresh instance holds no data, so the first rescan
// records the mode at once (an "auto" entry, partitionmode.go), naming the
// person who instantiated it. A builtin update keeps an installed tile's
// mode; for a manifest it can't read it asks this store (recordedPartition).

import (
	"encoding/json"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
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
// runs (workspace root + "\x00" + tile → *instantiator): the tile's first
// auto mode record carries them as `by` (stampAutoBy).
var instantiators sync.Map

// instantiator is one request's note: its own pointer, so a concurrent
// request for the same path never overwrites or removes it.
type instantiator struct{ key, by string }

// noteInstantiator notes p as tile's instantiator before the tree is
// written — so the watcher's rescan, which may settle the fresh tile first,
// sees it — unless a concurrent request for the same path noted theirs;
// created makes it the note once this request's copy is the one written,
// and done removes it (only its own).
func (b *Broker) noteInstantiator(tile string, p auth.Principal) *instantiator {
	by := p.UserID
	switch {
	case by != "":
	case p.Owner:
		by = "owner"
	default:
		by = p.Component
	}
	n := &instantiator{key: b.Reg.Root + "\x00" + tile, by: by}
	instantiators.LoadOrStore(n.key, n)
	return n
}

func (n *instantiator) created() { instantiators.Store(n.key, n) }
func (n *instantiator) done()    { instantiators.CompareAndDelete(n.key, n) }

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
	if n, ok := instantiators.Load(b.Reg.Root + "\x00" + tile); ok {
		next.History[len(next.History)-1].By = n.(*instantiator).by
	}
}

// withRecordedPartition is u reading the mode store (recordedPartition)
// for an installed manifest it can't read (SetUpdater).
func (b *Broker) withRecordedPartition(u *builtins.Updater) *builtins.Updater {
	if u != nil {
		u.SetRecordedPartition(b.recordedPartition)
	}
	return u
}

// recordedPartition is the "partition" xbind last read from tile's code, as
// a manifest writes it — the builtin updater's stand-in for an installed
// xbin.json it can't read (builtins.Updater.SetRecordedPartition), so a
// replace never writes upstream's: the open or declined request's mode when
// there is one (Q ≠ R), the recorded mode otherwise, none for a tile with
// no record (the zero state). ok is false when the store isn't up or the
// tile's record can't be read.
func (b *Broker) recordedPartition(tile string) (raw []byte, has, ok bool) {
	pm := b.parts
	if pm == nil {
		return nil, false, false
	}
	pm.mu.Lock()
	_, unread := pm.unread[util.TileKey(tile)]
	rec := pm.recs[tile]
	pm.mu.Unlock()
	var spec *registry.PartitionSpec
	switch {
	case unread:
		return nil, false, false
	case rec == nil:
	case rec.Request != nil:
		spec = rec.Request.Spec
	case rec.Declined != nil:
		spec = rec.Declined.Spec
	default:
		spec = rec.Mode
	}
	s := registry.SpecOf(spec)
	if s.IsZero() {
		return nil, false, true
	}
	words := []string{}
	if s.User {
		words = append(words, registry.PartitionWordUser)
	}
	if s.Global {
		words = append(words, registry.PartitionWordGlobal)
	}
	raw, _ = json.Marshal(words)
	return raw, true, true
}
