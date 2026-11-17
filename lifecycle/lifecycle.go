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
// A daemon that already owns its signal handling (it cancels its own context
// on SIGINT and SIGTERM) can still use the shared watchdog: build the Group
// with Attach instead of New. The Group then registers no signal handler and
// treats the end of the parent context as the start of shutdown.
//
// The in-flight and phase reports of the overrun line can be supplied by the
// daemon: see Options.InFlight, Options.PhaseReport and the late-bound
// Group.SetInFlight and Group.SetPhaseReport, for a daemon that builds its
// providers only after the watchdog is armed.
//
// Nothing here sleeps: the only clock use is time.Now, one time.AfterFunc and
// one bounded wait on a provider, so tests run under testing/synctest with a
// fake clock.
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

// ProviderTimeout bounds how long the watchdog waits for an InFlight or
// PhaseReport provider before it exits anyway, so a provider that takes a lock
// a stuck loop holds cannot defeat the hard exit.
const ProviderTimeout = 500 * time.Millisecond

// Event names reported to Options.OnEvent and the log.
const (
	EvSignal   = "shutdown_signal"   // the context ended; the clock started
	EvPhase    = "shutdown_phase"    // SetPhase was called
	EvClean    = "shutdown_complete" // every loop returned in time
	EvOverrun  = "shutdown_overrun"  // the deadline passed; hard exit follows
	EvStarting = "loop_started"
	EvStopped  = "loop_stopped"
)

// PhaseTiming is how long shutdown spent in one phase.
type PhaseTiming struct {
	Name     string
	Duration time.Duration
}

// Event is one lifecycle step, for logging or metrics.
type Event struct {
	Name    string
	Phase   string        // the current phase when the event happened
	Loop    string        // set for loop events
	Elapsed time.Duration // since the context ended; zero before that
	Err     error         // a loop's result for EvStopped
	// Attrs are the provider attributes of the overrun event (EvOverrun only).
	Attrs []slog.Attr
	// Phases and Total are set on EvClean when Options.PhaseDurations is true:
	// the time shutdown spent in each phase, in order, and the total since the
	// context ended.
	Phases []PhaseTiming
	Total  time.Duration
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
	// and SIGTERM. Ignored by Attach.
	Signals []os.Signal
	// Log receives the events. It MUST be a raw logger, not one whose handler
	// takes a lock a stuck loop could hold. Nil logs nothing.
	Log *slog.Logger
	// OnEvent, if set, is called for every event, from the goroutine that
	// caused it. The overrun event is delivered before the hard exit.
	OnEvent func(Event)
	// PhaseDurations, if true, adds a per-phase duration (attribute
	// "<phase>_duration_ms") and "total_ms" to the shutdown_complete log line,
	// and fills Event.Phases and Event.Total. Only time after the context
	// ended counts.
	PhaseDurations bool
	// InFlight, if set, is called when the deadline passes and returns extra
	// attributes (for example the daemon's own in-flight work) for the overrun
	// log line and Event.Attrs. It runs with no Group lock held and is
	// abandoned after ProviderTimeout. Group.SetInFlight replaces it later.
	InFlight func() []slog.Attr
	// PhaseReport, if set, is called when the deadline passes and returns the
	// phase to report (empty keeps the one set by SetPhase) plus extra
	// attributes. Same locking and timeout rules as InFlight.
	// Group.SetPhaseReport replaces it later.
	PhaseReport func() (phase string, attrs []slog.Attr)
	// Exit replaces os.Exit (tests only).
	Exit func(code int)
	// Notify replaces signal.NotifyContext (tests only).
	Notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)

	attach bool // set by Attach: no signal registration
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
	sigCtx   context.Context // ends on signal or parent end, never on Run
	failed   atomic.Bool     // a loop returned an error
	quiet    bool            // Run finished with no shutdown begun: emit nothing
	phases   []PhaseTiming

	fired     atomic.Bool
	once      sync.Once // Disarm and expire are mutually exclusive
	beginOnce sync.Once // the AfterFunc and Disarm both may run begin
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
	if opt.attach {
		notify = func(p context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(p)
		}
	}
	sigCtx, stop := notify(parent, opt.Signals...)
	// Release the registration as soon as the first signal lands, so a second
	// one gets default handling instead of being swallowed by the context.
	context.AfterFunc(sigCtx, stop)

	eg, gctx := errgroup.WithContext(sigCtx)
	g := &Group{opt: opt, ctx: gctx, stop: stop, eg: eg, loops: map[int]loop{}, phase: "running", sigCtx: sigCtx}
	g.phaseAt = time.Now()
	// Begin on the signal or parent ending, not on gctx: errgroup also ends
	// gctx when Wait returns, which is not a shutdown. A loop error calls
	// begin itself (see Go).
	context.AfterFunc(sigCtx, g.begin)
	return g
}

// Attach is New for a daemon that already owns its signal handling: no signal
// handler is registered (opt.Signals and opt.Notify are ignored), and shutdown
// begins when parent ends or a loop returns an error. Cancel parent from the
// daemon's own handler to start the watchdog. Run MUST still be called.
func Attach(parent context.Context, opt Options) *Group {
	opt.attach = true
	return New(parent, opt)
}

// SetInFlight installs the in-flight provider after the Group is armed (see
// Options.InFlight). Safe to call at any time.
func (g *Group) SetInFlight(fn func() []slog.Attr) {
	g.mu.Lock()
	g.opt.InFlight = fn
	g.mu.Unlock()
}

