package cmdrun_test

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
)

func TestResultOK(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		res  cmdrun.Result
		want bool
	}{
		{"zero value is a clean run", cmdrun.Result{}, true},
		{"non-zero exit", cmdrun.Result{Code: 2}, false},
		{"signal", cmdrun.Result{Code: -1, Signal: "killed"}, false},
		{"context ended", cmdrun.Result{CtxErr: boom}, false},
		{"start error", cmdrun.Result{Code: -1, StartErr: boom}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.OK(); got != tt.want {
				t.Fatalf("OK() = %v, want %v", got, tt.want)
			}
		})
	}
}
