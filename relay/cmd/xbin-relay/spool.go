package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/relay"
)

// A development relay without APNs (-spool-dir): every notification it
// would send is written to the directory instead, as the JSON `xcrun simctl
// push` takes (the APNs body, plus "Simulator Target Bundle" — the topic),
// one file a notification, for a script to hand to a simulator (the iOS UI
// tests: native/ios/scripts/mac-remote.sh, XBIN_E2E_PUSH). Nothing reaches
// Apple; device tokens aren't checked.
func spoolAPNs(dir string) (*relay.APNs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // signs provider tokens nobody checks
	if err != nil {
		return nil, err
	}
	return &relay.APNs{KeyID: "spool", TeamID: "spool", Key: key, Production: "http://spool", Development: "http://spool",
		Client: &http.Client{Transport: &spool{dir: dir}, Timeout: 10 * time.Second}}, nil
}

type spool struct {
	dir string
	n   atomic.Int64
}

func (s *spool) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("spool: the APNs body isn't JSON: %w", err)
	}
	topic := r.Header.Get("apns-topic")
	doc["Simulator Target Bundle"] = strings.TrimSuffix(topic, ".push-type.liveactivity")
	out, _ := json.Marshal(doc)
	n := s.n.Add(1)
	name := fmt.Sprintf("%d-%06d-%s.apns", time.Now().UnixNano(), n, r.Header.Get("apns-push-type"))
	tmp := filepath.Join(s.dir, "."+name)
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("apns-id", fmt.Sprintf("spool-%d", n))
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
}
