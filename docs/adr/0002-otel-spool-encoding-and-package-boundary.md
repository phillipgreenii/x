# ADR-0002: OTLP-JSON file encoding via collector pdata, in a subpackage of x

**Status**: Proposed (spike result; the exporter packets implement it)
**Date**: 2026-11-12
**Deciders**: phillipg (pending)

## Decision

1. **Use collector `pdata` for the default OTLP-JSON `Encoder`** (not a hand-written encoder).
   Pin exactly `go.opentelemetry.io/collector/pdata v1.65.0`:

   ```text
   go get go.opentelemetry.io/collector/pdata@v1.65.0
   ```

   v1.65.0 is the newest pdata that keeps `x` at `go 1.25.9` and forces no other upgrade (it needs
   `google.golang.org/grpc v1.83.0`, `x/net v0.55.0`, `x/sys v0.45.0`; `x` already has newer ones;
   `google.golang.org/protobuf` stays at v1.36.12). pdata v1.66.0 and later declare `go 1.26.0`
   (v1.71.0, the latest, additionally bumps grpc to v1.85.0, x/net, x/sys, x/text, x/sync). Move to
   a newer pdata only together with a deliberate `go 1.26.x` bump of `x`.

2. **Boundary: a subpackage of `x`, not a nested Go module.** Put the spool code in
   `github.com/phillipgreenii/x/otelsetup/spool` (path is the packets' call; the constraints are
   that `otelsetup` imports it, it never imports `otelsetup`, and nothing else in `x` imports it).
   `x/go.mod` gains pdata plus six indirect modules (below). Consumers of `x` that do not import the
   spool package carry none of them in `go mod download` or `gomod2nix.toml` (evidence below).

