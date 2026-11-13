package otelsetup_test

import (
	"context"

	"github.com/phillipgreenii/x/otelsetup"
	"github.com/phillipgreenii/x/otelsetup/spool"
)

// Example shows both transports. The instrumentation code is identical; only
// the Config differs, and an empty Config is the no-op.
func Example() {
	ctx := context.Background()

	// File spool: the collector ingests the files under SpoolDir. SpoolDir
	// must be absolute. Write failures are silent; observe them with
	// WithOnError.
	spooled, err := otelsetup.Init(ctx, otelsetup.Config{
		ServiceName: "mytool",
		SpoolDir:    "/var/tmp/example-otel-spool",
		SpoolRole:   spool.RoleUser,
	}, otelsetup.WithOnError(func(error) { /* count or remember; never print */ }))

	// OTLP/HTTP: the same code against a local collector.
	pushed, err2 := otelsetup.Init(ctx, otelsetup.Config{
		ServiceName: "mytool",
		Endpoint:    "http://127.0.0.1:4318",
	})

	for _, tel := range []*otelsetup.Telemetry{spooled, pushed} {
		if tel == nil {
			continue
		}
		_, span := tel.Tracer.Start(ctx, "work")
		span.End()
		_ = tel.Shutdown(ctx)
	}
	_, _ = err, err2
	// No Output comment: the example is compiled but not run, because running
	// it would write to SpoolDir and dial the collector.
}
