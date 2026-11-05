// repl_bytes.go — bytes in the sandbox: how a session reaches blob-backed
// files (replIO), the Uint8Array ⇄ []byte bridge every byte API shares, and
// the pure encoding globals — atob/btoa, base64, and the natives behind the
// bootstrap's TextEncoder/TextDecoder. None of them reaches anything but its
// arguments and this run's own files.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// replIO is what a session needs beyond its sqlite rows: the bytes of a
// blob-backed file, a writer that puts large or binary content in the blob
// store, and the drop of a removed file's objects. The Agent is one.
type replIO interface {
	readBlob(ctx context.Context, path string) ([]byte, error)
	putFileData(ctx context.Context, runID int64, name string, data []byte, declared string, src *fileSource) (f, prev *ReplFile, unchanged bool, err error)
	dropBlobs(paths []string)
}

func (s *replSession) ioCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// fileBytes is any session file's content as bytes.
func (s *replSession) fileBytes(f *ReplFile) ([]byte, error) {
	if !f.Binary {
		return []byte(f.Content), nil
	}
	if s.io == nil {
		return nil, fmt.Errorf("%s is held in the blob store, which this sandbox cannot reach", f.Path)
	}
	b, err := s.io.readBlob(s.ioCtx(), f.Blob)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Path, err)
	}
	return b, nil
}

// putData writes a file through the blob-aware writer, as js_eval.
func (s *replSession) putData(what, p string, data []byte, declared string) *ReplFile {
	if s.io == nil {
		s.throw(fmt.Errorf("%s: %s is too large for a text file, and this sandbox cannot reach the blob store", what, p))
	}
	f, _, _, err := s.io.putFileData(s.ioCtx(), s.runID, p, data, declared, toolSource(s.ioCtx(), "js_eval"))
	if err != nil {
		s.throw(fmt.Errorf("%s: %s: %w", what, p, err))
	}
	return f
}

// asBytes reads a Uint8Array (or any 8-bit view) or an ArrayBuffer. A
// subarray exports only its own window — not the whole buffer.
func (s *replSession) asBytes(v goja.Value) ([]byte, bool) {
	if v == nil {
		return nil, false
	}
	if _, ok := v.(*goja.Object); !ok {
		return nil, false
	}
	switch x := v.Export().(type) {
	case []byte:
		return x, true
	case []int8:
		b := make([]byte, len(x))
		for i, c := range x {
			b[i] = byte(c)
		}
		return b, true
	case goja.ArrayBuffer:
		return x.Bytes(), true
	}
	return nil, false
}

// bytesArg is a required byte argument: a Uint8Array, an ArrayBuffer, or a
// string (as its UTF-8 bytes) when strOK.
func (s *replSession) bytesArg(what string, v goja.Value, strOK bool) []byte {
	if b, ok := s.asBytes(v); ok {
		return b
	}
	if strOK && v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		if _, isObj := v.(*goja.Object); !isObj {
			return []byte(v.String())
		}
	}
	want := "a Uint8Array or ArrayBuffer"
	if strOK {
		want += ", or a string"
	}
	s.throw(fmt.Errorf("%s: expected %s", what, want))
	return nil
}

// bytesVal hands bytes to the VM as a Uint8Array over them (no copy).
func (s *replSession) bytesVal(b []byte) goja.Value {
	if b == nil {
		b = []byte{}
	}
	o, err := s.vm.New(s.u8, s.vm.ToValue(s.vm.NewArrayBuffer(b)))
	if err != nil {
		s.throw(err)
	}
	return o
}

// mimeFor guesses a written file's type from its name; "" lets the writer
// sniff it.
func mimeFor(p string) string {
	m, _, _ := mime.ParseMediaType(mime.TypeByExtension(path.Ext(p)))
	return m
}

// textMimeFor is a large string's type: its name's when that is text, else
// text/plain — a string is text, whatever it is called.
func textMimeFor(p string) string {
	if m := mimeFor(p); isTextMime(m) {
		return m
	}
	return "text/plain"
}

// --- encoding globals ---------------------------------------------------------

func (s *replSession) installBytes(vm *goja.Runtime) error {
	set := func(name string, fn func(goja.FunctionCall) goja.Value) error { return vm.Set(name, fn) }
	if err := set("atob", s.jsAtob); err != nil {
		return err
	}
	if err := set("btoa", s.jsBtoa); err != nil {
		return err
	}
	b64 := vm.NewObject()
	_ = b64.Set("encode", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(base64.StdEncoding.EncodeToString(s.bytesArg("base64.encode", call.Argument(0), true)))
	})
	_ = b64.Set("decode", func(call goja.FunctionCall) goja.Value {
		b, err := decodeBase64(call.Argument(0).String())
		if err != nil {
			s.throw(fmt.Errorf("base64.decode: %w", err))
		}
		return s.bytesVal(b)
	})
	if err := vm.Set("base64", b64); err != nil {
		return err
	}
	// The bootstrap wraps these two in TextEncoder/TextDecoder and deletes them.
	if err := set("__utf8enc", func(call goja.FunctionCall) goja.Value {
		return s.bytesVal([]byte(call.Argument(0).String()))
	}); err != nil {
		return err
	}
	return set("__utf8dec", func(call goja.FunctionCall) goja.Value {
		b := s.bytesArg("TextDecoder.decode", call.Argument(0), false)
		if call.Argument(1).ToBoolean() && !utf8.Valid(b) { // fatal
			panic(vm.NewTypeError("TextDecoder.decode: the data is not valid UTF-8"))
		}
		if !call.Argument(2).ToBoolean() { // ignoreBOM false: the BOM is dropped
			b = trimBOM(b)
		}
		return vm.ToValue(strings.ToValidUTF8(string(b), "�"))
	})
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// decodeBase64 takes standard or URL-safe base64, padded or not, with any
// whitespace (line-wrapped files) ignored.
func decodeBase64(in string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, in)
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "="))
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %v", err)
	}
	return b, nil
}

// atob/btoa follow the web: a "binary string" holds one byte per character.
func (s *replSession) jsAtob(call goja.FunctionCall) goja.Value {
	b, err := decodeBase64(call.Argument(0).String())
	if err != nil {
		s.throw(fmt.Errorf("atob: %w", err))
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return s.vm.ToValue(string(r))
}

func (s *replSession) jsBtoa(call goja.FunctionCall) goja.Value {
	str := call.Argument(0).String()
	b := make([]byte, 0, len(str))
	for _, r := range str {
		if r > 0xFF {
			s.throw(fmt.Errorf("btoa: the string has characters outside Latin1 — encode it first " +
				"(base64.encode(str) takes UTF-8 text or bytes directly)"))
		}
		b = append(b, byte(r))
	}
	return s.vm.ToValue(base64.StdEncoding.EncodeToString(b))
}
