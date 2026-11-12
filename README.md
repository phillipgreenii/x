# x

A small collection of Go packages for building CLI tools and daemons: structured logging and
XDG directory resolution, with more added as needed.

This is a plain Go module — not a nix flake. Add it the way you'd add any Go dependency:

```
go get github.com/phillipgreenii/x@<commit-sha>
```

There are no release tags; pin a commit and bump it like any other dependency.

## Packages

- **`jsonllogger`** — structured JSONL logging to a per-app log file. Use it when a CLI tool or
  daemon needs a durable, appendable log file instead of plain stdout/stderr — for example, a
  daemon whose logs should survive restarts and be tailed or shipped elsewhere.
- **`osdirs`** — resolves an app's XDG state/config directory, falling back to `$HOME` when the
  corresponding environment variable isn't set. Use it whenever a tool needs a per-app directory
  under `~/.local/state` or `~/.config`.
- **`otelsetup`** — OpenTelemetry tracing and metrics setup for short-lived CLI processes
  exporting to a local OTLP/HTTP collector: `Init(ctx, Config{Endpoint, ServiceName, Version})`
  returns a tracer, meter, and bounded `ForceFlush`/`Shutdown`. `WithRetry(initial, maxElapsed)` opts the exporters into a short-interval retry that fits the caller's flush budget, so one transient collector 503 does not drop the batch. It is a no-op (no network) when the
  endpoint is empty, uses delta metric temporality and explicit second-based histogram buckets, and
  ships a generated `metrics.json` catalog of metric names and units (`go generate ./otelsetup`).
  Importing it pulls the OTel modules into your `go.mod`; other packages are unaffected.
- **`gitclient`** — a git CLI client with an allowlisted child environment (only `PATH`, `HOME`,
  `SSH_AUTH_SOCK` are inherited, so an ambient `GIT_DIR`/`GIT_WORK_TREE`/`GIT_CONFIG_*` can never
  redirect it). `Client.Run(ctx, args...)` is the escape hatch for any verb.
- **`pprofserver`** — Go runtime profiling (`net/http/pprof`) for a long-running daemon, served on
  a listener that is never reachable from the network. It is opt-in: `StartFromEnv` opens nothing
  unless the environment variable named by the caller (for example `PA_MONITOR_PPROF_ADDR`) is set.
  The value MUST be an absolute unix-socket path (created mode 0600, replacing a stale socket,
  never removing a regular file) or a loopback `host:port`; anything else (wildcard, remote,
  relative) is refused with one stderr line and the daemon keeps running. It uses its own mux
  holding only the pprof routes (never `http.DefaultServeMux`) and sets no write timeout, so
  `/debug/pprof/profile?seconds=N` works. `pprofserver/mutation-check.sh` is the recorded mutation
  check proving the tests catch a loosened host or socket-mode check.
- **`cmdrun`** / **`cmdruntest`** — a shared in-process command-runner seam. `cmdrun` is the
  standard-library-only process-spawn core: `Call` (name, args, dir, stdin, env in `EnvAppend` or
  `EnvExact` mode, opt-in `OwnProcGroup`), `Result` (stdout and stderr captured separately, `Code`,
  `Signal`, `CtxErr` when the context ended the child, `StartErr` when it never started) and
  `Runner`, with `Exec` as the real implementation. Code that runs an external tool MUST take a
  `cmdrun.Runner` (defaulting to `cmdrun.Exec`) so its tests can answer in process. `cmdruntest`
  is the `testing` adapter: `cmdruntest.New(t)` returns a `Fake` whose `Runner()` answers from
  ordered rules (`On(...).Return/Respond/Seq`, limited with `Times`/`Nth`, first match wins),
  records every call in order (`Calls()`, safe for concurrent use) and fails the test on a call no
  rule answers. A test that stands in for an external tool MUST use the fake rather than
  re-executing the test binary (`GO_WANT_HELPER_PROCESS`): a race-built child costs about a second
  per call, a real `sh` about 9 ms. `cmdruntest.RunContract(t, runner)` is the contract suite
  against real `sh`; an adapter that wraps `Exec` MUST run it from an `integration`-tagged test.
  **Timeouts:** every `Call` SHOULD set `Timeout`; when positive, `Exec` derives a child context
  with it, kills the child at expiry and sets `Result.TimedOut` (with `CtxErr` =
  `context.DeadlineExceeded`). `TimedOut` stays false when the caller's own context ended the run,
  so a timeout is distinguishable from a cancel. `cmdruntest.AssertCallsBounded(t, moduleRoot)` is
  the guard: it parses a module's non-test Go files and fails on a `cmdrun.Call` literal with no
  `Timeout` field, unless the literal carries a `//cmdrun:unbounded <reason>` comment (non-empty
  reason, on the line above or the line the literal starts on). Adoption: each module adds one test
  calling `cmdruntest.AssertCallsBounded(t, ".")`, after bounding or annotating its existing calls;
  the guard is a test, not a runtime refusal. Guard fixtures live in `cmdruntest/testdata`.
  Design and scope: [`docs/adr/0001-shared-command-runner-seam.md`](docs/adr/0001-shared-command-runner-seam.md).
