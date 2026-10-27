package cmdruntest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// recorder captures Errorf so a test can assert that the Fake failed the
// test, without failing the real one.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func call(name string, args ...string) cmdrun.Call {
	return cmdrun.Call{Name: name, Args: args}
}

func TestFirstMatchWins(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.Match("git", "status")).Return(cmdruntest.Out("specific"))
	f.On(cmdruntest.MatchName("git")).Return(cmdruntest.Out("general"))
	run := f.Runner()

	if got := string(run(context.Background(), call("git", "status")).Stdout); got != "specific" {
		t.Fatalf("got %q", got)
	}
	if got := string(run(context.Background(), call("git", "log")).Stdout); got != "general" {
		t.Fatalf("got %q", got)
	}
}

func TestNthAnswersOnlyThatCall(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.MatchName("tool")).Nth(2).Return(cmdruntest.Fail(7, "second"))
	f.On(cmdruntest.MatchName("tool")).Return(cmdruntest.Out("other"))
	run := f.Runner()

	var codes []int
	var outs []string
	for range 3 {
		res := run(context.Background(), call("tool"))
		codes = append(codes, res.Code)
		outs = append(outs, string(res.Stdout))
	}
	if fmt.Sprint(codes) != "[0 7 0]" || fmt.Sprint(outs) != "[other  other]" {
		t.Fatalf("codes=%v outs=%q", codes, outs)
	}
}

func TestTimesLimitsARuleThenFallsThrough(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.MatchName("tool")).Times(2).Return(cmdruntest.Out("early"))
	f.On(cmdruntest.MatchName("tool")).Return(cmdruntest.Out("late"))
	run := f.Runner()

	var outs []string
	for range 3 {
		outs = append(outs, string(run(context.Background(), call("tool")).Stdout))
	}
	if strings.Join(outs, ",") != "early,early,late" {
		t.Fatalf("outs = %v", outs)
	}
}

func TestSeqAnswersInOrderThenStopsMatching(t *testing.T) {
	rec := &recorder{TB: t}
	f := cmdruntest.New(rec)
	f.On(cmdruntest.MatchName("poll")).Seq(cmdruntest.Out("a"), cmdruntest.Out("b"))
	run := f.Runner()

	var outs []string
	for range 2 {
		outs = append(outs, string(run(context.Background(), call("poll")).Stdout))
	}
	if strings.Join(outs, ",") != "a,b" || len(rec.errs) != 0 {
		t.Fatalf("outs = %v errs = %v", outs, rec.errs)
	}

	res := run(context.Background(), call("poll"))
	if !errors.Is(res.StartErr, cmdruntest.ErrUnmatched) || len(rec.errs) != 1 {
		t.Fatalf("a spent sequence must fall through to unmatched: %+v errs=%v", res, rec.errs)
	}
}

func TestRespondComputesFromTheCall(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.MatchName("echo")).Respond(func(c cmdrun.Call) cmdrun.Result {
		return cmdruntest.Out(strings.Join(c.Args, " "))
	})
	res := f.Runner()(context.Background(), call("echo", "a", "b"))
	if string(res.Stdout) != "a b" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestRuleWithoutAnswerIsACleanExit(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any)
	if res := f.Runner()(context.Background(), call("x")); !res.OK() {
		t.Fatalf("res = %+v", res)
	}
}

func TestUnmatchedCallFailsTheTestLoudly(t *testing.T) {
	rec := &recorder{TB: t}
	f := cmdruntest.New(rec)
	f.On(cmdruntest.Match("git", "status")).Return(cmdruntest.Out("ok"))

	res := f.Runner()(context.Background(), call("git", "push", "--force"))

	if !errors.Is(res.StartErr, cmdruntest.ErrUnmatched) || res.Code != -1 {
		t.Fatalf("res = %+v", res)
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "push") {
		t.Fatalf("errs = %q, want one naming the call", rec.errs)
	}
	if got := f.Calls(); len(got) != 1 || got[0].Name != "git" {
		t.Fatalf("the unmatched call must still be logged: %+v", got)
	}
}

func TestMatchRequiresExactArgs(t *testing.T) {
	m := cmdruntest.Match("git", "status")
	if m(call("git", "status", "-s")) || m(call("git")) || m(call("hg", "status")) {
		t.Fatal("Match accepted a different call")
	}
	if !m(call("git", "status")) {
		t.Fatal("Match rejected its own call")
	}
}

func TestCallsAreOrderedAndCopied(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any)
	run := f.Runner()
	args := []string{"one"}
	run(context.Background(), cmdrun.Call{Name: "a", Args: args, Stdin: []byte("in"), Env: []string{"K=V"}})
	run(context.Background(), call("b"))

	args[0] = "mutated" // the caller reuses its slice
	got := f.Calls()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("calls = %+v", got)
	}
	if got[0].Args[0] != "one" || string(got[0].Stdin) != "in" || got[0].Env[0] != "K=V" {
		t.Fatalf("the log must hold its own copy: %+v", got[0])
	}
	got[0].Args[0] = "scribble"
	if f.Calls()[0].Args[0] != "one" {
		t.Fatal("Calls() exposes the internal log")
	}
}

func TestConcurrentCallsAreAllLogged(t *testing.T) {
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Out("x"))
	run := f.Runner()

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(context.Background(), call("tool", fmt.Sprint(i)))
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, c := range f.Calls() {
		seen[c.Args[0]] = true
	}
	if len(seen) != n {
		t.Fatalf("logged %d distinct calls, want %d", len(seen), n)
	}
}

func TestOutAndFail(t *testing.T) {
	if r := cmdruntest.Out("s"); string(r.Stdout) != "s" || !r.OK() {
		t.Fatalf("Out = %+v", r)
	}
	if r := cmdruntest.Fail(4, "bad"); r.Code != 4 || string(r.Stderr) != "bad" || r.OK() {
		t.Fatalf("Fail = %+v", r)
	}
}
