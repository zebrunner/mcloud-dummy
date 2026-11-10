package android

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Proc is a running process of the device: a long-lived command (minicap, an instrumentation, an app...).
type Proc struct {
	PID     int
	PPID    int
	User    string
	Name    string // the NAME column of ps (comm)
	Cmdline string
	Started time.Time

	ctx    context.Context
	cancel context.CancelCauseFunc
}

// Done is closed when the process is killed.
func (p *Proc) Done() <-chan struct{} { return p.ctx.Done() }

// Context of the process, cancelled when it is killed.
func (p *Proc) Context() context.Context { return p.ctx }

// ExitCode of a killed process: 128 + signal.
func (p *Proc) ExitCode() int {
	if sig, ok := context.Cause(p.ctx).(signal); ok {
		return 128 + int(sig)
	}
	return 0
}

type signal int

func (s signal) Error() string { return fmt.Sprintf("signal %d", int(s)) }

// Procs is the process table.
type Procs struct {
	mu    sync.Mutex
	next  int
	procs map[int]*Proc
}

func newProcs() *Procs {
	return &Procs{next: 1000, procs: map[int]*Proc{}}
}

// Start registers a process; it ends when ctx is cancelled or it is killed. Call the returned func when it exits.
func (ps *Procs) Start(ctx context.Context, user, name, cmdline string) (*Proc, func()) {
	ps.mu.Lock()
	ps.next++
	p := &Proc{PID: ps.next, PPID: 1, User: user, Name: name, Cmdline: cmdline, Started: time.Now()}
	p.ctx, p.cancel = context.WithCancelCause(ctx)
	ps.procs[p.PID] = p
	ps.mu.Unlock()
	return p, func() {
		p.cancel(nil)
		ps.mu.Lock()
		delete(ps.procs, p.PID)
		ps.mu.Unlock()
	}
}

// Kill sends a signal: every signal terminates a dummy process.
func (ps *Procs) Kill(pid, sig int) bool {
	ps.mu.Lock()
	p, ok := ps.procs[pid]
	if ok {
		delete(ps.procs, pid)
	}
	ps.mu.Unlock()
	if ok {
		p.cancel(signal(sig))
	}
	return ok
}

// KillByName kills the processes with the name; it returns how many.
func (ps *Procs) KillByName(name string, sig int) int {
	n := 0
	for _, p := range ps.List() {
		if p.Name == name {
			if ps.Kill(p.PID, sig) {
				n++
			}
		}
	}
	return n
}

// KillAll kills every process (reboot).
func (ps *Procs) KillAll() {
	for _, p := range ps.List() {
		ps.Kill(p.PID, 9)
	}
}

// List the processes by pid.
func (ps *Procs) List() []*Proc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]*Proc, 0, len(ps.procs))
	for _, p := range ps.procs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Find returns the processes whose name matches.
func (ps *Procs) Find(name string) []*Proc {
	var out []*Proc
	for _, p := range ps.List() {
		if p.Name == name || p.Cmdline == name {
			out = append(out, p)
		}
	}
	return out
}

var staticProcs = []struct {
	pid  int
	user string
	name string
}{
	{1, "root", "init"},
	{2, "root", "[kthreadd]"},
	{300, "logd", "logd"},
	{400, "root", "zygote64"},
	{450, "system", "system_server"},
	{500, "u0_a50", "com.android.systemui"},
	{520, "radio", "com.android.phone"},
	{600, "u0_a60", "com.android.launcher3"},
	{700, "shell", "adbd"},
}

// PS formats the toybox ps output.
func (ps *Procs) PS(all bool) string {
	var b strings.Builder
	b.WriteString("USER            PID   PPID        VSZ    RSS WCHAN            ADDR S NAME\n")
	line := func(user string, pid, ppid int, name string) {
		fmt.Fprintf(&b, "%-12s %6d %6d %10d %6d %-16s %4d S %s\n", user, pid, ppid, 12345678, 45678, "0", 0, name)
	}
	if all {
		for _, p := range staticProcs {
			line(p.user, p.pid, 0, p.name)
		}
	}
	for _, p := range ps.List() {
		if all || p.User == "shell" {
			line(p.User, p.PID, p.PPID, p.Name)
		}
	}
	return b.String()
}
