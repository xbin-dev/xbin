package acp

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// chunks is a reader that delivers its parts one Read each; a *Gap part is
// returned as the error of an empty read.
type chunks struct{ parts []any }

func (r *chunks) Read(p []byte) (int, error) {
	if len(r.parts) == 0 {
		return 0, io.EOF
	}
	switch v := r.parts[0].(type) {
	case *Gap:
		r.parts = r.parts[1:]
		return 0, v
	case string:
		n := copy(p, v)
		if n == len(v) {
			r.parts = r.parts[1:]
		} else {
			r.parts[0] = v[n:]
		}
		return n, nil
	}
	panic("bad part")
}

// Offset counts what Next consumed — through each frame's newline, bad and
// blank lines included — however the bytes arrive, never the read-ahead,
// and from where the reader starts in the stream.
func TestDecoderOffset(t *testing.T) {
	a := `{"jsonrpc":"2.0","method":"a"}` + "\n"
	bad := "not json\n"
	blank := " \r\n"
	b := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\r\n"
	c := `{"jsonrpc":"2.0","method":"c"}` // the last line, no newline
	all := a + bad + blank + b + c
	for name, r := range map[string]io.Reader{
		"whole":    strings.NewReader(all),
		"one byte": iotest.OneByteReader(strings.NewReader(all)),
		"halves":   &chunks{parts: []any{all[:40], all[40:]}},
	} {
		for _, base := range []int64{0, 1000} {
			d := NewDecoderAt(r, base)
			if base == 0 {
				d = NewDecoder(r)
			}
			want := []struct {
				method string
				bad    bool
				off    int
			}{{"a", false, len(a)}, {"", true, len(a + bad)}, {"", false, len(a + bad + blank + b)}, {"c", false, len(all)}}
			for i, w := range want {
				m, err := d.Next()
				if w.bad != errors.Is(err, ErrBadLine) || (!w.bad && (err != nil || m.Method != w.method)) {
					t.Fatalf("%s #%d: %v %v", name, i, m, err)
				}
				if got := d.Offset(); got != base+int64(w.off) {
					t.Fatalf("%s #%d: offset %d, want %d", name, i, got, base+int64(w.off))
				}
				if m != nil && m.off != d.Offset() {
					t.Fatalf("%s #%d: the frame's own offset %d", name, i, m.off)
				}
			}
			if _, err := d.Next(); err != io.EOF {
				t.Fatalf("%s: end %v", name, err)
			}
			break // the reader is spent: one base per reader
		}
	}
	d := NewDecoderAt(strings.NewReader(a), 1000)
	if _, err := d.Next(); err != nil || d.Offset() != 1000+int64(len(a)) {
		t.Fatalf("based: %d %v", d.Offset(), err)
	}
}

// A gap (a reader's *Gap) is counted into Offset and reported once; the
// line broken on each side of it is dropped, and the frames after it read
// on. Serve reports it to OnBad and goes on.
func TestDecoderGap(t *testing.T) {
	a := `{"jsonrpc":"2.0","method":"a"}` + "\n"
	head := `{"jsonrpc":"2.0","me`
	tail := `thod":"lost"}` + "\n"
	b := `{"jsonrpc":"2.0","method":"b"}` + "\n"
	d := NewDecoder(&chunks{parts: []any{a + head, &Gap{Lost: 100}, tail + b}})
	if m, err := d.Next(); err != nil || m.Method != "a" || d.Offset() != int64(len(a)) {
		t.Fatalf("before: %v %v", m, err)
	}
	if _, err := d.Next(); !errors.Is(err, ErrGap) || !strings.Contains(err.Error(), "100 bytes") {
		t.Fatalf("the gap: %v", err)
	}
	if got, want := d.Offset(), int64(len(a+head)+100); got != want {
		t.Fatalf("offset after the gap %d, want %d", got, want)
	}
	m, err := d.Next()
	if err != nil || m.Method != "b" {
		t.Fatalf("after: %v %v", m, err)
	}
	if got, want := d.Offset(), int64(len(a+head)+100+len(tail+b)); got != want || m.off != want {
		t.Fatalf("offset %d (frame %d), want %d", got, m.off, want)
	}

	// a gap at a line's end, then one more before the next newline
	d = NewDecoder(&chunks{parts: []any{a, &Gap{Lost: 5}, "xx", &Gap{Lost: 7}, "yy\n" + b}})
	if m, err := d.Next(); err != nil || m.Method != "a" {
		t.Fatal(m, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Next(); !errors.Is(err, ErrGap) {
			t.Fatalf("gap %d: %v", i, err)
		}
	}
	if m, err := d.Next(); err != nil || m.Method != "b" || d.Offset() != int64(len(a)+5+2+7+3+len(b)) {
		t.Fatalf("after two gaps: %v %v %d", m, err, d.Offset())
	}

	var bads []error
	var seen []string
	c := NewConn(&chunks{parts: []any{a + head, &Gap{Lost: 3}, tail + b}}, io.Discard)
	c.OnBad = func(err error) { bads = append(bads, err) }
	c.OnNotify = func(m *Message) { seen = append(seen, m.Method) }
	if err := c.Serve(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "a,b" || len(bads) != 1 || !errors.Is(bads[0], ErrGap) || c.Offset() != int64(len(a+head)+3+len(tail+b)) {
		t.Fatalf("serve: %v %v %d", seen, bads, c.Offset())
	}
}
