package xbin

import (
	"net/http/httptest"
	"testing"
)

// In a preview nobody is writing: Caller says so, and UserCanWrite is false
// whatever level the header carries (xbind sends "read" then; an older
// xbind, or a header a test built by hand, must not make it true).
func TestCallerPreview(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-XBin-From", "apps/x")
	r.Header.Set("X-XBin-User", "bob")
	r.Header.Set("X-XBin-User-Level", "terminal")
	if c := Caller(r); c.Preview || !c.UserCanWrite() {
		t.Fatalf("a person's own call: %+v", c)
	}
	r.Header.Set("X-XBin-Preview", "1")
	if c := Caller(r); !c.Preview || c.UserCanWrite() {
		t.Fatalf("a preview's call: preview %v, UserCanWrite %v", c.Preview, c.UserCanWrite())
	}
	r.Header.Set("X-XBin-Preview", "true") // only xbind's own value counts
	if c := Caller(r); c.Preview {
		t.Fatal("X-XBin-Preview: true was taken for a preview")
	}
}
