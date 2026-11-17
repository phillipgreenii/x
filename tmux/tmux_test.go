// Seeded from the ccpool tmux client in nix-agent-support
// (packages/ccpool/internal/tmux), rewritten to take a cmdrun.Runner.

package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func newFake(t *testing.T, conf string) (Client, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	return Client{Bin: "/example/bin/tmux", Socket: "example-sock", Conf: conf, Run: f.Runner()}, f
}

func TestNewSessionArgv(t *testing.T) {
	c, f := newFake(t, "/example/tmux.conf")
	f.On(cmdruntest.Any)
	err := c.NewSession(context.Background(), "example-a", "/example/cwd",
		[]string{"Z=1", "A=2"}, []string{"claude", "--flag"})
	if err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := []string{
		"-L", "example-sock", "-f", "/example/tmux.conf",
		"new-session", "-d", "-s", "example-a", "-c", "/example/cwd",
		"-e", "Z=1", "-e", "A=2", "--", "claude", "--flag",
	}
	if calls[0].Name != "/example/bin/tmux" || !slices.Equal(calls[0].Args, want) {
		t.Fatalf("call = %s %q, want %q", calls[0].Name, calls[0].Args, want)
	}
	if calls[0].Timeout != 10*time.Second {
		t.Fatalf("timeout = %v, want 10s", calls[0].Timeout)
	}
}

func TestNewSessionFailure(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "duplicate session: example-a"))
	err := c.NewSession(context.Background(), "example-a", "/example/cwd", nil, []string{"sh"})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestEmptyConfIsDevNull(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	if err := c.Kill(context.Background(), "example-a"); err != nil {
		t.Fatal(err)
	}
	args := f.Calls()[0].Args
	if !slices.Equal(args[:4], []string{"-L", "example-sock", "-f", "/dev/null"}) {
		t.Fatalf("args = %q", args)
	}
}

func TestStateChecksHasSessionFirst(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out(""), cmdruntest.Out("0\n"))
	st, err := c.State(context.Background(), "example-a")
	if err != nil {
		t.Fatal(err)
	}
	if st != (SessionState{Exists: true}) {
		t.Fatalf("state = %+v", st)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if !slices.Equal(calls[0].Args[4:], []string{"has-session", "-t", "=example-a"}) {
		t.Fatalf("first call = %q", calls[0].Args)
	}
	if !slices.Equal(calls[1].Args[4:], []string{"display-message", "-p", "-t", "=example-a:", "#{pane_dead}"}) {
		t.Fatalf("second call = %q", calls[1].Args)
	}
}

func TestStatePaneDead(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out(""), cmdruntest.Out("1\n"))
	st, err := c.State(context.Background(), "example-a")
	if err != nil {
		t.Fatal(err)
	}
	if st != (SessionState{Exists: true, Dead: true}) {
		t.Fatalf("state = %+v", st)
	}
}

func TestStateNoServer(t *testing.T) {
	for _, msg := range []string{
		"no server running on /tmp/example/default",
		"error connecting to /tmp/example/default (No such file or directory)",
		"can't find session: example-a",
	} {
		c, f := newFake(t, "")
		f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, msg))
		st, err := c.State(context.Background(), "example-a")
		if err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
		if st != (SessionState{}) {
			t.Fatalf("%q: state = %+v", msg, st)
		}
		if n := len(f.Calls()); n != 1 {
			t.Fatalf("%q: calls = %d, want 1 (no display-message)", msg, n)
		}
	}
}

func TestStateOtherErrorPropagates(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "protocol version mismatch"))
	if _, err := c.State(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
}

func TestStateDisplayError(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out(""), cmdruntest.Fail(1, "boom"))
	if _, err := c.State(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
}

func TestPanePID(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("4242\n"))
	pid, err := c.PanePID(context.Background(), "example-a")
	if err != nil || pid != 4242 {
		t.Fatalf("pid = %d, err = %v", pid, err)
	}
	if !slices.Equal(f.Calls()[0].Args[4:], []string{"display-message", "-p", "-t", "=example-a:", "#{pane_pid}"}) {
		t.Fatalf("args = %q", f.Calls()[0].Args)
	}
}

