//go:build contract

package gitclient

// Contract test 4 (design §6, epic pg2-svfbb): the context contract's
// bounded-kill requirement (pr-pool watchdog's bounded-probe need, pg2-
// yy42). A git invocation that is genuinely still running when its
// context is canceled/deadlined must be killed PROMPTLY -- bounded by
// client.go's defaultWaitDelay, not left to run to completion -- and the
// returned error must satisfy errors.Is(err, context.Canceled) /
// errors.Is(err, context.DeadlineExceeded). client.go's run already
// implements this (bead pg2-svfbb.2); this test proves that mechanism
// rather than building a new one.
//
// The blocking mechanism: `worktree add` runs the post-checkout hook
// SYNCHRONOUSLY as part of the git process. A hook that sleeps makes the
// invocation genuinely, controllably blocked; because the hook is a
// GRANDCHILD process that inherits the git process's stdout/stderr pipes,
// killing the direct git child does not by itself close those pipes --
// exactly the WaitDelay scenario run's own doc comment describes ("a
// killed child that inherited open I/O pipes ... cannot stall the kill").
//
// core.hooksPath is set EXPLICITLY, repo-local, to a directory this test
// owns, rather than relying on the repository's default .git/hooks --
// verified empirically (this bead) that this development machine carries
// a GLOBAL core.hooksPath override (~/.config/git/config), which takes
// precedence over the default .git/hooks location and silently defeated
// an earlier version of this test (the planted default-location hook
// never ran at all, and CreateWorktree returned in milliseconds). Setting
// core.hooksPath repo-locally overrides that global config the same way
// gitfixture itself does, making the test hermetic to whatever hooks
// configuration happens to be ambient on the machine running it.
//
// Two controls make this decisive rather than a happy-path timing
// accident: (1) letting the SAME shape of invocation run to completion
// UNCANCELED, with a short hook sleep, proves the hook is a genuine block
// close to its sleep duration -- not a no-op that would make "it finished
// quickly" a live alternative explanation for the canceled case; (2) the
// canceled/deadlined runs use a hook sleep far longer than the assertion's
// upper bound, so completing inside that bound is only possible if the
// process was actually killed rather than waited out.
//
// Process-group leak check (bead pg2-i0q71 item 5): the hook script itself
// is only client.go's run's GRANDCHILD (git -> sh running the hook), and
// that grandchild backgrounds `sleep` as ITS OWN child -- a great-
// grandchild. Before client.go's Setpgid/negative-pid kill fix, canceling
// the direct git child left both the hook's `sh` and its backgrounded
// `sleep` running, orphaned, for up to the hook's full sleep duration --
// with this test's own t.TempDir() worktree already removed out from
// under them. plantSleepHook now has the script record its backgrounded
// sleep's own PID (via `$!`) to a file, and both kill tests poll that PID
// for a bounded window immediately after the killed call returns,
// proving directly -- not just by eyeballing `ps` during a manual run --
// that no `sleep` process outlives the test.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	contractKillControlSleepSeconds = 2
	contractKillLongSleepSeconds    = 20
	contractKillBound               = 12 * time.Second
	contractKillSleepDeathBound     = 3 * time.Second
)

// setupBlockableRepo creates a repo with one commit and an explicit,
// repo-local core.hooksPath (see the file doc comment for why this must
// not be left at the default), returning the client and the hooks
// directory to plant scripts into.
func setupBlockableRepo(t *testing.T, ctx context.Context) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := Init(ctx, dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if _, err := c.Run(ctx, "commit", "--allow-empty", "-m", "seed"); err != nil {
		t.Fatalf("seeding a commit: %v", err)
	}
	hooksDir := filepath.Join(dir, "test-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", hooksDir, err)
	}
	if _, err := c.Run(ctx, "config", "core.hooksPath", hooksDir); err != nil {
		t.Fatalf("configuring core.hooksPath: %v", err)
	}
	return c, hooksDir
}

// sleepPIDFile is the deterministic path plantSleepHook's script writes
// its backgrounded `sleep` child's PID into (see the file doc comment's
// process-group leak check).
func sleepPIDFile(hooksDir string) string {
	return filepath.Join(hooksDir, "sleep.pid")
}

// shellQuote wraps s in single quotes for safe embedding as one POSIX
// shell word, escaping any literal single quote in s itself.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plantSleepHook writes an executable post-checkout hook at
// <hooksDir>/post-checkout that sleeps for seconds before exiting 0. The
// sleep is backgrounded and waited on (rather than being the script's
// direct tail call) specifically so it gets its OWN pid, distinct from
// the hook script's, which the script records via sleepPIDFile so a test
// can later probe it directly.
func plantSleepHook(t *testing.T, hooksDir string, seconds int) {
	t.Helper()
	path := filepath.Join(hooksDir, "post-checkout")
	script := "#!/bin/sh\n" +
		"sleep " + strconv.Itoa(seconds) + " &\n" +
		"echo $! > " + shellQuote(sleepPIDFile(hooksDir)) + "\n" +
		"wait $!\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing hook %s: %v", path, err)
	}
}

// processAlive reports whether a process with the given pid still exists.
// It sends signal 0 (kill(2)'s documented no-op probe: existence/
// permission is checked, no signal is actually delivered) rather than
// shelling out to `ps` and parsing its output, which differs between
// macOS and Linux CI.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) != syscall.ESRCH
}

