package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a path-style bucket in memory: PUT, GET, DELETE and
// ListObjectsV2 by prefix, signatures unchecked.
type fakeS3 struct {
	mu      sync.Mutex
	objs    map[string][]byte // key → body
	failPut string            // PUTs of keys under this prefix answer 500
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/bucket"), "/")
	switch {
	case r.Method == "GET" && key == "":
		type content struct {
			Key          string
			Size         int64
			LastModified time.Time
		}
		var out struct {
			XMLName  xml.Name  `xml:"ListBucketResult"`
			Contents []content `xml:"Contents"`
		}
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objs {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.Contents = append(out.Contents, content{k, int64(len(f.objs[k])), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
		}
		_ = xml.NewEncoder(w).Encode(out)
	case r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		if f.failPut != "" && strings.HasPrefix(key, f.failPut) {
			http.Error(w, "InternalError", http.StatusInternalServerError)
			return
		}
		f.objs[key] = b
	case r.Method == "GET":
		b, ok := f.objs[key]
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case r.Method == "DELETE":
		delete(f.objs, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeS3) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// withFakeS3 points the handlers at a fake bucket under prefix "pre".
func withFakeS3(t *testing.T) (*fakeS3, http.Handler) {
	t.Helper()
	f := &fakeS3{objs: map[string][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	prev := client
	client = func() (*S3, config, error) {
		return &S3{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket", AccessKey: "a", SecretKey: "s", HTTP: srv.Client()},
			config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket", Prefix: "pre"}, nil
	}
	t.Cleanup(func() { client = prev })
	m := http.NewServeMux()
	m.HandleFunc("PUT /archive/{key}", putArchive)
	m.HandleFunc("GET /archive/{key}/versions", listVersions)
	m.HandleFunc("GET /archive/{key}/versions/{v}/file", getFile)
	m.HandleFunc("DELETE /archive/{key}/versions/{v}", deleteVersion)
	m.HandleFunc("POST /archive/erase", eraseSubkeys)
	return f, m
}

func call(t *testing.T, h http.Handler, method, target, subkey string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if subkey != "" {
		r.Header.Set(headerSubkey, subkey)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func put(t *testing.T, h http.Handler, key, subkey string, body []byte) string {
	t.Helper()
	w := call(t, h, "PUT", "/archive/"+key, subkey, body)
	var out struct{ Version string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Version == "" {
		t.Fatalf("PUT %s: %d %s", key, w.Code, w.Body)
	}
	return out.Version
}

const (
	keyA = "bk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bk-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// covers PD-56 11§6 — a sealed PUT leaves its subkey's marker; POST
// /archive/erase deletes exactly the versions sealed under the named keys,
// across archive keys, and their markers; versions under other keys and
// plain ones stay. The listing never shows a marker as a version.
func TestEraseBySubkey(t *testing.T) {
	f, h := withFakeS3(t)
	sealed := []byte(sealMagic + "\x00\x00\x00\x02{}ciphertext")
	a1 := put(t, h, "apps~x-1", keyA, sealed)
	d1 := put(t, h, ".data.0123", keyA, sealed)
	b1 := put(t, h, "apps~x-1", keyB, sealed)
	p1 := put(t, h, "apps~x-1", "", []byte("plain tar"))
	put(t, h, "apps~y-2", "not-a-key", sealed) // a malformed id leaves no marker

	if got := f.names(); !contains(got, "pre/.subkeys/"+keyA+"/apps~x-1/"+a1) || !contains(got, "pre/.subkeys/"+keyA+"/.data.0123/"+d1) {
		t.Fatalf("markers: %v", got)
	}
	w := call(t, h, "GET", "/archive/apps~x-1/versions", "", nil)
	if strings.Count(w.Body.String(), `"version"`) != 3 {
		t.Errorf("versions of apps~x-1: %s", w.Body)
	}

	w = call(t, h, "POST", "/archive/erase", "", []byte(`{"subkeys":["`+keyA+`"]}`))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"deleted":2}` {
		t.Fatalf("erase: %d %s", w.Code, w.Body)
	}
	got := f.names()
	for _, gone := range []string{"pre/apps~x-1/" + a1 + ".tar", "pre/.data.0123/" + d1 + ".tar", "pre/.subkeys/" + keyA + "/apps~x-1/" + a1} {
		if contains(got, gone) {
			t.Errorf("%s survived the erase", gone)
		}
	}
	for _, kept := range []string{"pre/apps~x-1/" + b1 + ".tar", "pre/apps~x-1/" + p1 + ".tar", "pre/.subkeys/" + keyB + "/apps~x-1/" + b1} {
		if !contains(got, kept) {
			t.Errorf("%s went with the erase", kept)
		}
	}
	if w := call(t, h, "POST", "/archive/erase", "", []byte(`{"subkeys":["../x"]}`)); w.Code != http.StatusBadRequest {
		t.Errorf("a malformed id: %d", w.Code)
	}
}

// covers PD-56 11§6 — the archiver can't read a sealed archive: a file
// from one is 422 (xbind extracts it); a plain tar's still comes back.
func TestGetFileSealed(t *testing.T) {
	_, h := withFakeS3(t)
	v := put(t, h, "apps~x-1", keyA, []byte(sealMagic+"\x00\x00\x00\x02{}ciphertext"))
	w := call(t, h, "GET", "/archive/apps~x-1/versions/"+v+"/file?path=source/index.html", "", nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "sealed archive: xbind extracts it") {
		t.Errorf("a sealed archive's file: %d %s", w.Code, w.Body)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "backup.json", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("{}"))
	_ = tw.WriteHeader(&tar.Header{Name: "source/index.html", Mode: 0o644, Size: 5})
	_, _ = tw.Write([]byte("hello"))
	_ = tw.Close()
	v = put(t, h, "apps~x-1", "", buf.Bytes())
	if w := call(t, h, "GET", "/archive/apps~x-1/versions/"+v+"/file?path=source/index.html", "", nil); w.Code != 200 || w.Body.String() != "hello" {
		t.Errorf("a plain tar's file: %d %s", w.Code, w.Body)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sealedBody is a sealed archive's first bytes as xbind writes them: the
// magic, the header's length and a header naming subkey id.
func sealedBody(id string) []byte {
	h := []byte(`{"v":1,"subkey":"` + id + `","chunk":65536,"kind":"main"}`)
	out := append([]byte(sealMagic), byte(len(h)>>24), byte(len(h)>>16), byte(len(h)>>8), byte(len(h)))
	return append(append(out, h...), "ciphertext"...)
}

// covers PD-56 11§6 — a PUT whose marker can't be stored keeps nothing: the
// archive is removed again and the answer is 502, so no backup names a
// version an erase couldn't find.
func TestPutMarkerFailureKeepsNothing(t *testing.T) {
	f, h := withFakeS3(t)
	f.failPut = "pre/.subkeys/"
	w := call(t, h, "PUT", "/archive/apps~x-1", keyA, sealedBody(keyA))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "wasn't kept") {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("left in the bucket: %v", got)
	}
}

// covers PD-56 11§6 — retention's DELETE takes a sealed version's marker
// with it (the key the version's header names); a plain version deletes as
// before; an erase counts only versions that were still there, and removes
// a marker whose version was already gone.
func TestDeleteVersionDropsItsMarker(t *testing.T) {
	f, h := withFakeS3(t)
	a1 := put(t, h, "apps~x-1", keyA, sealedBody(keyA))
	p1 := put(t, h, "apps~x-1", "", []byte("plain tar"))
	for _, v := range []string{a1, p1} {
		if w := call(t, h, "DELETE", "/archive/apps~x-1/versions/"+v, "", nil); w.Code != 200 {
			t.Fatalf("DELETE %s: %d %s", v, w.Code, w.Body)
		}
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("left after the deletes: %v", got)
	}
	// a marker an older archiver left behind its pruned version
	a2 := put(t, h, "apps~x-1", keyA, sealedBody(keyA))
	a3 := put(t, h, "apps~x-1", keyA, sealedBody(keyA))
	f.mu.Lock()
	delete(f.objs, "pre/apps~x-1/"+a2+".tar")
	f.mu.Unlock()
	w := call(t, h, "POST", "/archive/erase", "", []byte(`{"subkeys":["`+keyA+`"]}`))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"deleted":1}` {
		t.Fatalf("erase: %d %s", w.Code, w.Body)
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("left after the erase (%s): %v", a3, got)
	}
}
