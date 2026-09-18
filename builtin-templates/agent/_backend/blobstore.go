// blobstore.go — where the bytes of binary session files live.
//
// Text session files stay in sqlite, because the model reads them, the REPL
// loads them and the render pane shows them. Binary ones (images and other
// attachments) keep only their metadata there; the bytes go to the scope's
// `files` blob resource at a random, never-reused path, so an object is
// immutable once written and can be cached without invalidation.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// blobCallTimeout bounds each gateway call. xbin.Client() has no Timeout of its
// own — the same trap the heartbeat fell into — so without this a gateway that
// accepts the connection and then goes quiet would hang an upload, or worse, a
// drive assembling images into its context.
const blobCallTimeout = 30 * time.Second

type blobStore interface {
	Put(ctx context.Context, path string, data []byte, mime string) error
	Get(ctx context.Context, path string) ([]byte, error)
	Delete(ctx context.Context, path string) error
}

// gatewayBlobs talks to the xbin blob API for the `files` resource. Follows
// apps/imap-connector/backend/xbinio.go, with deadlines added.
type gatewayBlobs struct{}

func (gatewayBlobs) url(path string) (string, error) {
	res := xbin.Resource("files")
	if res == "" {
		return "", fmt.Errorf("the files blob resource is not granted to this backend yet")
	}
	return "http://xbin/api/xbin/blob/" + res + "/" + path, nil
}

func (g gatewayBlobs) Put(ctx context.Context, path string, data []byte, mime string) error {
	u, err := g.url(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, blobCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("blob put: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}

func (g gatewayBlobs) Get(ctx context.Context, path string) ([]byte, error) {
	u, err := g.url(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, blobCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("blob get: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBinaryFileBytes+1))
}

func (g gatewayBlobs) Delete(ctx context.Context, path string) error {
	u, err := g.url(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, blobCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("blob delete: %s", resp.Status)
	}
	return nil
}

// newBlobPath names an object. Random and never reused: the object behind a
// path never changes, so readers can cache it forever.
func newBlobPath(runID int64) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("runs/%d/%s", runID, hex.EncodeToString(b[:]))
}

// --- read cache ------------------------------------------------------------

// blobCache keeps recently read objects in memory. Every loop iteration
// re-assembles the context, so without it each step would re-fetch every image
// it shows from the gateway. Safe because objects are immutable (see
// newBlobPath). Bounded by total bytes, least-recently-used out first.
type blobCache struct {
	mu    sync.Mutex
	max   int
	size  int
	order []string
	data  map[string][]byte
}

func newBlobCache(max int) *blobCache {
	return &blobCache{max: max, data: map[string][]byte{}}
}

func (c *blobCache) get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[path]
	if ok {
		c.touch(path)
	}
	return b, ok
}

func (c *blobCache) put(path string, b []byte) {
	if len(b) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[path]; ok {
		c.touch(path)
		return
	}
	c.data[path] = b
	c.size += len(b)
	c.order = append(c.order, path)
	for c.size > c.max && len(c.order) > 0 {
		old := c.order[0]
		c.order = c.order[1:]
		c.size -= len(c.data[old])
		delete(c.data, old)
	}
}

func (c *blobCache) drop(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.data[path]; ok {
		c.size -= len(b)
		delete(c.data, path)
		for i, p := range c.order {
			if p == path {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
}

func (c *blobCache) touch(path string) {
	for i, p := range c.order {
		if p == path {
			c.order = append(append(c.order[:i:i], c.order[i+1:]...), path)
			return
		}
	}
}

// readBlob returns an object's bytes, from the cache when possible.
func (ag *Agent) readBlob(ctx context.Context, path string) ([]byte, error) {
	if b, ok := ag.blobCache.get(path); ok {
		return b, nil
	}
	b, err := ag.blobs.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	ag.blobCache.put(path, b)
	return b, nil
}

// dropBlobs deletes objects after their rows are gone. Best effort and after
// the sqlite commit: a failed delete leaves an orphan object, never a row that
// points at nothing.
func (ag *Agent) dropBlobs(paths []string) {
	for _, p := range paths {
		ag.blobCache.drop(p)
		if err := ag.blobs.Delete(context.Background(), p); err != nil {
			log.Printf("agent: could not delete blob %s (left orphaned): %v", p, err)
		}
	}
}

// deleteRunTree deletes a run and everything it spawned, then drops the blob
// objects their files owned — after the rows are gone, so a failed blob delete
// can only orphan an object, never leave a row pointing at nothing.
func (ag *Agent) deleteRunTree(id int64) error {
	ids := []int64{id}
	if kids, err := ag.db.descendants(id); err == nil {
		ids = append(ids, kids...)
	}
	blobs := ag.db.runBlobs(ids)
	if err := ag.db.deleteRun(id); err != nil {
		return err
	}
	ag.dropBlobs(blobs)
	return nil
}
