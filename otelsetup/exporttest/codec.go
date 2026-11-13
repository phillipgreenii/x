package exporttest

import (
	"bytes"
	"fmt"

	"github.com/phillipgreenii/x/otelsetup/spool"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Codec registers a spool Encoder with the suite: the Encoder plus the inverse
// decoders that turn one finished file back into the OTLP requests (one per
// record) it carries. To add an Encoder, build a Codec and pass Spool(codec)
// to RunContract; it MUST pass the same scenarios as the default.
type Codec struct {
	Name string
	// Unbounded marks an Encoder that does not split by a record cap, so
	// Scenario.WantSplit is not required of it.
	Unbounded bool
	// NewEncoder returns the Encoder; maxRecordBytes is the scenario's record
	// cap (0 means the Encoder's default) for Encoders that bound records.
	NewEncoder    func(maxRecordBytes int) spool.Encoder
	DecodeTraces  func(file []byte) ([]*coltrace.ExportTraceServiceRequest, error)
	DecodeMetrics func(file []byte) ([]*colmetrics.ExportMetricsServiceRequest, error)
}

func jsonEncoder(max int) spool.Encoder { return spool.JSONEncoder{MaxRecordBytes: max} }

func lines(file []byte) [][]byte {
	var out [][]byte
	for _, l := range bytes.Split(file, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// JSONCodec is the default Encoder with its own inverse: the collector pdata
// OTLP request types' UnmarshalJSON.
func JSONCodec() Codec {
	return Codec{
		Name:       "json",
		NewEncoder: jsonEncoder,
		DecodeTraces: func(file []byte) ([]*coltrace.ExportTraceServiceRequest, error) {
			var out []*coltrace.ExportTraceServiceRequest
			for _, l := range lines(file) {
				r := ptraceotlp.NewExportRequest()
				if err := r.UnmarshalJSON(l); err != nil {
					return nil, err
				}
				m, err := traceProto(r)
				if err != nil {
					return nil, err
				}
				out = append(out, m)
			}
			return out, nil
		},
		DecodeMetrics: func(file []byte) ([]*colmetrics.ExportMetricsServiceRequest, error) {
			var out []*colmetrics.ExportMetricsServiceRequest
			for _, l := range lines(file) {
				r := pmetricotlp.NewExportRequest()
				if err := r.UnmarshalJSON(l); err != nil {
					return nil, err
				}
				m, err := metricProto(r)
				if err != nil {
					return nil, err
				}
				out = append(out, m)
			}
			return out, nil
		},
	}
}

// JSONCodecIndependent decodes the SAME default Encoder with the collector's
// own ptrace and pmetric JSON unmarshalers (the ones a collector file receiver
// uses to read the line), not the Encoder-side request types, so the check is
// not circular with how the Encoder wrote it.
func JSONCodecIndependent() Codec {
	c := JSONCodec()
	c.Name = "json-collector-unmarshaler"
	c.DecodeTraces = func(file []byte) ([]*coltrace.ExportTraceServiceRequest, error) {
		var out []*coltrace.ExportTraceServiceRequest
		for _, l := range lines(file) {
			td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(l)
			if err != nil {
				return nil, err
			}
			m, err := traceProto(ptraceotlp.NewExportRequestFromTraces(td))
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	c.DecodeMetrics = func(file []byte) ([]*colmetrics.ExportMetricsServiceRequest, error) {
		var out []*colmetrics.ExportMetricsServiceRequest
		for _, l := range lines(file) {
			md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(l)
			if err != nil {
				return nil, err
			}
			m, err := metricProto(pmetricotlp.NewExportRequestFromMetrics(md))
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	return c
}

func traceProto(r ptraceotlp.ExportRequest) (*coltrace.ExportTraceServiceRequest, error) {
	b, err := r.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("pdata to proto: %w", err)
	}
	m := &coltrace.ExportTraceServiceRequest{}
	return m, proto.Unmarshal(b, m)
}

func metricProto(r pmetricotlp.ExportRequest) (*colmetrics.ExportMetricsServiceRequest, error) {
	b, err := r.MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("pdata to proto: %w", err)
	}
	m := &colmetrics.ExportMetricsServiceRequest{}
	return m, proto.Unmarshal(b, m)
}
