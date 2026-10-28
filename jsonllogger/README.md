# jsonllogger

Structured JSONL logging for a CLI tool or daemon.

`jsonllogger.New("<app>")` returns a `*slog.Logger` that appends one JSON object per line to
`${XDG_STATE_HOME}/<app>/<app>.jsonl` (falling back to `$HOME/.local/state` when
`XDG_STATE_HOME` is unset). Each line's level is lowercased and its timestamp is forced to UTC,
so output is consistent regardless of platform or timezone.

Use it when a program needs a durable, appendable log file rather than plain stdout/stderr — for
example, a daemon whose logs should survive restarts and be tailed or shipped elsewhere. The file
is opened once and kept open for the process's lifetime; there's nothing to close.

This package does not rotate the log, so the file grows without bound. If you need rotation, see
the next section.

## A second implementation exists

There are two `jsonllogger` implementations in the workspace, kept on purpose:

- **This package** (`github.com/phillipgreenii/x/jsonllogger`) is the small one.
- **`packages/jsonl-logger`** in `nix-support-apps` (module `github.com/phillipgreenii/jsonl-logger`)
  is the fuller one, written for the support-apps daemons whose log is shipped to the
  observability stack (see [References](#references)).

Both expose `New(app)` and `ReplaceAttr`, write to the same path, and produce the same line shape
(lowercase `level`, UTC `time`), so a log written by one reads the same as one from the other. The
file is opened append-only and never truncated in both, so a restart keeps the previous run's log.
They differ in what happens around the file:

- **Rotation.** This package has none. The sibling rotates by size by default: a 10 MiB live file
  and five rotated files (`<app>.jsonl.1` to `<app>.jsonl.5`), about 60 MiB per app.
- **`XDG_STATE_HOME` and `HOME` both unset.** This package's `New` returns an error
  (`jsonllogger: neither XDG_STATE_HOME nor HOME is set`), because a bare relative path would make
  the log's location depend on the working directory. The sibling returns no error and resolves to
  the relative path `.local/state/<app>/<app>.jsonl`.
- **Extra API.** This package has only `New` and `ReplaceAttr`. The sibling adds `NewWithOptions`
  (`WithMaxSize`, `WithMaxFiles`) and `NewRotatingWriter` for additional rotated JSONL files.

Pick this package when you want the minimal bootstrap and will manage the file's size yourself, or
it stays small. Inside a `nix-support-apps` binary, use the sibling.

### Why two exist

`x` was bootstrapped by copying `jsonllogger` out of `nix-support-apps` verbatim, and the two have
diverged since. Merging them means one repo depending on the other, which crosses a repo boundary
and needs a flake input: `nix-support-apps` packages its Go modules from a relative `replace` and a
rooted fileset, so pulling `x` in would add an input (and a lock bump for every change), while `x`
is consumed as a plain Go module pinned by commit. With few consumers, that cost is larger than
keeping two small, separately tested copies.

**Consolidate when a third consumer appears.** The operator ruled on 2026-10-28 (bead pg2-me1gep)
to document both implementations rather than merge them, and to revisit when a third consumer shows
up: one that is neither a `nix-support-apps` binary nor `pg-go-mutate-tui`. Until then, a change to
shared behaviour (the `ReplaceAttr` contract, the file path, append-only opening) MUST be made in
both.

### Current consumers

Counted on 2026-10-28 from the workspace root (the directory holding the repo checkouts), with
worktrees excluded:

```
rg -l --glob '*.go' --glob '!.worktrees/**' '"github\.com/phillipgreenii/x/jsonllogger"' \
  nix-agent-support nix-overlay nix-personal nix-repo-base nix-support-apps nix-ziprecruiter x
rg -l --glob '*.go' --glob '!.worktrees/**' '"github\.com/phillipgreenii/jsonl-logger"' \
  nix-agent-support nix-overlay nix-personal nix-repo-base nix-support-apps nix-ziprecruiter x
```

- **`x/jsonllogger`: 1 importing Go file, 1 consumer.** `pg-go-mutate-tui` in `nix-repo-base`,
  through its `internal/obslog` wrapper, which falls back to a stderr logger when `New` errors.
- **`nix-support-apps/packages/jsonl-logger`: 11 importing Go files, 10 consumers**, all in
  `nix-support-apps`: `chrome-tabs-exporter`, `display-class`, `docker-state-exporter`,
  `fsmonitor-daemon-exporter`, `git-repo-exporter`, `grafana-notifier`, `host-activity`,
  `launchd-health-exporter`, `mysql-probe-exporter` and `pg-osx-bridge-api`.

These numbers go stale; re-run the commands before relying on them.

### References

These live in `nix-support-apps`, a separate checkout, so they are paths rather than links:

- `packages/jsonl-logger/README.md`: the sibling implementation and its rotation behaviour.
- `docs/adr/0038-observability-app-registration-and-jsonl-logging.md`: the JSONL logging contract
  (lowercase level, UTC time) both implementations follow.
