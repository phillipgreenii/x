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

func TestEveryArgumentHasTrailingSemicolonEscaped(t *testing.T) {
	c, f := newFake(t, "/example/tmux.conf")
	f.On(cmdruntest.Any)
	err := c.NewSession(context.Background(), "example;", "/example/cwd;",
		[]string{"FOO=bar;"}, []string{"claude", "--model", ";", "Resume now;", "mid;dle"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-L", "example-sock", "-f", "/example/tmux.conf",
		"new-session", "-d", "-s", `example\;`, "-c", `/example/cwd\;`,
		"-e", `FOO=bar\;`, "--", "claude", "--model", `\;`, `Resume now\;`, "mid;dle",
	}
	if got := f.Calls()[0].Args; !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestTargetsEscapeTrailingSemicolon(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	_, _ = c.State(context.Background(), "x;")
	_ = c.Kill(context.Background(), "x;")
	for _, call := range f.Calls() {
		if !slices.Contains(call.Args, `=x\;`) && !slices.Contains(call.Args, `=x;:`) {
			t.Fatalf("call %q carries no escaped target", call.Args)
		}
	}
}

func TestCwdHashIsDoubled(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	if err := c.NewSession(context.Background(), "a", "/ex/a#b", nil, []string{"sh"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.Calls()[0].Args, "/ex/a##b") {
		t.Fatalf("args = %q", f.Calls()[0].Args)
	}
}

func TestArgumentEndingInEscapedSemicolonIsRejected(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	ctx := context.Background()
	for name, err := range map[string]error{
		"name": c.NewSession(ctx, `a\;`, "/ex", nil, []string{"sh"}),
		"cwd":  c.NewSession(ctx, "a", `/ex\;`, nil, []string{"sh"}),
		"env":  c.NewSession(ctx, "a", "/ex", []string{`K=v\;`}, []string{"sh"}),
		"argv": c.NewSession(ctx, "a", "/ex", nil, []string{"sh", `x\;`}),
		"kill": c.Kill(ctx, `a\;`),
	} {
		if !errors.Is(err, ErrUnescapable) {
			t.Errorf("%s: err = %v, want ErrUnescapable", name, err)
		}
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("tmux was called %d times", n)
	}
}

func TestUserConfOmitsConfFlag(t *testing.T) {
	c, f := newFake(t, "")
	c.UserConf = true
	f.On(cmdruntest.Any)
	if err := c.Kill(context.Background(), "example-a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-L", "example-sock", "kill-session", "-t", "=example-a"}
	if got := f.Calls()[0].Args; !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestConfBeatsUserConf(t *testing.T) {
	c, f := newFake(t, "/example/tmux.conf")
	c.UserConf = true
	f.On(cmdruntest.Any)
	_ = c.Kill(context.Background(), "example-a")
	if got := f.Calls()[0].Args; !slices.Equal(got[:4], []string{"-L", "example-sock", "-f", "/example/tmux.conf"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestNewSessionOmitsCwdWhenEmpty(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	if err := c.NewSession(context.Background(), "example-a", "", nil, []string{"sh"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()[0].Args; slices.Contains(got, "-c") {
		t.Fatalf("args = %q carry -c", got)
	}
}

func TestNewSessionRemainOnExitInSameInvocation(t *testing.T) {
	c, f := newFake(t, "")
	c.RemainOnExit = "failed"
	f.On(cmdruntest.Any)
	if err := c.NewSession(context.Background(), "example;", "/example/cwd", nil, []string{"sh", "-c", "x;"}); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := []string{
		"-L", "example-sock", "-f", "/dev/null",
		"new-session", "-d", "-s", `example\;`, "-c", "/example/cwd", "--", "sh", "-c", `x\;`,
		";", "set-option", "-t", "=example;:", "remain-on-exit", "failed",
	}
	if !slices.Equal(calls[0].Args, want) {
		t.Fatalf("args = %q, want %q", calls[0].Args, want)
	}
}

func TestPaneStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want PaneStatus
	}{
		{"alive", "0||\n", PaneStatus{Exists: true}},
		{"exit status", "1|3|\n", PaneStatus{Exists: true, Dead: true, Status: 3}},
		{"signal name", "1||term\n", PaneStatus{Exists: true, Dead: true, Signal: "term"}},
		{"signal number", "1||15\n", PaneStatus{Exists: true, Dead: true, Signal: "15"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newFake(t, "")
			f.On(cmdruntest.Any).Seq(cmdruntest.Out(""), cmdruntest.Out(tc.out))
			got, err := c.PaneStatus(context.Background(), "example-a")
			if err != nil || got != tc.want {
				t.Fatalf("PaneStatus = %+v, %v, want %+v", got, err, tc.want)
			}
			if a := f.Calls()[0].Args; !slices.Contains(a, "has-session") {
				t.Fatalf("first call = %q, want has-session first", a)
			}
		})
	}
}

func TestPaneStatusMissingAndErrors(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "no server running on /tmp/x"))
	got, err := c.PaneStatus(context.Background(), "example-a")
	if err != nil || got != (PaneStatus{}) {
		t.Fatalf("missing server: %+v, %v", got, err)
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out(""), cmdruntest.Out("garbage\n"))
	if _, err := c.PaneStatus(context.Background(), "example-a"); err == nil {
		t.Fatal("want error for malformed output")
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if _, err := c.PaneStatus(context.Background(), "example-a"); err == nil {
		t.Fatal("want error for other failure")
	}
}

func TestSendKeysArgv(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	if err := c.SendKeys(context.Background(), "example-a", "Escape", "Enter"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-L", "example-sock", "-f", "/dev/null", "send-keys", "-t", "=example-a:", "Escape", "Enter"}
	if got := f.Calls()[0].Args; !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestPasteLoadsBufferThenPastesBracketed(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	if err := c.Paste(context.Background(), "example-a", "line one\nline; two\\"); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	load, paste := calls[0], calls[1]
	if string(load.Stdin) != "line one\nline; two\\" {
		t.Fatalf("stdin = %q", load.Stdin)
	}
	buf := load.Args[len(load.Args)-2]
	if !slices.Equal(load.Args[4:], []string{"load-buffer", "-b", buf, "-"}) {
		t.Fatalf("load args = %q", load.Args)
	}
	if !slices.Equal(paste.Args[4:], []string{"paste-buffer", "-p", "-d", "-b", buf, "-t", "=example-a:"}) {
		t.Fatalf("paste args = %q", paste.Args)
	}
}

func TestPasteBufferNameUniquePerCall(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any)
	for range 2 {
		if err := c.Paste(context.Background(), "example-a", "body"); err != nil {
			t.Fatal(err)
		}
	}
	calls := f.Calls()
	a, b := calls[0].Args[len(calls[0].Args)-2], calls[2].Args[len(calls[2].Args)-2]
	if a == b {
		t.Fatalf("buffer name %q reused", a)
	}
}

func TestPasteLoadFailureSkipsPaste(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if err := c.Paste(context.Background(), "example-a", "body"); err == nil {
		t.Fatal("want error")
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestCapturePaneAndTail(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Seq(cmdruntest.Out("screen\n"), cmdruntest.Out("\n\na\nb\nc\n\n"))
	got, err := c.CapturePane(context.Background(), "example-a")
	if err != nil || got != "screen\n" {
		t.Fatalf("CapturePane = %q, %v", got, err)
	}
	tail, err := c.CaptureTail(context.Background(), "example-a", 2)
	if err != nil || tail != "b\nc" {
		t.Fatalf("CaptureTail = %q, %v", tail, err)
	}
	want := []string{"capture-pane", "-p", "-t", "=example-a:", "-S", "-2"}
	if got := f.Calls()[1].Args[4:]; !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestCaptureTailNonPositiveLinesIsOne(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("a\nb\n"))
	tail, err := c.CaptureTail(context.Background(), "example-a", 0)
	if err != nil || tail != "b" {
		t.Fatalf("CaptureTail = %q, %v", tail, err)
	}
}

func TestPaneCurrentPathTrimsNewline(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Out("/example/path with space\n"))
	got, err := c.PaneCurrentPath(context.Background(), "example-a")
	if err != nil || got != "/example/path with space" {
		t.Fatalf("PaneCurrentPath = %q, %v", got, err)
	}
	want := []string{"display-message", "-p", "-t", "=example-a:", "#{pane_current_path}"}
	if a := f.Calls()[0].Args[4:]; !slices.Equal(a, want) {
		t.Fatalf("args = %q", a)
	}
}

func TestParseListPanesKeepsOnlyActivePaneOfActiveWindow(t *testing.T) {
	out := "a:1:1:0:/p/a\n" +
		"a:1:0:0:/p/a-other-pane\n" +
		"a:0:1:0:/p/a-other-window\n" +
		"b:1:1:1:/p/with:colon and space\n" +
		"garbage\n"
	got := parseListPanes(out)
	want := map[string]PaneInfo{
		"a": {Live: true, Path: "/p/a"},
		"b": {Live: false, Path: "/p/with:colon and space"},
	}
	if len(got) != len(want) || got["a"] != want["a"] || got["b"] != want["b"] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestListPanesArgvAndNoServer(t *testing.T) {
	c, f := newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "no server running on /tmp/x"))
	got, err := c.ListPanes(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no server: %+v, %v", got, err)
	}
	want := []string{"list-panes", "-a", "-F", listPanesFormat}
	if a := f.Calls()[0].Args[4:]; !slices.Equal(a, want) {
		t.Fatalf("args = %q", a)
	}
	c, f = newFake(t, "")
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "boom"))
	if _, err := c.ListPanes(context.Background()); err == nil {
		t.Fatal("want error for a failure other than no server")
	}
}
