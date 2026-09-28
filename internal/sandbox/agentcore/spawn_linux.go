//go:build linux

package agentcore

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Spawner starts the processes the core runs and tells it when they end.
type Spawner interface {
	// Start starts a process; its wait status arrives on the channel.
	Start(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, <-chan unix.WaitStatus, error)
	// Register waits on a process started some other way (the channel
	// fires once, with its status).
	Register(pid int) <-chan unix.WaitStatus
}

// PID1Spawner is the spawner of a process that is its namespace's PID 1 (the
// VM guest's agent, `bx __sbx-agent`): every child and every orphan ends in
// its wait4(-1) loop. Statuses of pids nobody registered are dropped, but
// the last few are remembered so that Register of a pid that has already
// exited still answers. Call it once per process: it owns SIGCHLD.
func PID1Spawner() Spawner {
	p := &pid1{waiting: map[int]chan unix.WaitStatus{}, unclaimed: map[int]unix.WaitStatus{}}
	sigs := make(chan os.Signal, 64)
	signal.Notify(sigs, unix.SIGCHLD) // before the first Start: no exit goes unnoticed
	go p.reap(sigs)
	return p
}

// unclaimedMax bounds the reaped statuses PID1Spawner keeps for Register.
const unclaimedMax = 64

type pid1 struct {
	// mu is held across a Start, so the reaper can't collect a child before
	// its channel is registered.
	mu        sync.Mutex
	waiting   map[int]chan unix.WaitStatus
	unclaimed map[int]unix.WaitStatus
	order     []int // unclaimed, oldest first
}

// Start starts a process. On Linux os.StartProcess is a vfork: this thread
// waits in the kernel until the child execs. Nothing the child's exec needs
// may therefore be served by this process's own goroutines (the VM guest's
// FUSE relay is a process of its own for exactly that reason).
func (p *pid1) Start(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, <-chan unix.WaitStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	proc, err := os.StartProcess(argv0, argv, attr)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan unix.WaitStatus, 1)
	p.waiting[proc.Pid] = ch
	p.forget(proc.Pid) // a stale status of an earlier process with this pid
	return proc, ch, nil
}

func (p *pid1) Register(pid int) <-chan unix.WaitStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan unix.WaitStatus, 1)
	if ws, ok := p.unclaimed[pid]; ok {
		p.forget(pid)
		ch <- ws
		return ch
	}
	p.waiting[pid] = ch
	return ch
}

func (p *pid1) forget(pid int) {
	if _, ok := p.unclaimed[pid]; !ok {
		return
	}
	delete(p.unclaimed, pid)
	for i, q := range p.order {
		if q == pid {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

// reap is the wait loop. SIGCHLD wakes it; a child that exits between the
// drain and the receive leaves its signal queued, so none is missed.
func (p *pid1) reap(sigs <-chan os.Signal) {
	for {
		for {
			var ws unix.WaitStatus
			pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil || pid <= 0 {
				break
			}
			p.mu.Lock()
			if ch := p.waiting[pid]; ch != nil {
				delete(p.waiting, pid)
				ch <- ws
			} else {
				p.unclaimed[pid] = ws
				p.order = append(p.order, pid)
				if len(p.order) > unclaimedMax {
					delete(p.unclaimed, p.order[0])
					p.order = p.order[1:]
				}
			}
			p.mu.Unlock()
		}
		<-sigs
	}
}

// ProcSpawner waits on each process it starts on its own (a process that
// isn't PID 1: tests, tools). Register works only for this process's own
// children; for any other pid the channel closes without a status.
func ProcSpawner() Spawner { return procSpawner{} }

type procSpawner struct{}

func (procSpawner) Start(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, <-chan unix.WaitStatus, error) {
	proc, err := os.StartProcess(argv0, argv, attr)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan unix.WaitStatus, 1)
	go func() {
		st, err := proc.Wait()
		if err != nil {
			close(ch)
			return
		}
		ch <- unix.WaitStatus(st.Sys().(syscall.WaitStatus))
	}()
	return proc, ch, nil
}

func (procSpawner) Register(pid int) <-chan unix.WaitStatus {
	ch := make(chan unix.WaitStatus, 1)
	go func() {
		var ws unix.WaitStatus
		for {
			_, err := unix.Wait4(pid, &ws, 0, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				close(ch)
				return
			}
			ch <- ws
			return
		}
	}()
	return ch
}
