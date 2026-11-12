// Package daemonstop bounds a daemon's own shutdown with a hard deadline.
//
// Once the root context ends, Watch gives the daemon Options.Deadline to
// finish. Past it the watchdog logs shutdown_overrun, naming the shutdown
// phase in progress and any goroutines still running, and exits with ExitCode,
// so a stuck step can never hold the pidfile or state lock (and the next
// start) hostage past launchd's ExitTimeOut. A Disarm from a shutdown that
// finished in time and the expiry are mutually exclusive.
//
// Two ways to name the phase in the overrun line are supported and may be
// combined:
//
//   - a Tracker, passed as Options.Tracker: the daemon calls Phase as it
//     steps through shutdown and Done at the end; the Tracker logs
//     shutdown_started, shutdown_phase and shutdown_done, and the watchdog
//     reads Tracker.Current at the overrun;
//   - late-bound Hooks (InFlight, Phase), for a daemon whose providers exist
//     only after the watchdog is armed: the watchdog logs the in-flight
//     goroutines and the phase attributes the providers return.
//
// Logging goes through a raw slog logger, never the daemon's telemetry
// emitter: the emitter's flush is one of the steps that can hang.
package daemonstop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultDeadline is the deadline Watch uses when Options.Deadline is unset.
// A daemon whose launchd ExitTimeOut differs passes its own: the ExitTimeOut
// is the deadline plus a 7 s margin, under launchd's silent 60 s clamp.
const DefaultDeadline = 8 * time.Second

// ExitCode is the process exit status of a hard exit. It is not a branchable
// shared code: a launcher treats it as "restart the job".
const ExitCode = 10

// Log events.
const (
	EvStarted = "shutdown_started"
	EvPhase   = "shutdown_phase"
	EvDone    = "shutdown_done"
	EvOverrun = "shutdown_overrun"
)

// Clock is the time source of a Tracker and a Watchdog. The zero value uses
// the real clock; a test replaces either field.
type Clock struct {
	// Now returns the current time; nil selects time.Now.
	Now func() time.Time
	// AfterFunc runs f after d and returns a stop func that reports whether
	// it prevented the run; nil selects time.AfterFunc.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
}

func (c Clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Clock) afterFunc(d time.Duration, f func()) func() bool {
	if c.AfterFunc != nil {
		return c.AfterFunc(d, f)
	}
	return time.AfterFunc(d, f).Stop
}

// Tracker records the shutdown step in progress and logs its events. A nil
// Tracker is a no-op.
type Tracker struct {
	log   *slog.Logger
	clock Clock

	mu      sync.Mutex
	began   time.Time
	name    string
	started time.Time
}

// NewTracker returns a Tracker that logs to log (nil logs nothing).
func NewTracker(log *slog.Logger) *Tracker { return &Tracker{log: log} }

// WithClock sets the Tracker's time source and returns t. Call it before the
// Tracker is shared.
func (t *Tracker) WithClock(c Clock) *Tracker {
	if t != nil {
		t.clock = c
	}
	return t
}

// Begin logs shutdown_started and enters the "signal" phase.
func (t *Tracker) Begin() {
	if t == nil {
		return
	}
	now := t.clock.now()
	t.mu.Lock()
	// Begin can run from the context's AfterFunc goroutine, so the caller's own
	// first Phase may already have run; keep that phase rather than reset it.
	if t.began.IsZero() {
		t.began, t.name, t.started = now, "signal", now
	}
	t.mu.Unlock()
	t.emit(EvStarted)
}

// Phase ends the current phase (logging its duration) and starts name.
func (t *Tracker) Phase(name string) {
	if t == nil {
		return
	}
	now := t.clock.now()
	t.mu.Lock()
	prev, began := t.name, t.started
	t.name, t.started = name, now
	if t.began.IsZero() {
		t.began = now // Phase ahead of Begin: the total runs from here
	}
	t.mu.Unlock()
	if prev != "" {
		t.emit(EvPhase, "phase", prev, "duration_ms", now.Sub(began).Milliseconds())
	}
}

// Done logs the last phase and shutdown_done.
func (t *Tracker) Done() {
	if t == nil {
		return
	}
	t.Phase("done")
	t.mu.Lock()
	total := t.clock.now().Sub(t.began)
	t.mu.Unlock()
	t.emit(EvDone, "duration_ms", total.Milliseconds())
}

