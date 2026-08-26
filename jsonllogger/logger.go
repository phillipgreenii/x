// Package jsonllogger constructs the ADR 0038 JSONL logger that the
// support-apps Go binaries write their structured log to.
//
// It is a sibling library module (ADR 0008 §"Case B", go-builders "Pattern B")
// rather than a helper in each binary because this bootstrap was previously
// duplicated verbatim in every command that needed it, with no test coverage in
// any copy — so an os.O_APPEND -> os.O_TRUNC regression in one of them would
// have silently discarded each run's prior log (bead pg2-70l4r).
package jsonllogger

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// New returns a *slog.Logger writing structured JSONL (ADR 0038: lowercase
// level, UTC time) to ${XDG_STATE_HOME}/<app>/<app>.jsonl, falling back to
// $HOME/.local/state when XDG_STATE_HOME is unset. That path is what
// phillipgreenii.observability.logSources.<app> points Loki at.
//
// The file is APPENDED to, never truncated, so restarting the app keeps the
// previous run's log. It stays open for the process lifetime (the logger owns
// it; there is nothing to close on a long-running daemon).
func New(app string) (*slog.Logger, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(os.Getenv("HOME"), ".local", "state")
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
	// ADR 0038: lowercase level, time/level/msg keys, UTC RFC3339 time. slog
	// defaults to local-time and mixed-case level names, so both are
	// normalized by ReplaceAttr.
	h := slog.NewJSONHandler(f, &slog.HandlerOptions{ReplaceAttr: ReplaceAttr})
	return slog.New(h), nil
}

// ReplaceAttr normalizes slog output to the ADR 0038 JSONL contract: lowercase
// level names and UTC time values. New wires it in for the file logger;
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
