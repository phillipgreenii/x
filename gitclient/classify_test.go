package gitclient

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		exitCode int
		stderr   []byte
		wantNil  bool
		wantErr  string
	}{
		{
			name:     "success (exit 0) is not an error",
			args:     []string{"status", "--porcelain=v1", "-z"},
			exitCode: 0,
			stderr:   nil,
			wantNil:  true,
		},
		{
			name:     "general failure (exit 1)",
			args:     []string{"config", "--get", "remote.upstream.url"},
			exitCode: 1,
			stderr:   []byte("error: key does not exist\n"),
			wantErr:  "git config --get remote.upstream.url: exit 1: error: key does not exist",
		},
		{
			name:     "fatal error (exit 128)",
			args:     []string{"rev-parse", "--verify", "--quiet", "bogus^{commit}"},
			exitCode: 128,
			stderr:   []byte("fatal: bad revision 'bogus'\n"),
			wantErr:  "git rev-parse --verify --quiet bogus^{commit}: exit 128: fatal: bad revision 'bogus'",
		},
		{
			name:     "empty stderr",
			args:     []string{"fetch", "--no-prune", "origin"},
			exitCode: 1,
			stderr:   nil,
			wantErr:  "git fetch --no-prune origin: exit 1: ",
		},
		{
			name:     "trailing newlines are trimmed from stderr",
			args:     []string{"branch", "-d", "stale"},
			exitCode: 1,
			stderr:   []byte("error: branch not fully merged\n\n"),
			wantErr:  "git branch -d stale: exit 1: error: branch not fully merged",
		},
		{
			name:     "no args",
			args:     nil,
			exitCode: 2,
			stderr:   []byte("boom"),
			wantErr:  "git : exit 2: boom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classify(tc.args, tc.exitCode, tc.stderr)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("classify() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("classify() = nil, want an error")
			}
			var ge *GitError
			if !errors.As(err, &ge) {
				t.Fatalf("classify() = %T, want *GitError", err)
			}
			if ge.ExitCode != tc.exitCode {
				t.Errorf("ExitCode = %d, want %d", ge.ExitCode, tc.exitCode)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("Error() = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestClassifyCopiesArgsDefensively(t *testing.T) {
	args := []string{"status", "--porcelain=v1"}
	err := classify(args, 1, []byte("boom"))

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("classify() = %T, want *GitError", err)
	}

	args[0] = "mutated"
	if ge.Args[0] != "status" {
		t.Errorf("GitError.Args was aliased to the caller's slice: got %v after mutation", ge.Args)
	}
}

func TestClassifyDoesNotMapDetachedHEADOrNoRemote(t *testing.T) {
	// classify is a general seam: it must never itself return
	// ErrDetachedHEAD or ErrNoRemote, since both arise from call-specific
	// exit-code checks elsewhere (empty stdout on a successful
	// `branch --show-current`; `config --get`'s exit 1 interpreted by
	// RemoteURL, not by this generic seam).
	err := classify([]string{"config", "--get", "remote.origin.url"}, 1, nil)
	if errors.Is(err, ErrNoRemote) {
		t.Error("classify() must not itself produce ErrNoRemote")
	}
	if errors.Is(err, ErrDetachedHEAD) {
		t.Error("classify() must not itself produce ErrDetachedHEAD")
	}
	if errors.Is(err, ErrNotARepository) {
		t.Error("classify() must not itself produce ErrNotARepository")
	}
}