func TestPanePIDBadOutput(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("nope\n"))
	if _, err := c.PanePID(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
}

func TestPanePIDError(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if _, err := c.PanePID(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
}

func TestDeadReadsStatusAndTail(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out("1|3|\n"), cmdruntest.Out("boom\n"))
	d, err := c.Dead(context.Background(), "example-a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != 3 || d.Signal != 0 || d.Tail != "boom\n" {
		t.Fatalf("dead = %+v", d)
	}
	calls := f.Calls()
	if !slices.Equal(calls[0].Args[4:], []string{
		"display-message", "-p", "-t", "=example-a:",
		"#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}",
	}) {
		t.Fatalf("first = %q", calls[0].Args)
	}
	if !slices.Equal(calls[1].Args[4:], []string{"capture-pane", "-p", "-t", "=example-a:", "-S", "-20"}) {
		t.Fatalf("second = %q", calls[1].Args)
	}
}

func TestDeadSignal(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out("1||9\n"), cmdruntest.Out(""))
	d, err := c.Dead(context.Background(), "example-a", 5)
	if err != nil || d.Signal != 9 || d.Status != 0 {
		t.Fatalf("dead = %+v, err = %v", d, err)
	}
}

func TestDeadOnLivePaneIsError(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("0||\n"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want error for a live pane")
	}
}

func TestDeadBadOutputAndErrors(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("garbage\n"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want parse error")
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("1|x|\n"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want status parse error")
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("1||y\n"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want signal parse error")
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want display error")
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out("1|3|\n"), cmdruntest.Fail(1, "boom"))
	if _, err := c.Dead(context.Background(), "example-a", 5); err == nil {
		t.Fatal("want capture error")
	}
}

func TestListNoServerIsEmpty(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "no server running on /tmp/example/default"))
	got, err := c.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestListSessions(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("example-a\nexample-b\n"))
	got, err := c.List(context.Background())
	if err != nil || !slices.Equal(got, []string{"example-a", "example-b"}) {
		t.Fatalf("got %q, err %v", got, err)
	}
	if !slices.Equal(f.Calls()[0].Args[4:], []string{"list-sessions", "-F", "#{session_name}"}) {
		t.Fatalf("args = %q", f.Calls()[0].Args)
	}
}

func TestListError(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestKillWhenGone(t *testing.T) {
	for _, msg := range []string{"can't find session: example-a", "no server running on x", "error connecting to x (No such file or directory)"} {
		c, f := newFake(t, "")
		f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, msg))
		if err := c.Kill(context.Background(), "example-a"); err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
	}
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if err := c.Kill(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
	if !slices.Equal(f.Calls()[0].Args[4:], []string{"kill-session", "-t", "=example-a"}) {
		t.Fatalf("args = %q", f.Calls()[0].Args)
	}
}

func TestCancelledContext(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdrun.Result{Code: -1, CtxErr: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.State(ctx, "example-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("State err = %v", err)
	}
	if err := c.Kill(ctx, "example-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Kill err = %v", err)
	}
	if _, err := c.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List err = %v", err)
	}
}

func TestStartError(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdrun.Result{Code: -1, StartErr: errors.New("not found")})
	if err := c.Kill(context.Background(), "example-a"); err == nil {
		t.Fatal("want error")
	}
}

func TestNilRunDefaultsToExec(t *testing.T) {
	c := Client{Bin: "/nonexistent/example-tmux", Socket: "example-sock"}
	if err := c.Kill(context.Background(), "example-a"); err == nil {
		t.Fatal("want start error from Exec")
	}
}

func TestWriteConf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmux.conf")
	c := Client{Conf: path}
	if err := c.WriteConf("set -g remain-on-exit on", "set -g mouse off"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "set -g remain-on-exit on\nset -g mouse off\n" {
		t.Fatalf("conf = %q", b)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if err := (Client{}).WriteConf("x"); err == nil {
		t.Fatal("want error for empty Conf")
	}
	if err := (Client{Conf: filepath.Join(t.TempDir(), "no", "dir", "c")}).WriteConf("x"); err == nil {
		t.Fatal("want error for unwritable path")
	}
}

func TestCallsBounded(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, ".")
}
