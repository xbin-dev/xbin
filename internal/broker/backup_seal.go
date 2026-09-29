package broker

// backup_seal.go — sealed archives in backup and restore
// (plans/partitions/11-backup-encryption.md §1, §4; PD-56). Every archive
// xbind writes while a vault barrier is set up is sealed under its
// subject's backup key (backupkeys.go): the main archive under tile:, the
// scope's main data — split out into an archive of its own, .data.<TileKey>
// — under ns:<main namespace>, a deployment's data under its namespace's.
// Every restore opens archives through backup.Open, so a plaintext archive
// of any age restores as it always did, and a sealed one is authenticated
// whole before anything is written.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// archiveSeal is what an archive is sealed as: its key's subject (and the
// tile the subject belongs to) and the header's kind.
type archiveSeal struct{ subject, tile, kind string }

func mainSeal(tile string) archiveSeal { return archiveSeal{tileSubject(tile), tile, backup.KindMain} }

func dataSeal(tile string) archiveSeal {
	return archiveSeal{nsSubject(tile, util.MainDeployment), tile, backup.KindData}
}

func deploymentSeal(tile, dep string) archiveSeal {
	return archiveSeal{nsSubject(tile, dep), tile, backup.KindDeployment}
}

// dataArchiveKey is tile's data archive's key: .data.<TileKey>, which no
// CompKey equals (none starts with ".") and no deployment archive's either.
func dataArchiveKey(tile string) string { return ".data." + util.TileKey(tile) }

// putArchive streams the archive write builds to provider under key —
// sealed as s while a vault barrier is set up, today's tar without one —
// answering the version the archiver assigned. A sealed vault fails every
// archive: none can be sealed without the DEK.
func (b *Broker) putArchive(provider, key string, s archiveSeal, write func(*backup.Writer) error) (string, error) {
	v, _, err := b.putSealed(provider, key, s, write)
	return v, err
}

// putSealed is putArchive, also answering the id of the subkey the archive
// is sealed under ("" for a plaintext one).
func (b *Broker) putSealed(provider, key string, s archiveSeal, write func(*backup.Writer) error) (string, string, error) {
	sealed, err := b.sealing()
	if err != nil {
		return "", "", err
	}
	var id string
	var subkey []byte
	if sealed {
		if id, subkey, err = b.backupKeys().subkeyFor(s.subject, s.tile); err != nil {
			return "", "", fmt.Errorf("backup key: %w", err)
		}
	}
	pr, pw := io.Pipe()
	defer pr.Close() // an archiver that answers without reading the body releases the writer
	go func() {
		var out io.Writer = pw
		var sw io.WriteCloser
		if sealed {
			var err error
			sw, err = backup.NewSealedWriter(pw, subkey, id, s.kind)
			clear(subkey) // the archive's key is derived; the subkey isn't needed again
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			out = sw
		}
		bw := backup.NewWriter(out)
		err := write(bw)
		if err == nil {
			err = bw.Close()
		}
		if err == nil && sw != nil {
			err = sw.Close()
		}
		pw.CloseWithError(err)
	}()
	code, resp, err := b.archiveCall("PUT", provider, "/archive/"+key, pr, id)
	if err != nil {
		return "", "", err
	}
	if code >= 400 {
		return "", "", fmt.Errorf("archiver %s: %s", provider, firstLine(string(resp)))
	}
	var out struct{ Version string }
	_ = json.Unmarshal(resp, &out)
	return out.Version, id, nil
}

// headerBackupSubkey is proxy.HeaderBackupSubkey (the proxy's tests import
// the broker, so the broker doesn't import the proxy).
const headerBackupSubkey = "X-XBin-Backup-Subkey"

