// s3-archiver backend: an `archive` interface provider (docs/overview/14-lifecycle.md)
// that stores xbind's component backup archives in an S3 bucket. xbind (the owner)
// PUTs an archive per version — sealed (XBINSEAL…) in a workspace with a vault,
// a plain tar without one; this tile lists versions, streams a version back,
// extracts one file from a plain tar, and deletes every version sealed under
// an erased backup key (erase.go). Config (endpoint/region/bucket/prefix) lives
// in this tile's kv; the S3 credentials live in its vault.
package main

import (
	"archive/tar"
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var kv = xbin.KV(xbin.Resource("state"))

type config struct {
	Endpoint string `json:"endpoint"` // e.g. https://s3.us-east-1.amazonaws.com
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"` // key prefix inside the bucket
}

func loadConfig() config {
	var c config
	_ = kv.GetJSON("config", &c)
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	return c
}

// client is the bucket and its config (a variable: tests stand a fake S3 in).
var client = func() (*S3, config, error) {
	c := loadConfig()
	ak, _ := xbin.Secret("accessKey")
	sk, _ := xbin.Secret("secretKey")
	if c.Bucket == "" || c.Endpoint == "" || ak == "" || sk == "" {
		return nil, c, fmt.Errorf("s3-archiver is not configured — set endpoint/region/bucket and credentials on its page")
	}
	return &S3{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket,
		AccessKey: ak, SecretKey: sk, HTTP: &http.Client{Timeout: 10 * time.Minute}}, c, nil
}

func objKey(prefix, key, version string) string { return path.Join(prefix, key, version+".tar") }

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// putArchive stores a new version. The body is buffered to a temp file so we can
// give S3 a Content-Length (backups are a component's worth of data, not huge).
func putArchive(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	key := r.PathValue("key")
	tmp, err := os.CreateTemp("", "bk-*.tar")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, r.Body)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	version := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd[:])
	if err := s3.Put(objKey(cfg.Prefix, key, version), tmp, n); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	// A sealed archive names its backup key: the marker lets an erase find
	// every version sealed under it (S3 listings carry names only). Without
	// it the version is dropped again: an error answer means nothing was
	// kept, so no backup names a version the erase couldn't find.
	if id := r.Header.Get(headerSubkey); subkeyID.MatchString(id) {
		if err := s3.Put(markerKey(cfg.Prefix, id, key, version), strings.NewReader(""), 0); err != nil {
			msg := "its backup key's marker couldn't be stored, so the archive wasn't kept: " + err.Error()
			if derr := s3.Delete(objKey(cfg.Prefix, key, version)); derr != nil {
				msg += " (and removing the stored archive failed: " + derr.Error() + ")"
			}
			fail(w, http.StatusBadGateway, msg)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"version": version, "size": n})
}

type versionInfo struct {
	Version string `json:"version"`
	Time    string `json:"time"`
	Size    int64  `json:"size"`
}

// versionsOf lists a key's versions, newest first.
func versionsOf(s3 *S3, cfg config, key string) ([]versionInfo, error) {
	objs, err := s3.List(path.Join(cfg.Prefix, key) + "/")
	if err != nil {
		return nil, err
	}
	var out []versionInfo
	for _, o := range objs {
		base := path.Base(o.Key)
		if !strings.HasSuffix(base, ".tar") {
			continue
		}
		out = append(out, versionInfo{Version: strings.TrimSuffix(base, ".tar"), Time: o.LastModified.UTC().Format(time.RFC3339), Size: o.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func listVersions(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	vs, err := versionsOf(s3, cfg, r.PathValue("key"))
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if vs == nil {
		vs = []versionInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"versions": vs})
}

// resolve turns a version (or "latest") into its object key.
func resolve(s3 *S3, cfg config, key, v string) (string, error) {
	if v != "latest" && v != "" {
		return objKey(cfg.Prefix, key, v), nil
	}
	vs, err := versionsOf(s3, cfg, key)
	if err != nil {
		return "", err
	}
	if len(vs) == 0 {
		return "", fmt.Errorf("no versions for %s", key)
	}
	return objKey(cfg.Prefix, key, vs[0].Version), nil
}

func getVersion(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ok, err := resolve(s3, cfg, r.PathValue("key"), r.PathValue("v"))
	if err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	resp, err := s3.Get(ok)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	_, _ = io.Copy(w, resp.Body)
}

func getFile(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	want := r.URL.Query().Get("path")
	if want == "" {
		fail(w, http.StatusBadRequest, "need ?path=")
		return
	}
	ok, err := resolve(s3, cfg, r.PathValue("key"), r.PathValue("v"))
	if err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	resp, err := s3.Get(ok)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	body := bufio.NewReader(resp.Body)
	if head, _ := body.Peek(len(sealMagic)); string(head) == sealMagic {
		fail(w, http.StatusUnprocessableEntity, "sealed archive: xbind extracts it")
		return
	}
	tr := tar.NewReader(body)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == want {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.Copy(w, tr)
			return
		}
	}
	fail(w, http.StatusNotFound, "no such file in this version")
}

// deleteVersion prunes a version (xbind's retention), and the marker of the
// backup key a sealed one names, so none outlives its version.
func deleteVersion(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	key, v := r.PathValue("key"), r.PathValue("v")
	obj := objKey(cfg.Prefix, key, v)
	id := ""
	if resp, err := s3.Get(obj); err == nil {
		id = sealedSubkey(resp.Body)
		resp.Body.Close()
	}
	if err := s3.Delete(obj); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if id != "" {
		if err := s3.Delete(markerKey(cfg.Prefix, id, key, v)); err != nil {
			fail(w, http.StatusBadGateway, "deleted, but its backup key's marker wasn't: "+err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// --- this tile's own settings (its frontend calls these) --------------------

func getConfig(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()
	ak, _ := xbin.Secret("accessKey")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"config": c, "hasCreds": ak != ""})
}

func putConfig(w http.ResponseWriter, r *http.Request) {
	var c config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := kv.PutJSON("config", c); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// checkConn verifies the current config + credentials actually reach the bucket
// (called by the settings page after a save). A dial error here means the `net`
// interface isn't bound; 403 means bad keys; 404 means a wrong bucket/endpoint.
func checkConn(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s3.Probe(); err != nil {
		fail(w, http.StatusBadGateway, "cannot reach the bucket: "+err.Error()+
			" (if this is a 'dial'/'connection refused' error, bind this tile's net interface: bx bind "+xbin.Self()+" net=internet)")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bucket": cfg.Bucket})
}

func main() {
	m := http.NewServeMux()
	// The archive contract (called by xbind as the owner).
	m.HandleFunc("PUT /archive/{key}", putArchive)
	m.HandleFunc("GET /archive/{key}/versions", listVersions)
	m.HandleFunc("GET /archive/{key}/versions/{v}", getVersion)
	m.HandleFunc("GET /archive/{key}/versions/{v}/file", getFile)
	m.HandleFunc("DELETE /archive/{key}/versions/{v}", deleteVersion)
	m.HandleFunc("POST /archive/erase", eraseSubkeys) // optional: every version sealed under erased keys
	// This tile's own settings panel.
	m.HandleFunc("GET /config", getConfig)
	m.HandleFunc("PUT /config", putConfig)
	m.HandleFunc("POST /check", checkConn)
	xbin.Serve(m)
}
