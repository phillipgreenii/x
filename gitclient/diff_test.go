package gitclient

import (
	"reflect"
	"testing"
)

func TestParseNumstatRegularFiles(t *testing.T) {
	data := []byte("3\t1\tfoo.go\n0\t4\tbar/baz.go\n")

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
	data := []byte("-\t-\tbinary.png\n")

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
	data := []byte("2\t2\tpath with spaces.txt\n-\t-\tan image.png\n")

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

func TestParseNumstatMalformedLineErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("only-one-field\n")); err == nil {
		t.Fatal("expected an error for a line without three tab-separated fields")
	}
}

func TestParseNumstatNonNumericCountErrors(t *testing.T) {
	if _, err := parseNumstat([]byte("x\ty\tfoo.go\n")); err == nil {
		t.Fatal("expected an error for non-numeric, non-dash addition/deletion counts")
	}
}

func TestNumstatArgs(t *testing.T) {
	v := numstatArgs("main")
	want := []string{"diff", "--numstat", "main...HEAD"}
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
