package signins

// runner.go — where a guided sign-in's command runs (D230): a throwaway
// confined sandbox (internal/confine) over the base image with nothing of a
// tile's in it — no tile directory, no home, no layer — an empty HOME made
// and removed by the command's own wrapper (acp.MintCommand), internet
// egress only, and a terminal 1000 columns wide, so the page's address and
// the credential the CLI prints are one line each. Nobody can enter that
// sandbox: it has no session, no socket and no name. Without isolation (a
// development workspace) the same wrapper runs as a host process: the CLI
// found on xbind's own PATH, a clean environment, a throwaway HOME.

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// Runner starts a coding agent's credential-printing sign-in (its Mint)
// somewhere nothing of a tile's is, bounded by life.
type Runner interface {
	Start(spec acp.Signin, life time.Duration) (acp.GuidedExec, error)
}

// Confined is the Runner xbind serves with.
type Confined struct {
	// tests: a directory bound read-only into the sandbox and put first on
	// the CLI's lookup path (a scripted CLI). "" in service: the sandbox
	// binds nothing at all.
	testBin string
}

const (
	flowCols = 1000
	flowRows = 50
)

// Start runs spec's command clean on a terminal.
func (c Confined) Start(spec acp.Signin, life time.Duration) (acp.GuidedExec, error) {
	path := sandbox.RootfsPATH // the image's own directories
	if !confine.Isolated() {
		path = os.Getenv("PATH") // no image: the host's CLI
	}
	cmd := confine.Cmd{
		Net:     confine.NetInternet, // the CLI exchanges the code with its vendor
		Timeout: life,
	}
	if c.testBin != "" {
		path = c.testBin + ":" + path
		if confine.Isolated() {
			cmd.Binds = []sandbox.Bind{confine.RO(c.testBin)}
		}
	}
	cmd.Argv = acp.MintCommand(spec, path)
	p, err := confine.StartTTY(cmd, flowCols, flowRows)
	if err != nil {
		return nil, err
	}
	return newProcExec(p), nil
}

// procExec is a confine.Proc as a GuidedExec: a reader goroutine takes the
// terminal's output as it comes, Read hands it on.
type procExec struct {
	p *confine.Proc

	mu    sync.Mutex
	buf   []byte
	eof   bool
	wake  chan struct{}
	close sync.Once
}

func newProcExec(p *confine.Proc) *procExec {
	x := &procExec{p: p, wake: make(chan struct{}, 1)}
	go x.pump()
	return x
}

func (x *procExec) pump() {
	b := make([]byte, 8<<10)
	for {
		n, err := x.p.Read(b)
		x.mu.Lock()
		if n > 0 {
			x.buf = append(x.buf, b[:n]...)
		}
		if err != nil {
			x.eof = true // EIO once the terminal's other side is gone: the end
		}
		x.mu.Unlock()
		for i := range b[:n] {
			b[i] = 0
		}
		select {
		case x.wake <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func (x *procExec) Read(ctx context.Context) ([]byte, bool, *int, error) {
	for {
		x.mu.Lock()
		if len(x.buf) > 0 {
			b := x.buf
			x.buf = nil
			x.mu.Unlock()
			return b, true, nil, nil
		}
		eof := x.eof
		x.mu.Unlock()
		if eof {
			select {
			case <-x.p.Done():
				code := x.p.ExitCode()
				if code < 0 {
					return nil, false, nil, nil
				}
				return nil, false, &code, nil
			case <-ctx.Done():
				return nil, false, nil, nil // ended; its code isn't known yet
			}
		}
		select {
		case <-x.wake:
		case <-x.p.Done():
			// ended: what the terminal still holds is read by the pump first
			select {
			case <-x.wake:
			case <-time.After(200 * time.Millisecond):
				x.mu.Lock()
				x.eof = true
				x.mu.Unlock()
			}
		case <-ctx.Done():
			return nil, true, nil, ctx.Err()
		}
	}
}

func (x *procExec) Write(_ context.Context, p []byte) error {
	select {
	case <-x.p.Done():
		return errors.New("the sign-in has ended")
	default:
	}
	_, err := x.p.Write(p)
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) {
		return errors.New("the sign-in has ended")
	}
	return err
}

// Close kills the run and zeroes what was read of it and not yet taken.
func (x *procExec) Close() error {
	x.close.Do(func() {
		_ = x.p.Close()
		x.mu.Lock()
		for i := range x.buf {
			x.buf[i] = 0
		}
		x.buf, x.eof = nil, true
		x.mu.Unlock()
		select {
		case x.wake <- struct{}{}:
		default:
		}
	})
	return nil
}
