package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// settingsServer answers like an xbind of a given age: "d180" (every
// setting at /workspace-settings), "v0.3.66" (the partitioned tiles'
// switches at /workspace-policies only), "v0.3.65" (base auto-update only);
// tileCode: a credential that doesn't read the switches.
func settingsServer(t *testing.T, age string, tileCode bool) (*[]string, func()) {
	t.Helper()
	var got []string
	state := map[string]bool{"baseAutoUpdate": true, "partitionConsent": false, "credentialResetConfirm": false}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(b)))
		policies := age == "d180" || age == "v0.3.66"
		switch r.URL.Path {
		case "/api/xbin/workspace-settings":
			if r.Method == "PUT" {
				var p map[string]bool
				_ = json.Unmarshal(b, &p)
				for k, v := range p {
					if _, known := state[k]; !known || (k != "baseAutoUpdate" && age != "d180") {
						w.WriteHeader(400)
						_ = json.NewEncoder(w).Encode(map[string]string{"error": "need {baseAutoUpdate: bool}"})
						return
					}
					state[k] = v
				}
			}
			view := map[string]any{"baseAutoUpdate": state["baseAutoUpdate"]}
			if age == "d180" && !tileCode {
				view["partitionConsent"], view["credentialResetConfirm"] = state["partitionConsent"], state["credentialResetConfirm"]
			}
			_ = json.NewEncoder(w).Encode(view)
		case "/api/xbin/workspace-policies":
			if !policies {
				http.NotFound(w, r)
				return
			}
			if tileCode {
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "the workspace policies are for people and admins, not tile code"})
				return
			}
			if r.Method == "PUT" {
				var p map[string]bool
				_ = json.Unmarshal(b, &p)
				for k, v := range p {
					state[k] = v
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": 1, "partitionConsent": state["partitionConsent"], "credentialResetConfirm": state["credentialResetConfirm"]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	t.Setenv("XBIN_COMPONENT", "")
	return &got, srv.Close
}

// covers D180 — bx settings sets every setting by name (on|off|true|false)
// or by D175's flag form, in one PUT /workspace-settings; a bad name or
// state is refused before any request.
func TestBxSettingsSet(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]bool
	}{
		{[]string{"partition-consent", "on"}, map[string]bool{"partitionConsent": true}},
		{[]string{"credential-reset-confirm", "true", "base-auto-update", "off"}, map[string]bool{"credentialResetConfirm": true, "baseAutoUpdate": false}},
		{[]string{"--base-auto-update"}, map[string]bool{"baseAutoUpdate": true}},
		{[]string{"--base-auto-update=false"}, map[string]bool{"baseAutoUpdate": false}},
		{[]string{"--no-base-auto-update"}, map[string]bool{"baseAutoUpdate": false}},
		{[]string{"--partition-consent=on", "--no-credential-reset-confirm"}, map[string]bool{"partitionConsent": true, "credentialResetConfirm": false}},
		{[]string{"baseAutoUpdate", "OFF"}, map[string]bool{"baseAutoUpdate": false}},
	} {
		got, err := parseSettingsSet(tc.args)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("set %v = %v %v, want %v", tc.args, got, err, tc.want)
		}
	}
	for _, args := range [][]string{
		{}, {"partition-consent"}, {"partition-consent", "maybe"}, {"nope", "on"}, {"--nope"},
		{"--base-auto-update=perhaps"}, {"--no-base-auto-update=true"},
	} {
		if _, err := parseSettingsSet(args); err == nil {
			t.Errorf("set %v: no error", args)
		}
	}

	got, done := settingsServer(t, "d180", false)
	defer done()
	if err := cmdSettings([]string{"set", "partition-consent", "on", "--no-base-auto-update"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSettings([]string{"set", "--base-auto-update"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ls", "x"}, {"get"}, {"--base-auto-update"}} {
		if err := cmdSettings(args); err == nil {
			t.Errorf("bx settings %v: no error", args)
		}
	}
	want := []string{
		"GET /api/xbin/workspace-settings", // which xbind: it serves the switches
		`PUT /api/xbin/workspace-settings {"baseAutoUpdate":false,"partitionConsent":true}`,
		"GET /api/xbin/workspace-settings", // the answer printed
		`PUT /api/xbin/workspace-settings {"baseAutoUpdate":true}`,
		"GET /api/xbin/workspace-settings",
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", *got, want)
	}
}

// covers D180 — against an xbind before D180, bx settings reads the
// partitioned tiles' switches from /workspace-policies and sets them there;
// against one before partitions it shows base auto-update alone; a
// credential that doesn't read the switches is told so.
func TestBxSettingsOlderXbind(t *testing.T) {
	got, done := settingsServer(t, "v0.3.66", false)
	sv, err := loadSettings()
	if err != nil || sv.unified || sv.view["partitionConsent"] != false || sv.view["baseAutoUpdate"] != true {
		t.Fatalf("v0.3.66: %+v %v", sv, err)
	}
	if err := cmdSettings([]string{"set", "partition-consent", "on", "base-auto-update", "off"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/xbin/workspace-settings", "GET /api/xbin/workspace-policies", // loadSettings
		"GET /api/xbin/workspace-settings", "GET /api/xbin/workspace-policies", // which xbind
		`PUT /api/xbin/workspace-policies {"partitionConsent":true}`,
		`PUT /api/xbin/workspace-settings {"baseAutoUpdate":false}`,
		"GET /api/xbin/workspace-settings", "GET /api/xbin/workspace-policies",
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("v0.3.66 requests:\n got %q\nwant %q", *got, want)
	}
	if sv, _ := loadSettings(); sv.view["partitionConsent"] != true || sv.view["baseAutoUpdate"] != false {
		t.Fatalf("v0.3.66 after the set: %+v", sv.view)
	}
	done()

	_, done = settingsServer(t, "v0.3.65", false)
	sv, err = loadSettings()
	if _, has := sv.view["partitionConsent"]; err != nil || has || !strings.Contains(sv.groupNote["Partitioned tiles"], "no partitioned tiles") {
		t.Fatalf("v0.3.65: %+v %v", sv, err)
	}
	done()

	_, done = settingsServer(t, "d180", true)
	sv, err = loadSettings()
	if _, has := sv.view["partitionConsent"]; err != nil || has || !strings.Contains(sv.groupNote["Partitioned tiles"], "people and admins") {
		t.Fatalf("tile code: %+v %v", sv, err)
	}
	done()
}