// archiveCall is archiveDo carrying the subkey a sealed archive is under
// (X-XBin-Backup-Subkey, which the proxy sets from the context after
// stripping any a request carried; set here too for an archiver wired in
// without the proxy).
func (b *Broker) archiveCall(method, provider, apiPath string, body io.Reader, subkey string) (int, []byte, error) {
	if subkey == "" {
		return b.archiveDo(method, provider, apiPath, body)
	}
	if b.ProxyHandler == nil {
		return 0, nil, fmt.Errorf("no proxy wired")
	}
	req := httptest.NewRequest(method, "/api/"+provider+apiPath, body)
	ctx := auth.WithBackupSubkey(auth.WithPrincipal(req.Context(), auth.Principal{Owner: true}), subkey)
	req = req.WithContext(ctx)
	req.Header.Set(headerBackupSubkey, subkey)
	rec := httptest.NewRecorder()
	b.ProxyHandler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// splitBackup is what one sealed backup's main archive (schema 3) names:
// the random id its data archive shares, and that data archive (nil when
// the tile doesn't root its scope, or the scope declares no resource).
type splitBackup struct {
	id   string
	data *backup.DataRef
}

// putDataArchive writes root tile c's main data into its data archive
// (schema 3, kind data), answering what its main archive names. The
// archiver's version must be one xbind can fetch again: a main archive
// naming any other would restore without its data, so the backup fails
// before the main archive is written.
func (b *Broker) putDataArchive(c *registry.Component, provider string) (*splitBackup, error) {
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	sp := &splitBackup{id: hex.EncodeToString(rnd[:])}
	scope, isRoot := b.Reg.Scopes()[c.Path]
	if !isRoot || len(b.mainDeclared(c.Path, scope).Resources) == 0 {
		return sp, nil // no data: no data archive
	}
	key := dataArchiveKey(c.Path)
	v, id, err := b.putSealed(provider, key, dataSeal(c.Path), func(bw *backup.Writer) error { return b.writeDataArchive(bw, c, sp.id) })
	if err != nil {
		return nil, fmt.Errorf("archiving its data: %w", err)
	}
	if !archiveVersion.MatchString(v) {
		return nil, fmt.Errorf("archiving its data: archiver %s answered the PUT with version %q, which xbind can't fetch again (a PUT answers {\"version\": \"…\"} of letters, digits and ._:-, at most 128): no backup names what it stored", provider, v)
	}
	sp.data = &backup.DataRef{Key: key, Version: v, Subkey: id}
	return sp, nil
}

// writeDataArchive is a data archive: the manifest, then the scope's main
// data as a schema-1 main archive lays it out.
func (b *Broker) writeDataArchive(bw *backup.Writer, c *registry.Component, backupID string) error {
	scope := b.mainDeclared(c.Path, b.Reg.Scopes()[c.Path])
	m := backup.Manifest{Schema: backup.SchemaSplit, Kind: backup.KindData, Component: c.Path, Scope: c.Path, ScopeRoot: true,
		Resources: map[string]string{}, XBinVersion: b.Version, Created: time.Now().UTC().Format(time.RFC3339), Includes: []string{"data"},
		BackupID: backupID}
	if scope != nil {
		for name, res := range scope.Resources {
			m.Resources[name] = res.Type
		}
	}
	if err := bw.Manifest(m); err != nil {
		return err
	}
	if scope == nil {
		return nil
	}
	return b.writeScopeData(bw, c.Path, scope)
}

// fetchArchive GETs one version of key from provider.
func (b *Broker) fetchArchive(provider, key, version string) ([]byte, error) {
	code, body, err := b.archiveDo("GET", provider, "/archive/"+key+"/versions/"+version, nil)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusNotFound:
		return nil, fmt.Errorf("archiver %s has no version %s of %s: %w", provider, version, key, errArchiveGone)
	case code >= 400:
		return nil, fmt.Errorf("archiver %s: %s", provider, firstLine(string(body)))
	}
	return body, nil
}

var errArchiveGone = errors.New("not found")

// openArchive reads an archive fetched whole: a sealed one is authenticated
// end to end first, so a damaged or tampered one is refused before any of
// it is used; a plaintext one reads as it always did.
func (b *Broker) openArchive(body []byte) (*backup.Reader, error) {
	keys := b.backupKeyFunc()
	if backup.IsSealed(body) {
		r, _, err := backup.OpenSealed(bytes.NewReader(body), keys)
		if err == nil {
			_, err = io.Copy(io.Discard, r)
		}
		if err != nil {
			return nil, err
		}
	}
	return backup.Open(bytes.NewReader(body), keys)
}

// dataMissingError is why a split main archive's data isn't restored when
// its data archive is gone though its key isn't erased — the archiver
// lost it, or deleted it after answering the main archive's PUT with an
// error — or the main archive names a version no archiver URL can.
type dataMissingError struct{ why string }

func (e dataMissingError) Error() string {
	return "this backup's data archive is missing (" + e.why + ")"
}

