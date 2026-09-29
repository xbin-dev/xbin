package auth

import "context"

type backupSubkeyKey struct{}

// WithBackupSubkey marks one of xbind's own archive calls (the broker's, as
// the owner) with the id of the backup subkey the archive it PUTs is sealed
// under: the proxy sets X-XBin-Backup-Subkey from it on the forwarded
// request, after stripping whatever the request carried
// (plans/partitions/11-backup-encryption.md §6).
func WithBackupSubkey(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, backupSubkeyKey{}, id)
}

// BackupSubkeyOf is the id WithBackupSubkey put on ctx ("" for none).
func BackupSubkeyOf(ctx context.Context) string {
	id, _ := ctx.Value(backupSubkeyKey{}).(string)
	return id
}
