package osdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirHonoursXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got, err := StateDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
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
	got, err := StateDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "myapp")
	if got != want {
		t.Fatalf("StateDir(%q) = %q, want %q", "myapp", got, want)
	}
}

func TestStateDirErrorsWhenNeitherXDGStateHomeNorHOMEIsSet(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "HOME"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("unset %s: %v", v, err)
		}
	}
	if _, err := StateDir("myapp"); err == nil {
		t.Fatal("expected an error when neither XDG_STATE_HOME nor HOME is set")
	}
}

func TestConfigDirHonoursXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := ConfigDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
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
	got, err := ConfigDir("myapp")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "myapp")
	if got != want {
		t.Fatalf("ConfigDir(%q) = %q, want %q", "myapp", got, want)
	}
}

func TestConfigDirErrorsWhenNeitherXDGConfigHomeNorHOMEIsSet(t *testing.T) {
	for _, v := range []string{"XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("unset %s: %v", v, err)
		}
	}
	if _, err := ConfigDir("myapp"); err == nil {
		t.Fatal("expected an error when neither XDG_CONFIG_HOME nor HOME is set")
	}
}