// Current returns the phase in progress and how long it has run: "unknown"
// for a nil Tracker, "not_started" before the first Begin or Phase.
func (t *Tracker) Current() (string, time.Duration) {
	if t == nil {
		return "unknown", 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.name == "" {
		return "not_started", 0
	}
	return t.name, t.clock.now().Sub(t.started)
}

func (t *Tracker) emit(ev string, args ...any) {
	if t.log != nil {
		t.log.Info(ev, args...)
	}
}

// Flight is a goroutine that was still running at the deadline.
type Flight struct {
	Name string
	Age  time.Duration
}

// PhaseInfo is the shutdown step in progress when the deadline passed. Attrs
// are extra key/value pairs for the overrun line (for example sweep counters).
type PhaseInfo struct {
	Name    string
	Elapsed time.Duration
	Attrs   []any
}

// Hooks are late-bound providers: the watchdog is armed before the daemon
// exists, and the daemon registers the real providers once it does. Reads are
// lock-free. The zero value is ready to use.
type Hooks struct {
	inFlight atomic.Pointer[func() []Flight]
	phase    atomic.Pointer[func() PhaseInfo]
}

// SetInFlight registers the in-flight provider.
func (h *Hooks) SetInFlight(f func() []Flight) { h.inFlight.Store(&f) }

// SetPhase registers the phase provider.
func (h *Hooks) SetPhase(f func() PhaseInfo) { h.phase.Store(&f) }

// InFlight returns the registered flights, or nil before registration.
func (h *Hooks) InFlight() []Flight {
	if f := h.inFlight.Load(); f != nil {
		return (*f)()
	}
	return nil
}

// Phase returns the registered phase, or "not_started" before registration.
func (h *Hooks) Phase() PhaseInfo {
	if f := h.phase.Load(); f != nil {
		return (*f)()
	}
	return PhaseInfo{Name: "not_started"}
}

// Options configures Watch.
type Options struct {
	// Deadline is how long shutdown may take; zero selects DefaultDeadline.
	Deadline time.Duration
	// Log is the raw, unbuffered logger; nil logs nothing. It MUST NOT be the
	// daemon's telemetry emitter, whose Event can take a mutex a stuck
	// goroutine holds.
	Log *slog.Logger
	// Tracker names the phase in the overrun line and logs the shutdown
	// events: Watch calls Tracker.Begin when the context ends. May be nil.
	Tracker *Tracker
	// Hooks supplies the in-flight goroutines and the phase, late-bound. When
	// its phase provider is registered it takes precedence over Tracker. May
	// be nil.
	Hooks *Hooks
	// InFlight and Phase are direct providers, for a caller with no Hooks.
	// They take precedence over Hooks.
	InFlight func() []Flight
	Phase    func() PhaseInfo
	// Exit ends the process; nil selects os.Exit.
	Exit func(code int)
	// Clock is the time source of the deadline timer.
	Clock Clock
}

// Watchdog is an armed deadline.
type Watchdog struct {
	once  sync.Once
	mu    sync.Mutex
	stopT func() bool
	stopC func() bool
	opt   Options
	fired atomic.Bool
}

// Watch arms the watchdog on ctx: when ctx ends it calls Tracker.Begin and
// starts the deadline timer. Disarm and the expiry are mutually exclusive.
func Watch(ctx context.Context, opt Options) *Watchdog {
	if opt.Deadline <= 0 {
		opt.Deadline = DefaultDeadline
	}
	if opt.Exit == nil {
		opt.Exit = os.Exit
	}
	w := &Watchdog{opt: opt}
	w.stopC = context.AfterFunc(ctx, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		opt.Tracker.Begin()
		w.stopT = opt.Clock.afterFunc(opt.Deadline, w.expire)
	})
	return w
}

// Disarm stops the watchdog and reports whether it won the race: false means
// the deadline already fired (and the process is exiting).
func (w *Watchdog) Disarm() bool {
	won := false
	w.once.Do(func() { won = true })
	if !won {
		return false
	}
	w.stopC()
	w.mu.Lock()
	if w.stopT != nil {
		w.stopT()
	}
	w.mu.Unlock()
	return true
}

// Fired reports whether the deadline expired.
func (w *Watchdog) Fired() bool { return w.fired.Load() }

func (w *Watchdog) expire() {
	w.once.Do(func() {
		w.fired.Store(true)
		o := w.opt

		var flights []Flight
		haveFlights := false
		switch {
		case o.InFlight != nil:
			flights, haveFlights = o.InFlight(), true
		case o.Hooks != nil:
			flights, haveFlights = o.Hooks.InFlight(), true
		}

		var ph PhaseInfo
		switch {
		case o.Phase != nil:
			ph = o.Phase()
		case o.Hooks != nil && o.Hooks.phase.Load() != nil:
			ph = o.Hooks.Phase()
		case o.Tracker != nil:
			name, elapsed := o.Tracker.Current()
			ph = PhaseInfo{Name: name, Elapsed: elapsed}
		case o.Hooks != nil:
			ph = o.Hooks.Phase()
		default:
			ph = PhaseInfo{Name: "unknown"}
		}

		args := []any{
			"deadline_ms", o.Deadline.Milliseconds(),
			"phase", ph.Name,
			"phase_elapsed_ms", ph.Elapsed.Milliseconds(),
		}
		if haveFlights {
			sort.SliceStable(flights, func(i, j int) bool { return flights[i].Age > flights[j].Age })
			pairs := make([]string, 0, len(flights))
			for _, f := range flights {
				pairs = append(pairs, fmt.Sprintf("%s:%d", f.Name, f.Age.Milliseconds()))
			}
			args = append(args, "in_flight", strings.Join(pairs, ","))
		}
		args = append(args, ph.Attrs...)
		if o.Log != nil {
			o.Log.Error(EvOverrun, args...)
		}
		o.Exit(ExitCode)
	})
}
