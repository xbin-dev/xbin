package proxy

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

// HeaderBackupSubkey names the backup subkey a sealed archive xbind PUTs to
// an archiver is sealed under (plans/partitions/11-backup-encryption.md
// §6): its opaque id, which an archiver may store to delete every version
// sealed under it (POST /archive/erase). Like every X-XBin-* header, an
// inbound one is stripped: only xbind's own archive calls carry it, set
// through auth.WithBackupSubkey.
const HeaderBackupSubkey = "X-XBin-Backup-Subkey"

// setBackupSubkey is identify's last step: the header from the context, and
// only from there.
func setBackupSubkey(r *http.Request) {
	if id := auth.BackupSubkeyOf(r.Context()); id != "" {
		r.Header.Set(HeaderBackupSubkey, id)
	}
}
