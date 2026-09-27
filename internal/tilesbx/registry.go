package tilesbx

// registry.go — tile sandboxes in the sandbox registry (internal/sbx, D112):
// a row while one runs, with its name and its manager's claims, and a
// failure row for a start that failed or a sandbox that ended on its own,
// so the admin's runtime → sandboxes view shows them beside backends and
// terminals.

import (
	"errors"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// sbxMode is a definition's mode as the registry names it.
func sbxMode(mode string) sbx.Mode {
	if mode == ModeVM {
		return sbx.VM
	}
	return sbx.Namespace
}

// register lists a running sandbox; the returned func unlists it
// (idempotent; it removes only this run's row).
func (m *Manager) register(r *run) func() {
	d := r.def
	return m.deps.Sbx.Add(sbx.Entry{
		ID: RegistryID(r.k, d.Name), Kind: sbx.Tile, Tile: r.k.Tile,
		Name: d.Name, For: d.For, ForUser: d.ForUser,
		Mode: sbxMode(d.Mode), Accel: sbx.Accel(r.accel),
		MemMiB: d.MemMiB, VCPUs: d.VCPUs,
		PID: r.proc.Pid(), Started: r.started, Leaf: r.leaf, Net: r.class.Class,
	})
}

// failed records what the sandbox layer refused or failed at for k's
// sandbox d: a start (a refusal is Refused), or an end of its own (Exit).
func (m *Manager) failed(k Key, d *Def, stage sbx.Stage, err error) {
	var e *Error
	if errors.As(err, &e) && stage == sbx.Start {
		switch e.Refusal {
		case RefLimit, RefUnavailable:
			err = sbx.Refuse(err) // a policy, budget or availability refusal
		case RefState, RefNotFound:
			if e.State != StateError {
				return // a call at the wrong moment, not a failure
			}
		}
	}
	m.deps.Sbx.Fail(sbx.Failure{Time: time.Now(), Kind: sbx.Tile, Tile: k.Tile, User: d.ForUser,
		Mode: sbxMode(d.Mode), Stage: sbx.StageOf(err, stage), Error: d.Name + ": " + err.Error()})
}