// waitUntilDead polls pid until it is no longer alive or timeout elapses,
// absorbing the brief window between SIGKILL delivery and the OS reaping
// the process (during which it may still transiently exist as a zombie)
// rather than treating that race as a hard leak.
func waitUntilDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tryReadSleepPID reads the pid plantSleepHook's script recorded, with a
// short bounded retry: the hook is still a separate process writing this
// file concurrently with this test's own cancellation timer, so a read
// immediately after the killed call returns could otherwise race the
// write. If the file never appears, ok is false -- a legitimate outcome,
// not a leak: it means the whole process group was killed before the
// hook script even reached its `sleep N &` line (plausible under a slow/
// loaded scheduler, e.g. a race/coverage-instrumented run, given the
// deadline test's tight 300ms budget with no warm-up run), so `sleep`
// never started and there is nothing to leak-check.
func tryReadSleepPID(t *testing.T, hooksDir string) (pid int, ok bool) {
	t.Helper()
	path := sleepPIDFile(hooksDir)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("parsing pid from %s (%q): %v", path, data, err)
		}
		return pid, true
	}
	return 0, false
}

// assertHookSleepDidNotOutliveTheTest is the item-5 leak assertion shared
// by both kill tests: if the hook's backgrounded `sleep` ever started
// (tryReadSleepPID found its pid), it must not still be alive shortly
// after the killed invocation returned.
func assertHookSleepDidNotOutliveTheTest(t *testing.T, hooksDir string) {
	t.Helper()
	sleepPID, ok := tryReadSleepPID(t, hooksDir)
	if !ok {
		t.Logf("hook's sleep.pid was never written -- the invocation was killed before the hook could fork `sleep`, so there is nothing to leak-check")
		return
	}
	if !waitUntilDead(sleepPID, contractKillSleepDeathBound) {
		t.Errorf("the hook's backgrounded `sleep %ds` (pid %d) outlived the killed CreateWorktree() call by more than %v -- the process GROUP was not killed, only the direct git child", contractKillLongSleepSeconds, sleepPID, contractKillSleepDeathBound)
	}
}

func TestContextCancelKillsAGenuinelyBlockedInvocationPromptly(t *testing.T) {
	ctx := t.Context()
	c, hooksDir := setupBlockableRepo(t, ctx)

	// Control: a SHORT hook sleep, run to completion with NO cancellation,
	// proves the hook genuinely blocks the invocation for close to its
	// sleep duration.
	plantSleepHook(t, hooksDir, contractKillControlSleepSeconds)
	controlStart := time.Now()
	if err := c.CreateWorktree(ctx, filepath.Join(t.TempDir(), "wt-control"), "control-branch", CreateWorktreeOptions{}); err != nil {
		t.Fatalf("control CreateWorktree() error = %v", err)
	}
	controlElapsed := time.Since(controlStart)
	minExpected := time.Duration(contractKillControlSleepSeconds) * time.Second * 3 / 4
	if controlElapsed < minExpected {
		t.Fatalf("control CreateWorktree() (uncanceled) completed in %v, want at least %v (close to the hook's %ds sleep) -- the hook is not genuinely blocking, so the canceled assertion below would prove nothing", controlElapsed, minExpected, contractKillControlSleepSeconds)
	}

	// Guarantee: a hook sleep far longer than contractKillBound, canceled
	// shortly after starting.
	plantSleepHook(t, hooksDir, contractKillLongSleepSeconds)
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	killStart := time.Now()
	err := c.CreateWorktree(cancelCtx, filepath.Join(t.TempDir(), "wt-kill"), "kill-branch", CreateWorktreeOptions{})
	killElapsed := time.Since(killStart)
	cancel()

	if !errors.Is(err, context.Canceled) {
		t.Errorf("CreateWorktree() under a canceled context: error = %v, want errors.Is(_, context.Canceled)", err)
	}
	if killElapsed >= contractKillBound {
		t.Errorf("CreateWorktree() under cancellation took %v, want it killed within %v (bounded by defaultWaitDelay), not left to run out the hook's %ds sleep", killElapsed, contractKillBound, contractKillLongSleepSeconds)
	}

	assertHookSleepDidNotOutliveTheTest(t, hooksDir)
}

func TestContextDeadlineKillsAGenuinelyBlockedInvocationPromptly(t *testing.T) {
	ctx := t.Context()
	c, hooksDir := setupBlockableRepo(t, ctx)
	plantSleepHook(t, hooksDir, contractKillLongSleepSeconds)

	deadlineCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()

	killStart := time.Now()
	err := c.CreateWorktree(deadlineCtx, filepath.Join(t.TempDir(), "wt-deadline"), "deadline-branch", CreateWorktreeOptions{})
	killElapsed := time.Since(killStart)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CreateWorktree() under an expired deadline: error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
	}
	if killElapsed >= contractKillBound {
		t.Errorf("CreateWorktree() under a deadline took %v, want it killed within %v (bounded by defaultWaitDelay), not left to run out the hook's %ds sleep", killElapsed, contractKillBound, contractKillLongSleepSeconds)
	}

	assertHookSleepDidNotOutliveTheTest(t, hooksDir)
}
