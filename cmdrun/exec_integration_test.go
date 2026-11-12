//go:build integration

package cmdrun_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

func TestExecDoesNotMutateTheCall(t *testing.T) {
	c := cmdrun.Call{
		Name:  "sh",
		Args:  []string{"-c", "cat"},
		Stdin: []byte("in"),
		Env:   []string{"A=1"},
	}
	res := cmdrun.Exec(context.Background(), c)
	if !res.OK() || string(res.Stdout) != "in" {
		t.Fatalf("res = %+v", res)
	}
	if string(c.Stdin) != "in" || len(c.Env) != 1 || c.Env[0] != "A=1" || len(c.Args) != 2 {
		t.Fatalf("Exec modified its Call: %+v", c)
	}
}

func TestExecTimeoutEndsSleepingChildAndReportsIt(t *testing.T) {
	const timeout = 50 * time.Millisecond
	start := time.Now()
	res := cmdrun.Exec(context.Background(), cmdrun.Call{
		Name:         "sh",
		Args:         []string{"-c", "sleep 30"},
		OwnProcGroup: true,
		Timeout:      timeout,
	})
	elapsed := time.Since(start)
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, res = %+v", res)
	}
	if !errors.Is(res.CtxErr, context.DeadlineExceeded) || res.OK() {
		t.Fatalf("CtxErr = %v, OK = %v", res.CtxErr, res.OK())
	}
	if elapsed < timeout || elapsed > 5*time.Second {
		t.Fatalf("ended after %v, want about %v", elapsed, timeout)
	}
}

func TestExecCallerCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := cmdrun.Exec(ctx, cmdrun.Call{Name: "sh", Args: []string{"-c", "sleep 30"}, Timeout: time.Minute})
	if res.TimedOut || res.CtxErr == nil {
		t.Fatalf("res = %+v, want caller cancel and not TimedOut", res)
	}
}

func TestExecFastChildWithTimeoutIsClean(t *testing.T) {
	res := cmdrun.Exec(context.Background(), cmdrun.Call{Name: "sh", Args: []string{"-c", "true"}, Timeout: time.Minute})
	if !res.OK() || res.TimedOut {
		t.Fatalf("res = %+v", res)
	}
}
