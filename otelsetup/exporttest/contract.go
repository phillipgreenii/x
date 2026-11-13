package exporttest

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Mismatch is one way a transport differs from the oracle.
type Mismatch struct {
	Scenario  string
	Transport string
	// Signal is "traces", "metrics" or "run" (the transport failed outright).
	Signal string
	Detail string
}

func (m Mismatch) String() string {
	return fmt.Sprintf("scenario %s, transport %s, %s: %s", m.Scenario, m.Transport, m.Signal, m.Detail)
}

// CompareScenario runs s through every transport, using a fresh directory from
// newDir for each, and compares each one after Normalize against the first
// (the oracle, normally HTTP()). It also checks, per transport, that metric
// start times do not follow their times and that a Scenario.WantSplit really
// split a chunking transport's output.
func CompareScenario(newDir func() string, s Scenario, transports ...Transport) []Mismatch {
	var out []Mismatch
	if len(transports) < 2 {
		return []Mismatch{{Scenario: s.Name, Transport: "-", Signal: "run", Detail: "need an oracle and at least one other transport"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	caps := make([]Capture, len(transports))
	ok := make([]bool, len(transports))
	for i, tr := range transports {
		c, err := tr.Run(ctx, newDir(), s)
		if err != nil {
			out = append(out, Mismatch{s.Name, tr.Name(), "run", err.Error()})
			continue
		}
		caps[i], ok[i] = c, true
		if bad := checkMetricTimes(c.Metrics); len(bad) > 0 {
			out = append(out, Mismatch{s.Name, tr.Name(), "metrics", fmt.Sprintf("start time missing or after time for %v", bad)})
		}
		if s.WantSplit && c.Chunked && (c.TraceRecords < 2 || c.MetricRecords < 2) {
			out = append(out, Mismatch{s.Name, tr.Name(), "run", fmt.Sprintf("scenario must split, got %d trace and %d metric records", c.TraceRecords, c.MetricRecords)})
		}
	}
	if !ok[0] {
		return out
	}
	want := Normalize(caps[0])
	for i := 1; i < len(transports); i++ {
		if !ok[i] {
			continue
		}
		got := Normalize(caps[i])
		if d := equalDiff(want.Traces, got.Traces); d != "" {
			out = append(out, Mismatch{s.Name, transports[i].Name(), "traces", d})
		}
		if d := equalDiff(want.Metrics, got.Metrics); d != "" {
			out = append(out, Mismatch{s.Name, transports[i].Name(), "metrics", d})
		}
	}
	return out
}

// ContractMismatches runs every scenario; an empty result means the contract holds.
func ContractMismatches(newDir func() string, transports ...Transport) []Mismatch {
	var out []Mismatch
	for _, s := range Scenarios() {
		out = append(out, CompareScenario(newDir, s, transports...)...)
	}
	return out
}

// RunContract runs every scenario through every transport and fails t for each
// mismatch against the first transport, the oracle (use HTTP() first).
func RunContract(t *testing.T, transports ...Transport) {
	t.Helper()
	for _, s := range Scenarios() {
		t.Run(s.Name, func(t *testing.T) {
			for _, m := range CompareScenario(t.TempDir, s, transports...) {
				t.Error(m)
			}
		})
	}
}
