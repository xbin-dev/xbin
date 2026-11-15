package llm

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
)

// keywatch.go — telling a key's minter what its holder was refused (D226).
// A coding CLI that is refused a call — a quota reached, too many calls at
// once, a model nothing serves — often says nothing to its person: OpenCode
// waits out a 429's Retry-After in silence, for hours when the quota resets
// at midnight. The session that minted the key can say it instead, so the
// minter may watch the key: every answer of 400 or more to a call made
// with it is reported, with the error's code and message.

// KeyRefusal is one refused call of a watched key.
type KeyRefusal struct {
	Status  int
	Code    string // the OpenAI-shaped error's code ("" when the answer had none)
	Message string // its message, at most 500 characters
}

// keyWatch is the watchers by key id.
type keyWatch struct {
	n  atomic.Int64
	by sync.Map // key id → func(KeyRefusal)
}

// WatchKey calls f for every call made with the key id that is answered
// 400 or more, until stop. f runs on the request's goroutine, after the
// answer was written: it must not block.
func (s *Service) WatchKey(id string, f func(KeyRefusal)) (stop func()) {
	if id == "" || f == nil {
		return func() {}
	}
	s.watch.by.Store(id, f)
	s.watch.n.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			s.watch.by.Delete(id)
			s.watch.n.Add(-1)
		})
	}
}

// watched is h, reporting a refusal to the watcher of the key the request
// presents. A request without a watched key passes untouched.
func (s *Service) watched(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.watch.n.Load() == 0 {
			h(w, r)
			return
		}
		tok, ok := bearerKey(r)
		if !ok {
			h(w, r)
			return
		}
		g, live := s.keySource.Resolve(tok)
		f, has := s.watch.by.Load(g.ID)
		if !live || !has {
			h(w, r)
			return
		}
		rec := &refusalRec{ResponseWriter: w}
		h(rec.writer(), r)
		if rec.status < 400 {
			return
		}
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.buf, &e)
		code, _ := e.Error.Code.(string)
		msg := e.Error.Message
		if len(msg) > 500 {
			msg = msg[:500]
		}
		f.(func(KeyRefusal))(KeyRefusal{Status: rec.status, Code: code, Message: msg})
	}
}

// refusalRec keeps the status of an answer and, for an error, the start of
// its body. Everything is written through.
type refusalRec struct {
	http.ResponseWriter
	status int
	buf    []byte
}

const refusalBody = 8 << 10

func (r *refusalRec) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *refusalRec) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 400 && len(r.buf) < refusalBody {
		r.buf = append(r.buf, p[:min(len(p), refusalBody-len(r.buf))]...)
	}
	return r.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the connection's own flush and
// deadlines (the relay streams through it).
func (r *refusalRec) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *refusalRec) writer() http.ResponseWriter { return r }
