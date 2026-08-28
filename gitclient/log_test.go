package gitclient

import (
	"strings"
	"testing"
	"time"
)

// nulRecord joins fields with NUL and appends a trailing NUL, matching what
// `git log -z --format=<logFormat>` produces for one commit (see logArgs'
// doc comment: -z NUL-terminates every record, including the last).
func nulRecord(fields ...string) string {
	return strings.Join(fields, "\x00") + "\x00"
}

func TestParseCommitsSingleRecord(t *testing.T) {
	data := []byte(nulRecord(
		"abc123", "Fix bug", "Detailed explanation.",
		"Alice", "alice@example.com", "1700000000",
		"Bob", "bob@example.com", "1700000100",
	))

	got, err := parseCommits(data)
	if err != nil {
		t.Fatalf("parseCommits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseCommits() returned %d commits, want 1: %#v", len(got), got)
	}
	want := Commit{
		SHA:     "abc123",
		Subject: "Fix bug",
		Body:    "Detailed explanation.",
		Author: Signature{
			Name:  "Alice",
			Email: "alice@example.com",
			When:  time.Unix(1700000000, 0),
		},
		Committer: Signature{
			Name:  "Bob",
			Email: "bob@example.com",
			When:  time.Unix(1700000100, 0),
		},
	}
	if got[0] != want {
		t.Fatalf("parseCommits()[0] = %#v, want %#v", got[0], want)
	}
}

func TestParseCommitsMultipleRecords(t *testing.T) {
	data := []byte(
		nulRecord("sha1", "Subject one", "Body one",
			"Alice", "alice@example.com", "1700000000",
			"Alice", "alice@example.com", "1700000000") +
			nulRecord("sha2", "Subject two", "",
				"Bob", "bob@example.com", "1700000200",
				"Carol", "carol@example.com", "1700000300"),
	)

	got, err := parseCommits(data)
	if err != nil {
		t.Fatalf("parseCommits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parseCommits() returned %d commits, want 2: %#v", len(got), got)
	}
	if got[0].SHA != "sha1" || got[1].SHA != "sha2" {
		t.Fatalf("unexpected SHAs: %q, %q", got[0].SHA, got[1].SHA)
	}
	// Second commit has an empty body -- consecutive NULs must parse to "".
	if got[1].Body != "" {
		t.Errorf("got[1].Body = %q, want empty", got[1].Body)
	}
	if !got[1].Committer.When.Equal(time.Unix(1700000300, 0)) {
		t.Errorf("got[1].Committer.When = %v, want %v", got[1].Committer.When, time.Unix(1700000300, 0))
	}
}

func TestParseCommitsEmptyInput(t *testing.T) {
	got, err := parseCommits(nil)
	if err != nil {
		t.Fatalf("parseCommits(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parseCommits(nil) = %#v, want empty", got)
	}
}

func TestParseCommitsWrongFieldCountErrors(t *testing.T) {
	// One field short of a full record.
	data := []byte("sha\x00subject\x00body\x00author\x00author@example.com\x001700000000\x00committer\x00committer@example.com\x00")
	if _, err := parseCommits(data); err == nil {
		t.Fatal("expected an error for a field count that isn't a multiple of logFieldsPerCommit")
	}
}

func TestParseCommitsBadTimestampErrors(t *testing.T) {
	data := []byte(nulRecord(
		"sha", "subject", "body",
		"author", "author@example.com", "not-a-number",
		"committer", "committer@example.com", "1700000000",
	))
	if _, err := parseCommits(data); err == nil {
		t.Fatal("expected an error for a non-numeric author date")
	}
}

// TestParseCommitsBadCommitterTimestampErrors is the committer-date
// counterpart to TestParseCommitsBadTimestampErrors, which only ever
// exercises a bad AUTHOR date -- the author date is parsed first and
// returns immediately on error, so a bad committer date behind a VALID
// author date is a distinct, otherwise-unreached branch.
func TestParseCommitsBadCommitterTimestampErrors(t *testing.T) {
	data := []byte(nulRecord(
		"sha", "subject", "body",
		"author", "author@example.com", "1700000000",
		"committer", "committer@example.com", "not-a-number",
	))
	if _, err := parseCommits(data); err == nil {
		t.Fatal("expected an error for a non-numeric committer date")
	}
}

func TestLogArgsDefaults(t *testing.T) {
	v := logArgs(LogOptions{})
	want := []string{"log", "-z", "--format=" + logFormat, "HEAD"}
	if len(v.Args) != len(want) {
		t.Fatalf("logArgs(LogOptions{}).Args = %v, want %v", v.Args, want)
	}
	for i := range want {
		if v.Args[i] != want[i] {
			t.Fatalf("logArgs(LogOptions{}).Args = %v, want %v", v.Args, want)
		}
	}
	if !v.Parsed {
		t.Error("logArgs().Parsed = false, want true (log output is parsed)")
	}
}

func TestLogArgsBaseHeadRange(t *testing.T) {
	v := logArgs(LogOptions{Base: "main", Head: "feature"})
	last := v.Args[len(v.Args)-1]
	if last != "main..feature" {
		t.Errorf("logArgs range = %q, want %q", last, "main..feature")
	}
}

func TestLogArgsFilters(t *testing.T) {
	v := logArgs(LogOptions{
		NoMerges: true,
		Authors:  []string{"alice", "bob"},
		Limit:    5,
		Paths:    []string{"foo.go", "bar.go"},
	})
	joined := strings.Join(v.Args, " ")
	for _, want := range []string{"--no-merges", "--author=alice", "--author=bob", "-n 5", "-- foo.go bar.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logArgs args %v missing %q", v.Args, want)
		}
	}
}

// TestLogArgsSinceAndUntil covers the two time-window filters, formatted as
// RFC3339 -- neither NoMerges/Authors/Limit/Paths (TestLogArgsFilters) nor
// the zero-value default (TestLogArgsDefaults) exercises them.
func TestLogArgsSinceAndUntil(t *testing.T) {
	since := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	until := time.Date(2024, 6, 7, 8, 9, 10, 0, time.UTC)
	v := logArgs(LogOptions{Since: since, Until: until})
	joined := strings.Join(v.Args, " ")
	for _, want := range []string{
		"--since=" + since.Format(time.RFC3339),
		"--until=" + until.Format(time.RFC3339),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("logArgs args %v missing %q", v.Args, want)
		}
	}
}
