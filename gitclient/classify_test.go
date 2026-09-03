package gitclient

import (
	"context"
	"errors"
	"os/exec"
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

// TestClassifyRunErrNilIsNil covers classifyRunErr's short-circuit: a nil
// runErr (the invocation succeeded) must return nil regardless of ctx or
// stderr content.
func TestClassifyRunErrNilIsNil(t *testing.T) {
	if err := classifyRunErr(t.Context(), []string{"status"}, []byte("noise"), nil); err != nil {
		t.Errorf("classifyRunErr(nil runErr) = %v, want nil", err)
	}
}

// TestClassifyRunErrExitErrorDelegatesToClassify proves run's buffered
// spawn and Handle's streaming spawn (newHandle) apply IDENTICAL
// classification for an ordinary non-zero exit: classifyRunErr's
// *exec.ExitError branch must produce the exact same *GitError classify
// itself would for the same (args, exitCode, stderr).
func TestClassifyRunErrExitErrorDelegatesToClassify(t *testing.T) {
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, "sh", "-c", "exit 3")
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatal("cmd.Run() = nil, want a non-nil *exec.ExitError from `sh -c \"exit 3\"`")
	}

	args := []string{"status", "--porcelain"}
	got := classifyRunErr(ctx, args, []byte("some stderr\n"), runErr)

	want := classify(args, 3, []byte("some stderr\n"))
	var gotErr, wantErr *GitError
	if !errors.As(got, &gotErr) {
		t.Fatalf("classifyRunErr() = %T, want *GitError", got)
	}
	if !errors.As(want, &wantErr) {
		t.Fatalf("classify() = %T, want *GitError", want)
	}
	if got.Error() != want.Error() {
		t.Errorf("classifyRunErr() = %q, want classify()'s own %q", got.Error(), want.Error())
	}
}

// TestClassifyRunErrPrefersCtxErrOverExitStatus covers the context
// contract's own documented subtlety (client.go's newCmd doc comment):
// once ctx reports an error, classifyRunErr must wrap it (and fold in the
// underlying runErr via errors.Is-preserving %w) rather than classifying
// runErr as an ordinary *GitError -- even though runErr, taken alone,
// looks like an ordinary non-zero exit.
func TestClassifyRunErrPrefersCtxErrOverExitStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 1")
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatal("cmd.Run() = nil, want a non-nil error from `sh -c \"exit 1\"`")
	}

	err := classifyRunErr(ctx, []string{"status"}, nil, runErr)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("classifyRunErr() = %v, want errors.Is(_, context.Canceled)", err)
	}
	var ge *GitError
	if errors.As(err, &ge) {
		t.Errorf("classifyRunErr() = %v (*GitError), want the ctx error to take precedence, not a plain *GitError", err)
	}
}

// TestClassifyRunErrWrapsANonExitError covers classifyRunErr's final
// fallback branch: a runErr that is neither nil, a ctx error, nor an
// *exec.ExitError (a genuine spawn failure) must be wrapped as a plain
// error, not misclassified as a *GitError.
func TestClassifyRunErrWrapsANonExitError(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), t.TempDir()) // a directory is not executable
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatal("cmd.Run() = nil, want a spawn error (gitPath is a directory)")
	}

	err := classifyRunErr(t.Context(), []string{"status"}, nil, runErr)
	if err == nil {
		t.Fatal("classifyRunErr() = nil, want a wrapped spawn error")
	}
	var ge *GitError
	if errors.As(err, &ge) {
		t.Errorf("classifyRunErr() = %v (*GitError), want a non-GitError spawn error", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("classifyRunErr() = %v, want a spawn error, not a context error", err)
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
