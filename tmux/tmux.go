// Package tmux is a small client for driving tmux sessions on a dedicated
// -L socket through a cmdrun.Runner, so tests answer in process.
//
// Seeded from the ccpool tmux client in nix-agent-support
// (packages/ccpool/internal/tmux), rewritten to take a cmdrun.Runner and a
// context on every call. The API is a starting set; callers add what they
// need (send-keys, paste, capture) as they need it.
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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	Conf   string // passed as -f; empty means -f /dev/null
	Run    cmdrun.Runner
}

// SessionState is what State reports about a session.
type SessionState struct {
	Exists bool
	Dead   bool // the pane's process has exited
}

// DeadInfo describes a dead pane.
type DeadInfo struct {
	Status, Signal int    // #{pane_dead_status}, #{pane_dead_signal}
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
	args := []string{"new-session", "-d", "-s", name, "-c", strings.ReplaceAll(cwd, "#", "##")}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, "--")
	args = append(args, argv...)
	_, err := c.run(ctx, args...)
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
// output. It is an error to call it for a pane that is still running.
func (c Client) Dead(ctx context.Context, name string, tailLines int) (DeadInfo, error) {
	out, err := c.run(ctx, "display-message", "-p", "-t", "="+name+":",
		"#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}")
	if err != nil {
		return DeadInfo{}, err
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 3 {
		return DeadInfo{}, fmt.Errorf("tmux: unexpected pane state %q", strings.TrimSpace(out))
	}
	if parts[0] != "1" {
		return DeadInfo{}, fmt.Errorf("tmux: pane of %q is not dead", name)
	}
	var d DeadInfo
	if parts[1] != "" {
		if d.Status, err = strconv.Atoi(parts[1]); err != nil {
			return DeadInfo{}, fmt.Errorf("tmux: pane_dead_status %q: %w", parts[1], err)
		}
	}
	if parts[2] != "" {
		if d.Signal, err = strconv.Atoi(parts[2]); err != nil {
			return DeadInfo{}, fmt.Errorf("tmux: pane_dead_signal %q: %w", parts[2], err)
		}
	}
	d.Tail, err = c.run(ctx, "capture-pane", "-p", "-t", "="+name+":", "-S", "-"+strconv.Itoa(tailLines))
	if err != nil {
		return DeadInfo{}, err
	}
	return d, nil
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
	conf := c.Conf
	if conf == "" {
		conf = "/dev/null"
	}
	run := c.Run
	if run == nil {
		run = cmdrun.Exec
	}
	args, err := escapeArgs(args)
	if err != nil {
		return "", err
	}
	full := append([]string{"-L", c.Socket, "-f", conf}, args...)
	res := run(ctx, cmdrun.Call{Name: c.Bin, Args: full, Timeout: callTimeout})
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
