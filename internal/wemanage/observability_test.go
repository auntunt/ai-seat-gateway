package wemanage

import "testing"

func TestUsageSignalsConservative(t *testing.T) {
	x := sessionDiagnostic{Requests: 10, Input: 2000000, Cached: 1000, Output: 1000, Errors: 3, Strong: 10}
	signals := evaluateUsage(x)
	if len(signals) != 4 {
		t.Fatalf("want four review signals, got %+v", signals)
	}
	for _, s := range signals {
		if !validUsageRule(s.Rule) || s.State != "pending" || s.Evidence == "" || s.Verify == "" || s.Action == "" {
			t.Fatal(s)
		}
	}
	for _, normal := range []sessionDiagnostic{{}, {Requests: 1, Input: 100000, Strong: 1, Errors: 1}, {Requests: 10, Input: 100000, Cached: 90000, Output: 10000}} {
		if len(evaluateUsage(normal)) != 0 {
			t.Fatal("false positive", normal)
		}
	}
}
