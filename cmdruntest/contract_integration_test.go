//go:build integration

package cmdruntest_test

import (
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// TestExecPassesTheContract runs the shared contract against the real
// cmdrun.Exec and real sh. It is integration-tagged because it spawns
// subprocesses; the untagged tier stays process-free.
func TestExecPassesTheContract(t *testing.T) {
	cmdruntest.RunContract(t, cmdrun.Exec)
}
