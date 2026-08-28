package gitclient

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseStatus parses `git status --porcelain=v1 -z` output (statusArgs).
//
// Each record is: two status-code bytes (X, Y), a single ASCII-space
// separator, then the path, NUL-terminated. When the staged (X) code is a
// rename or copy, a SECOND NUL-terminated field follows: the ORIGINAL path.
// That second field comes AFTER the (new) path -- a REVERSED order versus
// the "orig -> new" arrow git prints in non -z mode -- so a naive parser
// that assumes "orig then new" silently swaps StatusEntry.Path and
// .OrigPath. -z mode also means paths are never quoted, so a path
// containing a literal space passes through as one token intact -- there
// is no whitespace field separator to trip over.
func parseStatus(data []byte) ([]StatusEntry, error) {
	var entries []StatusEntry
	i := 0
	n := len(data)
	for i < n {
		if i+3 > n {
			return nil, fmt.Errorf("gitclient: truncated status record at byte %d", i)
		}
		staged := StatusCode(data[i])
		unstaged := StatusCode(data[i+1])
		if data[i+2] != ' ' {
			return nil, fmt.Errorf("gitclient: status record at byte %d: expected a space after the XY status code, got %q", i, data[i+2])
		}
		i += 3

		pathLen := bytes.IndexByte(data[i:], 0)
		if pathLen < 0 {
			return nil, fmt.Errorf("gitclient: unterminated path in status record starting at byte %d", i)
		}
		entry := StatusEntry{
			Staged:   staged,
			Unstaged: unstaged,
			Path:     string(data[i : i+pathLen]),
		}
		i += pathLen + 1

		if staged == StatusRenamed || staged == StatusCopied {
			origLen := bytes.IndexByte(data[i:], 0)
			if origLen < 0 {
				return nil, fmt.Errorf("gitclient: unterminated orig path in status record starting at byte %d", i)
			}
			entry.OrigPath = string(data[i : i+origLen])
			i += origLen + 1
		}

		entries = append(entries, entry)
	}
	return entries, nil
}

// logFormat is the --format string for HistoryReader.Commits: logFieldsPerCommit
// fields per commit, NUL-separated, in the order parseCommits expects.
// Combined with logArgs' -z flag, git NUL-terminates each commit's
// expansion instead of appending a newline, so the whole invocation's
// stdout is one flat NUL-delimited stream, chunked in groups of
// logFieldsPerCommit.
//
// The separator between fields MUST be git's own "%x00" pretty-format
// placeholder (four printable ASCII characters: %, x, 0, 0) rather than a
// literal embedded NUL byte in this Go string. This is not stylistic: a
// literal NUL inside one argv element is not representable in a POSIX
// argv (C strings are NUL-terminated), so passing it to exec fails with
// "invalid argument" at the syscall layer -- confirmed behaviorally
// against real git 2.54.0 running `log -z --format=...`. "%x00" is a
// format-string ESCAPE that git itself expands into a raw NUL byte when
// producing its OUTPUT, so parseCommits' expectation of literal NUL
// bytes in the parsed stdout is unaffected; only the ARGUMENT string
// changes.
const logFormat = "%H%x00%s%x00%b%x00%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct"

// logFieldsPerCommit is the number of NUL-separated fields logFormat emits
// per commit: SHA, Subject, Body, AuthorName, AuthorEmail, AuthorWhen,
// CommitterName, CommitterEmail, CommitterWhen.
const logFieldsPerCommit = 9

// parseCommits parses the NUL-delimited `git log -z --format=<logFormat>`
// output built by logArgs.
func parseCommits(data []byte) ([]Commit, error) {
	if len(data) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(data), "\x00")
	// -z NUL-terminates every record, including the last, so splitting
	// leaves one trailing empty field to drop.
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields) == 0 {
		return nil, nil
	}
	if len(fields)%logFieldsPerCommit != 0 {
		return nil, fmt.Errorf("gitclient: log output has %d NUL-delimited fields, not a multiple of %d", len(fields), logFieldsPerCommit)
	}

	commits := make([]Commit, 0, len(fields)/logFieldsPerCommit)
	for i := 0; i < len(fields); i += logFieldsPerCommit {
		rec := fields[i : i+logFieldsPerCommit]
		authorWhen, err := parseUnixSeconds(rec[5])
		if err != nil {
			return nil, fmt.Errorf("gitclient: parsing author date %q: %w", rec[5], err)
		}
		committerWhen, err := parseUnixSeconds(rec[8])
		if err != nil {
			return nil, fmt.Errorf("gitclient: parsing committer date %q: %w", rec[8], err)
		}
		commits = append(commits, Commit{
			SHA:     rec[0],
			Subject: rec[1],
			Body:    rec[2],
			Author: Signature{
				Name:  rec[3],
				Email: rec[4],
				When:  authorWhen,
			},
			Committer: Signature{
				Name:  rec[6],
				Email: rec[7],
				When:  committerWhen,
			},
		})
	}
	return commits, nil
}

// parseUnixSeconds parses a %at/%ct-style decimal Unix-seconds timestamp.
func parseUnixSeconds(s string) (time.Time, error) {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}

// parseNumstat parses `git diff --numstat` output (numstatArgs): one line
// per changed file, "<added>\t<deleted>\t<path>", or "-\t-\t<path>" for a
// binary file (FileChange.Binary).
func parseNumstat(data []byte) ([]FileChange, error) {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	changes := make([]FileChange, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("gitclient: malformed --numstat line %q", line)
		}
		fc := FileChange{Path: fields[2]}
		if fields[0] == "-" && fields[1] == "-" {
			fc.Binary = true
		} else {
			add, err := strconv.Atoi(fields[0])
			if err != nil {
				return nil, fmt.Errorf("gitclient: parsing additions in %q: %w", line, err)
			}
			del, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("gitclient: parsing deletions in %q: %w", line, err)
			}
			fc.Additions = add
			fc.Deletions = del
		}
		changes = append(changes, fc)
	}
	return changes, nil
}

// parseCount parses the single-integer stdout of `rev-list --count ...`
// (commitsAheadArgs / RefReader.CommitsAhead).
func parseCount(data []byte) (int, error) {
	s := strings.TrimSpace(string(data))
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("gitclient: parsing rev-list --count output %q: %w", s, err)
	}
	return n, nil
}
