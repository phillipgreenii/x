package jsonllogger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testApp is the app name every case below bootstraps a logger for. It is
// deliberately not a real app name: nothing here may touch a real app's state
// directory.
const testApp = "jsonl-logger-testapp"

// xdgStateHome points XDG_STATE_HOME at a fresh temp dir and returns it, so no
// case can reach the real $HOME/.local/state.
func xdgStateHome(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	return state
}

// readJSONLLines reads path and returns its non-empty JSONL lines decoded. A
// truncating or offset-0-overwriting open leaves either too few lines or
// undecodable bytes, so decoding here is part of the assertion.
func readJSONLLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode JSONL line %q from %s: %v", line, path, err)
		}
		out = append(out, decoded)
	}
	return out
}

// TestNewHonoursXDGStateHome pins WHERE the log lands when XDG_STATE_HOME is
// set: ${XDG_STATE_HOME}/<app>/<app>.jsonl, and nowhere else.
func TestNewHonoursXDGStateHome(t *testing.T) {
	state := xdgStateHome(t)
	// A HOME that must NOT be used. If the fallback branch is taken anyway the
	// log lands here instead, which the assertions below catch.
	home := t.TempDir()
	t.Setenv("HOME", home)

	log, err := New(testApp)
	if err != nil {
		t.Fatalf("New(%q) = error %v, want nil", testApp, err)
	}
	log.Info("hello")

	want := filepath.Join(state, testApp, testApp+".jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("log file not at %s: %v", want, err)
	}
	unwanted := filepath.Join(home, ".local", "state", testApp, testApp+".jsonl")
	if _, err := os.Stat(unwanted); err == nil {
		t.Fatalf("log also written to the $HOME fallback %s; XDG_STATE_HOME was set", unwanted)
	}
}

// TestNewFallsBackToHomeLocalState pins the fallback path taken when
// XDG_STATE_HOME is unset: $HOME/.local/state/<app>/<app>.jsonl.
func TestNewFallsBackToHomeLocalState(t *testing.T) {
	// t.Setenv registers restoration of the original value; Unsetenv then makes
	// the variable genuinely absent rather than present-but-empty.
	t.Setenv("XDG_STATE_HOME", "")
	if err := os.Unsetenv("XDG_STATE_HOME"); err != nil {
		t.Fatalf("unset XDG_STATE_HOME: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// If the fallback is skipped, the state root is "" and the app dir resolves
	// RELATIVE to the process cwd. Chdir first so such a write lands in a temp
	// dir instead of polluting the source tree.
	t.Chdir(t.TempDir())

	log, err := New(testApp)
	if err != nil {
		t.Fatalf("New(%q) = error %v, want nil", testApp, err)
	}
	log.Info("hello")

	want := filepath.Join(home, ".local", "state", testApp, testApp+".jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("log file not at the $HOME/.local/state fallback %s: %v", want, err)
	}
}

// TestNewErrorsWhenNeitherXDGStateHomeNorHOMEIsSet guards against silently
// falling back further to a CWD-relative path (".local/state/<app>/...")
// when HOME is also unset -- that would make the log file's location
// depend on whatever directory the process happens to be run from.
func TestNewErrorsWhenNeitherXDGStateHomeNorHOMEIsSet(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "HOME"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("unset %s: %v", v, err)
		}
	}
	t.Chdir(t.TempDir()) // belt-and-suspenders: even a regression must not write here

	if _, err := New(testApp); err == nil {
		t.Fatal("expected an error when neither XDG_STATE_HOME nor HOME is set")
	}
}

// TestNewAppendsRatherThanTruncating is the regression guard on the open flags:
// a second logger over the same path must APPEND, so the first run's lines
// survive. Losing os.O_APPEND (or gaining os.O_TRUNC) is silent data loss.
func TestNewAppendsRatherThanTruncating(t *testing.T) {
	state := xdgStateHome(t)

	first, err := New(testApp)
	if err != nil {
		t.Fatalf("New(%q) (first) = error %v, want nil", testApp, err)
	}
	first.Info("line-from-the-first-logger")

	second, err := New(testApp)
	if err != nil {
		t.Fatalf("New(%q) (second) = error %v, want nil", testApp, err)
	}
	second.Info("line-from-the-second-logger")

	path := filepath.Join(state, testApp, testApp+".jsonl")
	lines := readJSONLLines(t, path)
	want := []string{"line-from-the-first-logger", "line-from-the-second-logger"}
	if len(lines) != len(want) {
		t.Fatalf("%s holds %d JSONL lines, want %d (the second logger truncated or overwrote): %v",
			path, len(lines), len(want), lines)
	}
	for i, msg := range want {
		if got, _ := lines[i]["msg"].(string); got != msg {
			t.Fatalf("line %d msg = %q, want %q", i, got, msg)
		}
	}
}

