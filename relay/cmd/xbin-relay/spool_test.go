package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/relay"
)

// A spooling relay writes what it would send to APNs, as simctl push takes it.
func TestSpoolWritesSimctlPayloads(t *testing.T) {
	dir := t.TempDir()
	a, err := spoolAPNs(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.Send(context.Background(), relay.Notification{Token: "ab12", Topic: "dev.xbin.app", Env: "development",
		Payload: []byte(`{"aps":{"alert":{"title":"New activity"},"mutable-content":1},"xbin":{"v":1}}`), CollapseID: "c"})
	if err != nil || id == "" {
		t.Fatalf("send: %q %v", id, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.apns"))
	if len(files) != 1 {
		t.Fatalf("spooled: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || doc["Simulator Target Bundle"] != "dev.xbin.app" || doc["xbin"] == nil || doc["aps"] == nil {
		t.Fatalf("the payload: %s", b)
	}
}
