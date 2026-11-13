package exporttest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/x/otelsetup/spool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Transport runs a Scenario through one export path and returns what it
// delivered. dir is a fresh empty directory the transport may use.
type Transport interface {
	Name() string
	Run(ctx context.Context, dir string, s Scenario) (Capture, error)
}

// drive instruments s with providers wired like otelsetup.Init (same resource
// attributes, batch span processor, periodic reader, histogram View) around the
// given exporters, then shuts the providers down, which flushes them.
func drive(ctx context.Context, s Scenario, te sdktrace.SpanExporter, me sdkmetric.Exporter) error {
	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", "contract"),
		attribute.String("service.version", "9.9.9"),
	))
	if err != nil {
		return err
	}
	buckets := s.Buckets
	if buckets == nil {
		buckets = DefaultBuckets
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(te))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(time.Hour))),
		sdkmetric.WithView(func(i sdkmetric.Instrument) (sdkmetric.Stream, bool) {
			if i.Kind != sdkmetric.InstrumentKindHistogram {
				return sdkmetric.Stream{}, false
			}
			return sdkmetric.Stream{
				Name: i.Name, Description: i.Description, Unit: i.Unit,
				Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: buckets},
			}, true
		}),
	)
	s.Run(ctx, Providers{Traces: tp, Metrics: mp})
	return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
}

type httpTransport struct{}

// HTTP is the oracle transport: the real OTLP/HTTP trace and metric exporters,
// configured as otelsetup.Init configures them (insecure endpoint, /v1 paths,
// delta temporality), talking to an in-process LoopbackReceiver.
func HTTP() Transport { return httpTransport{} }

func (httpTransport) Name() string { return "http-loopback" }

func (httpTransport) Run(ctx context.Context, _ string, s Scenario) (Capture, error) {
	rcv := NewLoopbackReceiver()
	defer rcv.Close()
	te, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(rcv.Addr()), otlptracehttp.WithURLPath("/v1/traces"),
		otlptracehttp.WithInsecure(), otlptracehttp.WithTimeout(10*time.Second))
	if err != nil {
		return Capture{}, err
	}
	me, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(rcv.Addr()), otlpmetrichttp.WithURLPath("/v1/metrics"),
		otlpmetrichttp.WithInsecure(), otlpmetrichttp.WithTimeout(10*time.Second),
		otlpmetrichttp.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality {
			return metricdata.DeltaTemporality
		}))
	if err != nil {
		return Capture{}, err
	}
	if err := drive(ctx, s, te, me); err != nil {
		return Capture{}, err
	}
	if errs := rcv.Errs(); len(errs) > 0 {
		return Capture{}, errors.Join(errs...)
	}
	return Capture{Traces: rcv.Traces(), Metrics: rcv.Metrics()}, nil
}

type spoolTransport struct{ codec Codec }

// Spool is the file-spool transport using codec's Encoder. The records it
// writes are decoded back to OTLP protobuf by the codec's decoders.
func Spool(codec Codec) Transport { return spoolTransport{codec: codec} }

func (t spoolTransport) Name() string { return "spool-" + t.codec.Name }

func (t spoolTransport) Run(ctx context.Context, dir string, s Scenario) (Capture, error) {
	var hookErrs []error
	cfg := spool.Config{
		Sink:    spool.NewFileSink(dir, spool.RoleUser, nil),
		Encoder: t.codec.NewEncoder(s.SpoolRecordBytes),
		Service: "contract",
		Hooks:   spool.Hooks{OnError: func(err error) { hookErrs = append(hookErrs, err) }},
	}
	te, err := spool.NewTraceExporter(ctx, cfg)
	if err != nil {
		return Capture{}, err
	}
	if err := drive(ctx, s, te, spool.NewMetricExporter(cfg)); err != nil {
		return Capture{}, err
	}
	if len(hookErrs) > 0 {
		return Capture{}, errors.Join(hookErrs...)
	}
	c := Capture{Chunked: !t.codec.Unbounded}
	ext := cfg.Encoder.Extension()
	for _, sig := range []string{spool.SignalTraces, spool.SignalMetrics} {
		files, err := spoolFiles(filepath.Join(dir, sig), ext)
		if err != nil {
			return Capture{}, err
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return Capture{}, err
			}
			if sig == spool.SignalTraces {
				reqs, err := t.codec.DecodeTraces(b)
				if err != nil {
					return Capture{}, fmt.Errorf("decode %s: %w", f, err)
				}
				c.Traces = append(c.Traces, reqs...)
				c.TraceRecords += len(reqs)
			} else {
				reqs, err := t.codec.DecodeMetrics(b)
				if err != nil {
					return Capture{}, fmt.Errorf("decode %s: %w", f, err)
				}
				c.Metrics = append(c.Metrics, reqs...)
				c.MetricRecords += len(reqs)
			}
		}
	}
	return c, nil
}

// spoolFiles lists the published files of one signal directory (none when the
// directory does not exist, which is what an empty flush leaves), and rejects
// anything that is not a finished file of the Encoder's extension: a leftover
// scratch file or a wrong extension is a transport bug the collector would
// trip on.
func spoolFiles(dir, ext string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ext) {
			return nil, fmt.Errorf("spool dir %s holds %q, want only finished *%s files", dir, e.Name(), ext)
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
