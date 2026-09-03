package gitclient

// Unit tests for Handle (bead pg2-f1cq7's hard acceptance bar: full test
// coverage, zero goroutine/process leaks, race-free under -race). These
// build bare *exec.Cmd values via newCmd -- the same helper every real
// Handle-returning role method (Fetch, CreateWorktree, Sync, Commit,
// Push, Clone) uses -- and drive them through newHandle directly, so the
// tests exercise the real spawn/process-group/cancel machinery without
// needing a git repository at all. Deliberately UNTAGGED: this bar is a
// hard, always-on gate, not a specially-tagged suite.

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// newTestHandleCmd builds a *exec.Cmd running script via `sh -c`, through
// the same newCmd helper production code uses.
func newTestHandleCmd(t *testing.T, ctx context.Context, script string) *exec.Cmd {
	t.Helper()
	return newCmd(ctx, "sh", t.TempDir(), nil, []string{"-c", script})
}

// TestHandleWaitClassifiesSuccessAndFailureLikeRun covers the acceptance
// bar's "Wait()'s classification unchanged from today's run()": a
// successful invocation's Wait() is nil, and a failing one's Wait()
// produces the exact same *GitError shape classify/classifyRunErr always
// produce for run's buffered spawn.
func TestHandleWaitClassifiesSuccessAndFailureLikeRun(t *testing.T) {
	ctx := t.Context()

	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "exit 0"), []string{"status"})
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Errorf("Wait() on a successful invocation = %v, want nil", err)
	}

	h2, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'boom' >&2; exit 7"), []string{"status"})
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	waitErr := h2.Wait()
	var ge *GitError
	if !errors.As(waitErr, &ge) {
		t.Fatalf("Wait() = %v (%T), want *GitError", waitErr, waitErr)
	}
	if ge.ExitCode != 7 {
		t.Errorf("GitError.ExitCode = %d, want 7", ge.ExitCode)
	}
	if ge.Stderr != "boom" {
		t.Errorf("GitError.Stderr = %q, want %q", ge.Stderr, "boom")
	}
}

// TestHandleAttachStreamBeforeDuringAfterCompletion covers the
// acceptance bar's explicit "AttachStream correctness called before/
// during/after completion (no missed/duplicated bytes)".
func TestHandleAttachStreamBeforeDuringAfterCompletion(t *testing.T) {
	ctx := t.Context()

	t.Run("before any output", func(t *testing.T) {
		h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'hello'"), nil)
		if err != nil {
			t.Fatalf("newHandle() error = %v", err)
		}
		var buf bytes.Buffer
		h.AttachStream(&buf, nil)
		if err := h.Wait(); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if got := buf.String(); got != "hello" {
			t.Errorf("attached stdout = %q, want %q", got, "hello")
		}
	})

	t.Run("mid-stream", func(t *testing.T) {
		h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'part1'; sleep 1; printf 'part2'"), nil)
		if err != nil {
			t.Fatalf("newHandle() error = %v", err)
		}
		time.Sleep(300 * time.Millisecond) // let part1 land before attaching
		var buf bytes.Buffer
		h.AttachStream(&buf, nil)
		if err := h.Wait(); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if got := buf.String(); got != "part1part2" {
			t.Errorf("attached stdout = %q, want %q (no missed/duplicated bytes)", got, "part1part2")
		}
	})

	t.Run("after completion", func(t *testing.T) {
		h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'done'"), nil)
		if err != nil {
			t.Fatalf("newHandle() error = %v", err)
		}
		if err := h.Wait(); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		var buf bytes.Buffer
		h.AttachStream(&buf, nil)
		if got := buf.String(); got != "done" {
			t.Errorf("attached stdout after completion = %q, want %q", got, "done")
		}
	})
}

// TestHandleKeepsStdoutAndStderrSeparate proves stdout and stderr are
// buffered/forwarded through independent streamBufs, never mixed.
func TestHandleKeepsStdoutAndStderrSeparate(t *testing.T) {
	ctx := t.Context()
	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'out'; printf 'err' >&2"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	var out, errBuf bytes.Buffer
	h.AttachStream(&out, &errBuf)
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if out.String() != "out" {
		t.Errorf("stdout = %q, want %q", out.String(), "out")
	}
	if errBuf.String() != "err" {
		t.Errorf("stderr = %q, want %q", errBuf.String(), "err")
	}
}

// TestHandleAttachStreamNilArgumentLeavesOtherStreamUnregistered covers
// AttachStream's documented per-argument nil handling: AttachStream(w,
// nil) must not disturb stderr's own (separately attachable) sink.
func TestHandleAttachStreamNilArgumentLeavesOtherStreamUnregistered(t *testing.T) {
	ctx := t.Context()
	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'out'; printf 'err' >&2"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	var out bytes.Buffer
	h.AttachStream(&out, nil)
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if out.String() != "out" {
		t.Errorf("stdout = %q, want %q", out.String(), "out")
	}

	var errBuf bytes.Buffer
	h.AttachStream(nil, &errBuf)
	if errBuf.String() != "err" {
		t.Errorf("stderr (attached separately, after completion) = %q, want %q", errBuf.String(), "err")
	}
}

// TestHandleSecondAttachStreamReplacesTheLiveSink documents and proves
// the "last-attacher-wins" hand-off semantics on Handle's own doc
// comment: a later AttachStream call replaces the earlier sink rather
// than fanning out to both.
func TestHandleSecondAttachStreamReplacesTheLiveSink(t *testing.T) {
	ctx := t.Context()
	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'part1'; sleep 1; printf 'part2'"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	var first bytes.Buffer
	h.AttachStream(&first, nil)
	time.Sleep(300 * time.Millisecond)

	var second bytes.Buffer
	h.AttachStream(&second, nil) // hands off from first to second

	if err := h.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if second.String() != "part1part2" {
		t.Errorf("second sink = %q, want %q (full replay at attach time, plus everything live afterward)", second.String(), "part1part2")
	}
	if first.String() != "part1" {
		t.Errorf("first sink = %q, want %q (only what arrived before the hand-off)", first.String(), "part1")
	}
}

