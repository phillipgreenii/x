package gitclient_test

// Shared by every gitclient_test (external test package) file that calls
// a Handle-returning role method (Fetch, CreateWorktree, ...) and only
// wants the old synchronous "did the whole invocation succeed" shape
// (bead pg2-f1cq7 changed those methods to return (*Handle, error)
// instead of error alone). Deliberately UNTAGGED (no //go:build line):
// mutate_integration_test.go and read_integration_test.go build under
// -tags integration, contract_envleak_test.go and contract_escape_test.go
// build under -tags contract, and CI's own check-coverage.sh combines
// both tags in one run (-tags=integration,contract) -- a copy pinned to
// either tag alone would either collide with a copy pinned to the other
// (both landing in this same package when both tags are active) or go
// missing when only one tag is active. One untagged definition avoids
// both failure modes.

import "github.com/phillipgreenii/x/gitclient"

// waitHandle blocks on h.Wait() when the spawn itself succeeded.
func waitHandle(h *gitclient.Handle, err error) error {
	if err != nil {
		return err
	}
	return h.Wait()
}
