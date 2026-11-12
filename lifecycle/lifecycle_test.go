package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// fakeSignals stands in for signal.NotifyContext: trigger plays the first
// signal, stops counts how often the registration was released.
type fakeSignals struct {
	cancel context.CancelFunc
	stops  atomic.Int32
}

func (f *fakeSignals) notify(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	f.cancel = cancel
	return ctx, func() { f.stops.Add(1); cancel() }
}

func (f *fakeSignals) trigger() { f.cancel() }

type recorder struct {
	mu     sync.Mutex
	events []Event
	exits  []int
}

func (r *recorder) onEvent(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) exit(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exits = append(r.exits, code)
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Name)
	}
	return out
}

func (r *recorder) exitCodes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.exits...)
}

func newGroup(t *testing.T, fs *fakeSignals, r *recorder, log *slog.Logger) *Group {
	t.Helper()
	return New(context.Background(), Options{
		Deadline: 8 * time.Second,
		ExitCode: 42,
		Log:      log,
		OnEvent:  r.onEvent,
		Exit:     r.exit,
		Notify:   fs.notify,
	})
}

func TestFirstSignalCancelsContextAndReleasesRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs, r := &fakeSignals{}, &recorder{}
		g := newGroup(t, fs, r, nil)
		g.Go("loop", func(ctx context.Context) error { <-ctx.Done(); return nil })
		synctest.Wait()
		if g.Context().Err() != nil {
			t.Fatal("context ended before any signal")
		}
		if n := fs.stops.Load(); n != 0 {
			t.Fatalf("registration released before the first signal: %d", n)
		}
		fs.trigger()
		synctest.Wait()
		if g.Context().Err() == nil {
			t.Fatal("first signal did not cancel the context")
		}
		// stop is what makes a second signal take its default action.
		if n := fs.stops.Load(); n < 1 {
			t.Fatalf("registration not released after the first signal: %d", n)
		}
		if err := g.Run(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestRealSignalIsCapturedOnce sends one captured signal to this very process
// (never to a parent or another process) through the real signal.NotifyContext.
func TestRealSignalIsCapturedOnce(t *testing.T) {
	r := &recorder{}
	g := New(context.Background(), Options{
		Signals: []os.Signal{syscall.SIGUSR1},
		OnEvent: r.onEvent,
		Exit:    r.exit,
	})
	g.Go("loop", func(ctx context.Context) error { <-ctx.Done(); return nil })
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.Context().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("signal did not end the context")
	}
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}
	if len(r.exitCodes()) != 0 {
		t.Fatalf("unexpected hard exit: %v", r.exitCodes())
	}
}

func TestWatchdogHardExitsNamingPhaseAndInFlightLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs, r := &fakeSignals{}, &recorder{}
		var logBuf bytes.Buffer
		var logMu sync.Mutex
		log := slog.New(slog.NewJSONHandler(lockedWriter{&logMu, &logBuf}, nil))
		g := newGroup(t, fs, r, log)

		release := make(chan struct{})
		g.Go("stuck", func(context.Context) error { <-release; return nil })
		g.Go("polite", func(ctx context.Context) error { <-ctx.Done(); return nil })
		synctest.Wait()
		time.Sleep(2 * time.Second) // so "stuck" has an age
		g.SetPhase("close_store")

		var runErr error
		done := make(chan struct{})
		go func() { runErr = g.Run(); close(done) }()
		fs.trigger()

		time.Sleep(8*time.Second - time.Millisecond)
		synctest.Wait()
		if got := r.exitCodes(); len(got) != 0 {
			t.Fatalf("exited %v before the deadline", got)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if got := r.exitCodes(); len(got) != 1 || got[0] != 42 {
			t.Fatalf("exit calls = %v, want [42]", got)
		}
		if !g.Fired() {
			t.Fatal("Fired() = false")
		}
		logMu.Lock()
		out := logBuf.String()
		logMu.Unlock()
		for _, want := range []string{`"msg":"shutdown_overrun"`, `"phase":"close_store"`, `"in_flight":"stuck:`, `"deadline_ms":8000`} {
			if !bytes.Contains([]byte(out), []byte(want)) {
				t.Errorf("log missing %s:\n%s", want, out)
			}
		}
		if bytes.Contains([]byte(out), []byte("polite:")) {
			t.Errorf("returned loop reported in flight:\n%s", out)
		}
		close(release)
		<-done
		if runErr != nil {
			t.Fatal(runErr)
		}
		if g.Disarm() {
			t.Fatal("Disarm won after the deadline fired")
		}
	})
}

func TestCleanFinishDisarmsWatchdog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs, r := &fakeSignals{}, &recorder{}
		g := newGroup(t, fs, r, nil)
		g.Go("loop", func(ctx context.Context) error {
			<-ctx.Done()
			time.Sleep(3 * time.Second) // a slow but in-time shutdown
			return nil
		})
		synctest.Wait()
		fs.trigger()
		if err := g.Run(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute) // well past the deadline
		synctest.Wait()
		if got := r.exitCodes(); len(got) != 0 {
			t.Fatalf("hard exit after a clean finish: %v", got)
		}
		if g.Fired() {
			t.Fatal("Fired() after a clean finish")
		}
		names := r.names()
		if len(names) == 0 || names[len(names)-1] != EvClean {
			t.Fatalf("events = %v, want last %s", names, EvClean)
		}
	})
}

func TestLoopErrorBeginsShutdownAndIsReturned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs, r := &fakeSignals{}, &recorder{}
		g := newGroup(t, fs, r, nil)
		boom := errors.New("boom")
		g.Go("bad", func(context.Context) error { return boom })
		g.Go("other", func(ctx context.Context) error { <-ctx.Done(); return nil })
		if err := g.Run(); !errors.Is(err, boom) {
			t.Fatalf("Run = %v, want boom", err)
		}
		if len(r.exitCodes()) != 0 {
			t.Fatal("hard exit on a prompt shutdown")
		}
	})
}

func TestLoopErrorThenStuckLoopOverruns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs, r := &fakeSignals{}, &recorder{}
		g := newGroup(t, fs, r, nil)
		release := make(chan struct{})
		g.Go("bad", func(context.Context) error { return errors.New("boom") })
		g.Go("stuck", func(context.Context) error { <-release; return nil })
		go func() { _ = g.Run() }()
		time.Sleep(8 * time.Second)
		synctest.Wait()
		if got := r.exitCodes(); len(got) != 1 {
			t.Fatalf("exit calls = %v, want one", got)
		}
		close(release)
	})
}

func TestDefaults(t *testing.T) {
	fs, r := &fakeSignals{}, &recorder{}
	g := New(context.Background(), Options{Notify: fs.notify, Exit: r.exit})
	if g.opt.Deadline != DefaultDeadline || g.opt.ExitCode != DefaultExitCode {
		t.Fatalf("defaults = %v %d", g.opt.Deadline, g.opt.ExitCode)
	}
	fs.trigger()
	if err := g.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestDeadlineFlag(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	d := DeadlineFlag(fs, "shutdown-deadline", 8*time.Second)
	if err := fs.Parse([]string{"--shutdown-deadline=12s"}); err != nil {
		t.Fatal(err)
	}
	if *d != 12*time.Second {
		t.Fatalf("flag = %v", *d)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
