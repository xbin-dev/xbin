package deployments

// ops_backup.go — the per-deployment backup acts (08-data §11; 11-contract
// §1.8): backing up a deployment's data now, restoring an archive into a
// deployment by replace, and a deployment's own backup schedule. Each is an
// admin's act in a person's own session (authz.go's rows), delegating to the
// broker's data plane through DataHooks without the tile lock: the data
// plane holds the namespace. Without its hook an act answers 501. The
// deployment a request names defaults to the primary.

import (
	"cmp"
	"context"

	"github.com/xbin-dev/xbin/internal/events"
)

func init() {
	register(OpBackup, Handler[BackupRequest]{Subject: primaryBy(OpBackup, (*BackupRequest).ref), Run: runBackup})
	register(OpRestore, Handler[RestoreRequest]{Subject: restoreSubject, Run: runRestore})
	register(OpBackupSchedule, Handler[BackupScheduleRequest]{Subject: primaryBy(OpBackupSchedule, (*BackupScheduleRequest).ref), Run: runBackupSchedule})
}

func (r *BackupRequest) ref() (string, string)         { return r.Tile, r.Deployment }
func (r *BackupScheduleRequest) ref() (string, string) { return r.Tile, r.Deployment }

// primaryBy is namedBy's subject where naming no deployment means the
// primary (11-contract §1.8's backup rows).
func primaryBy[R any](op Op, ref func(*R) (tile, dep string)) func(*Plane, *R) (Op, Subject, error) {
	return func(p *Plane, r *R) (Op, Subject, error) {
		tile, dep, err := p.primaryNamed(ref(r))
		if err != nil {
			return "", Subject{}, err
		}
		return op, p.subjectOf(tile, dep), nil
	}
}

// primaryNamed resolves a tile ref and the deployment a body names (its
// field or the ref's qualifier), the primary when it names none.
func (p *Plane) primaryNamed(ref, field string) (tile, dep string, err error) {
	tile, sel, err := p.resolveTile(ref)
	if err != nil {
		return "", "", err
	}
	if dep, err = named(field, sel); err != nil {
		return "", "", err
	}
	return tile, cmp.Or(dep, p.Primary(tile)), nil
}

// restoreSubject: the deployment restored into — into, else the archive's
// own deployment, else the primary. The archive's deployment need not exist
// any more (a removed deployment keeps its archives), so it is read from the
// deployment field alone; a ref's qualifier names one that exists.
func restoreSubject(p *Plane, r *RestoreRequest) (Op, Subject, error) {
	tile, from, err := p.primaryNamed(r.Tile, r.Deployment)
	if err != nil {
		return "", Subject{}, err
	}
	into, err := named(r.Into, "")
	if err != nil {
		return "", Subject{}, err
	}
	return OpRestore, p.subjectOf(tile, cmp.Or(into, from)), nil
}

// runBackup archives y's data now under its own key (main: the tile's main
// archive, as POST /backup writes it).
func runBackup(ctx context.Context, p *Plane, g Grant, r *BackupRequest) (any, error) {
	o, err := p.start(g, r.DryRun, false, nil, true, false, "")
	switch {
	case err != nil:
		return nil, err
	case p.BackupData == nil:
		return nil, notBuilt("backing up a deployment's data", "the data plane isn't wired")
	}
	ans, err := p.BackupData(o.tile, g.Subject.Deployment, r.DryRun)
	if err != nil || !r.DryRun {
		return ans, opError(o.tile, err)
	}
	return p.answer(ctx, true, nil, Impact{Data: "none", Affects: "nobody"}, false)
}

// runRestore restores an archive into y by replace, never the work tree:
// every other claimant of y's namespace is judged at the reset level (D127t),
// and each claimant's deployment of the name stops and hears op data.
func runRestore(ctx context.Context, p *Plane, g Grant, r *RestoreRequest) (any, error) {
	o, err := p.start(g, r.DryRun, false, nil, true, false, "")
	switch {
	case err != nil:
		return nil, err
	case p.RestoreData == nil:
		return nil, notBuilt("restoring a deployment's data", "the data plane isn't wired")
	}
	into := g.Subject.Deployment
	req := *r
	req.Tile, req.Into = o.tile, into
	if _, from, err := p.primaryNamed(r.Tile, r.Deployment); err == nil {
		req.Deployment = from // the archive's deployment, the primary's by default
	}
	authorize := func(t string) error {
		_, err := p.Authorize(g.P, OpReset, Subject{Tile: t, Deployment: into, Primary: p.Primary(t), Record: true})
		return err
	}
	ans, claimants, err := p.RestoreData(o.tile, o.by, req, authorize, p.stopDeployment)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	if r.DryRun {
		stops := []string{}
		for _, t := range claimants {
			stops = append(stops, map[bool]string{true: into, false: t + "+" + into}[t == o.tile])
		}
		return p.answer(ctx, true, nil, Impact{Data: "restore", Stops: stops, Affects: "deployment"}, false)
	}
	for _, t := range claimants {
		ev := dataEvent{Op: "data", Deployment: into}
		if p.DataOf != nil {
			if ds := p.DataOf(t, into); ds != nil {
				ev.Busy, ev.State = ds.Busy, ds.State
			}
		}
		if p.Hub != nil {
			p.Hub.Publish(events.Event{Type: "deployments", Component: t, Data: ev})
		}
	}
	return ans, nil
}

// runBackupSchedule sets y's own backup schedule, or removes it with ""; the
// broker checks the schedule and the retention.
func runBackupSchedule(ctx context.Context, p *Plane, g Grant, r *BackupScheduleRequest) (any, error) {
	o, err := p.start(g, r.DryRun, false, r.Seq, true, false, "")
	switch {
	case err != nil:
		return nil, err
	case p.SetBackupSchedule == nil:
		return nil, notBuilt("scheduling a deployment's backups", "the data plane isn't wired")
	}
	if err := p.SetBackupSchedule(o.tile, g.Subject.Deployment, r.Schedule, r.Retention, r.DryRun); err != nil {
		return nil, opError(o.tile, err)
	}
	return p.answer(ctx, r.DryRun, nil, Impact{Data: "none", Affects: "nobody"}, false)
}
