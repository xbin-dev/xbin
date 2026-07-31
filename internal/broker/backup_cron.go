package broker

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/xbin-dev/xbin/internal/server"
)

// Scheduled component backups (plans/lifecycle.md LC-5). Owner-registered jobs on
// the existing cron engine that fire a xbind backup (not an element endpoint)
// and prune to a retention count. Persisted in data/backup-schedule.json.

type backupSchedule struct {
	Component string `json:"component"`
	Schedule  string `json:"schedule"`  // 5-field cron or "@every 24h"
	Retention int    `json:"retention"` // versions to keep after each run (0 = keep all)
}

func (cr *cronRunner) backupPath() string {
	return filepath.Join(cr.b.Reg.Root, "data", "backup-schedule.json")
}

func (cr *cronRunner) loadBackups() {
	bts, err := os.ReadFile(cr.backupPath())
	if err != nil {
		return
	}
	var scheds []backupSchedule
	if json.Unmarshal(bts, &scheds) != nil {
		return
	}
	for _, s := range scheds {
		if err := cr.addBackup(s); err != nil {
			slog.Warn("backup schedule: dropping persisted entry", "component", s.Component, "err", err)
		}
	}
}

func (cr *cronRunner) persistBackups() {
	cr.mu.Lock()
	scheds := make([]backupSchedule, 0, len(cr.backups))
	for _, s := range cr.backups {
		scheds = append(scheds, s)
	}
	cr.mu.Unlock()
	sort.Slice(scheds, func(i, j int) bool { return scheds[i].Component < scheds[j].Component })
	bts, _ := json.MarshalIndent(scheds, "", "  ")
	_ = os.MkdirAll(filepath.Dir(cr.backupPath()), 0o755)
	_ = os.WriteFile(cr.backupPath(), bts, 0o644)
}

func (cr *cronRunner) addBackup(s backupSchedule) error {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if old, ok := cr.backupEntries[s.Component]; ok {
		cr.sched.Remove(old)
	}
	id, err := cr.sched.AddFunc(s.Schedule, func() { cr.b.runScheduledBackup(s) })
	if err != nil {
		return err
	}
	cr.backupEntries[s.Component] = id
	cr.backups[s.Component] = s
	return nil
}

func (cr *cronRunner) removeBackup(comp string) bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	id, ok := cr.backupEntries[comp]
	if !ok {
		return false
	}
	cr.sched.Remove(id)
	delete(cr.backupEntries, comp)
	delete(cr.backups, comp)
	return true
}

// runScheduledBackup performs a backup and then prunes to the retention count.
func (b *Broker) runScheduledBackup(s backupSchedule) {
	if _, err := b.doBackup(s.Component); err != nil {
		slog.Warn("scheduled backup failed", "component", s.Component, "err", err)
		return
	}
	if s.Retention > 0 {
		b.pruneVersions(s.Component, s.Retention)
	}
}

// pruneVersions deletes a component's oldest archived versions beyond keep.
func (b *Broker) pruneVersions(comp string, keep int) {
	provider := b.archiveProvider(comp)
	if provider == "" {
		return
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+backupKey(comp)+"/versions", nil)
	if err != nil || code >= 400 {
		return
	}
	var out struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if json.Unmarshal(body, &out) != nil {
		return
	}
	// The archiver returns versions newest-first, so anything past keep is old.
	for i := keep; i < len(out.Versions); i++ {
		_, _, _ = b.archiveDo("DELETE", provider, "/archive/"+backupKey(comp)+"/versions/"+out.Versions[i].Version, nil)
	}
}

// --- API --------------------------------------------------------------------

func (b *Broker) apiBackupScheduleList(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	b.cron.mu.Lock()
	out := make([]backupSchedule, 0, len(b.cron.backups))
	for _, s := range b.cron.backups {
		out = append(out, s)
	}
	b.cron.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	server.WriteJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (b *Broker) apiBackupScheduleSet(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var s backupSchedule
	if err := decodeJSON(r, &s); err != nil || s.Component == "" || s.Schedule == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component, schedule, retention?}"})
		return
	}
	if _, ok := b.Reg.Component(s.Component); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return
	}
	if err := b.cron.addBackup(s); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad schedule: " + err.Error()})
		return
	}
	b.cron.persistBackups()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiBackupScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	comp := r.URL.Query().Get("component")
	if !b.cron.removeBackup(comp) {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no schedule for that component"})
		return
	}
	b.cron.persistBackups()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
