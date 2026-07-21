package eval

import (
	"math"
	"testing"
	"time"
)

func relSet(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

const eps = 1e-9

func approx(a, b float64) bool { return math.Abs(a-b) < eps }

func TestRecallAtK(t *testing.T) {
	tests := []struct {
		name string
		res  RankedResult
		k    int
		want float64
	}{
		{
			name: "single relevant at top",
			res:  RankedResult{RelevantKeys: relSet("a"), RankedKeys: []string{"a", "b", "c"}},
			k:    3, want: 1.0,
		},
		{
			name: "relevant below cutoff missed",
			res:  RankedResult{RelevantKeys: relSet("c"), RankedKeys: []string{"a", "b", "c"}},
			k:    2, want: 0.0,
		},
		{
			name: "two relevant one found",
			res:  RankedResult{RelevantKeys: relSet("a", "z"), RankedKeys: []string{"a", "b"}},
			k:    2, want: 0.5,
		},
		{
			name: "no relevant labels is vacuously satisfied",
			res:  RankedResult{RelevantKeys: relSet(), RankedKeys: []string{"a"}},
			k:    3, want: 1.0,
		},
		{
			name: "empty result set",
			res:  RankedResult{RelevantKeys: relSet("a"), RankedKeys: nil},
			k:    3, want: 0.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recallAtK(tt.res, tt.k); !approx(got, tt.want) {
				t.Fatalf("recallAtK = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNDCGAtK(t *testing.T) {
	// Relevant doc at rank 0: DCG = 1/log2(2) = 1, IDCG = 1 → 1.0.
	// Relevant doc at rank 1: DCG = 1/log2(3) ≈ 0.6309, IDCG = 1 → ~0.6309.
	tests := []struct {
		name string
		res  RankedResult
		k    int
		want float64
	}{
		{
			name: "perfect ranking",
			res:  RankedResult{RelevantKeys: relSet("a"), RankedKeys: []string{"a", "b"}},
			k:    3, want: 1.0,
		},
		{
			name: "relevant at second position",
			res:  RankedResult{RelevantKeys: relSet("a"), RankedKeys: []string{"b", "a"}},
			k:    3, want: 1.0 / math.Log2(3),
		},
		{
			name: "two relevant perfect order",
			res:  RankedResult{RelevantKeys: relSet("a", "b"), RankedKeys: []string{"a", "b", "c"}},
			k:    3, want: 1.0,
		},
		{
			name: "no relevant in list scores zero",
			res:  RankedResult{RelevantKeys: relSet("z"), RankedKeys: []string{"a", "b"}},
			k:    3, want: 0.0,
		},
		{
			name: "no labels vacuous",
			res:  RankedResult{RelevantKeys: relSet(), RankedKeys: []string{"a"}},
			k:    3, want: 1.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ndcgAtK(tt.res, tt.k); !approx(got, tt.want) {
				t.Fatalf("ndcgAtK = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReciprocalRank(t *testing.T) {
	tests := []struct {
		name string
		res  RankedResult
		want float64
	}{
		{"first hit at rank 1", RankedResult{RelevantKeys: relSet("a"), RankedKeys: []string{"a", "b"}}, 1.0},
		{"first hit at rank 2", RankedResult{RelevantKeys: relSet("b"), RankedKeys: []string{"a", "b"}}, 0.5},
		{"first hit at rank 3", RankedResult{RelevantKeys: relSet("c"), RankedKeys: []string{"a", "b", "c"}}, 1.0 / 3.0},
		{"no hit", RankedResult{RelevantKeys: relSet("z"), RankedKeys: []string{"a", "b"}}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reciprocalRank(tt.res); !approx(got, tt.want) {
				t.Fatalf("reciprocalRank = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAggregate(t *testing.T) {
	results := []RankedResult{
		{Query: "q1", RelevantKeys: relSet("a"), RankedKeys: []string{"a", "b"}},
		{Query: "q2", RelevantKeys: relSet("b"), RankedKeys: []string{"a", "b"}},
	}
	rep := Aggregate(results, 2)
	if rep.NumQueries != 2 {
		t.Fatalf("NumQueries = %d, want 2", rep.NumQueries)
	}
	// recall@2: both relevant docs are within top-2 → 1.0 each → mean 1.0.
	if !approx(rep.RecallAtK, 1.0) {
		t.Fatalf("RecallAtK = %v, want 1.0", rep.RecallAtK)
	}
	// MRR: q1 first relevant at rank 1 (1.0), q2 at rank 2 (0.5) → mean 0.75.
	if !approx(rep.MRR, 0.75) {
		t.Fatalf("MRR = %v, want 0.75", rep.MRR)
	}
	if len(rep.PerQuery) != 2 {
		t.Fatalf("PerQuery len = %d, want 2", len(rep.PerQuery))
	}
}

func TestSummarizeLatency(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
	}
	rep := summarizeLatency(samples)
	if rep.Samples != 5 {
		t.Fatalf("Samples = %d, want 5", rep.Samples)
	}
	// nearest-rank p50 over 5 samples → ceil(0.5*5)=3 → index 2 → 30ms.
	if rep.P50 != 30*time.Millisecond {
		t.Fatalf("P50 = %v, want 30ms", rep.P50)
	}
	// p95 → ceil(0.95*5)=5 → index 4 → 50ms.
	if rep.P95 != 50*time.Millisecond {
		t.Fatalf("P95 = %v, want 50ms", rep.P95)
	}
	if rep.Max != 50*time.Millisecond {
		t.Fatalf("Max = %v, want 50ms", rep.Max)
	}
}

func TestSummarizeLatencyEmpty(t *testing.T) {
	rep := summarizeLatency(nil)
	if rep.Samples != 0 || rep.P50 != 0 || rep.P95 != 0 {
		t.Fatalf("empty sample should be zero report, got %+v", rep)
	}
}
