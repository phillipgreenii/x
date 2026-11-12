// Package cmdrun is the process-spawn core shared by code that runs an
// external command and by the tests that stand in for it. A Call describes
// one invocation, a Result is everything that came back, and a Runner is
// the seam: production code takes a Runner (defaulting to Exec), and tests
// pass the in-process fake from package cmdruntest instead of re-executing
// the test binary to impersonate a tool (design: docs/adr/0001).
//
// The package uses only the standard library and never imports "testing".
package cmdrun

import (
	"context"
	"time"
)

// EnvMode says how Call.Env combines with the parent environment.
type EnvMode int

const (
	// EnvAppend (the zero value) adds Call.Env to os.Environ(); on a
	// duplicate key the Call.Env entry wins.
	EnvAppend EnvMode = iota
	// EnvExact makes Call.Env the child's whole environment. A nil or empty
	// Env gives the child an empty environment, never the parent's.
	EnvExact
)

// DefaultWaitDelay is how long Exec waits, after the context has killed the
// child, for the child's stdout/stderr pipes to close when Call.WaitDelay is
// zero. It bounds the hang a grandchild that inherited the pipes can cause.
const DefaultWaitDelay = 2 * time.Second

// Call describes one process invocation.
type Call struct {
	// Name is the program, resolved through the parent's PATH when it has
	// no path separator. Args are the arguments after Name.
	Name string
	Args []string
	// Dir is the working directory; empty means the parent's.
	Dir string
	// Stdin is the child's standard input. Nil or empty means the null
	// device (the child reads end-of-file at once).
	Stdin []byte
	// Env and EnvMode: see EnvMode.
	Env     []string
	EnvMode EnvMode
	// OwnProcGroup is opt-in: the child runs in its own process group and a
	// context end kills the whole group, not just the child. Leave it off
	// for a child that reads the terminal (a background group is stopped by
	// SIGTTIN).
	OwnProcGroup bool
	// TermGrace is opt-in graceful cancel. When positive, a context end (a
	// caller cancel or Timeout) sends SIGTERM, to the whole process group
	// when OwnProcGroup is set, and Exec sends SIGKILL (to the same target)
	// only if the child has not exited TermGrace later. Zero keeps the
	// immediate SIGKILL. The pipe-release delay (WaitDelay, or
	// DefaultWaitDelay) starts after the grace, so the longest a cancelled
	// Exec can block is TermGrace plus that delay. A child that handles
	// SIGTERM and exits 0 still gets CtxErr set, since the context ended.
	TermGrace time.Duration
	// WaitDelay overrides DefaultWaitDelay when positive.
	WaitDelay time.Duration
	// Timeout, when positive, bounds the run: Exec derives a child context
	// with this timeout, kills the child when it expires and reports
	// Result.TimedOut. Zero means no bound beyond the caller's context.
	// Every Call in production code SHOULD set it; see cmdruntest.
	// AssertCallsBounded.
	Timeout time.Duration
}

// Result is the outcome of one Call. The fields keep "the child never
// started", "the child ended abnormally" and "the child exited non-zero"
// apart so an adapter can map them as its own callers expect.
type Result struct {
	// Stdout and Stderr are captured separately, never interleaved.
	Stdout []byte
	Stderr []byte
	// Code is the exit status, or -1 when the child never started or was
	// ended by a signal (including a signal sent because the context ended).
	Code int
	// Signal is the signal's name (for example "killed" or "terminated")
	// when a signal ended the child, empty otherwise.
	Signal string
	// CtxErr is ctx.Err() when the context had ended and the run did not
	// finish cleanly (the child was killed, or never started because the
	// context was already done). Nil on a clean run, whatever the context
	// did afterwards.
	CtxErr error
	// TimedOut is true when Call.Timeout expired and ended the child (CtxErr
	// is then context.DeadlineExceeded). It stays false when the caller's own
	// context ended the run first, so a timeout is distinguishable from a
	// caller cancel or a caller-supplied deadline.
	TimedOut bool
	// StartErr is non-nil when the child never started (unknown program, bad
	// Dir, an already-done context). A non-zero exit is NOT a StartErr.
	StartErr error
}

// OK reports a clean run: started, exited 0, not signaled, context intact.
func (r Result) OK() bool {
	return r.StartErr == nil && r.CtxErr == nil && r.Signal == "" && r.Code == 0
}

// Runner runs one Call and reports its Result. Exec is the real
// implementation; cmdruntest.Fake is the in-process double.
type Runner func(ctx context.Context, c Call) Result
