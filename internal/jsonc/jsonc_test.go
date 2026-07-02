package jsonc

import "testing"

func TestUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		src  string
		ok   bool
	}{
		{"plain", `{"a":1}`, true},
		{"line comment", "{\n// hi\n\"a\":1}", true},
		{"block comment", `{/* x */ "a":1}`, true},
		{"trailing comma obj", `{"a":1,}`, true},
		{"trailing comma arr", `{"a":[1,2,],}`, true},
		{"comment after comma", "{\"a\":1, // t\n}", true},
		{"slashes in string", `{"a":"http://x//y"}`, true},
		{"comment chars in string", `{"a":"/* not a comment */"}`, true},
		{"escaped quote", `{"a":"\"//","b":1}`, true},
		{"actually broken", `{"a":`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v map[string]any
			err := Unmarshal([]byte(c.src), &v)
			if (err == nil) != c.ok {
				t.Fatalf("Unmarshal(%q) err=%v, want ok=%v", c.src, err, c.ok)
			}
		})
	}
}

func TestStripPreservesStrings(t *testing.T) {
	src := `{"url":"https://a//b/*c*/d","n":1,}`
	var v struct {
		URL string `json:"url"`
		N   int    `json:"n"`
	}
	if err := Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	if v.URL != "https://a//b/*c*/d" || v.N != 1 {
		t.Fatalf("got %+v", v)
	}
}
