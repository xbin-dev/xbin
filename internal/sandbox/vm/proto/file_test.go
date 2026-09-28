package proto

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestFrames(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	big := bytes.Repeat([]byte("0123456789abcdef"), MaxFrame/16*2+100) // three frames
	if _, err := fw.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("after") // what follows the terminator stays unread
	r := bytes.NewReader(buf.Bytes())
	got, err := io.ReadAll(NewFrameReader(r))
	if err != nil || !bytes.Equal(got, append(big, "tail"...)) {
		t.Fatalf("round trip: %d bytes, %v", len(got), err)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "after" {
		t.Fatalf("read past the terminator: %q", rest)
	}

	// a stream cut anywhere before the terminator is never a clean end
	whole := buf.Bytes()[:buf.Len()-len("after")]
	for _, cut := range []int{0, 2, 4, 10, len(whole) - 4, len(whole) - 1} {
		_, err := io.ReadAll(NewFrameReader(bytes.NewReader(whole[:cut])))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("cut at %d: %v, want io.ErrUnexpectedEOF", cut, err)
		}
	}
	// a frame header past MaxFrame
	if _, err := ReadFrame(bytes.NewReader([]byte{0x7f, 0, 0, 0}), nil); err != ErrFrameTooLarge {
		t.Fatalf("an oversized frame: %v", err)
	}
	if err := WriteFrame(io.Discard, make([]byte, MaxFrame+1)); err != ErrFrameTooLarge {
		t.Fatalf("writing an oversized frame: %v", err)
	}
}

func TestBoundedLines(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	go func() {
		_, _ = b.Write([]byte(`{"op":"ok"}` + "\n" + `{"op":"` + strings.Repeat("x", 100) + "\"}\n"))
	}()
	c := NewConn(a, nil)
	var m Msg
	if err := c.RecvMax(&m, 64); err != nil || m.Op != "ok" {
		t.Fatalf("%+v %v", m, err)
	}
	if err := c.RecvMax(&m, 64); err != ErrLineTooLong {
		t.Fatalf("a long line: %v", err)
	}
	if _, err := b.Write([]byte("x")); err == nil {
		t.Fatal("the connection stayed open after a long line")
	}

	h, _, err := ReadHelloMax(strings.NewReader(`{"kind":"file","file":{"op":"stat","path":"/x"}}`+"\n"), MaxHello)
	if err != nil || h.Kind != "file" || h.File == nil || h.File.Path != "/x" {
		t.Fatalf("%+v %v", h, err)
	}
	if _, _, err := ReadHelloMax(strings.NewReader(`{"kind":"`+strings.Repeat("x", MaxHello)+"\"}\n"), MaxHello); err != ErrLineTooLong {
		t.Fatalf("a long hello: %v", err)
	}
	if _, _, err := ReadHelloMax(strings.NewReader(`{"kind":"ctl"`), MaxHello); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("a cut hello: %v", err)
	}
}
