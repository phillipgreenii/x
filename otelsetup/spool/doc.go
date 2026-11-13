// Package spool writes OTLP export requests to files in a spool directory for
// a collector to ingest (otlpjsonfile receivers), instead of pushing them over
// HTTP.
//
// Two seams keep the file format and the destination replaceable without
// touching any caller:
//
//   - Encoder (format): turns ONE standard OTLP export request into one or more
//     self-contained records plus a file extension. The default is OTLP-JSON
//     lines (JSONEncoder). A different format is a new Encoder; the exporters
//     and the Sink do not change.
//   - Sink (destination): publishes one finished file (signal, name, bytes)
//     atomically. FileSink does so on a file system reached through the FS
//     seam, so tests can fake ENOSPC, EACCES and short writes.
//
// The trace exporter is built on the public otlptrace.Client interface, so
// NewTraceExporter yields the same exporter type the OTLP/HTTP path does.
//
// Failure behaviour: any write error switches that exporter to a no-op. It
// never blocks or fails the host process and never prints; the error is
// reported through Hooks.OnError.
//
// The package never imports otelsetup, and nothing outside otelsetup should
// import it, so consumers that do not need the spool do not carry its
// dependencies.
package spool
