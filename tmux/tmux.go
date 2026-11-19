// Package tmux is a small client for driving tmux sessions on a dedicated
// -L socket through a cmdrun.Runner, so tests answer in process.
//
// Seeded from the ccpool tmux client in nix-agent-support
// (packages/ccpool/internal/tmux), rewritten to take a cmdrun.Runner and a
// context on every call. The API is a starting set; callers add what they
// need as they need it.
//
// Every call is `tmux -L <Socket> -f <Conf> ...` with a 10 second timeout, and
// session targets use `=<name>` so tmux matches the name exactly, never as a
// prefix.
//
// tmux reads an argument ending in ";" as the end of a command, so every
// argument forwarded to tmux (session names, targets, cwd, -e values, argv
// tokens) has a trailing ";" sent as `\;`, which tmux turns back into ";".
// A value that itself ends in `\;` cannot be passed on intact (tmux delivers
// `X\\;` as "X;"), so every call returns ErrUnescapable for it.
package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// ErrUnescapable is returned for an argument ending in `\;`, which tmux
// cannot deliver intact.
var ErrUnescapable = errors.New(`tmux: argument ends in \; and cannot be passed to tmux intact`)

// escapeArgs returns args with a trailing ";" escaped for tmux's parser.
func escapeArgs(args []string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case strings.HasSuffix(a, `\;`):
			return nil, fmt.Errorf("%w: %q", ErrUnescapable, a)
		case strings.HasSuffix(a, ";"):
			a = a[:len(a)-1] + `\;`
		}
		out[i] = a
	}
	return out, nil
}

const callTimeout = 10 * time.Second

// Client drives one tmux server, selected by Socket.
type Client struct {
	Bin    string // absolute path to tmux
	Socket string // -L socket name
	Conf   string // passed as -f; empty means -f /dev/null (unless UserConf)
	// UserConf leaves -f off the command line when Conf is empty, so tmux
	// reads the user's own configuration instead of /dev/null.
	UserConf bool
	// RemainOnExit, when set (for example "failed"), is applied to the new
	// pane in the same tmux invocation as NewSession, so it is in force before
	// the process can exit and a quick failure still leaves its dead pane.
	RemainOnExit string
	Run          cmdrun.Runner
}

// SessionState is what State reports about a session.
type SessionState struct {
	Exists bool
	Dead   bool // the pane's process has exited
}

// DeadInfo describes a dead pane.
type DeadInfo struct {
	Status, Signal int    // #{pane_dead_status}; the signal number (0 when none or unrecognised)
	SignalName     string // #{pane_dead_signal} as tmux prints it ("term" or a number), or ""
	Tail           string // capture-pane -p -S -<n>
}

// WriteConf writes Conf holding exactly the given lines (mode 0600).
func (c Client) WriteConf(lines ...string) error {
	if c.Conf == "" {
		return fmt.Errorf("tmux: WriteConf needs Conf set")
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(c.Conf, []byte(body), 0o600); err != nil {
		return fmt.Errorf("tmux: write conf: %w", err)
	}
	return nil
}

// NewSession starts a detached session running argv in cwd. env entries
// ("K=V") are exported at the session level with -e, in the given order.
func (c Client) NewSession(ctx context.Context, name, cwd string, env, argv []string) error {
	// tmux format-expands -c, so a literal # is sent as ##.
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", strings.ReplaceAll(cwd, "#", "##"))
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, "--")
	args = append(args, argv...)
	var tail []string
	if c.RemainOnExit != "" {
		tail = []string{"set-option", "-t", "=" + name + ":", "remain-on-exit", c.RemainOnExit}
	}
	_, err := c.exec(ctx, nil, tail, args)
	return err
}

// State reports whether the session exists and whether its pane has died.
// A missing server, a stale socket and a missing session all mean
// Exists == false, not an error.
func (c Client) State(ctx context.Context, name string) (SessionState, error) {
	// has-session first: display-message with a target that no longer exists
	// falls back to another session on the server and would report ITS pane. The pane target is "=name:" because a bare "=name" resolves no pane.
	if _, err := c.run(ctx, "has-session", "-t", "="+name); err != nil {
		if isGone(err) {
			return SessionState{}, nil
		}
		return SessionState{}, err
	}
	out, err := c.run(ctx, "display-message", "-p", "-t", "="+name+":", "#{pane_dead}")
	if err != nil {
		return SessionState{}, err
	}
	return SessionState{Exists: true, Dead: strings.TrimSpace(out) == "1"}, nil
}

// PanePID returns the pid of the session's pane process.
func (c Client) PanePID(ctx context.Context, name string) (int, error) {
	out, err := c.run(ctx, "display-message", "-p", "-t", "="+name+":", "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("tmux: pane pid %q: %w", strings.TrimSpace(out), err)
	}
	return pid, nil
}

