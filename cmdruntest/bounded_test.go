package cmdruntest_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdruntest"
)

func TestUnboundedCallsFlagsUnannotatedCalls(t *testing.T) {
	got, err := cmdruntest.UnboundedCalls(filepath.Join("testdata", "unbounded"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want the two literals in unbounded.go (test files exempt)", got)
	}
	for _, g := range got {
		if !strings.Contains(g, "unbounded.go:") {
			t.Fatalf("unexpected location %q", g)
		}
	}
}

func TestUnboundedCallsAcceptsTimeoutOrReason(t *testing.T) {
	for _, dir := range []string{"bounded", "annotated"} {
		got, err := cmdruntest.UnboundedCalls(filepath.Join("testdata", dir))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: got %v, want none", dir, got)
		}
	}
}

func TestAssertCallsBoundedPassesOnCleanFixtures(t *testing.T) {
	cmdruntest.AssertCallsBounded(t, filepath.Join("testdata", "bounded"))
	cmdruntest.AssertCallsBounded(t, filepath.Join("testdata", "annotated"))
}

func TestAssertCallsBoundedSkipsTestdataBelowRoot(t *testing.T) {
	// The package directory holds testdata/unbounded; it must not count.
	cmdruntest.AssertCallsBounded(t, ".")
}
