package tilesbx

// api_snapshots.go — snapshots and restores (§3.9; snapshot.go,
// restore.go); a clone is POST /sandboxes with from (clone.go). A snapshot
// or a restore copies off the request: ?wait=<seconds> (absent:
// waitMaxSec) bounds how long the call waits for it — done in time, the
// normal answer; not done, a snapshot answers 202 {…, pending: true} and a
// restore the sandbox with its busy stateDetail, and the caller polls.

import (
	"errors"
	"net/http"
)

// ServeSnapshots answers GET /sandboxes/{name}/snapshots: {snapshots}, by
// id, one being taken last with pending: true.
func (m *Manager) ServeSnapshots(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	list, err := m.Snapshots(k, d.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

// ServeSnapshot answers POST /sandboxes/{name}/snapshots?wait= {name,
// clientId}: 201 and the snapshot once it is taken (the sandbox stopped
// for it, and started again if it ran), 202 with pending: true when ?wait
// ran out first, 200 when clientId repeats one.
func (m *Manager) ServeSnapshot(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	var req SnapshotRequest
	if err := decode(w, r, defBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	wait, err := waitOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	sn, job, repeat, err := m.Snapshot(k, d.Name, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if repeat {
		status = http.StatusOK
	}
	if job != nil {
		if !waitJob(job, wait) {
			writeJSON(w, http.StatusAccepted, sn)
			return
		}
		if job.err != nil {
			writeErr(w, copyErr(job.err, "the snapshot"))
			return
		}
		if cur, ok := m.snapshotInfo(k, d.Name, sn.ID); ok {
			sn = cur
		}
	}
	writeJSON(w, status, sn)
}

// copyErr is a failed copy's answer: a refusal as itself, anything else
// (the disk, a copy that failed) a 500 saying what.
func copyErr(err error, what string) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return errors.New(what + " failed: " + err.Error())
}

// ServeRestore answers POST /sandboxes/{name}/snapshots/{sid}/restore?wait=:
// the sandbox once its state is the snapshot's (its execs killed; started
// again if it ran), or as it stands, busy, when ?wait ran out first.
func (m *Manager) ServeRestore(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	wait, err := waitOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	job, err := m.Restore(k, d.Name, r.PathValue("sid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if waitJob(job, wait) && job.err != nil {
		writeErr(w, copyErr(job.err, "the restore"))
		return
	}
	in, ok := m.infoOf(k, d.Name)
	if !ok {
		writeErr(w, refuse(RefNotFound, "no sandbox %q", d.Name))
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// ServeDeleteSnapshot answers DELETE /sandboxes/{name}/snapshots/{sid}:
// 204 — put aside for the confined remover.
func (m *Manager) ServeDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	if err := m.DeleteSnapshot(k, d.Name, r.PathValue("sid")); err != nil {
		writeErr(w, err)
		return
	}
	m.measureSoon(k, d) // what it took no longer counts
	w.WriteHeader(http.StatusNoContent)
}
