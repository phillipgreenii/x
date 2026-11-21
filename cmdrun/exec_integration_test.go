//go:build integration

package cmdrun_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

func TestExecChildInheritsExtraFileAsFd3(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if _, err := pw.WriteString("hello-fd3"); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	c := cmdrun.Call{Name: "sh", Args: []string{"-c", "cat <&3"}, ExtraFiles: []*os.File{pr}}
	res := cmdrun.Exec(context.Background(), c)
	if !res.OK() || string(res.Stdout) != "hello-fd3" {
		t.Fatalf("res = %+v", res)
	}
	if len(c.ExtraFiles) != 1 || c.ExtraFiles[0] != pr {
		t.Fatalf("Exec modified ExtraFiles: %+v", c.ExtraFiles)
	}
}

func TestExecWithoutExtraFilesFd3IsClosed(t *testing.T) {
	res := cmdrun.Exec(context.Background(), cmdrun.Call{Name: "sh", Args: []string{"-c", "cat <&3"}})
	if res.OK() {
		t.Fatalf("fd 3 unexpectedly open: %+v", res)
	}
}

// syncBuf is a writer the test can read while Exec still runs.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestExecStderrWriterReceivesLiveAndStillCaptures(t *testing.T) {
	w := &syncBuf{}
	done := make(chan cmdrun.Result, 1)
	// The child writes one line, then blocks until the test has seen it in
	// the writer: proof the forwarding is live, not replayed after exit.
	fifo := t.TempDir() + "/go"
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		done <- cmdrun.Exec(context.Background(), cmdrun.Call{
			Name:         "sh",
			Args:         []string{"-c", "echo first >&2; cat " + fifo + "; echo second >&2"},
			StderrWriter: w,
			Timeout:      time.Minute,
		})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(w.String(), "first\n") {
		if time.Now().After(deadline) {
			t.Fatalf("no live stderr, writer has %q", w.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case res := <-done:
		t.Fatalf("child ended before release: %+v", res)
	default:
	}
	if err := os.WriteFile(fifo, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if !res.OK() || string(res.Stderr) != "first\nsecond\n" || w.String() != "first\nsecond\n" {
		t.Fatalf("res = %+v, writer = %q", res, w.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestExecReapGroupAfterCancelKillsSigtermIgnoringDescendant(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	// The leader exits on SIGTERM; its background child ignores it.
	script := `(trap '' TERM; echo $BASHPID > ` + pidFile + `; while :; do sleep 1; done) & wait`
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 400; i++ {
			if b, _ := os.ReadFile(pidFile); len(b) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	res := cmdrun.Exec(ctx, cmdrun.Call{
		Name: "bash", Args: []string{"-c", script},
		OwnProcGroup: true, TermGrace: 300 * time.Millisecond, ReapGroupAfterCancel: true,
	})
	if res.CtxErr == nil {
		t.Fatalf("res = %+v", res)
	}
	b, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %q: %v", b, err)
	}
	// Exec has returned: the descendant must already be gone.
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("descendant %d survived Exec", pid)
	}
}

func TestExecFailingStderrWriterDoesNotFailTheRun(t *testing.T) {
	res := cmdrun.Exec(context.Background(), cmdrun.Call{
		Name: "sh", Args: []string{"-c", "echo a >&2; echo b >&2"}, StderrWriter: failWriter{},
	})
	if !res.OK() || string(res.Stderr) != "a\nb\n" {
		t.Fatalf("res = %+v", res)
	}
}
