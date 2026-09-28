package deployments

import (
	"net/http"
	"reflect"
	"slices"
	"testing"
)

// backupHooks installs recording backup hooks on f's plane.
func (f *govFx) backupHooks() {
	f.p.BackupData = func(tile, dep string, dry bool) (BackupAnswer, error) {
		f.note("backup " + tile + "/" + dep + " dry=" + boolStr(dry))
		return BackupAnswer{OK: "true", Deployment: dep, Version: "v1"}, nil
	}
	f.p.RestoreData = func(tile, by string, req RestoreRequest, authorize func(string) error, stop func(string, string)) (RestoreAnswer, []string, error) {
		claimants := f.claimants
		if claimants == nil {
			claimants = []string{tile}
		}
		for _, c := range claimants {
			if c == tile {
				continue
			}
			if err := authorize(c); err != nil {
				return RestoreAnswer{}, nil, err
			}
		}
		f.note("restore " + tile + " " + req.Deployment + "→" + req.Into + " by=" + by + " dry=" + boolStr(req.DryRun))
		if !req.DryRun {
			for _, c := range claimants {
				stop(c, req.Into)
			}
		}
		return RestoreAnswer{OK: "true", Deployment: req.Deployment, Into: req.Into, Restored: "v1", Skipped: []string{}}, claimants, nil
	}
	f.p.SetBackupSchedule = func(tile, dep string, schedule *string, retention *int, dry bool) error {
		f.note("backup-schedule " + tile + "/" + dep + " " + *schedule + " dry=" + boolStr(dry))
		return nil
	}
}

// covers D127c D127h D127t T9 — the per-deployment backup acts delegate to the
// broker's data plane: backup names the primary by default, a dry run writes
// nothing; restore goes into its target (into, else the archive's own
// deployment, else the primary), passes the broker a judgement of every
// other claimant, stops each claimant and tells it with op data; the
// schedule passes through with its seq checked; each is an admin's act, and
// answers 501 without its hook.
func TestDeploymentBackupOps(t *testing.T) {
	f := newGovFx(t, false)
	f.backupHooks()
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.took()
	f.evs.take()

	t.Run("backup", func(t *testing.T) {
		res, err := f.do(ownerP, OpBackup, &BackupRequest{Tile: opSite})
		if a, ok := res.(BackupAnswer); err != nil || !ok || a.Deployment != "main" {
			t.Fatalf("backup of the primary = %#v, %v", res, err)
		}
		if _, err := f.do(ownerP, OpBackup, &BackupRequest{Tile: opSite + "+dev"}); err != nil {
			t.Fatal(err)
		}
		if im := f.dry(ownerP, OpBackup, &BackupRequest{Tile: opSite, Deployment: "dev", DryRun: true}); im.Data != "none" {
			t.Errorf("dry run = %+v", im)
		}
		if calls := f.took(); !slices.Equal(calls, []string{"backup apps/site/main dry=false", "backup apps/site/dev dry=false", "backup apps/site/dev dry=true"}) {
			t.Errorf("calls = %q", calls)
		}
		_, err = f.do(ownerP, OpBackup, &BackupRequest{Tile: opSite, Deployment: "nope"})
		wantErr(t, "an unknown deployment", err, http.StatusNotFound, `apps/site has no deployment "nope"`)
	})

	t.Run("restore", func(t *testing.T) {
		f.claimants = []string{opSite, opAPI}
		defer func() { f.claimants = nil }()
		im := f.dry(ownerP, OpRestore, &RestoreRequest{Tile: opSite, Deployment: "main", Into: "dev", DryRun: true})
		if im.Data != "restore" || !slices.Equal(im.Stops, []string{"dev", opAPI + "+dev"}) || im.Affects != "deployment" {
			t.Errorf("dry run = %+v", im)
		}
		res, err := f.do(ownerP, OpRestore, &RestoreRequest{Tile: opSite, Deployment: "dev"})
		if a, ok := res.(RestoreAnswer); err != nil || !ok || a.Into != "dev" {
			t.Fatalf("restore of dev's archive = %#v, %v", res, err)
		}
		if calls := f.took(); !slices.Equal(calls, []string{"restore apps/site main→dev by=owner dry=true",
			"restore apps/site dev→dev by=owner dry=false", "stop apps/site/dev", "stop apps/api/dev"}) {
			t.Errorf("calls = %q", calls)
		}
		evs := f.evs.take()
		if len(evs) != 2 || evs[0].Component != opSite || evs[1].Component != opAPI ||
			!reflect.DeepEqual(evs[0].Data, dataEvent{Op: "data", Deployment: "dev", State: "empty"}) {
			t.Errorf("events = %+v", evs)
		}
	})

	t.Run("schedule", func(t *testing.T) {
		seq := f.rec(opSite).Seq
		if _, err := f.do(ownerP, OpBackupSchedule, &BackupScheduleRequest{Tile: opSite, Deployment: "dev", Schedule: ptr("0 3 * * *"), Seq: ptr(seq)}); err != nil {
			t.Fatal(err)
		}
		_, err := f.do(ownerP, OpBackupSchedule, &BackupScheduleRequest{Tile: opSite, Deployment: "dev", Schedule: ptr(""), Seq: ptr(seq + 7)})
		wantErr(t, "a stale seq", err, http.StatusConflict, "")
		if calls := f.took(); !slices.Equal(calls, []string{"backup-schedule apps/site/dev 0 3 * * * dry=false"}) {
			t.Errorf("calls = %q", calls)
		}
		if f.rec(opSite).Seq != seq {
			t.Error("a schedule wrote the record")
		}
	})

	t.Run("admins only", func(t *testing.T) {
		for _, op := range []struct {
			op  Op
			req any
		}{
			{OpBackup, &BackupRequest{Tile: opSite, Deployment: "dev"}},
			{OpRestore, &RestoreRequest{Tile: opSite, Deployment: "dev"}},
			{OpBackupSchedule, &BackupScheduleRequest{Tile: opSite, Deployment: "dev", Schedule: ptr("")}},
		} {
			_, err := f.do(userP("dev", opSite, "terminal"), op.op, op.req)
			if code, _ := errStatus(err); code != http.StatusForbidden {
				t.Errorf("%s by a terminal-level person: %v, want 403", op.op, err)
			}
		}
		if calls := f.took(); len(calls) != 0 {
			t.Errorf("a refusal called the data plane: %q", calls)
		}
	})

	t.Run("without the data plane", func(t *testing.T) {
		g := newGovFx(t, false)
		g.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
		for _, op := range []struct {
			op   Op
			req  any
			what string
		}{
			{OpBackup, &BackupRequest{Tile: opSite, Deployment: "dev"}, "backing up a deployment's data isn't built"},
			{OpRestore, &RestoreRequest{Tile: opSite, Deployment: "dev"}, "restoring a deployment's data isn't built"},
			{OpBackupSchedule, &BackupScheduleRequest{Tile: opSite, Deployment: "dev", Schedule: ptr("")}, "scheduling a deployment's backups isn't built"},
		} {
			_, err := g.do(ownerP, op.op, op.req)
			wantErr(t, string(op.op), err, http.StatusNotImplemented, op.what)
		}
	})
}