3. **Oracle every Encoder must satisfy** (the Encoder seam's contract test): for a fixture
   `ExportTraceServiceRequest` / `ExportMetricsServiceRequest` built from the
   `go.opentelemetry.io/proto/otlp` types, `proto.Marshal` it, run the Encoder, take the produced
   record bytes, decode them with the collector's own `ptrace.JSONUnmarshaler.UnmarshalTraces` /
   `pmetric.JSONUnmarshaler.UnmarshalMetrics`, re-marshal with `ptrace.ProtoMarshaler` /
   `pmetric.ProtoMarshaler`, `proto.Unmarshal` into the OTLP request type and require
   `proto.Equal(decoded, original)`. Fixtures MUST cover: a root span plus a child span (parent id,
   string/int/double/bool attributes, an event with attributes, a link with attributes, status
   error with message), a delta monotonic `Sum` (int value), and a delta explicit-bucket
   `Histogram` with `Min`, `Max`, `Sum`, bounds and bucket counts. A future non-JSON Encoder uses the
   same shape with the matching decoder. The default Encoder additionally asserts that trace and
   span ids are 32 and 16 lowercase hex characters and that enums are JSON integers.

## Item 1 to 3 results (PASS)

Scratch module `spike` (copy of `x/go.mod`, `go 1.25.9`, Go toolchain go1.26.8 with
`GOTOOLCHAIN=local`), outside the repo; `x/go.mod` untouched.

| Item                                                                     | Result                                                                                                                                                                                                                |
| ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. pdata resolves with `go 1.25.9` and protobuf v1.36.12, no forced bump | PASS for `pdata v1.65.0`: the `go.mod` diff is only `+ go.opentelemetry.io/collector/pdata v1.65.0 // indirect`. FAIL for `@latest` (v1.71.0): `go` 1.25.9 to 1.26.0 and grpc, x/net, x/sys, x/text, x/sync upgraded. |
| 2. Round trip proto to pdata JSON to collector unmarshalers and compare  | PASS: `TestTraceRoundTrip` and `TestMetricRoundTrip`, both `proto.Equal` to the original (fixtures as listed under the oracle).                                                                                       |
| 3. Ids hex, enums integers                                               | PASS (excerpts below).                                                                                                                                                                                                |

Added module set (modules providing packages under `ptrace`, `pmetric`, `ptraceotlp`,
`pmetricotlp`; `go list -deps`), versions as resolved:

```text
go.opentelemetry.io/collector/pdata       v1.65.0
go.opentelemetry.io/collector/featuregate v1.65.0
github.com/hashicorp/go-version           v1.9.0
github.com/json-iterator/go               v1.1.12
github.com/modern-go/concurrent           v0.0.0-20180306012644-bacd9c7ef1dd
github.com/modern-go/reflect2             v1.0.3-0.20250322232337-35a7c28c31ee
go.uber.org/multierr                      v1.11.0
```

The other deps of those packages (`google.golang.org/grpc v1.83.1`, `google.golang.org/protobuf
v1.36.12`, `genproto/googleapis/rpc`, `x/net`, `x/sys`, `x/text`) are already in `x`'s graph at the
same versions. `go mod tidy` also records `github.com/stretchr/testify v1.12.1 // indirect` in
`go.mod` (pdata's own test dependency; not downloaded for a consumer).

Observed JSON excerpts (pdata `ExportRequest.MarshalJSON`, one line each):

```json
{
  "resourceSpans": [
    {
      "resource": {
        "attributes": [
          { "key": "pn.spool.file_id", "value": { "stringValue": "abc" } }
        ]
      },
      "scopeSpans": [
        {
          "scope": { "name": "pn", "version": "1" },
          "spans": [
            {
              "traceId": "0102030405060708090a0b0c0d0e0f10",
              "spanId": "b1b2b3b4b5b6b7b8",
              "traceState": "k=v",
              "parentSpanId": "a1a2a3a4a5a6a7a8",
              "name": "child",
              "kind": 3,
              "startTimeUnixNano": "1700000000100000000",
              "endTimeUnixNano": "1700000000900000000",
              "attributes": [{ "key": "n", "value": { "intValue": "42" } }],
              "events": [
                { "timeUnixNano": "1700000000200000000", "name": "ev" }
              ],
              "links": [
                {
                  "traceId": "0102030405060708090a0b0c0d0e0f10",
                  "spanId": "c1c2c3c4c5c6c7c8"
                }
              ],
              "status": { "message": "boom", "code": 2 }
            }
          ]
        }
      ],
      "schemaUrl": "https://opentelemetry.io/schemas/1.37.0"
    }
  ]
}
```

```json
{
  "resourceMetrics": [
    {
      "scopeMetrics": [
        {
          "metrics": [
            {
              "name": "pn.count",
              "unit": "{x}",
              "sum": {
                "dataPoints": [
                  {
                    "startTimeUnixNano": "1700000000000000000",
                    "timeUnixNano": "1700000060000000000",
                    "asInt": "7"
                  }
                ],
                "aggregationTemporality": 1,
                "isMonotonic": true
              }
            },
            {
              "name": "pn.dur",
              "unit": "s",
              "histogram": {
                "dataPoints": [
                  {
                    "count": "4",
                    "sum": 12,
                    "bucketCounts": ["1", "2", "1"],
                    "explicitBounds": [1, 5],
                    "min": 0.5,
                    "max": 9.5
                  }
                ],
                "aggregationTemporality": 1
              }
            }
          ]
        }
      ]
    }
  ]
}
```

Notes for the Encoder authors: ids are lowercase hex (not base64); enums (`kind`, `status.code`,
`aggregationTemporality`) are integers; 64-bit integers, timestamps, `count` and `bucketCounts`
are JSON strings; `traceState` is emitted before `parentSpanId`; a root span has no `parentSpanId`
key. `proto.Equal` is the comparison, never JSON text equality.

## Boundary evidence (items 4 and 5)

`gomod2nix generate --with-deps` was NOT run: it is fetched from GitHub through nix and the spike
was restricted to the Go module proxy. The proxy used instead is the module set `go mod download
-json` returns for a consumer. It matches the committed lock files: `pn/gomod2nix.toml` has 28
`[mod.]` entries and `go mod download` returns 27 modules plus the `x` module itself;
`pg-nix-log-wrapped/gomod2nix.toml` has 25 and `go mod download` returns 24 plus `x`.

Method: copy each consumer (`nix-repo-base/modules/pn`, `.../pg-nix-log-wrapped`) to a scratch
directory, `go mod edit -replace github.com/phillipgreenii/x=<variant>`, `go mod tidy`, then
`go mod download -json`.

| Variant (x copy)                                                                         | pn modules downloaded | pg-nix-log-wrapped modules downloaded | In consumer `go.mod` / `go.sum`   |
| ---------------------------------------------------------------------------------------- | --------------------- | ------------------------------------- | --------------------------------- |
| baseline `x` (no pdata)                                                                  | 27                    | 24                                    | no pdata                          |
| subpackage `otelspool` with pdata in `x/go.mod`; consumer imports only `otelsetup`       | 27 (identical list)   | 24 (identical list)                   | no pdata, no new module           |
| nested module `x/otelspool` with its own `go.mod`; `x/go.mod` unchanged                  | 27 (identical list)   | not run (same mechanism)              | no pdata                          |
| subpackage, and `otelsetup` imports it (the planned shape), consumer imports `otelsetup` | not run               | 31 (the 24 plus the 7 modules above)  | pdata plus 6 indirect, all hashed |

Reading: Go's pruned module graph means a consumer's `go.mod` and `go.sum` record only modules that
supply packages it builds. `go list -m all` does show pdata (98 to 105 entries for pn) because the
graph lists the direct requirements of `x`, but those modules are not downloaded and so never
enter `gomod2nix.toml`. A nested module therefore buys nothing the subpackage does not already
give for the 9 other `x` consumers that never touch the spool.

Regeneration cost for the two importers (`pn`, `pg-nix-log-wrapped`; they are the only importers
of `otelsetup`): each `gomod2nix.toml` gains 7 `[mod.]` entries (pdata, featuregate, go-version,
json-iterator, modern-go concurrent and reflect2, multierr), each with a version and hash, and its
`cachePackages` gains the pdata and dependency packages. The `x` pseudo-version and hash entry
changes in every consumer regardless of this decision. Run
`go mod tidy && nix run github:nix-community/gomod2nix/<locked rev> -- generate --with-deps` in
each importer (the documented `nix-repo-base` procedure); the packet that first imports the spool
from `otelsetup` MUST do this for both and confirm the 7-module delta.

Why not a nested module: (a) the spool package needs `otelsetup` types (Config, Role) and
`otelsetup.Init` needs the spool factories, so a nested `x/otelspool` module that requires `x`
creates a module cycle with two pseudo-versions to bump in lockstep, and Init would have to give
up its in-module transport factories, contradicting "swapping HTTP for the spool is a Config
change"; (b) it adds a second `go.mod`, `go.sum` and update path to `x` for no measured saving;
(c) the only apps that pay with a subpackage are the two that already import `otelsetup` and
need the spool.

## Item 6

Not applicable: pdata is acceptable, so no hand-written Encoder field list is specified. If pdata
is later dropped (for example a format change away from OTLP-JSON), a hand-written Encoder is a new
`Encoder` implementation held to the same oracle.

## Consequences

- `x/go.mod` adds the seven modules above when the foundation packet lands; `go` stays 1.25.9.
- The default Encoder is thin: `UnmarshalProto` into `ptraceotlp.ExportRequest` /
  `pmetricotlp.ExportRequest`, then `MarshalJSON`, one record per line.
- A pdata major change or a format change is local to the Encoder implementation, guarded by the
  oracle above.
