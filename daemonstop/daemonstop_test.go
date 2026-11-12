package daemonstop

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manual clock: Advance moves Now and fires due timers.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1000, 0)} }

func (c *fakeClock) Clock() Clock {
	return Clock{
		Now: func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now },
		AfterFunc: func(d time.Duration, f func()) func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			t := &fakeTimer{at: c.now.Add(d), f: f}
			c.timers = append(c.timers, t)
			return func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				was := !t.stopped && !t.fired
				t.stopped = true
				return was
			}
		},
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func (c *fakeClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newLog(b *safeBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// waitFor polls up to 2 s for cond; AfterFunc callbacks run on their own goroutine.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestTrackerEvents(t *testing.T) {
	clk := newFakeClock()
	var buf safeBuf
	tr := NewTracker(newLog(&buf)).WithClock(clk.Clock())

	tr.Begin()
	clk.Advance(5 * time.Millisecond)
	tr.Phase("drain")
	clk.Advance(7 * time.Millisecond)
	tr.Done()

	out := buf.String()
	for _, want := range []string{
		"msg=shutdown_started",
		"msg=shutdown_phase phase=signal duration_ms=5",
		"msg=shutdown_phase phase=drain duration_ms=7",
		"msg=shutdown_done duration_ms=12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTrackerNilAndCurrent(t *testing.T) {
	var nilT *Tracker
	nilT.Begin()
	nilT.Phase("x")
	nilT.Done()
	if n, _ := nilT.Current(); n != "unknown" {
		t.Errorf("nil Current = %q", n)
	}
	tr := NewTracker(nil)
	if n, _ := tr.Current(); n != "not_started" {
		t.Errorf("fresh Current = %q", n)
	}
	tr.Phase("a")
	tr.Begin() // must keep phase "a"
	if n, _ := tr.Current(); n != "a" {
		t.Errorf("Begin reset the phase: %q", n)
	}
}

func TestOverrunWithTracker(t *testing.T) {
	clk := newFakeClock()
	var buf safeBuf
	log := newLog(&buf)
	tr := NewTracker(log).WithClock(clk.Clock())
	exited := make(chan int, 2)
	ctx, cancel := context.WithCancel(context.Background())
	w := Watch(ctx, Options{
		Deadline: 100 * time.Millisecond, Log: log, Tracker: tr, Clock: clk.Clock(),
		Exit: func(c int) { exited <- c },
	})
	cancel()
	waitFor(t, func() bool { return clk.timerCount() == 1 })
	tr.Phase("flush")
	clk.Advance(40 * time.Millisecond)
	clk.Advance(60 * time.Millisecond)
	if c := <-exited; c != 10 {
		t.Fatalf("exit code %d, want 10", c)
	}
	if !w.Fired() {
		t.Error("Fired false")
	}
	if w.Disarm() {
		t.Error("Disarm won after the deadline fired")
	}
	out := buf.String()
	for _, want := range []string{
		"msg=shutdown_started", "msg=shutdown_overrun", "deadline_ms=100",
		"phase=flush", "phase_elapsed_ms=100",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(exited) != 0 {
		t.Error("exit called more than once")
	}
}

func TestOverrunWithHooks(t *testing.T) {
	clk := newFakeClock()
	var buf safeBuf
	var h Hooks
	exited := make(chan int, 2)
	ctx, cancel := context.WithCancel(context.Background())
	w := Watch(ctx, Options{
		Deadline: 50 * time.Millisecond, Log: newLog(&buf), Hooks: &h, Clock: clk.Clock(),
		Exit: func(c int) { exited <- c },
	})
	// Registered after arming: late binding.
	h.SetInFlight(func() []Flight {
		return []Flight{{"young", 10 * time.Millisecond}, {"old", 90 * time.Millisecond}}
	})
	h.SetPhase(func() PhaseInfo {
		return PhaseInfo{Name: "sweep", Elapsed: 30 * time.Millisecond, Attrs: []any{"swept", 3}}
	})
	cancel()
	waitFor(t, func() bool { return clk.timerCount() == 1 })
	clk.Advance(50 * time.Millisecond)
	if c := <-exited; c != 10 {
		t.Fatalf("exit code %d, want 10", c)
	}
	if !w.Fired() {
		t.Error("Fired false")
	}
	out := buf.String()
	for _, want := range []string{
		"msg=shutdown_overrun", "phase=sweep", "phase_elapsed_ms=30",
		"in_flight=old:90,young:10", "swept=3", "deadline_ms=50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHooksBeforeRegistration(t *testing.T) {
	var h Hooks
	if p := h.Phase(); p.Name != "not_started" {
		t.Errorf("phase = %q", p.Name)
	}
	if f := h.InFlight(); f != nil {
		t.Errorf("in flight = %v", f)
	}
}

func TestDisarmBeforeDeadlinePreventsExit(t *testing.T) {
	clk := newFakeClock()
	var buf safeBuf
	tr := NewTracker(newLog(&buf)).WithClock(clk.Clock())
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	w := Watch(ctx, Options{
		Deadline: 100 * time.Millisecond, Log: newLog(&buf), Tracker: tr, Clock: clk.Clock(),
		Exit: func(c int) { exited <- c },
	})
	cancel()
	waitFor(t, func() bool { return clk.timerCount() == 1 })
	tr.Done()
	if !w.Disarm() {
		t.Fatal("Disarm lost the race")
	}
	clk.Advance(time.Second)
	select {
	case c := <-exited:
		t.Fatalf("exit(%d) called after Disarm", c)
	case <-time.After(20 * time.Millisecond):
	}
	if w.Fired() || strings.Contains(buf.String(), "shutdown_overrun") {
		t.Errorf("overrun after Disarm:\n%s", buf.String())
	}
	if w.Disarm() {
		t.Error("second Disarm reported a win")
	}
}

func TestDisarmWithoutContextEnd(t *testing.T) {
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := Watch(ctx, Options{Deadline: time.Millisecond, Exit: func(c int) { exited <- c }})
	if !w.Disarm() {
		t.Fatal("Disarm lost")
	}
	cancel()
	select {
	case c := <-exited:
		t.Fatalf("exit(%d) after Disarm", c)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestOverrunUnknownPhaseNoProviders(t *testing.T) {
	var buf safeBuf
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	Watch(ctx, Options{
		Deadline: time.Millisecond, Log: newLog(&buf),
		Exit: func(c int) { exited <- c },
	})
	cancel()
	if c := <-exited; c != ExitCode {
		t.Fatalf("code %d", c)
	}
	out := buf.String()
	if !strings.Contains(out, "phase=unknown") || strings.Contains(out, "in_flight") {
		t.Errorf("unexpected line:\n%s", out)
	}
}