// Dead reports how a dead pane ended and the last tailLines lines of its
// output. It is an error to call it for a pane that is still running or whose
// session does not exist. It reads the pane state through PaneStatus, so a
// pane killed by a signal (tmux prints the name, such as "term") is reported
// rather than rejected.
func (c Client) Dead(ctx context.Context, name string, tailLines int) (DeadInfo, error) {
	ps, err := c.PaneStatus(ctx, name)
	if err != nil {
		return DeadInfo{}, err
	}
	if !ps.Exists {
		return DeadInfo{}, fmt.Errorf("tmux: session %q does not exist", name)
	}
	if !ps.Dead {
		return DeadInfo{}, fmt.Errorf("tmux: pane of %q is not dead", name)
	}
	d := DeadInfo{Status: ps.Status, SignalName: ps.Signal, Signal: signalNumber(ps.Signal)}
	d.Tail, err = c.run(ctx, "capture-pane", "-p", "-t", "="+name+":", "-S", "-"+strconv.Itoa(tailLines))
	if err != nil {
		return DeadInfo{}, err
	}
	return d, nil
}

// signalNumbers maps the lower-case names tmux prints for #{pane_dead_signal}.
var signalNumbers = map[string]int{
	"hup": int(syscall.SIGHUP), "int": int(syscall.SIGINT), "quit": int(syscall.SIGQUIT),
	"ill": int(syscall.SIGILL), "trap": int(syscall.SIGTRAP), "abrt": int(syscall.SIGABRT),
	"bus": int(syscall.SIGBUS), "fpe": int(syscall.SIGFPE), "kill": int(syscall.SIGKILL),
	"usr1": int(syscall.SIGUSR1), "segv": int(syscall.SIGSEGV), "usr2": int(syscall.SIGUSR2),
	"pipe": int(syscall.SIGPIPE), "alrm": int(syscall.SIGALRM), "term": int(syscall.SIGTERM),
}

// signalNumber turns what tmux prints (a name or a number) into a signal
// number; 0 when it is empty or not recognised (SignalName still carries it).
func signalNumber(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return signalNumbers[strings.TrimPrefix(strings.ToLower(s), "sig")]
}

// List returns the session names; an absent server is an empty list.
func (c Client) List(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isGone(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// Kill removes the session; nil when it is already gone.
func (c Client) Kill(ctx context.Context, name string) error {
	if _, err := c.run(ctx, "kill-session", "-t", "="+name); err != nil && !isGone(err) {
		return err
	}
	return nil
}

// PaneStatus is what the server knows about a session's pane.
type PaneStatus struct {
	Exists bool   // false when the session is not on the socket
	Dead   bool   // the pane's process has ended
	Status int    // exit status, valid when Dead and Signal == ""
	Signal string // the signal that ended it as tmux prints it ("term" or a number), or ""
}

// PaneStatus reports whether the session exists and, if so, how its pane's
// process ended. has-session gates the pane query because display-message
// falls back to another session for a target that does not exist. A missing
// server or session is Exists == false, not an error.
func (c Client) PaneStatus(ctx context.Context, name string) (PaneStatus, error) {
	if _, err := c.run(ctx, "has-session", "-t", "="+name); err != nil {
		if isGone(err) {
			return PaneStatus{}, nil
		}
		return PaneStatus{}, err
	}
	out, err := c.run(ctx, "display-message", "-p", "-t", "="+name+":",
		"#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}")
	if err != nil {
		return PaneStatus{}, err
	}
	// Split on every "|" and never Fields: an empty status (a signal death) or
	// an empty signal (an exit) keeps its position.
	parts := strings.Split(strings.TrimRight(out, "\n"), "|")
	if len(parts) != 3 {
		return PaneStatus{}, fmt.Errorf("tmux: unexpected pane state %q", strings.TrimSpace(out))
	}
	ps := PaneStatus{Exists: true, Dead: parts[0] == "1", Signal: strings.TrimSpace(parts[2])}
	if parts[1] != "" {
		if ps.Status, err = strconv.Atoi(parts[1]); err != nil {
			return PaneStatus{}, fmt.Errorf("tmux: pane_dead_status %q: %w", parts[1], err)
		}
	}
	return ps, nil
}

// SendKeys sends key tokens (for example "Enter", "Escape") to the session's
// pane; each token is a key name or literal text, as tmux send-keys reads it.
func (c Client) SendKeys(ctx context.Context, name string, keys ...string) error {
	_, err := c.run(ctx, append([]string{"send-keys", "-t", "=" + name + ":"}, keys...)...)
	return err
}

// Paste delivers body to the session by bracketed paste, so a multi-line or
// special-character body arrives as one message instead of being read as keys.
// The caller sends Enter separately to submit. The tmux buffer name is unique
// per call, because paste-buffer -d deletes the buffer and two concurrent
// pastes sharing one name would deliver the wrong body.
func (c Client) Paste(ctx context.Context, name, body string) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("tmux: paste nonce: %w", err)
	}
	buf := "x-paste-" + hex.EncodeToString(nonce[:])
	if _, err := c.exec(ctx, []byte(body), nil, []string{"load-buffer", "-b", buf, "-"}); err != nil {
		return err
	}
	_, err := c.run(ctx, "paste-buffer", "-p", "-d", "-b", buf, "-t", "="+name+":")
	return err
}

