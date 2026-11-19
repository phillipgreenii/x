//go:build integration

package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func realClient(t *testing.T) (Client, string) {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		if os.Getenv("X_REQUIRE_TOOLS") == "1" {
			t.Fatalf("tmux not found and X_REQUIRE_TOOLS=1: %v", err)
		}
		t.Skip("tmux not on PATH")
	}
	// A short path: the tmux socket path under TMUX_TMPDIR must fit sun_path.
	dir, err := os.MkdirTemp("/tmp", "xt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	var b [4]byte
	_, _ = rand.Read(b[:])
	c := Client{
		Bin:    bin,
		Socket: "x-tmux-test-" + hex.EncodeToString(b[:]),
		Conf:   filepath.Join(dir, "tmux.conf"),
	}
	if err := c.WriteConf("set -g remain-on-exit on"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(bin, "-L", c.Socket, "kill-server").Run()
	})
	return c, dir
}

func waitDead(t *testing.T, c Client, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := c.State(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if st == (SessionState{Exists: true, Dead: true}) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s did not die within 10s", name)
}

func TestRealLifecycle(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	script := `echo $$ > ` + dir + `/pid; echo "$X" > ` + dir + `/x; exec sleep 30`
	if err := c.NewSession(ctx, "example-a", dir, []string{"X=1"}, []string{"sh", "-c", script}); err != nil {
		t.Fatal(err)
	}
	st, err := c.State(ctx, "example-a")
	if err != nil || st != (SessionState{Exists: true}) {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
	var pidFile []byte
	for i := 0; i < 100; i++ {
		pidFile, _ = os.ReadFile(filepath.Join(dir, "pid"))
		if strings.Contains(string(pidFile), "\n") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pid, err := c.PanePID(ctx, "example-a")
	if err != nil {
		t.Fatal(err)
	}
	if strconv.Itoa(pid) != strings.TrimSpace(string(pidFile)) {
		t.Fatalf("PanePID = %d, pid file = %q", pid, pidFile)
	}
	x, _ := os.ReadFile(filepath.Join(dir, "x"))
	if strings.TrimSpace(string(x)) != "1" {
		t.Fatalf("env X = %q, want 1", x)
	}
	if err := c.Kill(ctx, "example-a"); err != nil {
		t.Fatal(err)
	}
	st, err = c.State(ctx, "example-a")
	if err != nil || st.Exists {
		t.Fatalf("after kill state = %+v, err = %v", st, err)
	}
}

func TestRealExitLeavesDeadPane(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	if err := c.NewSession(ctx, "example-a", dir, nil, []string{"sh", "-c", "echo boom >&2; exit 3"}); err != nil {
		t.Fatal(err)
	}
	waitDead(t, c, "example-a")
	d, err := c.Dead(ctx, "example-a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != 3 || !strings.Contains(d.Tail, "boom") {
		t.Fatalf("dead = %+v", d)
	}
	if err := c.NewSession(ctx, "example-a", dir, nil, []string{"sleep", "30"}); err == nil {
		t.Fatal("same-name NewSession must fail while the dead pane remains")
	}
	if err := c.Kill(ctx, "example-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.NewSession(ctx, "example-a", dir, nil, []string{"sleep", "30"}); err != nil {
		t.Fatalf("NewSession after Kill: %v", err)
	}
}

func TestRealStateMissingWithAnotherAlive(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	if err := c.NewSession(ctx, "example-b", dir, nil, []string{"sleep", "30"}); err != nil {
		t.Fatal(err)
	}
	st, err := c.State(ctx, "example-a")
	if err != nil || st.Exists {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
}

func TestRealExactMatchNotPrefix(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	if err := c.NewSession(ctx, "example-ab", dir, nil, []string{"sleep", "30"}); err != nil {
		t.Fatal(err)
	}
	st, err := c.State(ctx, "example-a")
	if err != nil || st.Exists {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
	if err := c.Kill(ctx, "example-a"); err != nil {
		t.Fatal(err)
	}
	st, err = c.State(ctx, "example-ab")
	if err != nil || !st.Exists {
		t.Fatalf("example-ab state = %+v, err = %v", st, err)
	}
}

func TestRealList(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	for _, n := range []string{"example-a", "example-b"} {
		if err := c.NewSession(ctx, n, dir, nil, []string{"sleep", "30"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"example-a", "example-b"}) {
		t.Fatalf("list = %q", got)
	}
	if out, err := exec.Command(c.Bin, "-L", c.Socket, "kill-server").CombinedOutput(); err != nil {
		t.Fatalf("kill-server: %v (%s)", err, out)
	}
	got, err = c.List(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("after kill-server list = %q, err = %v", got, err)
	}
}

// Real tmux: arguments ending in ";" arrive intact and a # in the cwd does
// not send the pane to $HOME.
func TestRealSemicolonAndHashArguments(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	cwd := filepath.Join(dir, "a#b;")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	script := `pwd -P > ` + out + `; printf '%s\n' "$FOO" "$@" >> ` + out + `; exec sleep 30`
	if err := c.NewSession(ctx, "name;", cwd, []string{"FOO=bar;"},
		[]string{"sh", "-c", script, "sh", "--model", "Resume now;", ";"}); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for i := 0; i < 100; i++ {
		b, _ = os.ReadFile(out)
		if strings.Count(string(b), "\n") >= 5 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	wantCwd, _ := filepath.EvalSymlinks(cwd)
	want := wantCwd + "\nbar;\n--model\nResume now;\n;\n"
	if string(b) != want {
		t.Fatalf("pane saw %q, want %q", b, want)
	}
	names, err := c.List(ctx)
	if err != nil || !slices.Equal(names, []string{"name;"}) {
		t.Fatalf("List = %q, %v", names, err)
	}
	st, err := c.State(ctx, "name;")
	if err != nil || !st.Exists {
		t.Fatalf("State = %+v, %v", st, err)
	}
	if err := c.Kill(ctx, "name;"); err != nil {
		t.Fatal(err)
	}
}

func TestRealRemainOnExitFailedKeepsOnlyFailures(t *testing.T) {
	c, dir := realClient(t)
	c.Conf, c.RemainOnExit = "", "failed"
	ctx := context.Background()
	if err := c.NewSession(ctx, "example-fail", dir, nil, []string{"sh", "-c", "echo boom; exit 3"}); err != nil {
		t.Fatal(err)
	}
	if err := c.NewSession(ctx, "example-ok", dir, nil, []string{"sh", "-c", "exit 0"}); err != nil {
		t.Fatal(err)
	}
	waitDead(t, c, "example-fail")
	ps, err := c.PaneStatus(ctx, "example-fail")
	if err != nil || ps != (PaneStatus{Exists: true, Dead: true, Status: 3}) {
		t.Fatalf("PaneStatus = %+v, %v", ps, err)
	}
	tail, err := c.CaptureTail(ctx, "example-fail", 50)
	if err != nil || !strings.Contains(tail, "boom") {
		t.Fatalf("CaptureTail = %q, %v", tail, err)
	}
	for i := 0; i < 100; i++ {
		if ps, _ = c.PaneStatus(ctx, "example-ok"); !ps.Exists {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a clean exit must destroy the session under remain-on-exit failed")
}

func TestRealPasteSendKeysCaptureAndPaths(t *testing.T) {
	c, dir := realClient(t)
	ctx := context.Background()
	out := filepath.Join(dir, "got")
	if err := c.NewSession(ctx, "example-a", dir, nil, []string{"sh", "-c", "cat > " + out}); err != nil {
		t.Fatal(err)
	}
	body := "line one\nsemi; colon\\ back #hash"
	if err := c.Paste(ctx, "example-a", body); err != nil {
		t.Fatal(err)
	}
	if err := c.SendKeys(ctx, "example-a", "Enter"); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i := 0; i < 100; i++ {
		got, _ = os.ReadFile(out)
		if strings.Contains(string(got), "#hash") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(got), "semi; colon\\ back #hash") {
		t.Fatalf("pane read %q", got)
	}
	if _, err := c.CapturePane(ctx, "example-a"); err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	p, err := c.PaneCurrentPath(ctx, "example-a")
	if gotDir, _ := filepath.EvalSymlinks(p); err != nil || gotDir != wantDir {
		t.Fatalf("PaneCurrentPath = %q, %v, want %q", p, err, wantDir)
	}
	panes, err := c.ListPanes(ctx)
	if err != nil || !panes["example-a"].Live {
		t.Fatalf("ListPanes = %+v, %v", panes, err)
	}
}

func TestRealListPanesNoServer(t *testing.T) {
	c, _ := realClient(t)
	panes, err := c.ListPanes(context.Background())
	if err != nil || len(panes) != 0 {
		t.Fatalf("ListPanes = %+v, %v", panes, err)
	}
}

func TestRealSignalDeathDead(t *testing.T) {
	for _, tc := range []struct {
		sig, name string
		num       int
	}{{"TERM", "term", 15}, {"KILL", "kill", 9}} {
		sig := tc.sig
		t.Run(sig, func(t *testing.T) {
			c, dir := realClient(t)
			ctx := context.Background()
			script := `echo $$ > ` + dir + `/pid; exec sleep 30`
			if err := c.NewSession(ctx, "example-a", dir, nil, []string{"sh", "-c", script}); err != nil {
				t.Fatal(err)
			}
			var pid string
			for i := 0; i < 100 && !strings.Contains(pid, "\n"); i++ {
				b, _ := os.ReadFile(filepath.Join(dir, "pid"))
				pid = string(b)
				time.Sleep(50 * time.Millisecond)
			}
			if err := exec.Command("kill", "-"+sig, strings.TrimSpace(pid)).Run(); err != nil {
				t.Fatal(err)
			}
			waitDead(t, c, "example-a")
			d, err := c.Dead(ctx, "example-a", 20)
			if err != nil {
				t.Fatalf("Dead after SIG%s: %v", sig, err)
			}
			if d.SignalName != tc.name || d.Signal != tc.num || d.Status != 0 {
				t.Fatalf("dead = %+v, want signal %s (%d)", d, tc.name, tc.num)
			}
		})
	}
}
