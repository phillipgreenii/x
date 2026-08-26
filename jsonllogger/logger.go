// Package jsonllogger provides structured JSONL logging for CLI tools and
// daemons: New("app") returns a *slog.Logger that appends one JSON object
// per line to a per-app log file, with the level lowercased and the
// timestamp forced to UTC.
package jsonllogger

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// New returns a *slog.Logger writing structured JSONL (lowercase level,
// UTC time) to ${XDG_STATE_HOME}/<app>/<app>.jsonl, falling back to
// $HOME/.local/state when XDG_STATE_HOME is unset. It returns an error
// rather than falling back further if HOME is also unset: silently
// resolving to a bare ".local/state/<app>/<app>.jsonl" would make the log
// file's location depend on whatever directory the process happens to be
// run from.
//
// The file is APPENDED to, never truncated, so restarting the app keeps the
// previous run's log. It stays open for the process lifetime (the logger owns
// it; there is nothing to close on a long-running daemon).
func New(app string) (*slog.Logger, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return nil, errors.New("jsonllogger: neither XDG_STATE_HOME nor HOME is set")
		}
		state = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(state, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, app+".jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	// slog defaults to local-time and mixed-case level names, so both are
	// normalized by ReplaceAttr.
	h := slog.NewJSONHandler(f, &slog.HandlerOptions{ReplaceAttr: ReplaceAttr})
	return slog.New(h), nil
}

// ReplaceAttr normalizes slog output to this package's JSONL contract:
// lowercase level names and UTC time values. New wires it in for the file logger;
// it is exported so a caller building its own handler over another writer gets
// the same contract.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.LevelKey:
		a.Value = slog.StringValue(strings.ToLower(a.Value.Any().(slog.Level).String()))
	case slog.TimeKey:
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}