// CapturePane returns the visible text of the session's pane.
func (c Client) CapturePane(ctx context.Context, name string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-t", "="+name+":")
}

// CaptureTail returns the last lines lines of the session's pane output,
// scrollback included, with blank lines at both ends trimmed.
func (c Client) CaptureTail(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 1
	}
	out, err := c.run(ctx, "capture-pane", "-p", "-t", "="+name+":", "-S", "-"+strconv.Itoa(lines))
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.Trim(out, " \t\r\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}

// PaneCurrentPath returns the live working directory of the session's pane.
func (c Client) PaneCurrentPath(ctx context.Context, name string) (string, error) {
	out, err := c.run(ctx, "display-message", "-p", "-t", "="+name+":", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// PaneInfo is one session's liveness and live working directory.
type PaneInfo struct {
	Live bool   // the active pane's process is still running
	Path string // the active pane's current path
}

// listPanesFormat is one record per pane. The separator is ":" and not a tab:
// a tmux client outside a UTF-8 locale prints every control character in a
// format result as "_", which would collapse a tab-separated record. ":" is
// safe for the name because tmux rewrites ":" and "." in a session name to
// "_". The path is last because it is the only field that can hold the
// separator, and the parse splits into at most five fields.
const listPanesFormat = "#{session_name}:#{window_active}:#{pane_active}:#{pane_dead}:#{pane_current_path}"

// ListPanes reads every session's liveness and active-pane path in one tmux
// launch. Per session it keeps the active pane of the active window, which is
// the pane a per-session query resolves. An absent server is an empty map.
func (c Client) ListPanes(ctx context.Context) (map[string]PaneInfo, error) {
	out, err := c.run(ctx, "list-panes", "-a", "-F", listPanesFormat)
	if err != nil {
		if isGone(err) {
			return map[string]PaneInfo{}, nil
		}
		return nil, err
	}
	return parseListPanes(out), nil
}

func parseListPanes(out string) map[string]PaneInfo {
	res := map[string]PaneInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, ":", 5)
		if len(f) != 5 || f[0] == "" || f[1] != "1" || f[2] != "1" {
			continue
		}
		res[f[0]] = PaneInfo{Live: f[3] != "1", Path: f[4]}
	}
	return res
}

// goneError is a tmux failure that means "no such server or session".
type goneError struct{ err error }

func (g goneError) Error() string { return g.err.Error() }
func (g goneError) Unwrap() error { return g.err }

func isGone(err error) bool {
	_, ok := err.(goneError)
	return ok
}

// run executes one tmux verb and returns its stdout. A non-zero exit whose
// message says the server or session is absent comes back as a goneError.
func (c Client) run(ctx context.Context, args ...string) (string, error) {
	return c.exec(ctx, nil, nil, args)
}

// exec runs one tmux invocation: args, then (when tail is non-empty) a ";"
// command separator and a second command. Both are escaped separately, so the
// separator itself stays unescaped. stdin is fed to the child when non-nil.
func (c Client) exec(ctx context.Context, stdin []byte, tail, args []string) (string, error) {
	run := c.Run
	if run == nil {
		run = cmdrun.Exec
	}
	args, err := escapeArgs(args)
	if err != nil {
		return "", err
	}
	var full []string
	full = append(full, "-L", c.Socket)
	switch {
	case c.Conf != "":
		full = append(full, "-f", c.Conf)
	case !c.UserConf:
		full = append(full, "-f", "/dev/null")
	}
	full = append(full, args...)
	if len(tail) > 0 {
		tail, err = escapeArgs(tail)
		if err != nil {
			return "", err
		}
		full = append(full, ";")
		full = append(full, tail...)
	}
	res := run(ctx, cmdrun.Call{Name: c.Bin, Args: full, Stdin: stdin, Timeout: callTimeout})
	verb := args[0]
	switch {
	case res.CtxErr != nil:
		return "", fmt.Errorf("tmux %s: %w", verb, res.CtxErr)
	case res.StartErr != nil:
		return "", fmt.Errorf("tmux %s: %w", verb, res.StartErr)
	case res.OK():
		return string(res.Stdout), nil
	}
	msg := strings.TrimSpace(string(res.Stderr))
	err = fmt.Errorf("tmux %s: exit %d (signal %q): %s", verb, res.Code, res.Signal, msg)
	for _, gone := range []string{"no server running", "error connecting to", "can't find session"} {
		if strings.Contains(msg, gone) {
			return "", goneError{err}
		}
	}
	return "", err
}
