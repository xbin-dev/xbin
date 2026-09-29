package boot

// partitionroute.go — the proxy's side of people's partitions wired to the
// runner's (plans/partitions/02 §7, 03 §A.2). The proxy asks a
// proxy.PartitionRunner to start and hold a user partition; the runner's
// people's-partitions side (EnsurePartition / TrackPartition, its start
// classes and refusal errors) speaks its own types. partitionStarts adapts
// one to the other explicitly — never by a type assertion that could
// quietly fail on a signature drift — and partitionRunnerOf builds it from
// the runner. Until the runner has that side, partitionRunnerOf is nil:
// a call reaching a user partition answers 503 "no partition runner", and
// boot logs an error naming every partitioned tile, so the gap is never
// silent. TestPartitionRunnerWired fails once the runner grows
// EnsurePartition while partitionRunnerOf is still nil.
//
// Also here: registerPartitionInstance, the runner's
// RegisterPartitionInstance hook (auth's RegisterInstancePartition with the
// person's uid from the users store).

import (
	"context"
	"errors"
	"log/slog"

	"github.com/xbin-dev/xbin/internal/proxy"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionRunnerOf builds the proxy's PartitionRunner over run; nil while
// the runner can't start people's partitions. The integrator of the
// runner's side sets it (plans/partitions/records/F2.md, "Seams"):
//
//	partitionRunnerOf = func(run *runner.Runner) proxy.PartitionRunner {
//		return partitionStarts[runner.StartClass]{
//			ensure: run.EnsurePartition, track: run.TrackPartition,
//			classes: [...]runner.StartClass{proxy.StartInteractive: runner.StartInteractive,
//				proxy.StartBackground: runner.StartBackground, proxy.StartMail: runner.StartMail},
//			unavailable: []error{runner.ErrPartitionBusy, runner.ErrPartitionDeferred,
//				runner.ErrPartitionRefused, runner.ErrNoPartition},
//		}
//	}
var partitionRunnerOf func(run *runner.Runner) proxy.PartitionRunner

// partitionStarts is a proxy.PartitionRunner over a runner's start and
// hold functions, whose start class type is C.
type partitionStarts[C any] struct {
	ensure func(ctx context.Context, c *registry.Component, dep, part string, class C) (string, error)
	track  func(tile, dep, part string, passive bool) func()
	// classes maps the proxy's start classes onto the runner's.
	classes [3]C
	// unavailable are the runner's refusals the proxy answers 503: each
	// is marked sbx.ErrRefused, keeping its text.
	unavailable []error
}

var _ proxy.PartitionRunner = partitionStarts[uint8]{}

func (a partitionStarts[C]) EnsurePartition(ctx context.Context, c *registry.Component, dep, part string, class proxy.PartitionStart) (string, error) {
	if int(class) >= len(a.classes) {
		return "", sbx.Refuse(errors.New("a person's partition can't start: an unknown start class"))
	}
	sock, err := a.ensure(ctx, c, dep, part, a.classes[class])
	for _, e := range a.unavailable {
		if errors.Is(err, e) {
			return "", sbx.Refuse(err)
		}
	}
	return sock, err
}

func (a partitionStarts[C]) TrackPartition(tile, dep, part string, passive bool) func() {
	return a.track(tile, dep, part, passive)
}

// wirePartitionRunner installs the proxy's partition runner, or — without
// one — says which partitioned tiles' people can't be served.
func (st *State) wirePartitionRunner(px *proxy.Proxy) {
	if partitionRunnerOf != nil {
		px.Partitions = partitionRunnerOf(st.Run)
	}
	if px.Partitions != nil || st.Reg == nil {
		return
	}
	var tiles []string
	for _, c := range st.Reg.Components() {
		if spec, ok := c.Partitioned(); ok && spec.User {
			tiles = append(tiles, c.Path)
		}
	}
	if len(tiles) > 0 {
		slog.Error("partitions: this xbind has no partition runner, so people's partitions of these tiles answer 503", "tiles", tiles)
	}
}

// registerPartitionInstance registers the instance token of a generation
// of partition part of deployment dep of tile (the runner's
// RegisterPartitionInstance hook): auth's RegisterInstancePartition with
// the person's uid, which the runner's PartitionIdent minted before the
// start. A person without one registers nothing (fail closed).
func (st *State) registerPartitionInstance(token, tile, dep, part string) {
	pt := util.Partition(part)
	uid := ""
	if id, ok := pt.User(); ok && st.Users != nil {
		if u, ok := st.Users.Get(id); ok {
			uid = u.UID
		}
	}
	st.Auth.RegisterInstancePartition(token, tile, dep, pt, uid)
}
