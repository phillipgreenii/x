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
- **`gitfixture`** / **`gittest`** — hermetic, real git repositories for tests. Tests that need git
  MUST use these rather than hand-rolling `git init` or `exec.Command("git", ...)`. See
  [Git fixtures](#git-fixtures).

## Testing

```
go vet ./...
go test -race ./...
go test -race -tags integration,contract ./...
```

- Untagged tests are unit tests: no real git binary, no subprocess.
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
