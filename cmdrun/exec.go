package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Exec is the real Runner: it starts c as a child process, waits for it,
// and maps the outcome onto a Result. It never panics on a failing child
// and never returns a Go error; every failure is a Result field.
//
// A context end kills the child (SIGKILL), or the whole process group when
// c.OwnProcGroup is set. When c.TermGrace is positive the end sends SIGTERM
// first (to the group under OwnProcGroup) and SIGKILL only if the child is
// still running TermGrace later. A child that exits cleanly while a grandchild still
// holds its pipes open is released after the wait delay and reported as the
// clean run it was, with whatever output arrived by then.
func Exec(ctx context.Context, c Call) Result {
	parent := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.Env = buildEnv(c)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	waitDelay := DefaultWaitDelay
	if c.WaitDelay > 0 {
		waitDelay = c.WaitDelay
	}
	cmd.WaitDelay = waitDelay
	if c.OwnProcGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	// signal delivers sig to the child, or to its whole process group when
	// OwnProcGroup is set. A vanished target is reported as os.ErrProcessDone.
	signal := func(sig syscall.Signal) error {
		pid := cmd.Process.Pid
		if c.OwnProcGroup {
			pid = -pid
		}
		err := syscall.Kill(pid, sig)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	switch {
	case c.TermGrace > 0:
		// The pipe-release delay only starts counting once the grace has
		// run out, so it is added on top of the grace.
		cmd.WaitDelay = c.TermGrace + waitDelay
		var killTimer *time.Timer
		defer func() {
			if killTimer != nil {
				killTimer.Stop()
			}
		}()
		cmd.Cancel = func() error {
			if err := signal(syscall.SIGTERM); err != nil {
				return err
			}
			killTimer = time.AfterFunc(c.TermGrace, func() { _ = signal(syscall.SIGKILL) })
			return nil
		}
	case c.OwnProcGroup:
		cmd.Cancel = func() error { return signal(syscall.SIGKILL) }
	}

	runErr := cmd.Run()

	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	state := cmd.ProcessState
	if state == nil {
		res.Code = -1
		res.StartErr = runErr
		res.CtxErr = ctx.Err()
		res.TimedOut = timedOut(c, parent, res.CtxErr)
		return res
	}

	res.Code = state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.Signal = ws.Signal().String()
		res.Code = -1
	}
	if runErr != nil {
		res.CtxErr = ctx.Err()
		res.TimedOut = timedOut(c, parent, res.CtxErr)
	}
	return res
}

// timedOut reports whether ctxErr came from c.Timeout rather than from the
// caller's own context.
func timedOut(c Call, parent context.Context, ctxErr error) bool {
	return c.Timeout > 0 && errors.Is(ctxErr, context.DeadlineExceeded) && parent.Err() == nil
}

// buildEnv returns the child environment for c. The result is never nil, so
// EnvExact with no entries really is an empty environment (a nil Cmd.Env
// would inherit the parent's).
func buildEnv(c Call) []string {
	if c.EnvMode == EnvExact {
		return append([]string{}, c.Env...)
	}
	parent := os.Environ()
	env := make([]string, 0, len(parent)+len(c.Env))
	env = append(env, parent...)
	return append(env, c.Env...)
}