// TestHandleConcurrentWaitAndAttachStreamAreRaceFree covers the
// acceptance bar's "concurrent Wait()/AttachStream() calls from multiple
// goroutines" -- correctness is secondary here; the point is `go test
// -race` finding nothing.
func TestHandleConcurrentWaitAndAttachStreamAreRaceFree(t *testing.T) {
	ctx := t.Context()
	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "for i in 1 2 3 4 5; do printf 'x'; done"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = h.Wait()
		}(i)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			h.AttachStream(&buf, &buf)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Wait() call %d = %v, want nil", i, err)
		}
	}
}

// TestHandleNeverConsumedStillReapsProcessAndGoroutine covers the
// acceptance bar's "never-consumed Handle still reaps the process (no
// zombie, no leaked goroutine)": Wait and AttachStream are deliberately
// never called here.
func TestHandleNeverConsumedStillReapsProcessAndGoroutine(t *testing.T) {
	ctx := t.Context()
	before := runtime.NumGoroutine()

	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "exit 0"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	pid := h.cmd.Process.Pid

	deadline := time.Now().Add(3 * time.Second)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("process %d still alive after 3s despite never being Wait()ed on -- the internal goroutine must reap it regardless", pid)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	deadline = time.Now().Add(3 * time.Second)
	for {
		// +2 slack for scheduler/runtime/test-framework noise: the point
		// is the completion goroutine does not accumulate, not that the
		// count is pinned exactly.
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count did not return near baseline (before=%d, now=%d) -- the completion goroutine may have leaked", before, runtime.NumGoroutine())
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHandleContextCancellationMidStreamKillsPromptlyAndClassifiesAsCanceled
// covers the acceptance bar's "context cancellation/timeout while a
// stream is attached mid-flight": a live sink is attached, the process is
// genuinely still running (a long sleep), and cancellation must both kill
// it promptly (newCmd's process-group mechanism) and classify the result
// as context.Canceled, without corrupting whatever the attached sink
// already received.
func TestHandleContextCancellationMidStreamKillsPromptlyAndClassifiesAsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	h, err := newHandle(ctx, newTestHandleCmd(t, ctx, "printf 'start'; sleep 5; printf 'never'"), nil)
	if err != nil {
		t.Fatalf("newHandle() error = %v", err)
	}
	var buf bytes.Buffer
	h.AttachStream(&buf, nil)

	time.Sleep(300 * time.Millisecond) // let "start" land before cancellation
	cancelStart := time.Now()
	cancel()

	err = h.Wait()
	elapsed := time.Since(cancelStart)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() after cancellation = %v, want errors.Is(_, context.Canceled)", err)
	}
	if elapsed >= 4*time.Second {
		t.Errorf("Wait() took %v after cancellation, want it killed promptly, not left to run out the 5s sleep", elapsed)
	}
	if buf.String() != "start" {
		t.Errorf("attached stdout after cancellation = %q, want exactly %q (the bytes written before the kill, no corruption)", buf.String(), "start")
	}
}

// TestNewHandleReturnsSpawnErrorWithNilHandle covers newHandle's own
// cmd.Start() failure path: the process never ran, so there is nothing to
// wait on, and the caller gets (nil, err) -- the same shape run's own
// spawn-error path returns.
func TestNewHandleReturnsSpawnErrorWithNilHandle(t *testing.T) {
	ctx := t.Context()
	cmd := newCmd(ctx, t.TempDir(), t.TempDir(), nil, []string{"status"}) // a directory, not an executable
	h, err := newHandle(ctx, cmd, []string{"status"})
	if err == nil {
		t.Fatal("newHandle() error = nil, want a spawn error (the \"binary\" is a directory)")
	}
	if h != nil {
		t.Errorf("newHandle() Handle = %v, want nil alongside a spawn error", h)
	}
}

// TestNewFakeHandleWaitReturnsGivenErrImmediately covers NewFakeHandle's
// role as a fake Fetcher/WorktreeManager/etc.'s test double: Wait must
// return the given err immediately (nil or otherwise), and repeatedly.
func TestNewFakeHandleWaitReturnsGivenErrImmediately(t *testing.T) {
	wantErr := errors.New("boom")
	h := NewFakeHandle(nil, nil, wantErr)
	if err := h.Wait(); err != wantErr {
		t.Errorf("Wait() = %v, want %v", err, wantErr)
	}
	if err := h.Wait(); err != wantErr {
		t.Errorf("second Wait() = %v, want %v", err, wantErr)
	}

	okHandle := NewFakeHandle(nil, nil, nil)
	if err := okHandle.Wait(); err != nil {
		t.Errorf("Wait() on a nil-err fake = %v, want nil", err)
	}
}

// TestNewFakeHandleAttachStreamReplaysGivenBytes covers NewFakeHandle's
// stdout/stderr replay, exactly like a real completed invocation's would.
func TestNewFakeHandleAttachStreamReplaysGivenBytes(t *testing.T) {
	h := NewFakeHandle([]byte("out bytes"), []byte("err bytes"), nil)
	var out, errBuf bytes.Buffer
	h.AttachStream(&out, &errBuf)
	if out.String() != "out bytes" {
		t.Errorf("stdout replay = %q, want %q", out.String(), "out bytes")
	}
	if errBuf.String() != "err bytes" {
		t.Errorf("stderr replay = %q, want %q", errBuf.String(), "err bytes")
	}
}