// openDataArchive opens the data archive main archive m of comp names. One
// whose key was erased — whether or not the archiver still holds it — and
// one that is missing answer why as gone (an erasedError, a
// dataMissingError) and no error: the main archive restores without it, and
// says so. One that is there must be the one m's backup wrote: sealed
// under the key m names, with m's backup id.
func (b *Broker) openDataArchive(provider, comp string, m backup.Manifest) (r *backup.Reader, gone, err error) {
	ref := m.Data
	switch {
	case ref.Key != dataArchiveKey(comp):
		return nil, nil, fmt.Errorf("the archive names data archive %q, not %s's", ref.Key, comp)
	case !archiveVersion.MatchString(ref.Version):
		return nil, dataMissingError{fmt.Sprintf("the backup names version %q of it, which no archiver URL can", ref.Version)}, nil
	}
	erased := func(err error) bool {
		var e erasedError
		if errors.As(err, &e) {
			gone = e
			return true
		}
		return false
	}
	body, err := b.fetchArchive(provider, ref.Key, ref.Version)
	if err != nil {
		if !errors.Is(err, errArchiveGone) {
			return nil, nil, fmt.Errorf("its data archive: %w", err)
		}
		if ref.Subkey != "" {
			if _, kerr := b.backupKeys().lookup(ref.Subkey); erased(kerr) {
				return nil, gone, nil // its versions were collected after the erase
			}
		}
		return nil, dataMissingError{fmt.Sprintf("archiver %s has no version %s of %s", provider, ref.Version, ref.Key)}, nil
	}
	if h, sealed, herr := backup.ReadSealHeader(bytes.NewReader(body)); herr == nil && ref.Subkey != "" && (!sealed || h.Subkey != ref.Subkey) {
		return nil, nil, fmt.Errorf("its data archive %s %s isn't the one this backup wrote: it isn't sealed under the backup key the backup names", ref.Key, ref.Version)
	}
	if r, err = b.openArchive(body); err != nil {
		if erased(err) {
			return nil, gone, nil
		}
		return nil, nil, fmt.Errorf("its data archive: %w", err)
	}
	dm := r.M
	switch {
	case !dm.DataArchive() || dm.Component != comp || !dm.ScopeRoot || dm.Scope != comp:
		return nil, nil, fmt.Errorf("its data archive %s %s isn't %s's data", ref.Key, ref.Version, comp)
	case m.BackupID != "" && dm.BackupID != m.BackupID:
		return nil, nil, fmt.Errorf("its data archive %s %s isn't the one this backup wrote: another backup's", ref.Key, ref.Version)
	}
	return r, nil, nil
}

// extractMember answers one member of comp's archive version: POST /restore
// with file. xbind reads every archive itself — an archiver can't parse a
// sealed one — following a split main archive's data pointer for data/….
func (b *Broker) extractMember(comp, version, name string) ([]byte, int, error) {
	provider := b.archiveProvider(comp)
	if provider == "" {
		return nil, http.StatusBadGateway, fmt.Errorf("no archiver bound for %q", comp)
	}
	body, err := b.fetchArchive(provider, backupKey(comp), version)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errArchiveGone) {
			code = http.StatusNotFound
		}
		return nil, code, err
	}
	br, err := b.openArchive(body)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	if strings.HasPrefix(name, backup.DataPrefix) && br.M.Data != nil {
		dr, gone, err := b.openDataArchive(provider, comp, br.M)
		switch {
		case err != nil:
			return nil, http.StatusConflict, err
		case errors.As(gone, new(dataMissingError)):
			return nil, http.StatusNotFound, gone
		case gone != nil:
			return nil, http.StatusConflict, gone
		}
		br = dr
	}
	if name == backup.ManifestName {
		data, _ := json.MarshalIndent(br.M, "", "  ")
		return data, http.StatusOK, nil
	}
	for {
		n, rd, err := br.Next()
		if err == io.EOF {
			return nil, http.StatusNotFound, fmt.Errorf("no such file in this version: %s", name)
		}
		if err != nil {
			return nil, http.StatusConflict, err
		}
		if n == name {
			data, err := io.ReadAll(rd)
			if err != nil {
				return nil, http.StatusConflict, err
			}
			return data, http.StatusOK, nil
		}
	}
}
