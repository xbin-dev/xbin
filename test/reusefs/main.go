// reusefs is a FUSE passthrough that deliberately RECYCLES inode numbers:
// when a file/dir is removed, its inode number goes on a freelist and the
// next new node gets it. This simulates ext4/xfs inode reuse
// deterministically on filesystems that never reuse numbers (btrfs, tmpfs) —
// the trigger for stale per-inode cache entries in gocryptfs single-tenant
// mode. Usage: reusefs BACKING MOUNTPOINT
package main

import (
	"context"
	"log"
	"os"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type inoAlloc struct {
	mu   sync.Mutex
	m    map[uint64]uint64 // real ino -> virtual ino
	free []uint64          // recycled virtual inos, LIFO
	next uint64
}

func (a *inoAlloc) get(real uint64) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v, ok := a.m[real]; ok {
		return v
	}
	var v uint64
	if n := len(a.free); n > 0 {
		v = a.free[n-1] // reuse — the whole point
		a.free = a.free[:n-1]
	} else {
		a.next++
		v = a.next
	}
	a.m[real] = v
	return v
}

func (a *inoAlloc) release(real uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v, ok := a.m[real]; ok {
		delete(a.m, real)
		a.free = append(a.free, v)
	}
}

var alloc = &inoAlloc{m: map[uint64]uint64{}, next: 1000}

type reuseNode struct {
	fs.LoopbackNode
}

// backingPath rebuilds the underlying path of child `name` (LoopbackNode's
// own path() is unexported).
func (n *reuseNode) backingPath(name string) string {
	p := n.Path(n.Root())
	out := n.RootData.Path
	if p != "" {
		out += "/" + p
	}
	if name != "" {
		out += "/" + name
	}
	return out
}

func (n *reuseNode) Unlink(ctx context.Context, name string) syscall.Errno {
	var st syscall.Stat_t
	stErr := syscall.Lstat(n.backingPath(name), &st)
	errno := n.LoopbackNode.Unlink(ctx, name)
	if errno == 0 && stErr == nil && st.Nlink <= 1 {
		alloc.release(st.Ino)
	}
	return errno
}

func (n *reuseNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	var st syscall.Stat_t
	stErr := syscall.Lstat(n.backingPath(name), &st)
	errno := n.LoopbackNode.Rmdir(ctx, name)
	if errno == 0 && stErr == nil {
		alloc.release(st.Ino)
	}
	return errno
}

func (n *reuseNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// A replacing rename frees the target's inode.
	np, ok := newParent.(*reuseNode)
	var st syscall.Stat_t
	stErr := syscall.EINVAL
	if ok {
		if err := syscall.Lstat(np.backingPath(newName), &st); err == nil && st.Nlink <= 1 {
			stErr = 0
		}
	}
	errno := n.LoopbackNode.Rename(ctx, name, newParent, newName, flags)
	if errno == 0 && stErr == 0 {
		alloc.release(st.Ino)
	}
	return errno
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: reusefs BACKING MOUNTPOINT")
	}
	backing, mnt := os.Args[1], os.Args[2]

	root := &fs.LoopbackRoot{
		Path: backing,
		NewNode: func(rootData *fs.LoopbackRoot, parent *fs.Inode, name string, st *syscall.Stat_t) fs.InodeEmbedder {
			st.Ino = alloc.get(st.Ino) // recycled identity, seen by idFromStat
			return &reuseNode{fs.LoopbackNode{RootData: rootData}}
		},
	}
	var st syscall.Stat_t
	if err := syscall.Stat(backing, &st); err != nil {
		log.Fatalf("stat backing: %v", err)
	}
	root.Dev = uint64(st.Dev)
	st.Ino = alloc.get(st.Ino)
	rootNode := &reuseNode{fs.LoopbackNode{RootData: root}}
	root.RootNode = rootNode

	server, err := fs.Mount(mnt, rootNode, &fs.Options{
		MountOptions: fuse.MountOptions{FsName: "reusefs", Name: "reusefs"},
	})
	if err != nil {
		log.Fatalf("mount: %v", err)
	}
	server.Wait()
}
