package llm

import (
	"bytes"
	"errors"
	"io"
)

// MaxSSELine is the longest stream line ReadSSE accepts: some providers
// send a large tool call's arguments as one data line.
const MaxSSELine = 8 << 20

// ErrSSELineTooLong is ReadSSE's error for a line past MaxSSELine.
var ErrSSELineTooLong = errors.New("llm: stream line too long")

var (
	sseData  = []byte("data:")
	sseEvent = []byte("event:")
	sseDone  = []byte("[DONE]")
)

// ReadSSE reads a server-sent event stream, calling fn with each event's
// name ("" when the stream names none, as Chat Completions does) and data,
// until the "[DONE]" data, the end of the stream, or fn's error (which it
// returns). The data slice is only valid during the call.
func ReadSSE(r io.Reader, fn func(event string, data []byte) error) error {
	var (
		buf   []byte
		name  string
		data  []byte
		has   bool
		eof   bool
		chunk = make([]byte, 32<<10)
	)
	flush := func() (done bool, err error) {
		if !has {
			name = ""
			return false, nil
		}
		d, n := data, name
		data, name, has = data[:0], "", false
		if bytes.Equal(d, sseDone) {
			return true, nil
		}
		if len(d) == 0 {
			return false, nil
		}
		return false, fn(n, d)
	}
	line := func(l []byte) (done bool, err error) {
		l = bytes.TrimRight(l, "\r")
		switch {
		case len(l) == 0: // a blank line ends the event
			return flush()
		case bytes.HasPrefix(l, sseData):
			d := bytes.TrimPrefix(l[len(sseData):], []byte(" "))
			if has {
				data = append(data, '\n')
			}
			data, has = append(data, d...), true
		case bytes.HasPrefix(l, sseEvent):
			name = string(bytes.TrimSpace(l[len(sseEvent):]))
		}
		return false, nil
	}
	for {
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			done, err := line(buf[:i])
			buf = buf[i+1:]
			if done || err != nil {
				return err
			}
		}
		if eof {
			if len(buf) > 0 {
				if done, err := line(buf); done || err != nil {
					return err
				}
			}
			_, err := flush()
			return err
		}
		if len(buf) > MaxSSELine {
			return ErrSSELineTooLong
		}
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err == io.EOF {
			eof = true
		} else if err != nil {
			return err
		}
	}
}

// UsageSink is fed a response's bytes as they pass and answers its usage
// afterwards: StreamUsage for an SSE stream, BodyUsage for a plain body.
type UsageSink interface {
	io.Writer
	Usage() (Usage, bool)
}

// The most a sink holds. A stream line within usageLineMax is decoded as
// JSON; a longer one (a response.completed carrying a long answer) is
// scraped from its last usageTail bytes, as is a body past usageBodyMax.
const (
	usageLineMax = 256 << 10
	usageBodyMax = 1 << 20
	usageTail    = 16 << 10
)

// StreamUsage reads token usage out of an SSE stream of either wire while a
// relay writes the stream's bytes to it: a chat chunk's "usage" (the last
// chunk, with stream_options.include_usage) or a response.completed
// event's "response.usage". The last usage seen wins. It never fails and
// holds at most usageLineMax bytes: Write always accepts everything.
type StreamUsage struct {
	line  []byte
	over  bool // the current line passed usageLineMax: line holds its tail
	usage Usage
	ok    bool
}

// Write takes the next bytes of the stream.
func (s *StreamUsage) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.add(p)
			break
		}
		s.add(p[:i])
		s.end()
		p = p[i+1:]
	}
	return n, nil
}

func (s *StreamUsage) add(p []byte) {
	s.line = append(s.line, p...)
	if len(s.line) > usageLineMax {
		s.over = true
		s.line = append(s.line[:0], s.line[len(s.line)-usageTail:]...)
	}
}

// end closes the line being assembled.
func (s *StreamUsage) end() {
	l := bytes.TrimSpace(s.line)
	switch {
	case s.over:
		if u, ok := UsageFromTail(l); ok {
			s.usage, s.ok = u, true
		}
	case bytes.HasPrefix(l, sseData):
		if u, ok := UsageFromBody(bytes.TrimSpace(l[len(sseData):])); ok {
			s.usage, s.ok = u, true
		}
	}
	s.line, s.over = s.line[:0], false
}

// Usage is the stream's usage once it ended (a last line without its
// newline is read too); ok is false when the stream reported none.
func (s *StreamUsage) Usage() (Usage, bool) {
	if len(s.line) > 0 {
		s.end()
	}
	return s.usage, s.ok
}

// BodyUsage reads token usage out of a non-streaming response body of
// either wire while a relay writes the body to it. A body within
// usageBodyMax is decoded whole; a larger one is scraped from its tail.
type BodyUsage struct {
	buf  []byte
	over bool
}

// Write takes the next bytes of the body.
func (b *BodyUsage) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > usageBodyMax {
		b.over = true
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-usageTail:]...)
	}
	return len(p), nil
}

// Usage is the body's usage; ok is false when it carries none.
func (b *BodyUsage) Usage() (Usage, bool) {
	if b.over {
		return UsageFromTail(b.buf)
	}
	return UsageFromBody(b.buf)
}