// SetPhaseReport installs the phase provider after the Group is armed (see
// Options.PhaseReport). Safe to call at any time.
func (g *Group) SetPhaseReport(fn func() (string, []slog.Attr)) {
	g.mu.Lock()
	g.opt.PhaseReport = fn
	g.mu.Unlock()
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
		if err != nil {
			g.failed.Store(true)
			g.begin()
		}
		g.emit(Event{Name: EvStopped, Loop: name, Err: err})
		return err
	})
}

// SetPhase names the step the daemon is in, so an overrun report says where
// shutdown hung (for example "drain_queue", "close_store").
func (g *Group) SetPhase(name string) {
	now := time.Now()
	g.mu.Lock()
	g.closePhaseLocked(now)
	g.phase = name
	g.phaseAt = now
	g.mu.Unlock()
	g.emit(Event{Name: EvPhase})
}

// Run waits for every loop and returns the first loop error. It disarms the
// watchdog on return; if the deadline already fired the process is exiting.
// A finish with no shutdown begun (every loop returned on its own: no signal,
// no loop error, parent alive) emits neither shutdown_signal nor
// shutdown_complete, because nothing was shut down.
func (g *Group) Run() error {
	err := g.eg.Wait()
	// Disarm before stop: stop ends the context, and that must not be mistaken
	// for a signal.
	g.Disarm()
	g.stop()
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
	g.quiet = g.sigCtx.Err() == nil && !g.failed.Load()
	quiet := g.quiet
	if g.timer != nil {
		g.timer.Stop()
	}
	g.mu.Unlock()
	// The context's AfterFunc runs in its own goroutine, so every loop can
	// return, and Disarm run, before it has recorded the end. The context has
	// ended, so record it here (once; this waits for a begin already running)
	// rather than skip shutdown_complete.
	if !quiet {
		g.begin()
	}
	g.mu.Lock()
	ended := !g.endedAt.IsZero()
	var phases []PhaseTiming
	var total time.Duration
	if ended {
		now := time.Now()
		g.closePhaseLocked(now)
		phases = append(phases, g.phases...)
		total = now.Sub(g.endedAt)
	}
	g.mu.Unlock()
	if ended {
		ev := Event{Name: EvClean}
		if g.opt.PhaseDurations {
			ev.Phases, ev.Total = phases, total
		}
		g.emit(ev)
	}
	return true
}

// Fired reports whether the deadline expired.
func (g *Group) Fired() bool { return g.fired.Load() }

// begin runs once when the context ends.
func (g *Group) begin() { g.beginOnce.Do(g.doBegin) }

func (g *Group) doBegin() {
	g.mu.Lock()
	if g.quiet {
		g.mu.Unlock()
		return
	}
	g.endedAt = time.Now()
	if !g.disarmed {
		g.timer = time.AfterFunc(g.opt.Deadline, g.expire)
	}
	g.mu.Unlock()
	g.emit(Event{Name: EvSignal})
}

// closePhaseLocked records the time the current phase has spent since the
// context ended and restarts the phase clock; time before the end is not
// shutdown time. g.mu MUST be held.
func (g *Group) closePhaseLocked(now time.Time) {
	if g.endedAt.IsZero() {
		return
	}
	from := g.phaseAt
	if from.Before(g.endedAt) {
		from = g.endedAt
	}
	g.phases = append(g.phases, PhaseTiming{Name: g.phase, Duration: now.Sub(from)})
	g.phaseAt = now
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

// callProvider runs fn in its own goroutine and gives up after ProviderTimeout
// or a panic, so a provider can never block the hard exit.
func callProvider(fn func() (string, []slog.Attr)) (string, []slog.Attr, bool) {
	type res struct {
		p string
		a []slog.Attr
	}
	ch := make(chan res, 1)
	go func() {
		defer func() { _ = recover() }()
		p, a := fn()
		ch <- res{p, a}
	}()
	t := time.NewTimer(ProviderTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.p, r.a, true
	case <-t.C:
		return "", nil, false
	}
}

func (g *Group) expire() {
	g.once.Do(func() {
		g.fired.Store(true)
		phase, phaseElapsed, flights, elapsed := g.snapshot()
		g.mu.Lock()
		inFlight, phaseReport := g.opt.InFlight, g.opt.PhaseReport
		g.mu.Unlock()
		var attrs []slog.Attr
		if phaseReport != nil {
			if p, a, ok := callProvider(phaseReport); ok {
				if p != "" {
					phase = p
				}
				attrs = append(attrs, a...)
			}
		}
		if inFlight != nil {
			if _, a, ok := callProvider(func() (string, []slog.Attr) { return "", inFlight() }); ok {
				attrs = append(attrs, a...)
			}
		}
		pairs := make([]string, 0, len(flights))
		for _, f := range flights {
			pairs = append(pairs, fmt.Sprintf("%s:%d", f.Name, f.Age.Milliseconds()))
		}
		if g.opt.Log != nil {
			args := []any{
				"deadline_ms", g.opt.Deadline.Milliseconds(),
				"phase", phase,
				"phase_elapsed_ms", phaseElapsed.Milliseconds(),
				"in_flight", strings.Join(pairs, ","),
			}
			for _, a := range attrs {
				args = append(args, a)
			}
			g.opt.Log.Error(EvOverrun, args...)
		}
		if g.opt.OnEvent != nil {
			g.opt.OnEvent(Event{Name: EvOverrun, Phase: phase, Elapsed: elapsed, Attrs: attrs})
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
		if ev.Name == EvClean && g.opt.PhaseDurations {
			for _, p := range ev.Phases {
				args = append(args, p.Name+"_duration_ms", p.Duration.Milliseconds())
			}
			args = append(args, "total_ms", ev.Total.Milliseconds())
		}
		g.opt.Log.Info(ev.Name, args...)
	}
	if g.opt.OnEvent != nil {
		g.opt.OnEvent(ev)
	}
}
