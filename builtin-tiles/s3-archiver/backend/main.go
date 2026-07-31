// s3-archiver backend: an `archive` interface provider (plans/lifecycle.md)
// that stores xbind's component backup tars in an S3 bucket. xbind (the owner)
// PUTs a tar per version; this tile lists versions, streams a version back, or
// extracts one file from a version. Config (endpoint/region/bucket/prefix) lives
// in this tile's kv; the S3 credentials live in its vault.
package main

import (
	"archive/tar"
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

func client() (*S3, config, error) {
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
	tr := tar.NewReader(resp.Body)
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

func deleteVersion(w http.ResponseWriter, r *http.Request) {
	s3, cfg, err := client()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := s3.Delete(objKey(cfg.Prefix, r.PathValue("key"), r.PathValue("v"))); err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
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
	// This tile's own settings panel.
	m.HandleFunc("GET /config", getConfig)
	m.HandleFunc("PUT /config", putConfig)
	m.HandleFunc("POST /check", checkConn)
	xbin.Serve(m)
}
