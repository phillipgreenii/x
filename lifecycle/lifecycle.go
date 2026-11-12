// Package lifecycle is the run group of a long-running daemon: one signal
// context, the daemon's loops under an errgroup, and a hard deadline on the
// whole shutdown.
//
// The first SIGINT or SIGTERM cancels the group's context and starts the
// shutdown clock. A second signal is no longer captured, so it takes the
// default action (the process dies). If the loops have not all returned within
// Options.Deadline of the context ending, a watchdog logs the shutdown phase
// and the loops still running, then hard-exits with Options.ExitCode, so a
// goroutine stuck on a lock cannot hold the daemon's state lock (and the next
// start) hostage. A clean finish disarms the watchdog.
//
// The deadline is owned by the deployment, not by the daemon: the caller fills
// Options.Deadline from a flag (see DeadlineFlag) that the nix module also uses
// to derive the launchd ExitTimeOut, so the two cannot drift.
//
// Nothing here sleeps: the only clock use is time.Now and one time.AfterFunc,
// so tests run under testing/synctest with a fake clock.
package lifecycle

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// DefaultDeadline is used when Options.Deadline is zero.
const DefaultDeadline = 8 * time.Second

// DefaultExitCode is used when Options.ExitCode is zero.
const DefaultExitCode = 10

// Event names reported to Options.OnEvent and the log.
const (
	EvSignal   = "shutdown_signal"   // the context ended; the clock started
	EvPhase    = "shutdown_phase"    // SetPhase was called
	EvClean    = "shutdown_complete" // every loop returned in time
	EvOverrun  = "shutdown_overrun"  // the deadline passed; hard exit follows
	EvStarting = "loop_started"
	EvStopped  = "loop_stopped"
)

// Event is one lifecycle step, for logging or metrics.
type Event struct {
	Name    string
	Phase   string        // the current phase when the event happened
	Loop    string        // set for loop events
	Elapsed time.Duration // since the context ended; zero before that
	Err     error         // a loop's result for EvStopped
}

// Flight is a loop that was still running at the deadline.
type Flight struct {
	Name string
	Age  time.Duration
}

// Options configures a Group.
type Options struct {
	// Deadline is how long shutdown may take once the context ends. Zero
	// selects DefaultDeadline.
	Deadline time.Duration
	// ExitCode is the process status of a hard exit. Zero selects
	// DefaultExitCode. Pick a value the supervisor treats as a failure.
	ExitCode int
	// Signals are captured by the first-signal context. Nil selects SIGINT
	// and SIGTERM.
	Signals []os.Signal
	// Log receives the events. It MUST be a raw logger, not one whose handler
	// takes a lock a stuck loop could hold. Nil logs nothing.
	Log *slog.Logger
	// OnEvent, if set, is called for every event, from the goroutine that
	// caused it. The overrun event is delivered before the hard exit.
	OnEvent func(Event)
	// Exit replaces os.Exit (tests only).
	Exit func(code int)
	// Notify replaces signal.NotifyContext (tests only).
	Notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
}

// DeadlineFlag registers a duration flag (for example "shutdown-deadline") on
// fs and returns the pointer to hand to Options.Deadline after Parse.
func DeadlineFlag(fs *flag.FlagSet, name string, def time.Duration) *time.Duration {
	return fs.Duration(name, def, "hard deadline for a graceful shutdown; past it the process exits with a failure status")
}

// Group runs a daemon's loops until the first signal, a loop error or the
// parent context ending, then waits for them under the shutdown deadline.
type Group struct {
	opt  Options
	ctx  context.Context // ends on signal, loop error or parent end
	stop context.CancelFunc

	eg *errgroup.Group

	mu       sync.Mutex
	loops    map[int]loop
	next     int
	phase    string
	phaseAt  time.Time
	endedAt  time.Time
	timer    *time.Timer
	disarmed bool

	fired atomic.Bool
	once  sync.Once // Disarm and expire are mutually exclusive
}

type loop struct {
	name  string
	since time.Time
}

