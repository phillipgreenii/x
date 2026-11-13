package spool

import "os"

// Role says who the spool belongs to. It selects the directory and file modes
// and the owner the spool directory must have.
type Role int

const (
	// RoleUser is a per-user spool: directories 0700, files 0600, owned by the
	// current uid. It is the zero value.
	RoleUser Role = iota
	// RoleRoot is the root spool read by a collector running as another user:
	// directories 0755, files 0644, owned by uid 0.
	RoleRoot
)

// String returns the value of the spool=user|root counter attribute.
func (r Role) String() string {
	if r == RoleRoot {
		return "root"
	}
	return "user"
}

// DirMode is the permission for directories the Sink creates.
func (r Role) DirMode() os.FileMode {
	if r == RoleRoot {
		return 0o755
	}
	return 0o700
}

// FileMode is the permission for files the Sink creates.
func (r Role) FileMode() os.FileMode {
	if r == RoleRoot {
		return 0o644
	}
	return 0o600
}

// ExpectedUID is the uid that must own the spool directories; currentUID is
// the process's uid, used for RoleUser.
func (r Role) ExpectedUID(currentUID int) int {
	if r == RoleRoot {
		return 0
	}
	return currentUID
}

// Signal names, also the per-signal subdirectory of the spool.
const (
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
)
