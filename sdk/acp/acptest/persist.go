package acptest

// --persist: a session's updates on disk, replayed by session/load as the
// real adapters replay their own history.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/sdk/acp"
)

func (f *fake) sessionsDir() string { return filepath.Join(f.getenv("HOME"), ".fakeacp", "sessions") }

// sessionFile is where session id's updates live ("" for an id that isn't
// one of ours).
func (f *fake) sessionFile(id string) string {
	if id == "" || strings.HasPrefix(id, ".") || strings.ContainsAny(id, `/\`) {
		return ""
	}
	return filepath.Join(f.sessionsDir(), id+".jsonl")
}

// newSession is a new session's id, its (empty) history created.
func (f *fake) newSession() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := "fake-" + hex.EncodeToString(b)
	if err := os.MkdirAll(f.sessionsDir(), 0o700); err == nil {
		if file, err := os.OpenFile(f.sessionFile(id), os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = file.Close()
		}
	}
	return id
}

// append adds one update to session sid's history.
func (f *fake) append(sid string, update []byte) {
	path := f.sessionFile(sid)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(append([]byte{}, update...), '\n'))
}

// record adds an update to the history without sending it (the user's
// prompt, which the client already has).
func (f *fake) record(v any) {
	if !f.o.Persist {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.append(f.session(), raw)
}

// replay makes id the session and streams its history back (sent as is,
// not recorded again).
func (f *fake) replay(id string) error {
	path := f.sessionFile(id)
	if path == "" {
		return errors.New("no such session: " + id)
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("no such session: " + id)
	}
	defer file.Close()
	f.mu.Lock()
	f.sid = id
	f.mu.Unlock()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			_ = f.conn.Notify(acp.MSessionUpdate, map[string]any{"sessionId": id, "update": json.RawMessage(line)})
		}
	}
	return sc.Err()
}
