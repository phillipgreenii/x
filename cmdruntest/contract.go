package cmdruntest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// RunContract exercises r against the real POSIX sh: exit codes, the
// stdout/stderr split, stdin, both env modes, the working directory, a
// start error, signal death, context cancel and the opt-in process-group
// kill. It is the one place the thin real wrapper is tested; an adapter
// that wraps cmdrun.Exec calls it from its own tests so its mapping cannot
// drift from the contract.
//
// It spawns subprocesses, so it belongs in an integration-tagged test, never
// in the untagged unit tier. It uses t.Setenv, so it cannot run in a
// parallel test.
func RunContract(t *testing.T, r cmdrun.Runner) {
	t.Helper()
	sh := func(script string) cmdrun.Call {
		return cmdrun.Call{Name: "sh", Args: []string{"-c", script}}
	}

	t.Run("StdoutAndStderrAreSeparate", func(t *testing.T) {
		res := r(t.Context(), sh("echo out; echo err >&2"))
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "out\n")
		eq(t, "stderr", string(res.Stderr), "err\n")
	})

	t.Run("LargeOutputOnBothStreamsDoesNotDeadlock", func(t *testing.T) {
		res := r(t.Context(), sh("yes x | head -n 100000; yes y | head -n 100000 >&2"))
		requireClean(t, res)
		eq(t, "stdout bytes", strconv.Itoa(len(res.Stdout)), "200000")
		eq(t, "stderr bytes", strconv.Itoa(len(res.Stderr)), "200000")
	})

	t.Run("NonZeroExitIsDataNotAnError", func(t *testing.T) {
		res := r(t.Context(), sh("echo partial; exit 3"))
		eq(t, "code", strconv.Itoa(res.Code), "3")
		eq(t, "stdout", string(res.Stdout), "partial\n")
		if res.StartErr != nil || res.CtxErr != nil || res.Signal != "" {
			t.Fatalf("exit 3 must set only Code, got %+v", res)
		}
		if res.OK() {
			t.Fatal("OK() true for a non-zero exit")
		}
	})

	t.Run("StdinIsDelivered", func(t *testing.T) {
		c := sh("cat")
		c.Stdin = []byte("hello stdin")
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "hello stdin")
	})

	t.Run("NoStdinReadsEndOfFile", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		res := r(ctx, sh("cat"))
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "")
	})

	t.Run("EnvAppendAddsToTheParentEnvironment", func(t *testing.T) {
		t.Setenv("CMDRUN_AMBIENT", "parent")
		c := sh(`echo "$CMDRUN_AMBIENT/$CMDRUN_X"`)
		c.Env = []string{"CMDRUN_X=added"}
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "parent/added\n")
	})

	t.Run("EnvAppendLastDuplicateWins", func(t *testing.T) {
		t.Setenv("CMDRUN_AMBIENT", "parent")
		c := sh(`echo "$CMDRUN_AMBIENT"`)
		c.Env = []string{"CMDRUN_AMBIENT=child"}
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "child\n")
	})

	t.Run("EnvExactReplacesTheParentEnvironment", func(t *testing.T) {
		t.Setenv("CMDRUN_AMBIENT", "parent")
		c := sh(`echo "${CMDRUN_AMBIENT-unset}/$CMDRUN_X"`)
		c.Env = []string{"CMDRUN_X=only"}
		c.EnvMode = cmdrun.EnvExact
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "unset/only\n")
	})

	t.Run("EnvExactWithNoEntriesIsAnEmptyEnvironment", func(t *testing.T) {
		t.Setenv("CMDRUN_AMBIENT", "parent")
		c := sh(`echo "${CMDRUN_AMBIENT-unset}"`)
		c.EnvMode = cmdrun.EnvExact
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), "unset\n")
	})

	t.Run("DirSetsTheWorkingDirectory", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		c := sh("pwd -P")
		c.Dir = dir
		res := r(t.Context(), c)
		requireClean(t, res)
		eq(t, "stdout", string(res.Stdout), dir+"\n")
	})

	t.Run("UnknownProgramIsAStartError", func(t *testing.T) {
		res := r(t.Context(), cmdrun.Call{Name: "cmdrun-no-such-program-xyz"})
		if res.StartErr == nil {
			t.Fatalf("StartErr nil for an unknown program: %+v", res)
		}
		eq(t, "code", strconv.Itoa(res.Code), "-1")
		if res.CtxErr != nil || res.Signal != "" {
			t.Fatalf("start error must set only StartErr and Code, got %+v", res)
		}
		if res.OK() {
			t.Fatal("OK() true for a start error")
		}
	})

	t.Run("MissingDirIsAStartError", func(t *testing.T) {
		c := sh("true")
		c.Dir = filepath.Join(t.TempDir(), "does-not-exist")
		res := r(t.Context(), c)
		if res.StartErr == nil {
			t.Fatalf("StartErr nil for a missing Dir: %+v", res)
		}
		eq(t, "code", strconv.Itoa(res.Code), "-1")
	})

	t.Run("SignalDeathNamesTheSignal", func(t *testing.T) {
		res := r(t.Context(), sh("kill -TERM $$"))
		eq(t, "signal", res.Signal, "terminated")
		eq(t, "code", strconv.Itoa(res.Code), "-1")
		if res.StartErr != nil || res.CtxErr != nil {
			t.Fatalf("a self-inflicted signal sets only Signal and Code, got %+v", res)
		}
	})

	t.Run("DeadlineKillsTheChildAndReportsTheContext", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		res := r(ctx, sh("exec sleep 30"))
		requirePromptly(t, start, 10*time.Second)
		if !errors.Is(res.CtxErr, context.DeadlineExceeded) {
			t.Fatalf("CtxErr = %v, want DeadlineExceeded (%+v)", res.CtxErr, res)
		}
		eq(t, "signal", res.Signal, "killed")
		eq(t, "code", strconv.Itoa(res.Code), "-1")
		if res.StartErr != nil {
			t.Fatalf("StartErr = %v for a started child", res.StartErr)
		}
	})

	t.Run("CancelKillsTheChildAndReportsCanceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(150*time.Millisecond, cancel)
		start := time.Now()
		res := r(ctx, sh("exec sleep 30"))
		requirePromptly(t, start, 10*time.Second)
		if !errors.Is(res.CtxErr, context.Canceled) {
			t.Fatalf("CtxErr = %v, want Canceled (%+v)", res.CtxErr, res)
		}
		eq(t, "signal", res.Signal, "killed")
	})

	t.Run("AlreadyDoneContextNeverRunsTheChild", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "ran")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		res := r(ctx, sh("touch "+marker))
		if !errors.Is(res.CtxErr, context.Canceled) {
			t.Fatalf("CtxErr = %v, want Canceled (%+v)", res.CtxErr, res)
		}
		eq(t, "code", strconv.Itoa(res.Code), "-1")
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the child ran although the context was already done")
		}
	})

	t.Run("CleanRunIgnoresALaterCancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		res := r(ctx, sh("echo done"))
		cancel()
		requireClean(t, res)
	})

	t.Run("WaitDelayBoundsAGrandchildHoldingThePipes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		c := sh("sleep 3 & wait")
		c.WaitDelay = 300 * time.Millisecond
		start := time.Now()
		res := r(ctx, c)
		requirePromptly(t, start, 2500*time.Millisecond)
		if res.CtxErr == nil {
			t.Fatalf("CtxErr nil after a deadline kill: %+v", res)
		}
	})

	t.Run("OwnProcGroupKillsTheGrandchildToo", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		c := sh("sleep 30 & echo $! > " + pidFile + "; wait")
		c.OwnProcGroup = true
		c.WaitDelay = 20 * time.Second // only a group kill can finish sooner
		start := time.Now()
		res := r(ctx, c)
		requirePromptly(t, start, 10*time.Second)
		if res.CtxErr == nil {
			t.Fatalf("CtxErr nil after a deadline kill: %+v", res)
		}
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("grandchild pid file: %v", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("grandchild pid %q: %v", raw, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("grandchild %d survived the group kill", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("ConcurrentCallsDoNotShareState", func(t *testing.T) {
		const n = 8
		out := make([]string, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := r(t.Context(), sh("echo "+strconv.Itoa(i)))
				out[i] = string(res.Stdout)
			}()
		}
		wg.Wait()
		for i, got := range out {
			eq(t, "call "+strconv.Itoa(i), got, strconv.Itoa(i)+"\n")
		}
	})
}

func requireClean(t *testing.T, res cmdrun.Result) {
	t.Helper()
	if !res.OK() {
		t.Fatalf("want a clean run, got %+v", res)
	}
}

func requirePromptly(t *testing.T, start time.Time, limit time.Duration) {
	t.Helper()
	if d := time.Since(start); d > limit {
		t.Fatalf("took %v, want under %v", d, limit)
	}
}

func eq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}
