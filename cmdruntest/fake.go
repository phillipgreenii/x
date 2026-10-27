// Package cmdruntest is the *testing.T adapter over cmdrun: the in-process
// Fake that tests pass wherever production code takes a cmdrun.Runner, and
// RunContract, the suite every real Runner must pass. Like gittest it is the
// only package in its pair that imports "testing".
package cmdruntest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
)

// ErrUnmatched is the StartErr a Fake returns for a call no rule answers.
// The same call also fails the test, so it cannot be swallowed.
var ErrUnmatched = errors.New("cmdruntest: no rule matches this call")

// Fake is an in-process cmdrun.Runner: calls are recorded in order and
// answered from rules, with no process involved. It is safe for concurrent
// use (a runner is often called from goroutines).
type Fake struct {
	t     testing.TB
	mu    sync.Mutex
	rules []*Rule
	calls []cmdrun.Call
}

// New returns an empty Fake that reports an unmatched call on t.
func New(t testing.TB) *Fake {
	t.Helper()
	return &Fake{t: t}
}

// Rule answers the calls its matcher accepts. Rules are tried in the order
// they were added and the first one still eligible wins. A Rule counts the
// calls it is offered (those no earlier rule answered) that its matcher
// accepts, 1-based; Times, Nth and Seq restrict which of those counts it
// answers, and a call outside them falls through to the next rule.
type Rule struct {
	match func(cmdrun.Call) bool
	fn    func(cmdrun.Call) cmdrun.Result
	seq   []cmdrun.Result
	times int
	nth   int
	count int
}

// On adds a rule for calls accepted by match. Until Return, Respond or Seq
// is called the rule answers with a zero Result (a clean exit 0 with no
// output).
func (f *Fake) On(match func(cmdrun.Call) bool) *Rule {
	r := &Rule{match: match}
	f.mu.Lock()
	f.rules = append(f.rules, r)
	f.mu.Unlock()
	return r
}

// Return makes the rule answer every eligible call with res.
func (r *Rule) Return(res cmdrun.Result) *Rule {
	r.fn = func(cmdrun.Call) cmdrun.Result { return res }
	return r
}

// Respond makes the rule answer every eligible call with fn's result. fn
// runs under the Fake's lock: it MUST NOT call back into the Fake.
func (r *Rule) Respond(fn func(cmdrun.Call) cmdrun.Result) *Rule {
	r.fn = fn
	return r
}

// Seq makes the rule answer its 1st, 2nd, ... matching call with results[0],
// results[1], ...; once the sequence is spent the rule no longer matches.
func (r *Rule) Seq(results ...cmdrun.Result) *Rule {
	r.seq = slices.Clone(results)
	return r
}

// Times limits the rule to its first n matching calls.
func (r *Rule) Times(n int) *Rule {
	r.times = n
	return r
}

// Nth limits the rule to its n-th matching call (1-based) only.
func (r *Rule) Nth(n int) *Rule {
	r.nth = n
	return r
}

func (r *Rule) eligible() bool {
	if r.nth > 0 && r.count != r.nth {
		return false
	}
	if r.times > 0 && r.count > r.times {
		return false
	}
	if r.seq != nil && r.count > len(r.seq) {
		return false
	}
	return true
}

func (r *Rule) answer(c cmdrun.Call) cmdrun.Result {
	switch {
	case r.seq != nil:
		return r.seq[r.count-1]
	case r.fn != nil:
		return r.fn(c)
	}
	return cmdrun.Result{}
}

// Runner returns the Fake as a cmdrun.Runner. A call no rule answers fails
// the test (t.Errorf, so it is safe from any goroutine) and returns a Result
// with StartErr ErrUnmatched and Code -1. A context already done is NOT
// special: the Fake answers from its rules, so a test of cancellation
// handling scripts the cancelled Result itself.
func (f *Fake) Runner() cmdrun.Runner {
	return func(_ context.Context, c cmdrun.Call) cmdrun.Result {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, cloneCall(c))
		for _, r := range f.rules {
			if !r.match(c) {
				continue
			}
			r.count++
			if r.eligible() {
				return r.answer(c)
			}
		}
		f.t.Helper()
		f.t.Errorf("cmdruntest: unmatched call %s", describe(c))
		return cmdrun.Result{Code: -1, StartErr: ErrUnmatched}
	}
}

// Calls returns every call made so far, in order, including unmatched ones.
// The slice and the calls in it are copies.
func (f *Fake) Calls() []cmdrun.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]cmdrun.Call, len(f.calls))
	for i, c := range f.calls {
		out[i] = cloneCall(c)
	}
	return out
}

// Match accepts calls with exactly this program name and argument list.
func Match(name string, args ...string) func(cmdrun.Call) bool {
	return func(c cmdrun.Call) bool {
		return c.Name == name && slices.Equal(c.Args, args)
	}
}

// MatchName accepts every call to the program name, whatever its arguments.
func MatchName(name string) func(cmdrun.Call) bool {
	return func(c cmdrun.Call) bool { return c.Name == name }
}

// Any accepts every call.
func Any(cmdrun.Call) bool { return true }

// Out is a clean exit 0 whose stdout is s.
func Out(s string) cmdrun.Result { return cmdrun.Result{Stdout: []byte(s)} }

// Fail is a non-zero exit with code and stderr text s.
func Fail(code int, s string) cmdrun.Result {
	return cmdrun.Result{Code: code, Stderr: []byte(s)}
}

func cloneCall(c cmdrun.Call) cmdrun.Call {
	c.Args = slices.Clone(c.Args)
	c.Env = slices.Clone(c.Env)
	c.Stdin = slices.Clone(c.Stdin)
	return c
}

func describe(c cmdrun.Call) string {
	return fmt.Sprintf("%s %q (dir %q)", c.Name, c.Args, c.Dir)
}
