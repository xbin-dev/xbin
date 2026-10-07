package server

import "testing"

// The injection goes after <head>; a page without one gets it after its
// doctype (in front of it the page would render in quirks mode), and a
// page with neither gets it first.
func TestPlaceInjection(t *testing.T) {
	const in = "<script>x</script>"
	for _, c := range []struct{ body, want string }{
		{"<!doctype html><html><head><title>t</title></head><body>b</body></html>",
			"<!doctype html><html><head>" + in + "<title>t</title></head><body>b</body></html>"},
		{"<!DOCTYPE html>\n<body>no head</body>", "<!DOCTYPE html>" + in + "\n<body>no head</body>"},
		{"\xEF\xBB\xBF  <!doctype html><p>bom</p>", "\xEF\xBB\xBF  <!doctype html>" + in + "<p>bom</p>"},
		{"<p>bare</p>", in + "<p>bare</p>"},
		{"<p>not a <!doctype html> at the start</p>", in + "<p>not a <!doctype html> at the start</p>"},
		{"<HEAD lang=x>h", "<HEAD lang=x>" + in + "h"},
	} {
		if got := string(placeInjection([]byte(c.body), in)); got != c.want {
			t.Errorf("%q:\n got  %q\n want %q", c.body, got, c.want)
		}
	}
}
