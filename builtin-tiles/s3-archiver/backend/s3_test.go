package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Verify SigV4 against AWS's published worked example ("Signature Version 4
// signing" — the GET IAM ListUsers vanilla case). If our signing reproduces the
// documented signature, the algorithm is correct even though we can't hit a live
// bucket. https://docs.aws.amazon.com/general/latest/gr/sigv4-signed-request-examples.html
func TestSigV4Vector(t *testing.T) {
	t.Setenv("TZ", "UTC")
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	headers := http.Header{
		"content-type": []string{"application/x-www-form-urlencoded; charset=utf-8"},
		"host":         []string{"iam.amazonaws.com"},
		"x-amz-date":   []string{"20150830T123600Z"},
	}
	q := url.Values{"Action": {"ListUsers"}, "Version": {"2010-05-08"}}
	auth := sign("GET", "/", q, headers, emptyHash,
		"us-east-1", "iam", "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", when)

	const wantSig = "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if !strings.Contains(auth, "Signature="+wantSig) {
		t.Fatalf("SigV4 mismatch.\n got: %s\nwant Signature=%s", auth, wantSig)
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-date") {
		t.Errorf("signed headers wrong: %s", auth)
	}
}

func TestObjectPath(t *testing.T) {
	s := &S3{Bucket: "my-bucket"}
	if got := s.objectPath("backups/apps_x/v1.tar"); got != "/my-bucket/backups/apps_x/v1.tar" {
		t.Errorf("objectPath = %q", got)
	}
	if got := s.objectPath(""); got != "/my-bucket" {
		t.Errorf("list path = %q", got)
	}
	// A space in a segment must be %20 (rfc3986), not '+'.
	if got := s.objectPath("a b"); got != "/my-bucket/a%20b" {
		t.Errorf("encoded path = %q", got)
	}
}