// New returns a Group whose context ends on the first of opt.Signals, a loop
// returning an error, or parent ending. The watchdog is armed from that moment.
// Run MUST be called (it disarms the watchdog and releases the signal
// registration).
func New(parent context.Context, opt Options) *Group {
	if opt.Deadline <= 0 {
		opt.Deadline = DefaultDeadline
	}
	if opt.ExitCode == 0 {
		opt.ExitCode = DefaultExitCode
	}
	if len(opt.Signals) == 0 {
		opt.Signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	if opt.Exit == nil {
		opt.Exit = os.Exit
	}
	notify := opt.Notify
	if notify == nil {
		notify = signal.NotifyContext
	}
	sigCtx, stop := notify(parent, opt.Signals...)
	// Release the registration as soon as the first signal lands, so a second
	// one gets default handling instead of being swallowed by the context.
	context.AfterFunc(sigCtx, stop)

	eg, gctx := errgroup.WithContext(sigCtx)
	g := &Group{opt: opt, ctx: gctx, stop: stop, eg: eg, loops: map[int]loop{}, phase: "running"}
	g.phaseAt = time.Now()
	context.AfterFunc(gctx, g.begin)
	return g
}

// Context ends when shutdown begins.
func (g *Group) Context() context.Context { return g.ctx }

// Go runs fn as a named loop. fn MUST return once its context ends; a non-nil
// result begins shutdown and is returned by Run.
func (g *Group) Go(name string, fn func(ctx context.Context) error) {
	g.mu.Lock()
	id := g.next
	g.next++
	g.loops[id] = loop{name: name, since: time.Now()}
	g.mu.Unlock()
	g.emit(Event{Name: EvStarting, Loop: name})
	g.eg.Go(func() error {
		err := fn(g.ctx)
		g.mu.Lock()
		delete(g.loops, id)
		g.mu.Unlock()
		g.emit(Event{Name: EvStopped, Loop: name, Err: err})
		return err
	})
}

// SetPhase names the step the daemon is in, so an overrun report says where
// shutdown hung (for example "drain_queue", "close_store").
func (g *Group) SetPhase(name string) {
	g.mu.Lock()
	g.phase = name
	g.phaseAt = time.Now()
	g.mu.Unlock()
	g.emit(Event{Name: EvPhase})
}

// Run waits for every loop and returns the first loop error. It disarms the
// watchdog on return; if the deadline already fired the process is exiting.
func (g *Group) Run() error {
	err := g.eg.Wait()
	g.stop()
	g.Disarm()
	return err
}

// Disarm stops the watchdog and reports whether it won the race; false means
// the deadline already fired.
func (g *Group) Disarm() bool {
	won := false
	g.once.Do(func() { won = true })
	if !won {
		return false
	}
	g.mu.Lock()
	g.disarmed = true
	if g.timer != nil {
		g.timer.Stop()
	}
	ended := !g.endedAt.IsZero()
	g.mu.Unlock()
	if ended {
		g.emit(Event{Name: EvClean})
	}
	return true
}

// Fired reports whether the deadline expired.
func (g *Group) Fired() bool { return g.fired.Load() }

// begin runs once when the context ends.
func (g *Group) begin() {
	g.mu.Lock()
	g.endedAt = time.Now()
	if !g.disarmed {
		g.timer = time.AfterFunc(g.opt.Deadline, g.expire)
	}
	g.mu.Unlock()
	g.emit(Event{Name: EvSignal})
}

func (g *Group) snapshot() (phase string, phaseElapsed time.Duration, flights []Flight, elapsed time.Duration) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, l := range g.loops {
		flights = append(flights, Flight{Name: l.name, Age: now.Sub(l.since)})
	}
	sort.SliceStable(flights, func(i, j int) bool {
		if flights[i].Age != flights[j].Age {
			return flights[i].Age > flights[j].Age
		}
		return flights[i].Name < flights[j].Name
	})
	if !g.endedAt.IsZero() {
		elapsed = now.Sub(g.endedAt)
	}
	return g.phase, now.Sub(g.phaseAt), flights, elapsed
}

func (g *Group) expire() {
	g.once.Do(func() {
		g.fired.Store(true)
		phase, phaseElapsed, flights, elapsed := g.snapshot()
		pairs := make([]string, 0, len(flights))
		for _, f := range flights {
			pairs = append(pairs, fmt.Sprintf("%s:%d", f.Name, f.Age.Milliseconds()))
		}
		if g.opt.Log != nil {
			g.opt.Log.Error(
				EvOverrun,
				"deadline_ms", g.opt.Deadline.Milliseconds(),
				"phase", phase,
				"phase_elapsed_ms", phaseElapsed.Milliseconds(),
				"in_flight", strings.Join(pairs, ","),
			)
		}
		if g.opt.OnEvent != nil {
			g.opt.OnEvent(Event{Name: EvOverrun, Phase: phase, Elapsed: elapsed})
		}
		g.opt.Exit(g.opt.ExitCode)
	})
}

func (g *Group) emit(ev Event) {
	phase, _, _, elapsed := g.snapshot()
	ev.Phase = phase
	ev.Elapsed = elapsed
	if g.opt.Log != nil {
		args := []any{"phase", ev.Phase}
		if ev.Loop != "" {
			args = append(args, "loop", ev.Loop)
		}
		if ev.Err != nil {
			args = append(args, "err", ev.Err.Error())
		}
		if ev.Elapsed > 0 {
			args = append(args, "elapsed_ms", ev.Elapsed.Milliseconds())
		}
		g.opt.Log.Info(ev.Name, args...)
	}
	if g.opt.OnEvent != nil {
		g.opt.OnEvent(ev)
	}
}