- **`gitfixture`** / **`gittest`** — hermetic, real git repositories for tests. Tests that need git
  MUST use these rather than hand-rolling `git init` or `exec.Command("git", ...)`. See
  [Git fixtures](#git-fixtures).
- **`lifecycle`** — the run group of a long-running daemon: one signal context, the daemon's loops
  under an `errgroup` (adds `golang.org/x/sync` to your `go.mod`), and one hard shutdown deadline.
  See [Daemon lifecycle](#daemon-lifecycle).

## Daemon lifecycle

`lifecycle.New(ctx, lifecycle.Options{...})` returns a `Group`. The first SIGINT or SIGTERM ends
`g.Context()` (as does a loop returning an error) and starts the shutdown clock. The signal
registration is released at that moment (`context.AfterFunc(ctx, stop)`), so a second signal gets the
default action and kills the process. If the loops have not all returned within `Deadline` of the
context ending, a watchdog logs `shutdown_overrun` (deadline, the phase set by `g.SetPhase`, and every
loop still running with its age) and calls `Exit(ExitCode)`, so a stuck goroutine cannot keep the
state lock (and the next start) hostage. A clean `Run` disarms it. The package never sleeps: its only
clock use is `time.Now` and one `time.AfterFunc`, so tests run under `testing/synctest`.

```go
deadline := lifecycle.DeadlineFlag(flag.CommandLine, "shutdown-deadline", 8*time.Second)
flag.Parse()

g := lifecycle.New(context.Background(), lifecycle.Options{
	Deadline: *deadline,
	ExitCode: 10,        // a status the supervisor treats as a failure
	Log:      rawLogger, // MUST NOT take a lock a stuck loop could hold
})
g.Go("http", func(ctx context.Context) error { return serve(ctx) })
g.Go("sweeper", func(ctx context.Context) error { return sweep(ctx) })
// call g.SetPhase("drain_queue") as shutdown proceeds, so an overrun names where it hung
if err := g.Run(); err != nil {
	log.Fatal(err)
}
```

The deadline is owned by nix, not by the daemon: the module passes it to the daemon as
`--shutdown-deadline` and derives launchd's `ExitTimeOut` from the same value, so the two cannot
drift. `ExitTimeOut` MUST exceed the deadline so the watchdog fires before launchd sends SIGKILL
(launchd clamps it at 60 s):

```nix
let
  shutdownDeadline = 8; # seconds, the single source
  margin = 7;
in
{
  command = "exec ${pkg}/bin/mydaemon serve --shutdown-deadline=${toString shutdownDeadline}s";
  serviceConfig.ExitTimeOut = shutdownDeadline + margin;
}
```

`Options.OnEvent` receives `shutdown_signal`, `shutdown_phase`, `shutdown_complete`,
`shutdown_overrun`, `loop_started` and `loop_stopped` for logging or metrics. `Options.Exit` and
`Options.Notify` exist for tests: pass a fake `Notify` and an `Exit` that records the code.

## Testing

```
go vet ./...
go test -race ./...
go test -race -tags integration,contract ./...
```

- Untagged tests are unit tests: no real git binary, no subprocess (the `cmdruntest` fake is how a test stands in for an external command).
- `integration` tests exercise the packages' role interfaces against a real git binary, always
  through the `gitfixture`/`gittest` isolated-repo fixture (never a real repository).
- `contract` tests are the acceptance suite that proves the hermeticity guarantees themselves —
  that a client's child git process can't read or write outside the repo it's anchored at even
  under a hostile ambient environment, that the client's environment allowlist is complete, and
  that a canceled/deadlined context kills a blocked git invocation promptly.

All three commands run in CI on every push (`.github/workflows/ci.yml`) — that's this repo's only
gate. The `flake.nix` here exists solely for formatting (see below). The `integration`/`contract` suites need a real `git` binary on
PATH (present on GitHub's `ubuntu-latest` runners); a consumer's own `nix flake check` never runs
them — only this repo's own CI does.

CI also runs a coverage gate (`.github/scripts/check-coverage.sh`): the whole-module aggregate
coverage from `go test -race -tags=integration,contract -coverpkg=./...` (all three tiers'
statements combined, not any single package's own view) must be at least 90.0%, or the job fails.

## Git fixtures

`gittest.New(t, gitfixture.RepoOptions{})` returns a `*gitfixture.Repo` (`Dir`, `Client`) backed by a
real git repository under `t.TempDir()`. It is hermetic by construction: `HOME` is a fresh empty
directory, there is no system config, `GIT_CEILING_DIRECTORIES` is the fixture root, hooks are
redirected to an empty directory, the commit identity is `gitfixture <Suite>`, and the child
environment is an allowlist, so a hostile ambient `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` or
`GIT_CONFIG_*` cannot reach (or be written into) a real repository. `gitfixture` is the same thing
without the `testing` import, for non-test helpers.

| Need                                                                                                                | Use                                                                                                                       |
| ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Any git verb (worktree add/lock, switch -c, merge --ff-only, branch, config, rev-parse, checkout, push, fetch, ...) | `repo.Client.Run(ctx, args...)` — anchored at the repo, hermetic environment. No separate runner exists on purpose.       |
| Write / commit files                                                                                                | `repo.WriteFile(rel, content)`, `repo.Commit(ctx, msg, files)`                                                            |
| An initial commit on the initial branch                                                                             | `RepoOptions{InitialCommit: true}` (one empty commit; not valid with `Bare`)                                              |
| A bare remote                                                                                                       | `repo.AddBareRemote(ctx, "origin")` (also registers it as a remote of `repo`)                                             |
| Clone a repo or remote into a working repo                                                                          | `repo.Clone(ctx, name)` / `gittest.Clone(t, src, name)`                                                                   |
| Several named repos under one root                                                                                  | `RepoOptions{Name: "alpha"}`, then `repo.NewSibling(ctx, "beta", opts)` / `gittest.NewSibling(t, repo, "beta", opts)`     |
| A git dir outside the work tree                                                                                     | `RepoOptions{SeparateGitDir: "gitdirs/repo.git"}` (relative to the fixture root)                                          |
| A path beside the repo (worktree target)                                                                            | `repo.Root()`, `repo.RootPath(rel)` — containment-checked against the fixture root                                        |
| A hermetic client at another path (a linked worktree, a dir beside the repo)                                        | `repo.ClientAt(ctx, dir)` / `gittest.ClientAt(t, repo, dir)` — `dir` must resolve inside the fixture root, else it errors |
| The hermetic option set, for an Opener seam or `gitclient.New` / `Discover` / `Clone` in code under test            | `repo.Options()` — a fresh `[]gitclient.Option` each call; never hand-roll the set                                        |

Notes:

- The repository lives at `<root>/<Name>` (default `<root>/repo`); `<root>/home`, `<root>/hooks` and
  `<root>/remotes` are fixture infrastructure and cannot be used as a `Name`.
- `Name`, `SeparateGitDir`, `RootPath` and `Clone`'s name are containment-checked: anything that
  would escape the fixture root is rejected. An explicitly named repository must not already exist.
- A clone of a bare remote from `AddBareRemote` lands under that remote's own nested fixture root.
- Run arbitrary git through `Client.Run` only with paths from `Root`/`RootPath`/`Dir`. `Run` is
  anchored at the repo and hermetic, but it cannot stop a caller from passing an outside path as an
  argument.

## Formatting

`flake.nix` imports `phillipg-nix-repo-base`'s `flakeModules.treefmt`, so formatting uses the same
treefmt program set and settings as every other workspace repo (gofumpt, nixfmt, prettier for
md/yaml/json, shellcheck, shfmt). It builds nothing, so Go consumers of this module are unaffected.

- Apply: `nix fmt`
- Check (no changes): `nix flake check` (runs `checks.treefmt`)

CI does not run a format check (deliberately unchanged). A pre-commit/prek hook module is not
adopted; the treefmt config is what a quiet pre-commit-fix hookpoint would invoke.
