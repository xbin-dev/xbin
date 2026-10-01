package broker

// The partitioned tiles' switches (PD-55; plans/partitions 05 §2, 06 §9):
//
//   - partitionConsent: a person's consent is needed before another
//     partitioned tile uses their data (the cross-tile edge rule, 05 §2).
//   - credentialResetConfirm: a sign-in link, password or SSO email an admin
//     sets for someone who holds partitions waits for them (06 §9).
//
// Both are off by default. Since D180 they are workspace settings
// (internal/wssettings, data/workspace-settings.json) beside base
// auto-update, set from the admin console's workspace → settings tab or `bx
// settings`; the routes are the server's (internal/server/wssettings.go:
// /workspace-settings, and /workspace-policies, v0.3.66's, as an alias). The
// broker reads them — b.Policies() — and raises the admins' alert while the
// settings can't be read.
//
// Both switches are protections, so a file that can't be read never turns
// one off: a switch whose value can't be read keeps the last value read, or
// counts as on when none was (wssettings).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

// WorkspacePolicies are the partitioned tiles' switches. The zero value is
// the default: both off.
type WorkspacePolicies struct {
	// PartitionConsent: another partitioned tile reaches a person's data
	// only with that person's consent (05 §2).
	PartitionConsent bool `json:"partitionConsent"`
	// CredentialResetConfirm: an admin-set credential for a person holding
	// partitions takes effect only after they confirm, or 24 h after they
	// were notified (06 §9).
	CredentialResetConfirm bool `json:"credentialResetConfirm"`
}

// settings is the workspace settings store the server's routes share
// (boot sets Settings); without one, the workspace's own
// data/workspace-settings.json, opened on first use.
func (b *Broker) settings() *wssettings.Store {
	b.settingsOnce.Do(func() {
		if b.Settings == nil {
			b.Settings = wssettings.New(filepath.Join(b.Reg.Root, "data", "workspace-settings.json"))
		}
	})
	return b.Settings
}

// Policies is the partitioned tiles' switches for xbind's own checks. A
// switch the settings don't let it read keeps its last value, or is on;
// wssettings logs it and /alerts shows it to admins.
func (b *Broker) Policies() WorkspacePolicies {
	st, _ := b.settings().Load()
	return WorkspacePolicies{PartitionConsent: st.PartitionConsent, CredentialResetConfirm: st.CredentialResetConfirm}
}

// settingsAlert is the admins' /alerts line while a workspace setting can't
// be read: crit when a partitioned tiles' switch can't (a protection held
// on), warn for base auto-update alone (held off).
func (b *Broker) settingsAlert() (Alert, bool) {
	_, probs := b.settings().Load()
	err := probs.Of()
	if err == nil {
		return Alert{}, false
	}
	level, held := "warn", "base auto-update is off"
	if probs.Of(wssettings.Keys(wssettings.GroupPartitions)...) != nil {
		level, held = "crit", "each partitioned tiles' switch that can't be read keeps its last value, or is on"
		if probs.Of(wssettings.KeyBaseAutoUpdate) != nil {
			held = "base auto-update is off and " + held
		}
	}
	return Alert{Level: level, Kind: "workspace-settings", Message: err.Error() +
		" — fix or remove the file by hand; until then " + held}, true
}

func (b *Broker) registerPolicies(srv *server.Server) {
	b.registerPartitionMode(srv)     // POST /partitions/mode: keep or switch (partitionswitch.go)
	b.registerPartitionConsents(srv) // consents, the ledger, the edges (partitionconsent.go)
	b.registerPartitionOps(srv)      // the listing, stop/reset/purge, log shares, credential confirmations (partitionops.go)
}

// canReadPolicies: who reads the partitioned tiles' switches — admins, and
// a person through their own session or device, or a terminal or agent
// session they drive (server.ReadsPartitionSettings).
func (b *Broker) canReadPolicies(p auth.Principal) bool {
	return server.ReadsPartitionSettings(p, b.IsAdmin(p))
}

// fileStamp is what tells a changed file (a restore, a hand edit) apart
// from the one a cache holds (partitionconsent.go).
type fileStamp struct {
	exists bool
	mod    int64 // mtime, ns
	size   int64
}

func stampFile(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size()}, nil
}

// hostless drops the host path an *fs.PathError carries.
func hostless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}
