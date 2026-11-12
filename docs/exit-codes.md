# Shared exit codes and the inventory behind them

The shared codes live in package `exitcode` (see its README table). This page records the codes
the workspace apps used when the table was defined (source read 2026-11-12, plus the pg-apps deep
review finding XU-03), and how each maps to a shared code. It is the work list for auditing the
apps; it is a snapshot, not a contract.

Operator ruling (Phillip, 2026-11-11): "start with a for coursly granular types of exits. ie,
app specific concepts should not be specied there, but daemon down is generic enough to have a
slot specified there. determine the list of exit codes and define them as well as possible. it
goes first and the review existing apps and create beads to fix violations. it is ok to break
contracts, just make sure both sides are included in the specific landing of a change."

## Design decisions

- 0 success, 1 generic, 2 usage: fixed by the workspace rule (code-file-standards.md, Exit Codes)
  and by the repo ADR that makes 2 the global usage code.
- 3 to 11 are the generic outcomes that recur in at least two apps: unavailable (daemon down,
  total fan-out failure), not found, degraded or partial, timeout, config, denied, busy,
  shutdown overrun, conflict.
- 12 to 19 are reserved for future shared codes. 20 to 63 are the app-specific range.
- 64 to 78 (sysexits), 124 (timeout(1)), 126 and 127 (shell), 128+n (signals) and 255 are never
  assigned. A process exits 128+n only when relaying a child killed by signal n.
- Sizing "daemon down": today four apps use four different codes for it (1, 3, 4 and 3). It
  takes the single code 3, `Unavailable`, with the total failure of a fan-out.

## Inventory

`->` shows the shared code the meaning maps to. "app" means it stays app-specific (20 to 63).

### pg-router (`pg-router/cmd/pg-router`, `internal/shutdown`)

| Code | Meaning today                                                                     | Maps to                                                     |
| ---- | --------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| 0    | success                                                                           | OK 0                                                        |
| 1    | generic; also "no running core", a refused lock, a failed probe, a rejected event | Error 1; "no running core" -> Unavailable 3; lock -> Busy 9 |
| 2    | usage (including `--for` over 24h)                                                | Usage 2                                                     |
| 3    | `drain` precheck or config failure                                                | Config 7                                                    |
| 9    | busy / decline relayed from a handler                                             | Busy 9                                                      |
| 10   | shutdown watchdog                                                                 | ShutdownOverrun 10                                          |

### pg-connector umbrella (`pg-connector/cmd/pg-connector`)

| Code | Meaning today                                     | Maps to                                            |
| ---- | ------------------------------------------------- | -------------------------------------------------- |
| 0    | all sources healthy                               | OK 0                                               |
| 1    | generic; also usage, unauthenticated, unavailable | Error 1; split to Usage 2, Denied 8, Unavailable 3 |
| 2    | at least one source degraded                      | Degraded 5                                         |
| 3    | no healthy source (total failure)                 | Unavailable 3                                      |
| 4    | `not_found`                                       | NotFound 4                                         |

### pg-connector backends (`pg-connector/pkg/scriptout/errors.go`, `cmd/pg-connector-*`)

| Code | Meaning today                                                       | Maps to                           |
| ---- | ------------------------------------------------------------------- | --------------------------------- |
| 2    | client `not_found`; also usage; also `status` degraded              | NotFound 4 / Usage 2 / Degraded 5 |
| 3    | client `unauthenticated`; also `status` down                        | Denied 8 / Unavailable 3          |
| 4    | client `unavailable`; also process config error                     | Unavailable 3 / Config 7          |
| 5    | client `unknown_op`; also serve refused to start or failed draining | app / Unavailable 3 or Config 7   |
| 6    | client `version_mismatch`                                           | Conflict 11                       |
| 7    | client `invalid_argument`                                           | Usage 2                           |
| 8    | client `query_not_recognized`                                       | Usage 2                           |
| 10   | shutdown watchdog                                                   | ShutdownOverrun 10                |

The wire error names (`not_found`, `unauthenticated`, ...) stay; only the process exit code
changes. The backend's own `status` codes are 0 healthy, 2 degraded, 3 down.

