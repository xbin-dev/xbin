package boot

// partitionroute.go — the proxy's side of people's partitions wired to the
// runner's (plans/partitions/02 §7, 03 §A.2). The proxy asks a
// proxy.PartitionRunner to start and hold a user partition; the runner's
// people's-partitions side (EnsurePartition / TrackPartition, its start
// classes and refusal errors) speaks its own types. partitionStarts adapts
// one to the other explicitly — never by a type assertion that could
// quietly fail on a signature drift — and partitionRunnerOf builds it from
// the runner. Without one (partitionRunnerOf nil) a call reaching a user
// partition answers 503 "no partition runner", and boot logs an error
// naming every partitioned tile, so the gap is never silent;
// TestPartitionRunnerWired fails a build whose runner has EnsurePartition
// while partitionRunnerOf is nil.
//
// Also here: registerPartitionInstance, the runner's
// RegisterPartitionInstance hook (auth's RegisterInstancePartition with the
// uid of the person's incarnation the runner's state was made for).

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

// partitionRunnerOf builds the proxy's PartitionRunner over run: the
// runner's EnsurePartitionGen (the generation, which the proxy follows
// across a swap: D173) and TrackPartition, its start classes by name,
// and its refusals — the caps (ErrPartitionBusy, whose text is the 503's
// exactly), a deferred delivery, a start this xbind can't make now
// (ErrPartitionRefused: no --isolate, a person who may not run it, …) and
// a missing global instance (which the proxy never asks this for) — each a
// 503 keeping its text. nil would leave every person's partition at 503
// "no partition runner" (TestPartitionRunnerWired).
var partitionRunnerOf = func(run *runner.Runner) proxy.PartitionRunner {
	return partitionStarts[runner.StartClass, runner.Gen]{
		ensure: run.EnsurePartitionGen, track: run.TrackPartition,
		classes: [...]runner.StartClass{proxy.StartInteractive: runner.StartInteractive,
			proxy.StartBackground: runner.StartBackground, proxy.StartMail: runner.StartMail},
		unavailable: []error{runner.ErrPartitionBusy, runner.ErrPartitionDeferred,
			runner.ErrPartitionRefused, runner.ErrNoPartition},
	}
}

// partitionStarts is a proxy.PartitionRunner over a runner's start and
// hold functions, whose start class type is C and generation type G.
type partitionStarts[C any, G proxy.PartitionGen] struct {
	ensure func(ctx context.Context, c *registry.Component, dep, part string, class C) (G, error)
	track  func(tile, dep, part string, passive bool) func()
	// classes maps the proxy's start classes onto the runner's.
	classes [3]C
	// unavailable are the runner's refusals the proxy answers 503: each
	// is marked sbx.ErrRefused, keeping its text.
	unavailable []error
}

var _ proxy.PartitionRunner = partitionStarts[uint8, runner.Gen]{}

func (a partitionStarts[C, G]) EnsurePartition(ctx context.Context, c *registry.Component, dep, part string, class proxy.PartitionStart) (proxy.PartitionGen, error) {
	if int(class) >= len(a.classes) {
		return nil, sbx.Refuse(errors.New("a person's partition can't start: an unknown start class"))
	}
	gen, err := a.ensure(ctx, c, dep, part, a.classes[class])
	for _, e := range a.unavailable {
		if errors.Is(err, e) {
			return nil, sbx.Refuse(err)
		}
	}
	if err != nil {
		return nil, err
	}
	return gen, nil
}

func (a partitionStarts[C, G]) TrackPartition(tile, dep, part string, passive bool) func() {
	return a.track(tile, dep, part, passive)
}

// wirePartitionProxy installs the proxy's partition runner, or — without
// one — says which partitioned tiles' people can't be served.
func (st *State) wirePartitionProxy(px *proxy.Proxy) {
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
// the uid of the person's incarnation the runner's state was made for
// (PartitionIdent's answer), so a token of an earlier incarnation never
// authenticates as a recreated person (coverage compares the uid on every
// lookup). No uid registers nothing (fail closed). The runner calls it
// under its state's lock: it never calls into the runner.
func (st *State) registerPartitionInstance(token, tile, dep, part, uid string) {
	if uid == "" {
		return
	}
	st.Auth.RegisterInstancePartition(token, tile, dep, util.Partition(part), uid)
}
