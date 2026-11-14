package broker

// sealed.go — the broker's side of sealed components (D227): what its
// listings leave out, and the hooks boot wires to the sealed set for
// backups. The refusals a sealed path gets on the API are the server's
// table (server/sealedclass.go); the handlers named SealedHandled there
// apply their rules where they decide.

import (
	"net/http"
	"slices"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// listSealed reports whether a listing answering r includes the row of
// path: always for a tile; for a sealed component only when the caller
// asked (?sealed=1), so a console that predates them never shows one.
func listSealed(r *http.Request, path string) bool {
	return !util.SealedPath(path) || server.WantsSealed(r)
}

// listedGrants is the grant table a listing answering r shows: without the
// release's own grants to its sealed components unless the caller asked —
// rows an older console would offer to revoke, which nobody can.
func listedGrants(r *http.Request, grants []registry.Grant) []registry.Grant {
	if server.WantsSealed(r) {
		return grants
	}
	return slices.DeleteFunc(slices.Clone(grants), func(g registry.Grant) bool { return util.SealedPath(g.From) })
}

// SealedHooks is what the broker asks the sealed set (boot wires them; all
// nil-safe through the methods below).
type SealedHooks struct {
	// DataFormat is the data format a backup of the sealed component at
	// path records (0: none).
	DataFormat func(path string) int
	// RestoreRefusal says why a backup in data format data, written by
	// release, can't be restored into the component at path (nil: it can).
	RestoreRefusal func(path string, data int, release string) error
}

func (b *Broker) sealedDataFormat(path string) int {
	if f := b.Sealed.DataFormat; f != nil && util.SealedPath(path) {
		return f(path)
	}
	return 0
}

// sealedRestoreRefusal is why archive m can't be restored into the sealed
// component at path (nil: it can, or path is a tile): its data is in a
// format this release's code can't read.
func (b *Broker) sealedArchiveRefusal(path string, m backup.Manifest) error {
	return b.sealedRestoreRefusal(path, m.SealedData, m.XBinVersion)
}

// sealedRestoreGate is what a restore into the sealed component at comp
// must pass before anything is stopped or written (restoreFrom, the one
// place a tile archive is written back):
//
//   - The archive, and the data archive it names, are sealed ones: written
//     under a backup key this workspace's vault holds (backupkeys.go: a
//     subkey derived per subject under the vault barrier's key, or one an
//     admin imported with its workspace's key bundle) and authenticated end
//     to end. A plain tar is whatever the archiver served — the archiver
//     is a tile, and whoever writes its code would otherwise write a
//     sealed component's data, registrations included. Its manifest is
//     then xbind's own word: the component it names (checked by
//     restoreFrom) and the data format it records.
//   - The data format is one this release reads, and the component is not
//     held for its data (foreign data at its key, a store newer than the
//     code, an unreadable stamp): the hold says the data there is not this
//     code's to overwrite.
//   - It carries no deployment state: a sealed component has no record.
func (b *Broker) sealedRestoreGate(comp string, br, data *backup.Reader) error {
	const what = "it is restored only from a backup xbin sealed itself (encrypted under a backup key this workspace's vault holds, and authenticated)"
	switch {
	case !br.Sealed():
		return &sealedError{Status: http.StatusConflict, Msg: util.SealedRefusal(comp, what+"; this archive is a plain one, which is the archiver's word alone")}
	case data != nil && !data.Sealed():
		return &sealedError{Status: http.StatusConflict, Msg: util.SealedRefusal(comp, what+"; its data archive is a plain one, which is the archiver's word alone")}
	case br.M.Deployments != nil:
		return &sealedError{Status: http.StatusConflict, Msg: util.SealedRefusal(comp, "it has no deployments, and this archive carries a deployment record")}
	}
	return b.sealedArchiveRefusal(comp, br.M)
}

func (b *Broker) sealedRestoreRefusal(path string, data int, release string) error {
	if f := b.Sealed.RestoreRefusal; f != nil && util.SealedPath(path) {
		return f(path, data, release)
	}
	return nil
}
