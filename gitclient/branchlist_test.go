package gitclient

import (
	"reflect"
	"testing"
)

func TestParseBranchListMultipleLines(t *testing.T) {
	got := parseBranchList([]byte("feature\nmain\n"))
	want := []string{"feature", "main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBranchList() = %v, want %v", got, want)
	}
}

func TestParseBranchListSingleLine(t *testing.T) {
	got := parseBranchList([]byte("main\n"))
	want := []string{"main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBranchList() = %v, want %v", got, want)
	}
}

func TestParseBranchListEmptyInputReturnsNil(t *testing.T) {
	if got := parseBranchList(nil); got != nil {
		t.Errorf("parseBranchList(nil) = %v, want nil", got)
	}
	if got := parseBranchList([]byte{}); got != nil {
		t.Errorf("parseBranchList([]byte{}) = %v, want nil", got)
	}
}

// TestParseBranchListMissingTrailingNewlineStillParses covers a
// defensive edge: git's own `branch --format` output always ends with a
// trailing newline, but the parser must not require one -- it only trims
// at most one, never fails on its absence.
func TestParseBranchListMissingTrailingNewlineStillParses(t *testing.T) {
	got := parseBranchList([]byte("main"))
	want := []string{"main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBranchList(no trailing newline) = %v, want %v", got, want)
	}
}
