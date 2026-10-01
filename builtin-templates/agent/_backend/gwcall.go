// gwcall.go — small calls through the xbin gateway for a partitioned agent's
// plumbing (conf.go, team.go, partition_routes.go): the `conf` kv resource,
// and a person's partition reaching its own global instance (docs/partitions.md
// "a user partition calling its own global instance": xbind attributes the
// call to the partition's person). Every call brings its own timeout — the
// SDK client has none.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// gwTimeout bounds one plumbing call. A var so tests can shorten it.
var gwTimeout = 5 * time.Second

// gwResp is a call's answer: the status and at most 4 MiB of body.
type gwResp struct {
	Status int
	Type   string
	Body   []byte
}

// gwDo sends one request through the gateway, bounded by ctx's deadline —
// gwTimeout when it has none.
func gwDo(ctx context.Context, method, u string, body []byte, ctype string) (gwResp, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gwTimeout)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return gwResp{}, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return gwResp{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return gwResp{Status: resp.StatusCode, Type: resp.Header.Get("Content-Type"), Body: b}, err
}

// --- a kv resource ------------------------------------------------------------------

// kvStore is what conf.go needs of a kv resource; tests use memKV.
type kvStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error) // ok=false: no such key
	Put(ctx context.Context, key string, val []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// gatewayKV is a kv resource through the gateway (xbin.KV's routes, with
// timeouts).
type gatewayKV struct{ res string }

func (k gatewayKV) url(key string) string {
	return "http://xbin/api/xbin/kv/" + k.res + "/" + url.PathEscape(key)
}

func (k gatewayKV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	r, err := gwDo(ctx, http.MethodGet, k.url(key), nil, "")
	switch {
	case err != nil:
		return nil, false, err
	case r.Status == http.StatusNotFound:
		return nil, false, nil
	case r.Status != http.StatusOK:
		return nil, false, gwErr("kv get", r)
	}
	return r.Body, true, nil
}

func (k gatewayKV) Put(ctx context.Context, key string, val []byte) error {
	r, err := gwDo(ctx, http.MethodPut, k.url(key), val, "application/octet-stream")
	if err == nil && r.Status != http.StatusOK {
		err = gwErr("kv put", r)
	}
	return err
}

func (k gatewayKV) Delete(ctx context.Context, key string) error {
	r, err := gwDo(ctx, http.MethodDelete, k.url(key), nil, "")
	if err == nil && r.Status != http.StatusOK && r.Status != http.StatusNotFound {
		err = gwErr("kv delete", r)
	}
	return err
}

func (k gatewayKV) List(ctx context.Context, prefix string) ([]string, error) {
	r, err := gwDo(ctx, http.MethodGet, "http://xbin/api/xbin/kv/"+k.res+"/?prefix="+url.QueryEscape(prefix), nil, "")
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, gwErr("kv list", r)
	}
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

func gwErr(what string, r gwResp) error {
	return fmt.Errorf("%s: HTTP %d: %s", what, r.Status, strings.TrimSpace(clip(string(r.Body), 300)))
}

// --- the global instance ---------------------------------------------------------------

// callGlobal is a person's partition calling its own global instance at path
// (the tile's API: "/config", "/health"), attributed there to the partition's
// person. A var so tests can stand in for the global instance.
var callGlobal = func(ctx context.Context, method, path string, body []byte, ctype string) (gwResp, error) {
	if !userMode() {
		return gwResp{}, errors.New("only a person's partition calls the global instance")
	}
	if method != http.MethodGet {
		defer noteGlobalWrite() // handoff_user.go: what global lists may have changed
	}
	return gwDo(ctx, method, xbin.GlobalURL(path), body, ctype)
}
