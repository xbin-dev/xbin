package vm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// initrd returns the path of the guest initramfs: a newc cpio holding only
// the agent as /init, cached by the agent's content hash (xbind writes it
// from its own trusted binary — no tile data involved).
func (m *Manager) initrd() (string, error) {
	agent, err := os.ReadFile(m.assets.Agent)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(agent)
	dir := filepath.Join(m.Root, ".xbin", "vm", "initrd")
	out := filepath.Join(dir, hex.EncodeToString(sum[:8])+".cpio")
	if isFile(out) {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	writeNewc(&buf, "init", 0o100755, agent)
	writeNewc(&buf, "TRAILER!!!", 0, nil)
	tmp, err := os.CreateTemp(dir, ".initrd-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), out); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return out, nil
}

// writeNewc appends one entry in the kernel's "newc" cpio format.
func writeNewc(w io.Writer, name string, mode uint32, data []byte) {
	pad := func(n int) { w.Write(make([]byte, (4-n%4)%4)) }
	hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		1, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
	io.WriteString(w, hdr)
	io.WriteString(w, name)
	w.Write([]byte{0})
	pad(len(hdr) + len(name) + 1)
	w.Write(data)
	pad(len(data))
}
