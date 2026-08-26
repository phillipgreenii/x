// Package osdirs resolves per-application XDG state and config directories,
// with the $HOME fallback the XDG Base Directory spec requires when the
// corresponding environment variable is unset.
package osdirs

import (
	"errors"
	"os"
	"path/filepath"
)

// StateDir returns ${XDG_STATE_HOME}/<app>, falling back to
// $HOME/.local/state/<app> when XDG_STATE_HOME is unset. It errors rather
// than falling back further if HOME is also unset -- see xdgDir.
func StateDir(app string) (string, error) {
	dir, err := xdgDir("XDG_STATE_HOME", ".local", "state")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, app), nil
}

// ConfigDir returns ${XDG_CONFIG_HOME}/<app>, falling back to
// $HOME/.config/<app> when XDG_CONFIG_HOME is unset. It errors rather than
// falling back further if HOME is also unset -- see xdgDir.
func ConfigDir(app string) (string, error) {
	dir, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, app), nil
}

// xdgDir resolves envVar, falling back to $HOME/fallbackRelParts. It
// returns an error rather than silently resolving to a bare relative path
// (e.g. ".local/state") when HOME is also unset -- that would make the
// caller's directory depend on whatever the current working directory
// happens to be.
func xdgDir(envVar string, fallbackRelParts ...string) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("osdirs: neither " + envVar + " nor HOME/user home directory is set")
	}
	return filepath.Join(append([]string{home}, fallbackRelParts...)...), nil
}
