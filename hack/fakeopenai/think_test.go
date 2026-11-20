package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fake-think (D249): its list entry describes its effort; it says the effort
// it was asked for; and, as DeepSeek's API, it refuses a request that offers
// tools while an earlier answer lacks its reasoning_content.
func TestThinkingModel(t *testing.T) {
	srv := httptest.NewServer(newMux(false))
	defer srv.Close()
	post := func(body string) (int, string) {
		r, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return r.StatusCode, b.String()
	}
	tools := `"tools":[{"type":"function","function":{"name":"note","parameters":{}}}]`
	code, body := post(`{"model":"fake-think","reasoning_effort":"max",` + tools + `,"messages":[{"role":"user","content":"quick"}]}`)
	if code != 200 || !strings.Contains(body, `[effort max] Quick answer.`) || !strings.Contains(body, `"reasoning_content"`) {
		t.Fatalf("a first call: %d %s", code, body)
	}
	lacks := `{"model":"fake-think",` + tools + `,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"x"},{"role":"user","content":"quick"}]}`
	if code, body := post(lacks); code != 400 || !strings.Contains(body, "reasoning_content") {
		t.Fatalf("an answer without its reasoning: %d %s", code, body)
	}
	has := strings.Replace(lacks, `"content":"x"`, `"content":"x","reasoning_content":"r"`, 1)
	if code, body := post(has); code != 200 || !strings.Contains(body, "[effort default]") {
		t.Fatalf("with its reasoning: %d %s", code, body)
	}
	if code, _ := post(strings.Replace(lacks, tools+",", "", 1)); code != 200 {
		t.Fatalf("without tools the rule doesn't apply: %d", code)
	}
	r, _ := http.Get(srv.URL + "/v1/models")
	var list struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	_ = json.NewDecoder(r.Body).Decode(&list)
	r.Body.Close()
	found := false
	for _, m := range list.Data {
		if string(m["id"]) == `"fake-think"` {
			found = strings.Contains(string(m["effort"]), `"supported_levels":["low","high","max"]`)
		} else if m["effort"] != nil {
			t.Errorf("%s has an effort", m["id"])
		}
	}
	if !found {
		t.Errorf("fake-think's list entry: %v", list.Data)
	}
}
