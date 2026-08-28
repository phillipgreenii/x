package gitclient

import (
	"reflect"
	"testing"
)

func TestParseNumstatRegularFiles(t *testing.T) {
	data := []byte("3\t1\t" + "foo.go" + "\x00" +
		"0\t4\t" + "bar/baz.go" + "\x00")

	got, err := parseNumstat(data)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	want := []FileChange{
		{Path: "foo.go", Additions: 3, Deletions: 1},
		{Path: "bar/baz.go", Additions: 0, Deletions: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumstat() = %#v, want %#v", got, want)
	}
}

func TestParseNumstatBinaryFileUsesDashMarkers(t *testing.T) {
	data := []byte("-\t-\t" + "binary.png" + "\x00")

	got, err := parseNumstat(data)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	want := []FileChange{{Path: "binary.png", Binary: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumstat() = %#v, want %#v", got, want)
	}
}

func TestParseNumstatMixedBinaryAndTextWithSpacesInPath(t *testing.T) {
	data := []byte("2\t2\t" + "path with spaces.txt" + "\x00" +
		"-\t-\t" + "an image.png" + "\x00")

	got, err := parseNumstat(data)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	want := []FileChange{
		{Path: "path with spaces.txt", Additions: 2, Deletions: 2},
		{Path: "an image.png", Binary: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumstat() = %#v, want %#v", got, want)
	}
}

func TestParseNumstatEmptyInput(t *testing.T) {
	got, err := parseNumstat(nil)
	if err != nil {
		t.Fatalf("parseNumstat(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parseNumstat(nil) = %#v, want empty", got)
	}
}

func TestParseNumstatMalformedRecordMissingSecondTabErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("only-one-field-no-second-tab\x00")); err == nil {
		t.Fatal("expected an error for a record without a second tab")
	}
}

func TestParseNumstatNonNumericCountErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("x\ty\t" + "foo.go" + "\x00")); err == nil {
		t.Fatal("expected an error for non-numeric, non-dash addition/deletion counts")
	}
}

// TestParseNumstatNonNumericDeletionCountErrors is the deletions-specific
// counterpart to TestParseNumstatNonNumericCountErrors, which makes BOTH
// the additions and deletions fields non-numeric -- additions is parsed
// first and returns immediately on error, so a bad deletions count behind a
// VALID additions count is a distinct, otherwise-unreached branch.
func TestParseNumstatNonNumericDeletionCountErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("3\ty\t" + "foo.go" + "\x00")); err == nil {
		t.Fatal("expected an error for a non-numeric deletion count")
	}
}

func TestParseNumstatUnterminatedPathErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("3\t1\tno-terminating-nul")); err == nil {
		t.Fatal("expected an error for a path with no terminating NUL")
	}
}

// TestParseNumstatRenameRecordPopulatesRealOldAndNewPaths is the numstat
// analogue of TestParseStatusRenameFieldOrderIsReversedNewThenOrig: a
// rename/copy record's on-wire shape is
//
//	"<add>\t<del>\t" + NUL (empty third field, signaling a rename) +
//	old-path + NUL + new-path + NUL
//
// -- old path FIRST, then new path (the NATURAL order -- unlike status
// -z's reversed new-then-orig order). A naive 3-way split (the pre-fix
// parser) would take the whole record's leftover text as one literal
// path, never recognizing the empty-field marker or decoding real paths.
func TestParseNumstatRenameRecordPopulatesRealOldAndNewPaths(t *testing.T) {
	data := []byte("2\t1\t" + "\x00" + "old.txt" + "\x00" + "new.txt" + "\x00")

	got, err := parseNumstat(data)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseNumstat() returned %d entries, want 1: %#v", len(got), got)
	}
	fc := got[0]
	if fc.Path != "new.txt" {
		t.Errorf("fc.Path = %q, want the NEW path %q, not a literal descriptor string", fc.Path, "new.txt")
	}
	if fc.OrigPath != "old.txt" {
		t.Errorf("fc.OrigPath = %q, want the ORIGINAL path %q", fc.OrigPath, "old.txt")
	}
	if fc.Additions != 2 || fc.Deletions != 1 {
		t.Errorf("(Additions,Deletions) = (%d,%d), want (2,1)", fc.Additions, fc.Deletions)
	}
}

func TestParseNumstatPureRenameRecordHasZeroAdditionsDeletions(t *testing.T) {
	// A pure rename with no content change reports "0\t0\t" -- verified
	// behaviorally against real git 2.54.0.
	data := []byte("0\t0\t" + "\x00" + "old.txt" + "\x00" + "new.txt" + "\x00")

	got, err := parseNumstat(data)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	if len(got) != 1 || got[0].Path != "new.txt" || got[0].OrigPath != "old.txt" {
		t.Fatalf("parseNumstat() = %#v, want a single rename entry old.txt -> new.txt", got)
	}
}

func TestParseNumstatUnterminatedOrigPathInRenameRecordErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("2\t1\t\x00no-terminating-nul-for-orig-path")); err == nil {
		t.Fatal("expected an error for a rename record missing its original-path terminator")
	}
}

func TestParseNumstatUnterminatedNewPathInRenameRecordErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("2\t1\t\x00old.txt\x00no-terminating-nul-for-new-path")); err == nil {
		t.Fatal("expected an error for a rename record missing its new-path terminator")
	}
}

func TestNumstatArgs(t *testing.T) {
	v := numstatArgs("main")
	want := []string{"diff", "--numstat", "-z", "main...HEAD"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("numstatArgs(\"main\").Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("numstatArgs().Parsed = false, want true (diff output is parsed)")
	}
}

func TestParseCount(t *testing.T) {
	n, err := parseCount([]byte("42\n"))
	if err != nil {
		t.Fatalf("parseCount: %v", err)
	}
	if n != 42 {
		t.Errorf("parseCount(\"42\\n\") = %d, want 42", n)
	}
}

func TestParseCountNonNumericErrors(t *testing.T) {
	if _, err := parseCount([]byte("not-a-number\n")); err == nil {
		t.Fatal("expected an error for non-numeric rev-list --count output")
	}
}

func TestCommitsAheadArgs(t *testing.T) {
	v := commitsAheadArgs("main", "feature")
	want := []string{"rev-list", "--count", "main..feature"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("commitsAheadArgs().Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("commitsAheadArgs().Parsed = false, want true (rev-list output is parsed)")
	}
}
