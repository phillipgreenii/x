package exporttest

import (
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// LoopbackReceiver is an in-process OTLP/HTTP collector: an httptest server on
// an ephemeral loopback port that decodes protobuf request bodies into the
// standard export requests. Nothing leaves the machine.
type LoopbackReceiver struct {
	srv *httptest.Server

	mu      sync.Mutex
	traces  []*coltrace.ExportTraceServiceRequest
	metrics []*colmetrics.ExportMetricsServiceRequest
	errs    []error
}

// NewLoopbackReceiver starts the receiver; the caller MUST Close it.
func NewLoopbackReceiver() *LoopbackReceiver {
	r := &LoopbackReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handle))
	return r
}

// Addr is the host:port the receiver listens on.
func (r *LoopbackReceiver) Addr() string { return r.srv.Listener.Addr().(*net.TCPAddr).String() }

// Close stops the server.
func (r *LoopbackReceiver) Close() { r.srv.Close() }

// Traces returns the trace requests received so far, in arrival order.
func (r *LoopbackReceiver) Traces() []*coltrace.ExportTraceServiceRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*coltrace.ExportTraceServiceRequest(nil), r.traces...)
}

// Metrics returns the metric requests received so far, in arrival order.
func (r *LoopbackReceiver) Metrics() []*colmetrics.ExportMetricsServiceRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*colmetrics.ExportMetricsServiceRequest(nil), r.metrics...)
}

// Errs returns the requests the receiver could not accept (wrong path or
// content type, undecodable body).
func (r *LoopbackReceiver) Errs() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func (r *LoopbackReceiver) fail(w http.ResponseWriter, code int, err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
	http.Error(w, err.Error(), code)
}

func (r *LoopbackReceiver) handle(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		r.fail(w, http.StatusMethodNotAllowed, fmt.Errorf("loopback: method %s", req.Method))
		return
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/x-protobuf" {
		r.fail(w, http.StatusUnsupportedMediaType, fmt.Errorf("loopback: content type %q", ct))
		return
	}
	var body io.Reader = req.Body
	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(req.Body)
		if err != nil {
			r.fail(w, http.StatusBadRequest, fmt.Errorf("loopback: gzip: %w", err))
			return
		}
		defer func() { _ = gz.Close() }()
		body = gz
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		r.fail(w, http.StatusBadRequest, fmt.Errorf("loopback: read body: %w", err))
		return
	}
	var resp proto.Message
	switch req.URL.Path {
	case "/v1/traces":
		m := &coltrace.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(raw, m); err != nil {
			r.fail(w, http.StatusBadRequest, fmt.Errorf("loopback: decode traces: %w", err))
			return
		}
		r.mu.Lock()
		r.traces = append(r.traces, m)
		r.mu.Unlock()
		resp = &coltrace.ExportTraceServiceResponse{}
	case "/v1/metrics":
		m := &colmetrics.ExportMetricsServiceRequest{}
		if err := proto.Unmarshal(raw, m); err != nil {
			r.fail(w, http.StatusBadRequest, fmt.Errorf("loopback: decode metrics: %w", err))
			return
		}
		r.mu.Lock()
		r.metrics = append(r.metrics, m)
		r.mu.Unlock()
		resp = &colmetrics.ExportMetricsServiceResponse{}
	default:
		r.fail(w, http.StatusNotFound, fmt.Errorf("loopback: path %s", req.URL.Path))
		return
	}
	out, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
