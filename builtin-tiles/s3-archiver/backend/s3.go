package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// A minimal, dependency-free S3 client: just the four verbs the archive contract
// needs (PUT/GET/LIST/DELETE), path-style so it works with AWS, MinIO, R2, B2,
// etc., signed with SigV4. Kept small and self-contained on purpose — the signing
// is unit-tested against AWS's published vector (s3_test.go).

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA256("")

type S3 struct {
	Endpoint  string // e.g. https://s3.us-east-1.amazonaws.com or https://minio:9000
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
}

type s3obj struct {
	Key          string
	Size         int64
	LastModified time.Time
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// sign returns the SigV4 Authorization header value for a prepared request. The
// request's Host, X-Amz-Date and X-Amz-Content-Sha256 headers must already be
// set; payloadHash is the hex body hash or "UNSIGNED-PAYLOAD".
func sign(method, path string, query url.Values, headers http.Header, payloadHash, region, service, accessKey, secretKey string, t time.Time) string {
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")

	// Canonical headers: lowercase name, trimmed value, sorted, ';'-joined names.
	var names []string
	lower := map[string]string{}
	for name, vals := range headers {
		ln := strings.ToLower(name)
		names = append(names, ln)
		lower[ln] = strings.TrimSpace(vals[0])
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + lower[n] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method, path, canonicalQuery(query), canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crHash[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature)
}

// canonicalQuery sorts and RFC3986-encodes the query for signing.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, rfc3986(k)+"="+rfc3986(v))
		}
	}
	return strings.Join(parts, "&")
}

// rfc3986 encodes per AWS rules (encode everything but unreserved).
func rfc3986(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// objectPath is the canonical, per-segment-encoded request path /<bucket>/<key…>
// (path-style). key may contain '/', which separates segments (not encoded).
func (s *S3) objectPath(key string) string {
	segs := []string{s.Bucket}
	if key != "" {
		segs = append(segs, strings.Split(key, "/")...)
	}
	for i, seg := range segs {
		segs[i] = rfc3986(seg)
	}
	return "/" + strings.Join(segs, "/")
}

func (s *S3) do(method, key string, query url.Values, body io.Reader, length int64, payloadHash string) (*http.Response, error) {
	path := s.objectPath(key)
	// Use the canonical (rfc3986) query for BOTH the URL and the signature so
	// they can never diverge (url.Values.Encode differs on some characters).
	cq := canonicalQuery(query)
	u := strings.TrimRight(s.Endpoint, "/") + path
	if cq != "" {
		u += "?" + cq
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	if length >= 0 {
		req.ContentLength = length
	}
	now := time.Now().UTC()
	req.Header.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Host = req.URL.Host
	signHeaders := http.Header{
		"host":                 []string{req.URL.Host},
		"x-amz-date":           []string{req.Header.Get("X-Amz-Date")},
		"x-amz-content-sha256": []string{payloadHash},
	}
	auth := sign(method, path, query, signHeaders, payloadHash, s.Region, "s3", s.AccessKey, s.SecretKey, now)
	req.Header.Set("Authorization", auth)
	c := s.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	return c.Do(req)
}

func (s *S3) Put(key string, body io.Reader, length int64) error {
	resp, err := s.do("PUT", key, url.Values{}, body, length, "UNSIGNED-PAYLOAD")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 put %s: %s: %s", key, resp.Status, b)
	}
	return nil
}

// Get returns the object response; the caller streams and closes Body.
func (s *S3) Get(key string) (*http.Response, error) {
	resp, err := s.do("GET", key, url.Values{}, nil, 0, emptyHash)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("s3 get %s: %s: %s", key, resp.Status, b)
	}
	return resp, nil
}

func (s *S3) Delete(key string) error {
	resp, err := s.do("DELETE", key, url.Values{}, nil, 0, emptyHash)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return fmt.Errorf("s3 delete %s: %s", key, resp.Status)
	}
	return nil
}

// List returns all objects under prefix (ListObjectsV2, paginated).
func (s *S3) List(prefix string) ([]s3obj, error) {
	var out []s3obj
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do("GET", "", q, nil, 0, emptyHash)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("s3 list: %s: %s", resp.Status, body)
		}
		var r struct {
			Contents []struct {
				Key          string
				Size         int64
				LastModified time.Time
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		if err := xml.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		for _, c := range r.Contents {
			out = append(out, s3obj{Key: c.Key, Size: c.Size, LastModified: c.LastModified})
		}
		if !r.IsTruncated || r.NextContinuationToken == "" {
			break
		}
		token = r.NextContinuationToken
	}
	return out, nil
}
