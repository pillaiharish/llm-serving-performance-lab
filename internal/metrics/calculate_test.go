package metrics

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

func TestCalculateDeterministicMetrics(t *testing.T) {
	start := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	observation := benchmark.RequestObservation{
		RequestID:         "req-000001",
		RequestStartedAt:  timePointer(start),
		HeadersReceivedAt: timePointer(start.Add(40 * time.Millisecond)),
		FirstByteAt:       timePointer(start.Add(45 * time.Millisecond)),
		FirstContentAt:    timePointer(start.Add(100 * time.Millisecond)),
		LastContentAt:     timePointer(start.Add(190 * time.Millisecond)),
		CompletedAt:       timePointer(start.Add(200 * time.Millisecond)),
		StreamEvents: []benchmark.StreamEvent{
			{Sequence: 1, ReceivedAt: start.Add(100 * time.Millisecond), HasContent: true, ContentBytes: 1},
			{Sequence: 2, ReceivedAt: start.Add(120 * time.Millisecond), HasContent: true, ContentBytes: 2},
			{Sequence: 3, ReceivedAt: start.Add(150 * time.Millisecond), HasContent: true, ContentBytes: 3},
			{Sequence: 4, ReceivedAt: start.Add(190 * time.Millisecond), HasContent: true, ContentBytes: 4},
			{Sequence: 5, ReceivedAt: start.Add(195 * time.Millisecond)},
		},
		Usage: benchmark.TokenUsage{
			InputTokens:  5,
			OutputTokens: 10,
			TotalTokens:  15,
			Source:       "server_usage",
			Available:    true,
		},
		ResponseBodyBytes: 500,
	}

	got := Calculate(observation)
	assertScalar(t, got.TimeToHeaders, 40)
	assertScalar(t, got.TTFB, 45)
	assertScalar(t, got.TTFT, 100)
	assertScalar(t, got.TTLT, 190)
	assertScalar(t, got.E2E, 200)
	assertScalar(t, got.TPOT, 10)
	assertScalar(t, got.OutputTokensPerSecond, 50)
	assertScalar(t, got.DecodeTokensPerSecond, 100)

	if !got.InterChunkLatency.Available {
		t.Fatalf("inter-chunk latency unavailable: %s", got.InterChunkLatency.Reason)
	}
	if got.InterChunkLatency.Count != 3 || got.InterChunkLatency.MeanMS != 30 || got.InterChunkLatency.MinMS != 20 || got.InterChunkLatency.MaxMS != 40 {
		t.Fatalf("unexpected inter-chunk summary: %+v", got.InterChunkLatency)
	}
	if want := []float64{20, 30, 40}; !reflect.DeepEqual(got.InterChunkLatency.ValuesMS, want) {
		t.Fatalf("inter-chunk values = %v, want %v", got.InterChunkLatency.ValuesMS, want)
	}
	if got.ITL.Available || got.ITL.Source != "not_available" || got.ITL.Reason != TrueITLReason {
		t.Fatalf("unexpected ITL representation: %+v", got.ITL)
	}
	if got.StreamEventCount != 5 || got.ContentEventCount != 4 || got.ResponseBytes != 10 || got.ResponseBodyBytes != 500 {
		t.Fatalf("unexpected counts: %+v", got)
	}
}

