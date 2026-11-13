package spool

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// JSONEncoder is the default Encoder: OTLP-JSON, one request per line, via
// the collector's pdata (ids are hex, enums are integers, 64-bit values are
// strings).
type JSONEncoder struct {
	// MaxRecordBytes caps one line; zero selects DefaultMaxRecordBytes.
	MaxRecordBytes int
}

func (e JSONEncoder) limit() int {
	if e.MaxRecordBytes > 0 {
		return e.MaxRecordBytes
	}
	return DefaultMaxRecordBytes
}

// Extension implements Encoder.
func (JSONEncoder) Extension() string { return ".jsonl" }

// EncodeTraces implements Encoder.
func (e JSONEncoder) EncodeTraces(req *coltrace.ExportTraceServiceRequest) ([][]byte, error) {
	limit := e.limit()
	var out [][]byte
	for _, part := range splitTraces(req, limit) {
		recs, err := encodeBounded(part, limit, encodeTraceJSON, func(r *coltrace.ExportTraceServiceRequest) []*coltrace.ExportTraceServiceRequest {
			return splitTraces(r, proto.Size(r)/2)
		})
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// EncodeMetrics implements Encoder.
func (e JSONEncoder) EncodeMetrics(req *colmetrics.ExportMetricsServiceRequest) ([][]byte, error) {
	limit := e.limit()
	var out [][]byte
	for _, part := range splitMetrics(req, limit) {
		recs, err := encodeBounded(part, limit, encodeMetricJSON, func(r *colmetrics.ExportMetricsServiceRequest) []*colmetrics.ExportMetricsServiceRequest {
			return splitMetrics(r, proto.Size(r)/2)
		})
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func encodeTraceJSON(req *coltrace.ExportTraceServiceRequest) ([]byte, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	pd := ptraceotlp.NewExportRequest()
	if err := pd.UnmarshalProto(b); err != nil {
		return nil, fmt.Errorf("spool: decode trace request: %w", err)
	}
	j, err := pd.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return append(j, '\n'), nil
}

func encodeMetricJSON(req *colmetrics.ExportMetricsServiceRequest) ([]byte, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	pd := pmetricotlp.NewExportRequest()
	if err := pd.UnmarshalProto(b); err != nil {
		return nil, fmt.Errorf("spool: decode metric request: %w", err)
	}
	j, err := pd.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return append(j, '\n'), nil
}

// encodeBounded encodes req; if the record exceeds limit (JSON is larger than
// the protobuf the pre-split measured) it splits again and recurses.
func encodeBounded[R proto.Message](req R, limit int, enc func(R) ([]byte, error), split func(R) []R) ([][]byte, error) {
	rec, err := enc(req)
	if err != nil {
		return nil, err
	}
	if len(rec) <= limit {
		return [][]byte{rec}, nil
	}
	parts := split(req)
	if len(parts) < 2 {
		return nil, tooLarge("record", len(rec), limit)
	}
	var out [][]byte
	for _, p := range parts {
		recs, err := encodeBounded(p, limit, enc, split)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}
