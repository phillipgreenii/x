//go:build integration

package cmdrun_test

import (
	"context"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
)

func TestExecDoesNotMutateTheCall(t *testing.T) {
	c := cmdrun.Call{
		Name:  "sh",
		Args:  []string{"-c", "cat"},
		Stdin: []byte("in"),
		Env:   []string{"A=1"},
	}
	res := cmdrun.Exec(context.Background(), c)
	if !res.OK() || string(res.Stdout) != "in" {
		t.Fatalf("res = %+v", res)
	}
	if string(c.Stdin) != "in" || len(c.Env) != 1 || c.Env[0] != "A=1" || len(c.Args) != 2 {
		t.Fatalf("Exec modified its Call: %+v", c)
	}
}