func TestCalculateUnavailableAndEdgeMetrics(t *testing.T) {
	start := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	base := benchmark.RequestObservation{
		RequestStartedAt: timePointer(start),
		FirstContentAt:   timePointer(start.Add(10 * time.Millisecond)),
		LastContentAt:    timePointer(start.Add(20 * time.Millisecond)),
		CompletedAt:      timePointer(start.Add(50 * time.Millisecond)),
		StreamEvents: []benchmark.StreamEvent{
			{Sequence: 1, ReceivedAt: start.Add(10 * time.Millisecond), HasContent: true},
			{Sequence: 2, ReceivedAt: start.Add(20 * time.Millisecond), HasContent: true},
		},
		Usage: benchmark.TokenUsage{OutputTokens: 2, Source: "server_usage", Available: true},
	}

	tests := []struct {
		name  string
		alter func(*benchmark.RequestObservation)
		check func(*testing.T, RequestMetrics)
	}{
		{
			name: "missing first byte",
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TTFB)
			},
		},
		{
			name: "missing usage",
			alter: func(value *benchmark.RequestObservation) {
				value.Usage = benchmark.TokenUsage{Source: "not_available"}
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.OutputTokensPerSecond)
				assertUnavailable(t, got.DecodeTokensPerSecond)
			},
		},
		{
			name:  "zero output tokens",
			alter: func(value *benchmark.RequestObservation) { value.Usage.OutputTokens = 0 },
			check: func(t *testing.T, got RequestMetrics) {
				assertScalar(t, got.OutputTokensPerSecond, 0)
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
			},
		},
		{
			name:  "one output token",
			alter: func(value *benchmark.RequestObservation) { value.Usage.OutputTokens = 1 },
			check: func(t *testing.T, got RequestMetrics) {
				assertScalar(t, got.OutputTokensPerSecond, 20)
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
			},
		},
		{
			name: "no content",
			alter: func(value *benchmark.RequestObservation) {
				value.FirstContentAt = nil
				value.LastContentAt = nil
				value.StreamEvents = nil
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TTFT)
				assertUnavailable(t, got.TTLT)
				assertUnavailable(t, got.InterChunkScalarForTest())
			},
		},
		{
			name: "one content event",
			alter: func(value *benchmark.RequestObservation) {
				value.StreamEvents = value.StreamEvents[:1]
				value.LastContentAt = value.FirstContentAt
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
				if got.InterChunkLatency.Available {
					t.Fatal("inter-chunk latency unexpectedly available")
				}
			},
		},
		{
			name:  "zero E2E duration",
			alter: func(value *benchmark.RequestObservation) { value.CompletedAt = value.RequestStartedAt },
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.OutputTokensPerSecond)
			},
		},
		{
			name: "reversed timestamp",
			alter: func(value *benchmark.RequestObservation) {
				value.HeadersReceivedAt = timePointer(start.Add(-time.Millisecond))
			},
			check: func(t *testing.T, got RequestMetrics) { assertUnavailable(t, got.TimeToHeaders) },
		},
		{
			name:  "request error retains legitimate E2E evidence",
			alter: func(value *benchmark.RequestObservation) { value.Error = "timeout" },
			check: func(t *testing.T, got RequestMetrics) { assertScalar(t, got.E2E, 50) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			observation.StreamEvents = append([]benchmark.StreamEvent(nil), base.StreamEvents...)
			if test.alter != nil {
				test.alter(&observation)
			}
			got := Calculate(observation)
			test.check(t, got)
			assertFiniteMetrics(t, got)
		})
	}
}

func (m RequestMetrics) InterChunkScalarForTest() Scalar {
	return Scalar{Available: m.InterChunkLatency.Available, Value: m.InterChunkLatency.MeanMS, Unit: "ms", Reason: m.InterChunkLatency.Reason}
}

func assertScalar(t *testing.T, got Scalar, want float64) {
	t.Helper()
	if !got.Available || math.Abs(got.Value-want) > 1e-9 {
		t.Fatalf("scalar = %+v, want available value %v", got, want)
	}
}

func assertUnavailable(t *testing.T, got Scalar) {
	t.Helper()
	if got.Available || got.Reason == "" {
		t.Fatalf("scalar = %+v, want unavailable with reason", got)
	}
}

func assertFiniteMetrics(t *testing.T, got RequestMetrics) {
	t.Helper()
	values := []float64{
		got.TimeToHeaders.Value, got.TTFB.Value, got.TTFT.Value, got.TTLT.Value, got.E2E.Value,
		got.TPOT.Value, got.OutputTokensPerSecond.Value, got.DecodeTokensPerSecond.Value,
		got.InterChunkLatency.MeanMS, got.InterChunkLatency.MinMS, got.InterChunkLatency.MaxMS,
	}
	values = append(values, got.InterChunkLatency.ValuesMS...)
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("non-finite metric value: %v", value)
		}
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
