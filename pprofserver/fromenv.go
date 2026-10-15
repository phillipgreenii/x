package pprofserver

import (
	"fmt"
	"io"
)

// StartFromEnv starts the profiling endpoint when getenv(envName) is non-empty
// and returns the function that stops it: a harmless no-op when the variable is
// unset or the address was rejected.
//
// A rejected address is reported as one line on w and never stops the daemon:
// profiling is a diagnostic, and losing it must not cost the daemon its real
// work. When the endpoint starts, one line on w names the address it is
// listening on. Callers pass a non-nil w (daemons pass os.Stderr).
func StartFromEnv(envName string, getenv func(string) string, w io.Writer) (stop func()) {
	addr := getenv(envName)
	if addr == "" {
		return func() {}
	}
	srv, err := Start(addr)
	if err != nil {
		fmt.Fprintf(w, "daemon: %s ignored: %v\n", envName, err)
		return func() {}
	}
	fmt.Fprintf(w, "daemon: pprof listening on %s\n", srv.Addr())
	return srv.Close
}
