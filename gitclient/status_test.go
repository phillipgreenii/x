package gitclient

import (
	"reflect"
	"testing"
)

func TestParseStatusSimpleEntries(t *testing.T) {
	// " M" modified-unstaged, "??" untracked -- plain, no rename field.
	data := []byte(" M" + " " + "file1.txt" + "\x00" +
		"??" + " " + "newfile.txt" + "\x00")

	got, err := parseStatus(data)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	want := []StatusEntry{
		{Staged: StatusUnmodified, Unstaged: StatusModified, Path: "file1.txt"},
		{Staged: StatusUntracked, Unstaged: StatusUntracked, Path: "newfile.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatus() = %#v, want %#v", got, want)
	}
}

func TestParseStatusRenameFieldOrderIsReversedNewThenOrig(t *testing.T) {
	// The on-wire record for a staged rename is:
	//   "R " + " " + new-path + NUL + orig-path + NUL
	// i.e. the NEW path comes first and the ORIGINAL path second -- the
	// reverse of the "orig -> new" order git prints outside -z mode. A
	// parser that assumes "orig then new" would swap Path and OrigPath.
	data := []byte("R " + " " + "new.txt" + "\x00" + "orig.txt" + "\x00")

	got, err := parseStatus(data)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseStatus() returned %d entries, want 1: %#v", len(got), got)
	}
	entry := got[0]
	if entry.Path != "new.txt" {
		t.Errorf("entry.Path = %q, want %q", entry.Path, "new.txt")
	}
	if entry.OrigPath != "orig.txt" {
		t.Errorf("entry.OrigPath = %q, want %q", entry.OrigPath, "orig.txt")
	}
	if entry.Staged != StatusRenamed {
		t.Errorf("entry.Staged = %q, want %q", entry.Staged, StatusRenamed)
	}
}

func TestParseStatusCopyAlsoCarriesOrigPath(t *testing.T) {
	data := []byte("C " + " " + "copy-new.txt" + "\x00" + "copy-orig.txt" + "\x00")

	got, err := parseStatus(data)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseStatus() returned %d entries, want 1: %#v", len(got), got)
	}
	if got[0].Path != "copy-new.txt" || got[0].OrigPath != "copy-orig.txt" {
		t.Errorf("got %#v", got[0])
	}
}

func TestParseStatusPathContainingSpaces(t *testing.T) {
	// -z mode never quotes paths, so an internal space must survive intact
	// rather than being treated as a field separator.
	data := []byte(" M" + " " + "path with spaces.txt" + "\x00")

	got, err := parseStatus(data)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseStatus() returned %d entries, want 1: %#v", len(got), got)
	}
	if got[0].Path != "path with spaces.txt" {
		t.Errorf("entry.Path = %q, want %q", got[0].Path, "path with spaces.txt")
	}
}

func TestParseStatusMixedRecordsInOneStream(t *testing.T) {
	data := []byte(
		" M" + " " + "modified.txt" + "\x00" +
			"R " + " " + "new name.txt" + "\x00" + "old name.txt" + "\x00" +
			"??" + " " + "untracked.txt" + "\x00",
	)

	got, err := parseStatus(data)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	want := []StatusEntry{
		{Staged: StatusUnmodified, Unstaged: StatusModified, Path: "modified.txt"},
		{Staged: StatusRenamed, Unstaged: StatusUnmodified, Path: "new name.txt", OrigPath: "old name.txt"},
		{Staged: StatusUntracked, Unstaged: StatusUntracked, Path: "untracked.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatus() = %#v, want %#v", got, want)
	}
}

func TestParseStatusEmptyInputIsClean(t *testing.T) {
	got, err := parseStatus(nil)
	if err != nil {
		t.Fatalf("parseStatus(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parseStatus(nil) = %#v, want empty", got)
	}
}

func TestParseStatusTruncatedRecordErrors(t *testing.T) {
	// Only 2 bytes: not even a full "XY " prefix.
	if _, err := parseStatus([]byte("M ")); err == nil {
		t.Fatal("expected an error for a truncated XY prefix")
	}
}

func TestParseStatusMissingSeparatorErrors(t *testing.T) {
	// Third byte must be a space; here it's 'x'.
	if _, err := parseStatus([]byte("MMxfoo\x00")); err == nil {
		t.Fatal("expected an error when the XY-to-path separator is not a space")
	}
}

func TestParseStatusUnterminatedPathErrors(t *testing.T) {
	if _, err := parseStatus([]byte(" M no-terminating-nul")); err == nil {
		t.Fatal("expected an error for a path with no terminating NUL")
	}
}

func TestParseStatusUnterminatedOrigPathErrors(t *testing.T) {
	if _, err := parseStatus([]byte("R  new.txt\x00missing-nul-orig")); err == nil {
		t.Fatal("expected an error for a rename record missing its orig-path terminator")
	}
}

func TestStatusArgs(t *testing.T) {
	v := statusArgs()
	wantArgs := []string{"-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z"}
	if !reflect.DeepEqual(v.Args, wantArgs) {
		t.Errorf("statusArgs().Args = %v, want %v", v.Args, wantArgs)
	}
	if !v.Parsed {
		t.Error("statusArgs().Parsed = false, want true (status output is parsed)")
	}
}

func TestIsTrackedArgs(t *testing.T) {
	v := isTrackedArgs("some/path.txt")
	wantArgs := []string{"ls-files", "--error-unmatch", "some/path.txt"}
	if !reflect.DeepEqual(v.Args, wantArgs) {
		t.Errorf("isTrackedArgs().Args = %v, want %v", v.Args, wantArgs)
	}
	if !v.Parsed {
		t.Error("isTrackedArgs().Parsed = false, want true (ls-files output is parsed)")
	}
}
