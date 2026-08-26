# jsonllogger

Structured JSONL logging for a CLI tool or daemon.

`jsonllogger.New("<app>")` returns a `*slog.Logger` that appends one JSON object per line to
`${XDG_STATE_HOME}/<app>/<app>.jsonl` (falling back to `$HOME/.local/state` when
`XDG_STATE_HOME` is unset). Each line's level is lowercased and its timestamp is forced to UTC,
so output is consistent regardless of platform or timezone.

Use it when a program needs a durable, appendable log file rather than plain stdout/stderr — for
example, a daemon whose logs should survive restarts and be tailed or shipped elsewhere. The file
is opened once and kept open for the process's lifetime; there's nothing to close.
