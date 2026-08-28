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
// separator, then the path, NUL-terminated. When EITHER the staged (X) OR
// the unstaged (Y) code is a rename or copy, a SECOND NUL-terminated field
// follows: the ORIGINAL path. Git emits the work-tree-only shape (X=' ',
// Y='R'/'C') for a rename/copy that exists only in the worktree -- e.g.
// `mv old new && git add -N new` -- so checking only X misses it and the
// parser mis-reads the next record's own XY prefix as path bytes,
// producing a confusing "expected a space after the XY status code"
// error. That second field comes AFTER the (new) path -- a REVERSED order
// versus the "orig -> new" arrow git prints in non -z mode -- so a naive
// parser that assumes "orig then new" silently swaps StatusEntry.Path and
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

		if staged == StatusRenamed || staged == StatusCopied || unstaged == StatusRenamed || unstaged == StatusCopied {
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

// parseNumstat parses `git diff --numstat -z` output (numstatArgs).
//
// Each record starts with two tab-delimited fields, additions and
// deletions ("<added>\t<deleted>\t", or "-\t-\t" for a binary file --
// FileChange.Binary), followed by a THIRD field that is NUL-terminated
// rather than tab-terminated (numstat has only one path field per record,
// so there is no second tab to delimit it from a trailing newline the way
// non -z mode uses one).
//
// For an ordinary (non-renamed) file, that third field is the path itself
// and the record ends there. For a rename/copy -- which git detects BY
// DEFAULT on `diff --numstat`, no flag required -- the third field is
// EMPTY (a bare NUL immediately following the second tab), signaling that
// TWO more NUL-terminated fields follow: the ORIGINAL path, then the NEW
// path, in that order. This is the numstat analogue of statusArgs'
// rename record, but with the paths in the opposite (natural, old-then-
// new) order -- status -z reverses them (new, then original); numstat -z
// does not. Verified behaviorally against real git 2.54.0 for both a
// root-level rename and a same-directory rename (which non -z mode
// compacts into a "dir/{old => new}" descriptor that -z mode does NOT
// produce -- -z always gives full, separate paths).
func parseNumstat(data []byte) ([]FileChange, error) {
	var changes []FileChange
	i := 0
	n := len(data)
	for i < n {
		addEnd := bytes.IndexByte(data[i:], '\t')
		if addEnd < 0 {
			return nil, fmt.Errorf("gitclient: malformed --numstat record at byte %d: no tab after the additions field", i)
		}
		addField := string(data[i : i+addEnd])
		i += addEnd + 1

		delEnd := bytes.IndexByte(data[i:], '\t')
		if delEnd < 0 {
			return nil, fmt.Errorf("gitclient: malformed --numstat record at byte %d: no tab after the deletions field", i)
		}
		delField := string(data[i : i+delEnd])
		i += delEnd + 1

		pathEnd := bytes.IndexByte(data[i:], 0)
		if pathEnd < 0 {
			return nil, fmt.Errorf("gitclient: unterminated path in --numstat record starting at byte %d", i)
		}
		pathField := data[i : i+pathEnd]
		i += pathEnd + 1

		fc := FileChange{}
		if addField == "-" && delField == "-" {
			fc.Binary = true
		} else {
			add, err := strconv.Atoi(addField)
			if err != nil {
				return nil, fmt.Errorf("gitclient: parsing additions %q: %w", addField, err)
			}
			del, err := strconv.Atoi(delField)
			if err != nil {
				return nil, fmt.Errorf("gitclient: parsing deletions %q: %w", delField, err)
			}
			fc.Additions = add
			fc.Deletions = del
		}

		if len(pathField) == 0 {
			// Rename/copy record: the path field was empty -- two more
			// NUL-terminated fields follow, old path then new path.
			origEnd := bytes.IndexByte(data[i:], 0)
			if origEnd < 0 {
				return nil, fmt.Errorf("gitclient: unterminated original path in --numstat rename record starting at byte %d", i)
			}
			origPath := string(data[i : i+origEnd])
			i += origEnd + 1

			newEnd := bytes.IndexByte(data[i:], 0)
			if newEnd < 0 {
				return nil, fmt.Errorf("gitclient: unterminated new path in --numstat rename record starting at byte %d", i)
			}
			fc.Path = string(data[i : i+newEnd])
			fc.OrigPath = origPath
			i += newEnd + 1
		} else {
			fc.Path = string(pathField)
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
