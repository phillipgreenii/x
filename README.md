# x

Shared Go library for `phillipgreenii`'s own CLI tools and daemons — the plumbing common to
several small programs (structured logging, XDG directory resolution, and more as it grows),
extracted so there is one implementation instead of several independently hand-rolled ones.

This is a **plain Go module, not a nix flake.** It is consumed the same way any third-party Go
dependency is: `go get github.com/phillipgreenii/x@<commit-sha>`, then `go mod tidy`. There are
no release tags — pin a commit SHA and bump it like any other dependency.

## Packages

- `jsonllogger` — the ADR 0038 JSONL logger bootstrap (relocated from
  `phillipgreenii-nix-support-apps`'s `packages/jsonl-logger`).
- `osdirs` — XDG state/config directory resolution with the `$HOME` fallback every consumer of
  the two above needs.

## Design

See `phillipg-nix-repo-base`'s `docs/superpowers/specs/2026-08-25-go-support-design.md` for the
full rationale, scope, and phased rollout plan.

## Testing

`go test -race ./...` and `go vet ./...` (also run in CI on every push — see
`.github/workflows/ci.yml`). This repo has no nix flake, so there is no `nix flake check` here;
CI is the only gate.
