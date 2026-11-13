package spool

import (
	"errors"
	"fmt"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Record size budget. The writer cap MUST stay below the collector receiver's
// max_log_size (16 MiB); a test pins the invariant.
const (
	// DefaultMaxRecordBytes is the largest record (one line) the default
	// encoder emits.
	DefaultMaxRecordBytes = 4 << 20
	// ReceiverMaxLogSize is the max_log_size the collector's otlpjsonfile
	// receivers are configured with.
	ReceiverMaxLogSize = 16 << 20
)

// ErrRecordTooLarge reports that a single span or metric alone exceeds the
// record cap, so it cannot be split further.
var ErrRecordTooLarge = errors.New("spool: a single item exceeds the record size cap")

// Encoder is the format seam. It turns ONE standard OTLP export request into
// one or more self-contained records. Swap contract:
//
//   - each returned record is standalone (carries its own resource and scope)
//     and already framed for the format (JSON lines end in a newline); the
//     exporter concatenates the records of one request into one file;
//   - an implementation MUST bound the size of each record (splitting the
//     request BEFORE encoding) and return ErrRecordTooLarge for an
//     indivisible item over the bound;
//   - Extension (with the leading dot) names the files; the collector side
//     needs a receiver for it and the sweep matches it;
//   - it MUST preserve the order of Resource.Attributes (the exporter
//     prepends pn.spool.file_id so it lands in the file's first bytes) and
//     MUST NOT modify the request.
//
// Every Encoder MUST pass the round-trip oracle in the package tests: decode
// with the format's reference decoder and require proto.Equal to the input.
type Encoder interface {
	Extension() string
	EncodeTraces(req *coltrace.ExportTraceServiceRequest) ([][]byte, error)
	EncodeMetrics(req *colmetrics.ExportMetricsServiceRequest) ([][]byte, error)
}

func tooLarge(kind string, size, limit int) error {
	return fmt.Errorf("%w: %s of %d bytes, cap %d", ErrRecordTooLarge, kind, size, limit)
}
