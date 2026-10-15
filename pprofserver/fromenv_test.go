package pprofserver

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testEnvName = "TEST_DAEMON_PPROF_ADDR"

// envOf returns a getenv stub that yields value for testEnvName and "" for
// every other variable.
func envOf(value string) func(string) string {
	return func(k string) string {
		if k == testEnvName {
			return value
		}
		return ""
	}
}

func TestStartFromEnvUnsetIsSilent(t *testing.T) {
	var out bytes.Buffer
	stop := StartFromEnv(testEnvName, envOf(""), &out)
	stop()
	if out.Len() != 0 {
		t.Fatalf("unset %s wrote %q, want nothing", testEnvName, out.String())
	}
}

func TestStartFromEnvRejectedAddressDoesNotStopTheDaemon(t *testing.T) {
	var out bytes.Buffer
	stop := StartFromEnv(testEnvName, envOf("0.0.0.0:6060"), &out)
	stop() // must be callable and harmless
	if !strings.Contains(out.String(), testEnvName) {
		t.Fatalf("rejection did not name the variable: %q", out.String())
	}
	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("rejection wrote %d lines, want exactly 1: %q", n, out.String())
	}
}

func TestStartFromEnvServesAndStops(t *testing.T) {
	var out bytes.Buffer
	stop := StartFromEnv(testEnvName, envOf("127.0.0.1:0"), &out)
	line := out.String()
	const prefix = "daemon: pprof listening on "
	if !strings.HasPrefix(line, prefix) {
		stop()
		t.Fatalf("no listening line: %q", line)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		stop()
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		stop()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	stop()
	if resp, err := http.Get("http://" + addr + "/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("still serving after stop")
	}
}

func TestStartFromEnvUnixSocket(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "pprof.sock")

	var out bytes.Buffer
	stop := StartFromEnv(testEnvName, envOf(sock), &out)
	defer stop()
	if !strings.Contains(out.String(), sock) {
		t.Fatalf("listening line does not name the socket: %q", out.String())
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}
