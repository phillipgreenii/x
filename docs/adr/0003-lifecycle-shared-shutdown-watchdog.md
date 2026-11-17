# ADR-0003: `lifecycle` is the shared shutdown watchdog in x

**Status**: Accepted
**Date**: 2026-11-16
**Deciders**: phillipg

## Recommendation

The shared hard-deadline shutdown watchdog for daemons is the `lifecycle` package of `x`
(`github.com/phillipgreenii/x/lifecycle`). `x` MUST NOT carry a second watchdog package.

## Context

A daemon that hangs during shutdown holds its pidfile or state lock past launchd's `ExitTimeOut`
and blocks the next start. The cure is a watchdog: once the root context ends, give the daemon a
fixed deadline, then log what was still running and exit with a failure code (10 by default; a
launcher restarts the job on 10).

Several daemons each carried their own copy of that watchdog (the recorder, `pg-router`,
`pa-monitor`, and a sequential phase waiter in `host-activity`). They share event names, the
overrun attributes, the exit code and a race-sensitive handshake between disarming and expiry, so
a fix to the handshake would have to be made once per copy.

`lifecycle` already provides this: one signal context, the daemon's loops under an errgroup, and a
watchdog that logs the shutdown phase and the loops still running at the overrun, then hard-exits
with `Options.ExitCode`. A clean finish disarms it. The deadline is owned by the deployment: the
caller fills `Options.Deadline` from a flag (`DeadlineFlag`) that the nix module also uses to
derive the launchd `ExitTimeOut`, so the two cannot drift.

A separate package, `daemonstop`, was added to `x` on 2026-11-12 with the same purpose. It briefly
existed on `main` and was removed on 2026-11-16.

## Decision

1. Daemons use `lifecycle` for the shutdown watchdog. The deadline is passed by flag from the
   deployment, and nix derives the launchd `ExitTimeOut` from the same value.
2. `daemonstop` is rejected and its package is deleted. Two watchdogs would duplicate a
   race-sensitive handshake and would give daemons two log vocabularies for the same events.
3. The removal is a forward commit, not a history rewrite: the `daemonstop` commit was already on
   `origin/main` and later `x` commits that other repos pin descend from it, so a rewrite would
   orphan those pins. No code outside `x` imported `daemonstop`.

Operator ruling (Phillip, 2026-11-16): "ok stick with lifecycle."

## Consequences

- Each daemon that still carries its own watchdog migrates to `lifecycle` as separate work.
- `host-activity`'s sequential phase waiter, which never exits and owns its own deadline timer, is
  a different contract; it MAY adopt the `lifecycle` event-name constants later.
  Decided 2026-11-17, after `lifecycle` gained provider hooks, per-phase durations and `Attach`:
  `host-activity` stays excluded. Those additions closed the gaps for the recorder and `pa-monitor`,
  but not this one. `lifecycle` hard-exits from its timer goroutine with `os.Exit`, while
  `host-activity` returns exit code 10 from its run loop so deferred cleanup still runs; and it logs
  `shutdown_started` and `shutdown_done` where `lifecycle` logs `shutdown_signal` and
  `shutdown_complete`. Adopting it would change both the exit path and the event names, so it is
  not a no-behaviour-change move. Revisit if the cleanup moves into the loops themselves.
- The `daemonstop` commit stays in `x` history; only its content is removed.
