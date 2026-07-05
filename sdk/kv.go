package xbin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// KV opens a granted kv resource (declared in scope.json, requested in
// "uses"). resource is the canonical target, most conveniently taken from
// the env xbind injects: xbin.KV(xbin.Resource("events")).
//
//	kv := xbin.KV(xbin.Resource("events"))
//	kv.PutJSON("2026-07-02/standup", ev)
//	keys, _ := kv.List("2026-07-02/")
func KV(resource string) *KVStore {
	return &KVStore{res: resource, c: Client()}
}

type KVStore struct {
	res string
	c   *http.Client
}

func (k *KVStore) url(key string) string {
	return "http://xbin/api/xbin/kv/" + k.res + "/" + url.PathEscape(key)
}

func (k *KVStore) Get(key string) ([]byte, error) {
	resp, err := k.c.Get(k.url(key))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kv get: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (k *KVStore) GetJSON(key string, v any) error {
	b, err := k.Get(key)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (k *KVStore) Put(key string, val []byte) error {
	req, err := http.NewRequest(http.MethodPut, k.url(key), bytes.NewReader(val))
	if err != nil {
		return err
	}
	resp, err := k.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kv put: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (k *KVStore) PutJSON(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return k.Put(key, b)
}

func (k *KVStore) Delete(key string) error {
	req, err := http.NewRequest(http.MethodDelete, k.url(key), nil)
	if err != nil {
		return err
	}
	resp, err := k.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kv delete: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// List returns keys with the given prefix ("" = all).
func (k *KVStore) List(prefix string) ([]string, error) {
	resp, err := k.c.Get("http://xbin/api/xbin/kv/" + k.res + "/?prefix=" + url.QueryEscape(prefix))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kv list: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// ErrNotFound is returned by Get for missing keys.
var ErrNotFound = fmt.Errorf("xbin: not found")
