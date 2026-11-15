# Observability adoption gaps: `jsonllogger` and `otelsetup`

Source: the 2026-11-11 pg-apps deep review (findings XO-05, XO-09, XO-14) and operator ruling
(Phillip, 2026-11-11): "option (a), fill gaps first, then adopt per app."

## What x gained for this

| Gap found                                                          | Now in x                                                              |
| ------------------------------------------------------------------ | --------------------------------------------------------------------- |
| Only a per-app XDG file; no stderr or injected writer              | `jsonllogger.NewHandler`, `NewLogger`, `New(..., WithWriter(w))`      |
| No `service` field to tell processes apart on a shared stderr file | `WithService`                                                         |
| No level control                                                   | `WithLevel` (accepts a `*slog.LevelVar`)                              |
| Four JSONL dialects (`ts`/`event`, `file_id`, ...)                 | `WithKeyRenames` keeps an existing dialect while migrating            |
| File not rotation-safe, no size cap                                | `RotatingFile` (reopens after rename or delete) and `WithMaxBytes`    |
| Log plus OTLP bridge fan-out hand-written in router and desk       | `jsonllogger.Fanout`                                                  |
| Each app copies a goroutine-safe capture buffer for tests          | `jsonllogger/jsonlloggertest` (`Buffer`, `Records`, `New`)            |
| No shared restart, build-info or shutdown signals (XO-06, XO-09)   | `otelsetup.RegisterCommonSignals` and `CommonSignals.ObserveShutdown` |

## Still missing in x (not done here; each needs an operator decision or a dependency)

- Prometheus scrape reader for daemons. `otelsetup.Init` supports OTLP push and spool only, while
  pg-router and pg-desk expose a scrape endpoint. Adding it pulls the OTel Prometheus exporter and
  `client_golang` into x's `go.mod`; deliberately not added without sign-off.
- OTLP log export (`otlploghttp`/`otelslog` bridge) that router and desk telemetry packages carry.
- `daemon.Run(name, opts, body)` Template Method (XO-14), event-log writer with rotation, status endpoint.

## Per-app gap list and adoption beads proposed

| App (repo)                          | Gaps closed by x now                                      | Still blocks full adoption                                     |
| ----------------------------------- | --------------------------------------------------------- | -------------------------------------------------------------- |
| pg-router (nix-agent-support)       | writer, service, level, Fanout, shutdown signals          | scrape reader, OTLP log bridge in x; keep `eventlog`           |
| pg-router handlers                  | service field, stderr writer, test buffer                 | none; call `NewLogger(os.Stderr, WithService)`                 |
| pg-connector daemons (github, jira) | rotation, size cap, service, key renames                  | `logs` has typed `stateLine` rows and three files; map to slog |
| pg-desk                             | writer, Fanout, level, service                            | scrape reader, OTLP log bridge                                 |
| pg-claude-code-recorder             | renames (`ts`, `event`, `file_id`, `generation`), service | hand-rolled metrics registry replaced by otelsetup scrape      |
| ccpool, pg-keep-awake, work-report  | none specific                                             | copied `telemetry` package depends on scrape reader/log bridge |
