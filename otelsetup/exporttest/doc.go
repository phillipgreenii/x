// Package exporttest is the transport-parity contract suite for otelsetup. It
// is TEST-ONLY support code: no production package imports it (a test in this
// package pins that), so apps never pay for the oracle or for net/http/httptest.
//
// The property under test: for the same spans and instruments, every
// transport hands the collector the same standard OTLP request. The oracle is
// the real OTLP/HTTP exporters talking to an in-process loopback receiver
// (LoopbackReceiver). Every other transport, today the file spool with its
// Encoder, is run through the same scenarios and its output decoded back to
// OTLP protobuf. Normalize strips what legitimately differs (the spool-only
// pn.spool.file_id resource attribute, absolute timestamps, ordering, and the
// random ids, which are remapped by a stable bijection so parent and link
// relationships survive) and the normalized requests must be equal.
//
// Swapping a transport or an Encoder must not change callers; to prove an
// Encoder, register a Codec (its Encoder plus the inverse decoders) and pass
// Spool(codec) to RunContract next to HTTP().
package exporttest
