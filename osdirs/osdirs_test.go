package osdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirHonoursXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got := StateDir("myapp")
	want := filepath.Join(dir, "myapp")
	if got != want {
		t.Fatalf("StateDir(%q) = %q, want %q", "myapp", got, want)
	}
}

func TestStateDirFallsBackToHomeLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	if err := os.Unsetenv("XDG_STATE_HOME"); err != nil {
		t.Fatalf("unset XDG_STATE_HOME: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := StateDir("myapp")
	want := filepath.Join(home, ".local", "state", "myapp")
	if got != want {
		t.Fatalf("StateDir(%q) = %q, want %q", "myapp", got, want)
	}
}

func TestConfigDirHonoursXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got := ConfigDir("myapp")
	want := filepath.Join(dir, "myapp")
	if got != want {
		t.Fatalf("ConfigDir(%q) = %q, want %q", "myapp", got, want)
	}
}

func TestConfigDirFallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		t.Fatalf("unset XDG_CONFIG_HOME: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := ConfigDir("myapp")
	want := filepath.Join(home, ".config", "myapp")
	if got != want {
		t.Fatalf("ConfigDir(%q) = %q, want %q", "myapp", got, want)
	}
}
