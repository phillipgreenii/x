package otelsetup

import "encoding/json"

//go:generate go run ./internal/genmetrics

// MetricSpec describes one metric emitted by pn / pg-nix-log-wrapped.
type MetricSpec struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	Kind string `json:"kind"`
}

// Metrics is the catalog of every metric name and unit. It is the source for
// the generated metrics.json that dashboard lint checks PromQL names against.
// Add a metric here, then run `go generate ./otelsetup`.
var Metrics = []MetricSpec{
	{Name: "pn.command.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.invocation.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.build.duration", Unit: "s", Kind: "histogram"},
	{Name: "nix.derivations", Unit: "{derivation}", Kind: "counter"},
}

// MetricsJSON renders the catalog exactly as committed in metrics.json.
func MetricsJSON() ([]byte, error) {
	b, err := json.MarshalIndent(Metrics, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
