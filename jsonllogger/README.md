# jsonllogger

The ADR 0038 JSONL logger bootstrap, shared across `phillipgreenii`'s own CLI tools and daemons.

`jsonllogger.New("<app>")` returns a `*slog.Logger` that appends one JSON object per line to
`${XDG_STATE_HOME}/<app>/<app>.jsonl` (falling back to `$HOME/.local/state` when
`XDG_STATE_HOME` is unset), with the level lowercased and the timestamp forced to UTC.

It exists because this bootstrap was previously copy-pasted into every binary that needed it,
with no test coverage in any copy: an `O_APPEND` -> `O_TRUNC` regression in one copy would have
silently discarded each run's prior log (`phillipgreenii-nix-support-apps` bead `pg2-70l4r`).
