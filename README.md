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
go test -race ./...
go vet ./...
```

Both also run in CI on every push (`.github/workflows/ci.yml`) — that's this repo's only gate;
there's no nix flake here.
