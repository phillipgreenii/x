package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Exec is the real Runner: it starts c as a child process, waits for it,
// and maps the outcome onto a Result. It never panics on a failing child
// and never returns a Go error; every failure is a Result field.
//
// A context end kills the child (SIGKILL), or the whole process group when
// c.OwnProcGroup is set. A child that exits cleanly while a grandchild still
// holds its pipes open is released after the wait delay and reported as the
// clean run it was, with whatever output arrived by then.
func Exec(ctx context.Context, c Call) Result {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.Env = buildEnv(c)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.WaitDelay = DefaultWaitDelay
	if c.WaitDelay > 0 {
		cmd.WaitDelay = c.WaitDelay
	}
	if c.OwnProcGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
	}

	runErr := cmd.Run()

	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	state := cmd.ProcessState
	if state == nil {
		res.Code = -1
		res.StartErr = runErr
		res.CtxErr = ctx.Err()
		return res
	}

	res.Code = state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.Signal = ws.Signal().String()
		res.Code = -1
	}
	if runErr != nil {
		res.CtxErr = ctx.Err()
	}
	return res
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
