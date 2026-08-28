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
gate; there's no nix flake here. The `integration`/`contract` suites need a real `git` binary on
PATH (present on GitHub's `ubuntu-latest` runners); a consumer's own `nix flake check` never runs
them — only this repo's own CI does.