### pg-desk (`pg-desk/internal/focus/lock.go`, `cmd/pg-desk`)

| Code | Meaning today                              | Maps to              |
| ---- | ------------------------------------------ | -------------------- |
| 1    | generic                                    | Error 1              |
| 2    | usage (focus verbs); partial (typed verbs) | Usage 2 / Degraded 5 |
| 3    | total failure                              | Unavailable 3        |
| 4    | `head-check`: head moved                   | Conflict 11          |
| 6    | focus: period closed                       | app                  |
| 8    | focus: partial                             | Degraded 5           |
| 9    | `run` declined                             | Busy 9               |

The typed verbs and the focus verbs disagree on 2 today (partial versus usage); the focus scheme
already moved usage to 2 and partial to 8.

### pg-claude-code-recorder (`internal/wire/wire.go`)

| Code | Meaning today     | Maps to                      |
| ---- | ----------------- | ---------------------------- |
| 0    | success           | OK 0                         |
| 1    | unexpected        | Error 1                      |
| 2    | usage, bad config | Usage 2 / Config 7           |
| 3    | daemon offline    | Unavailable 3                |
| 4    | not found         | NotFound 4                   |
| 5    | behind            | app                          |
| 6    | timeout           | Timeout 6                    |
| 7    | cursor expired    | Conflict 11                  |
| 8    | refused           | Denied 8 or Busy 9 per cause |
| 9    | doctor mismatch   | Conflict 11                  |
| 10   | shutdown overrun  | ShutdownOverrun 10           |

The recorder has the most complete table and was the starting point of the shared one.

### Other apps read for this inventory

| App                                          | Codes today                                                                                                                                  | Maps to                                                                      |
| -------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| pg-task-focus                                | 1 error, 2 usage, 3 daemon unreachable, 4 daemon refused, 5 store unavailable, 6 not ready, 7 serve could not start, 8 check found a problem | 3 -> Unavailable 3; 6 -> Unavailable 3; 7 -> Error 1 or Config 7; others app |
| pg-git-lander                                | 1 internal, 2 usage, 3 busy, 4 stopped, 5 refused; hooks use 75 (EX_TEMPFAIL), 126, 127                                                      | 3 -> Busy 9; 75, 126, 127 are relayed child statuses                         |
| pg-keep-awake                                | 1 unexpected, 2 usage, 3 state directory unwritable, 4 config invalid, 5 keeper down, 6 degraded; shim relays 126, 127, 128+n                | 3 -> Denied 8; 4 -> Config 7; 5 -> Unavailable 3; 6 -> Degraded 5            |
| beads-exporter                               | 1 internal, 2 usage, 3 config                                                                                                                | 3 -> Config 7                                                                |
| pg-checkout-pool                             | 2 usage, 3 exhausted, 4 low disk, 5 ref unresolved, 6 quarantine only, 7 wedged, 8 no lease, 9 internal, 10 busy, 11 owner has lease         | 10 -> Busy 9; 9 internal -> Error 1; 8 -> NotFound 4; rest app               |
| pg-probe                                     | 1 error, 2 usage, 3 refused, 124 timeout                                                                                                     | 124 -> Timeout 6                                                             |
| pg-decider backfill                          | 0 done, 10 floor, 11 failed, 12 stopped, 20 summary failed, 64 usage                                                                         | 64 -> Usage 2 (64 is sysexits); 10 to 12 collide with shared codes           |
| pg-review-router-adapter, pg-router-handlers | 2 usage, 9 busy decline                                                                                                                      | Usage 2, Busy 9                                                              |
| ccpool                                       | 8 no such session, 8 usage limited                                                                                                           | 8 collides: NotFound 4 / Busy 9                                              |

Notes for the audit:

- 3 is "daemon down" in the recorder and desk and "busy" in pg-git-lander and pg-checkout-pool;
  4 is "not found" in the recorder and "config" in the connector backends. These are the
  collisions that make a caller need a per-tool table.
- Migrating a code changes a contract; per the operator ruling both sides (the app and every
  caller that branches on the code, such as pg-router sources and adapters) MUST land together.
