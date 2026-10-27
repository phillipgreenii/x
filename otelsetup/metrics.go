package otelsetup

import "encoding/json"

//go:generate go run ./internal/genmetrics

// MetricSpec describes one metric emitted by pn / pg-nix-log-wrapped.
type MetricSpec struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	Kind string `json:"kind"`
	// Buckets, when set on a histogram, are its explicit bucket boundaries.
	// Init applies them to the instrument of that name instead of the
	// seconds-based default (a View would otherwise force the default onto
	// every histogram). Omitted from metrics.json when empty.
	Buckets []float64 `json:"buckets,omitempty"`
}

// PlanBuildsBuckets are the explicit boundaries of pn.plan.builds (number of
// derivations nix planned to build). 400 is an exact edge on purpose: it is the
// operator's alert threshold (bead pg2-sdbhh4.10), and a Prometheus bucket is
// le-inclusive, so a plan of exactly 400 is counted in the le="400" bucket.
var PlanBuildsBuckets = []float64{0, 25, 50, 100, 200, 300, 400, 600, 1000, 2000}

// Metrics is the catalog of every metric name and unit. It is the source for
// the generated metrics.json that dashboard lint checks PromQL names against.
// Add a metric here, then run `go generate ./otelsetup`.
var Metrics = []MetricSpec{
	{Name: "pn.command.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.invocation.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.build.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.derivations", Unit: "{derivation}", Kind: "counter"},
	{Name: "pn.plan.builds", Unit: "{derivation}", Kind: "histogram", Buckets: PlanBuildsBuckets},
}

// catalogBuckets returns the catalog's explicit boundaries for the histogram
// named name, if it declares any.
func catalogBuckets(name string) ([]float64, bool) {
	for _, m := range Metrics {
		if m.Name == name && len(m.Buckets) > 0 {
			return m.Buckets, true
		}
	}
	return nil, false
}

// MetricsJSON renders the catalog exactly as committed in metrics.json.
func MetricsJSON() ([]byte, error) {
	b, err := json.MarshalIndent(Metrics, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
