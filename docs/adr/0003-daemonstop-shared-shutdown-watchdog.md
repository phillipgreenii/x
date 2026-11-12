# ADR-0003: A shared shutdown watchdog in x (`daemonstop`)

**Status**: Accepted
**Date**: 2026-11-12
**Deciders**: phillipg

## Recommendation

Add one standard-library-only package, `daemonstop`, to `x` (`github.com/phillipgreenii/x`). It
holds the hard-deadline shutdown watchdog that four daemons each carry their own copy of, behind
one API that covers both shapes those copies use.

## Context

A daemon that hangs during shutdown holds its pidfile or state lock past launchd's `ExitTimeOut`
and blocks the next start. The cure is a watchdog: once the root context ends, give the daemon a
fixed deadline, then log what was still running and exit with code 10 (a launcher restarts the
job on 10).

Four copies exist today:

- the recorder (`pg-claude-code-recorder/internal/shutdown`): `Hooks` (late-bound `InFlight` and
  `Phase` providers, registered after the watchdog is armed), logs in-flight goroutines and phase
  attributes at the overrun; no `Tracker`;
- `pg-router/internal/shutdown` and `pa-monitor/internal/shutdown`: a `Tracker` that logs
  `shutdown_started`, `shutdown_phase` and `shutdown_done`, which `Watch` reads for the current
  phase at the overrun; the two files differ only in the default deadline and in how `Begin`
  treats a `Phase` call that arrived first;
- `host-activity/cmd/host-activity/shutdown.go`: a sequential phase waiter with the same event
  names that never exits (the caller returns the exit code).

The copies share event names (`shutdown_started`, `shutdown_phase`, `shutdown_done`,
`shutdown_overrun`), the overrun attributes, the exit code 10, and the race-free `Disarm` versus
expiry handshake. A fix to the handshake would have to be made four times.

## Decision

`daemonstop` exposes one `Watch(ctx, Options) *Watchdog` with `Disarm` and `Fired`, and both
phase-naming shapes in `Options`:

1. **`Options.Tracker`**: a `*Tracker` (nil-safe) that logs the three lifecycle events. `Watch`
   calls `Tracker.Begin` when the context ends and reads `Tracker.Current` at the overrun. The
   router/pa-monitor `Begin` is adopted (a phase set before `Begin` is kept), because it is the
   safe superset.
2. **`Options.Hooks`** (and the direct `InFlight` and `Phase` funcs): late-bound providers
   (`Hooks.SetInFlight`, `Hooks.SetPhase`) whose results are logged as `in_flight` (oldest first,
   `name:age_ms`) and the phase name, elapsed time and extra attributes.

Precedence for the phase at the overrun is: `Options.Phase`, then a registered `Hooks` phase
provider, then `Tracker.Current`, then `Hooks`' `not_started` default, then `unknown`. The
`in_flight` attribute is written only when a flight provider is configured, so a Tracker-only
daemon's line is unchanged. Overrun attributes, event names and exit code 10 are preserved.

Time is injectable through `Clock` (`Now`, `AfterFunc`) and the process exit through
`Options.Exit`, so the contract tests run in milliseconds with a manual clock and no real exit.

The default deadline is 8 s (the recorder and pa-monitor value); a daemon with a different
`ExitTimeOut` passes its own (pg-router passes one derived from its drain phases).

## Consequences

- Each consumer replaces its copy with an import and a pin bump; that migration is separate work
  and is not part of adding the package.
- `host-activity`'s sequential waiter is not covered: it never exits and owns its own deadline
  timer, which is a different contract. It MAY adopt the event-name constants later.
- The package carries a clock seam only for tests; production callers leave `Clock` zero.
