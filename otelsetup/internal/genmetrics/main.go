// Command genmetrics regenerates otelsetup/metrics.json from otelsetup.Metrics.
// Run via `go generate ./otelsetup` (working directory is the otelsetup dir).
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/x/otelsetup"
)

func main() {
	b, err := otelsetup.MetricsJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("metrics.json", b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