// TestNewWritesNormalizedFields asserts New's handler applies the field
// normalization (not just that a file appears): lowercase level, UTC time.
func TestNewWritesNormalizedFields(t *testing.T) {
	state := xdgStateHome(t)

	log, err := New(testApp)
	if err != nil {
		t.Fatalf("New(%q) = error %v, want nil", testApp, err)
	}
	log.Info("hello")

	lines := readJSONLLines(t, filepath.Join(state, testApp, testApp+".jsonl"))
	if len(lines) != 1 {
		t.Fatalf("got %d JSONL lines, want 1: %v", len(lines), lines)
	}
	assertNormalizedFields(t, lines[0])
}

// TestNewErrorsWhenStateDirCannotBeCreated covers the os.MkdirAll arm: a
// regular file sits where the per-app state DIRECTORY must go, so MkdirAll
// fails. Asserting the failing op is "mkdir" is what distinguishes this arm from
// the os.OpenFile arm below — with the state path blocked, an unchecked MkdirAll
// error still surfaces later as an "open" error.
func TestNewErrorsWhenStateDirCannotBeCreated(t *testing.T) {
	state := xdgStateHome(t)
	dir := filepath.Join(state, testApp)
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatalf("write blocking file %s: %v", dir, err)
	}

	log, err := New(testApp)
	if err == nil {
		t.Fatalf("New(%q) = (%v, nil); want a non-nil error, %s is a regular file", testApp, log, dir)
	}
	if log != nil {
		t.Fatalf("New(%q) returned a non-nil logger alongside error %v", testApp, err)
	}
	assertPathError(t, err, "mkdir", dir)
}

// TestNewErrorsWhenLogFileCannotBeOpened covers the os.OpenFile arm: the state
// directory is created fine, but a DIRECTORY sits where the log FILE must go, so
// opening it for writing fails.
func TestNewErrorsWhenLogFileCannotBeOpened(t *testing.T) {
	state := xdgStateHome(t)
	logPath := filepath.Join(state, testApp, testApp+".jsonl")
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		t.Fatalf("mkdir blocking directory %s: %v", logPath, err)
	}

	log, err := New(testApp)
	if err == nil {
		t.Fatalf("New(%q) = (%v, nil); want a non-nil error, %s is a directory", testApp, log, logPath)
	}
	if log != nil {
		t.Fatalf("New(%q) returned a non-nil logger alongside error %v", testApp, err)
	}
	assertPathError(t, err, "open", logPath)
}

// TestReplaceAttrNormalizesLevelAndTime exercises ReplaceAttr (lowercase
// level, UTC time) directly against a JSONHandler writing to a buffer,
// without touching the filesystem.
func TestReplaceAttrNormalizesLevelAndTime(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: ReplaceAttr}))

	log.Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSONL line: %v (raw: %s)", err, buf.String())
	}
	assertNormalizedFields(t, decoded)
}

// assertNormalizedFields checks the level/time normalization New's handler applies.
func assertNormalizedFields(t *testing.T, decoded map[string]any) {
	t.Helper()
	level, _ := decoded["level"].(string)
	if level != "info" {
		t.Fatalf("level = %q, want lowercase %q", level, "info")
	}
	timeStr, _ := decoded["time"].(string)
	if _, err := time.Parse(time.RFC3339, timeStr); err != nil {
		t.Fatalf("time %q did not parse as RFC3339: %v", timeStr, err)
	}
	if !strings.HasSuffix(timeStr, "Z") && !strings.HasSuffix(timeStr, "+00:00") {
		t.Fatalf("time %q is not UTC (want suffix Z or +00:00)", timeStr)
	}
}

// assertPathError checks err is an fs.PathError for the given syscall op and
// path, which is how each failure arm is told apart from the other.
func assertPathError(t *testing.T, err error, wantOp, wantPath string) {
	t.Helper()
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error %v (%T) is not an *fs.PathError", err, err)
	}
	if pathErr.Op != wantOp {
		t.Fatalf("error op = %q (path %q), want %q — the error came from the wrong arm",
			pathErr.Op, pathErr.Path, wantOp)
	}
	if pathErr.Path != wantPath {
		t.Fatalf("error path = %q, want %q", pathErr.Path, wantPath)
	}
}
