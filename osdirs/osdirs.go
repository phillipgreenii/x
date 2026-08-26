// Package osdirs resolves per-application XDG state and config directories,
// with the $HOME fallback the XDG Base Directory spec requires when the
// corresponding environment variable is unset. This one helper replaces the
// independently hand-rolled copies of the same fallback logic found across
// this workspace's Go binaries.
package osdirs

import (
	"os"
	"path/filepath"
)

// StateDir returns ${XDG_STATE_HOME}/<app>, falling back to
// $HOME/.local/state/<app> when XDG_STATE_HOME is unset.
func StateDir(app string) string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local", "state"), app)
}

// ConfigDir returns ${XDG_CONFIG_HOME}/<app>, falling back to
// $HOME/.config/<app> when XDG_CONFIG_HOME is unset.
func ConfigDir(app string) string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), app)
}

func xdgDir(envVar string, fallbackRelParts ...string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(append([]string{home}, fallbackRelParts...)...)
}
