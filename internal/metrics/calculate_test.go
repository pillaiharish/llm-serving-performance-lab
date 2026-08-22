package metrics

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

func TestCalculateDeterministicMetrics(t *testing.T) {
	start := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	observation := benchmark.RequestObservation{
		RunID:                   "20260816T120000Z-a31f00ff",
		RequestID:               "req-000001",
		RequestStartedAt:        timePointer(start),
		HeadersReceivedAt:       timePointer(start.Add(40 * time.Millisecond)),
		HeadersAfterNS:          int64Pointer((40 * time.Millisecond).Nanoseconds()),
		FirstByteAt:             timePointer(start.Add(45 * time.Millisecond)),
		FirstByteAfterNS:        int64Pointer((45 * time.Millisecond).Nanoseconds()),
		FirstStreamEventAt:      timePointer(start.Add(100 * time.Millisecond)),
		FirstStreamEventAfterNS: int64Pointer((100 * time.Millisecond).Nanoseconds()),
		FirstContentAt:          timePointer(start.Add(100 * time.Millisecond)),
		FirstContentAfterNS:     int64Pointer((100 * time.Millisecond).Nanoseconds()),
		LastContentAt:           timePointer(start.Add(190 * time.Millisecond)),
		LastContentAfterNS:      int64Pointer((190 * time.Millisecond).Nanoseconds()),
		CompletedAt:             timePointer(start.Add(200 * time.Millisecond)),
		CompletedAfterNS:        int64Pointer((200 * time.Millisecond).Nanoseconds()),
		StreamEvents: []benchmark.StreamEvent{
			{Sequence: 1, ReceivedAt: start.Add(100 * time.Millisecond), ReceivedAfterNS: (100 * time.Millisecond).Nanoseconds(), HasContent: true, ContentBytes: 1},
			{Sequence: 2, ReceivedAt: start.Add(120 * time.Millisecond), ReceivedAfterNS: (120 * time.Millisecond).Nanoseconds(), HasContent: true, ContentBytes: 2},
			{Sequence: 3, ReceivedAt: start.Add(150 * time.Millisecond), ReceivedAfterNS: (150 * time.Millisecond).Nanoseconds(), HasContent: true, ContentBytes: 3},
			{Sequence: 4, ReceivedAt: start.Add(190 * time.Millisecond), ReceivedAfterNS: (190 * time.Millisecond).Nanoseconds(), HasContent: true, ContentBytes: 4},
			{Sequence: 5, ReceivedAt: start.Add(195 * time.Millisecond), ReceivedAfterNS: (195 * time.Millisecond).Nanoseconds()},
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

	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	var persisted benchmark.RequestObservation
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("unmarshal observation: %v", err)
	}
	got := Calculate(persisted)
	if got.RunID != observation.RunID || got.RequestID != observation.RequestID {
		t.Fatalf("metric identity = (%q, %q)", got.RunID, got.RequestID)
	}
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
		RequestStartedAt:    timePointer(start),
		FirstContentAt:      timePointer(start.Add(10 * time.Millisecond)),
		FirstContentAfterNS: int64Pointer((10 * time.Millisecond).Nanoseconds()),
		LastContentAt:       timePointer(start.Add(20 * time.Millisecond)),
		LastContentAfterNS:  int64Pointer((20 * time.Millisecond).Nanoseconds()),
		CompletedAt:         timePointer(start.Add(50 * time.Millisecond)),
		CompletedAfterNS:    int64Pointer((50 * time.Millisecond).Nanoseconds()),
		StreamEvents: []benchmark.StreamEvent{
			{Sequence: 1, ReceivedAt: start.Add(10 * time.Millisecond), ReceivedAfterNS: (10 * time.Millisecond).Nanoseconds(), HasContent: true},
			{Sequence: 2, ReceivedAt: start.Add(20 * time.Millisecond), ReceivedAfterNS: (20 * time.Millisecond).Nanoseconds(), HasContent: true},
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
				value.FirstContentAfterNS = nil
				value.LastContentAt = nil
				value.LastContentAfterNS = nil
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
				value.LastContentAfterNS = value.FirstContentAfterNS
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
			name: "zero E2E duration",
			alter: func(value *benchmark.RequestObservation) {
				value.CompletedAt = value.RequestStartedAt
				value.CompletedAfterNS = int64Pointer(0)
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.OutputTokensPerSecond)
			},
		},
		{
			name: "zero decode duration",
			alter: func(value *benchmark.RequestObservation) {
				value.LastContentAt = value.FirstContentAt
				value.LastContentAfterNS = value.FirstContentAfterNS
				value.StreamEvents[1].ReceivedAt = value.StreamEvents[0].ReceivedAt
				value.StreamEvents[1].ReceivedAfterNS = value.StreamEvents[0].ReceivedAfterNS
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
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
			name: "relative offset preferred over wall time",
			alter: func(value *benchmark.RequestObservation) {
				value.HeadersReceivedAt = timePointer(start.Add(40 * time.Millisecond))
				value.HeadersAfterNS = int64Pointer((45 * time.Millisecond).Nanoseconds())
			},
			check: func(t *testing.T, got RequestMetrics) { assertScalar(t, got.TimeToHeaders, 45) },
		},
		{
			name: "negative relative offset",
			alter: func(value *benchmark.RequestObservation) {
				value.HeadersReceivedAt = timePointer(start.Add(40 * time.Millisecond))
				value.HeadersAfterNS = int64Pointer(-1)
			},
			check: func(t *testing.T, got RequestMetrics) { assertUnavailable(t, got.TimeToHeaders) },
		},
		{
			name:  "incomplete decode offsets",
			alter: func(value *benchmark.RequestObservation) { value.LastContentAfterNS = nil },
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
			},
		},
		{
			name: "decreasing decode offsets",
			alter: func(value *benchmark.RequestObservation) {
				value.FirstContentAfterNS = int64Pointer((20 * time.Millisecond).Nanoseconds())
				value.LastContentAfterNS = int64Pointer((10 * time.Millisecond).Nanoseconds())
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.TPOT)
				assertUnavailable(t, got.DecodeTokensPerSecond)
			},
		},
		{
			name: "decreasing stream offsets",
			alter: func(value *benchmark.RequestObservation) {
				value.StreamEvents[1].ReceivedAfterNS = (5 * time.Millisecond).Nanoseconds()
			},
			check: func(t *testing.T, got RequestMetrics) {
				if got.InterChunkLatency.Available || got.InterChunkLatency.Reason == "" {
					t.Fatalf("inter-chunk latency = %+v", got.InterChunkLatency)
				}
			},
		},
		{
			name: "wall-time fallback when decode offsets absent",
			alter: func(value *benchmark.RequestObservation) {
				value.FirstContentAfterNS = nil
				value.LastContentAfterNS = nil
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertScalar(t, got.TPOT, 10)
				assertScalar(t, got.DecodeTokensPerSecond, 100)
			},
		},
		{
			name: "wall-time fallback when simple offsets absent",
			alter: func(value *benchmark.RequestObservation) {
				value.HeadersReceivedAt = timePointer(start.Add(25 * time.Millisecond))
				value.HeadersAfterNS = nil
				value.CompletedAfterNS = nil
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertScalar(t, got.TimeToHeaders, 25)
				assertScalar(t, got.E2E, 50)
				assertScalar(t, got.OutputTokensPerSecond, 40)
			},
		},
		{
			name: "invalid completion offset does not fall back",
			alter: func(value *benchmark.RequestObservation) {
				value.CompletedAfterNS = int64Pointer(-1)
			},
			check: func(t *testing.T, got RequestMetrics) {
				assertUnavailable(t, got.E2E)
				assertUnavailable(t, got.OutputTokensPerSecond)
			},
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

func TestCalculateEvidenceBackedTrueITLAfterJSONRoundTrip(t *testing.T) {
	observation := trueITLObservation([]time.Duration{100 * time.Millisecond, 120 * time.Millisecond, 150 * time.Millisecond, 190 * time.Millisecond}, []int{1, 1, 1, 1}, 4)
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var persisted benchmark.RequestObservation
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := Calculate(persisted)
	if !got.ITL.Available || got.ITL.Source != benchmark.TokenTimingSourceVLLM || got.ITL.Count != 3 || got.ITL.MeanMS != 30 || got.ITL.MinMS != 20 || got.ITL.MaxMS != 40 || !reflect.DeepEqual(got.ITL.ValuesMS, []float64{20, 30, 40}) {
		t.Fatalf("ITL = %+v", got.ITL)
	}
	if !got.InterChunkLatency.Available || !reflect.DeepEqual(got.InterChunkLatency.ValuesMS, []float64{20, 30, 40}) {
		t.Fatalf("ICL changed: %+v", got.InterChunkLatency)
	}
}

func TestCalculateRejectsIncompleteTokenEvidenceWithoutChangingICL(t *testing.T) {
	tests := []struct {
		name   string
		alter  func(*benchmark.RequestObservation)
		reason string
	}{
		{name: "multi-token event", alter: func(value *benchmark.RequestObservation) {
			value.StreamEvents[1].GeneratedTokenCount = 2
			value.Usage.OutputTokens = 5
		}, reason: "multiple generated token IDs"},
		{name: "coverage mismatch", alter: func(value *benchmark.RequestObservation) { value.Usage.OutputTokens = 5 }, reason: "does not match"},
		{name: "usage missing", alter: func(value *benchmark.RequestObservation) {
			value.Usage = benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable}
		}, reason: "usage not available"},
		{name: "single output token", alter: func(value *benchmark.RequestObservation) {
			value.StreamEvents = value.StreamEvents[:1]
			value.Usage.OutputTokens = 1
		}, reason: "at least two output tokens"},
		{name: "negative offset", alter: func(value *benchmark.RequestObservation) { value.StreamEvents[0].ReceivedAfterNS = -1 }, reason: "must not be negative"},
		{name: "decreasing offsets", alter: func(value *benchmark.RequestObservation) {
			value.StreamEvents[1].ReceivedAfterNS = value.StreamEvents[0].ReceivedAfterNS - 1
		}, reason: "not ordered"},
		{name: "not completed", alter: func(value *benchmark.RequestObservation) { value.TokenTiming.CompletedThroughDone = false }, reason: "through [DONE]"},
		{name: "invalid generated ID", alter: func(value *benchmark.RequestObservation) {
			value.TokenTiming.InvalidReason = "negative generated token ID"
		}, reason: "negative generated token ID"},
		{name: "no token IDs", alter: func(value *benchmark.RequestObservation) {
			for index := range value.StreamEvents {
				value.StreamEvents[index].TokenIDsPresent = false
				value.StreamEvents[index].GeneratedTokenCount = 0
			}
		}, reason: "was not observed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := trueITLObservation([]time.Duration{100 * time.Millisecond, 120 * time.Millisecond, 150 * time.Millisecond, 190 * time.Millisecond}, []int{1, 1, 1, 1}, 4)
			test.alter(&observation)
			got := Calculate(observation)
			if got.ITL.Available || !strings.Contains(got.ITL.Reason, test.reason) || got.ITL.Count != 0 || len(got.ITL.ValuesMS) != 0 {
				t.Fatalf("ITL = %+v, want reason containing %q", got.ITL, test.reason)
			}
			if test.name != "single output token" && test.name != "negative offset" && test.name != "decreasing offsets" && !got.InterChunkLatency.Available {
				t.Fatalf("ICL became unavailable: %+v", got.InterChunkLatency)
			}
			assertFiniteMetrics(t, got)
		})
	}
}

func TestCalculateAllowsZeroITLGap(t *testing.T) {
	got := Calculate(trueITLObservation([]time.Duration{100 * time.Millisecond, 100 * time.Millisecond}, []int{1, 1}, 2))
	if !got.ITL.Available || !reflect.DeepEqual(got.ITL.ValuesMS, []float64{0}) {
		t.Fatalf("ITL = %+v", got.ITL)
	}
}

func trueITLObservation(offsets []time.Duration, counts []int, outputTokens int) benchmark.RequestObservation {
	start := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	events := make([]benchmark.StreamEvent, len(offsets))
	for index, offset := range offsets {
		events[index] = benchmark.StreamEvent{
			Sequence: index + 1, ReceivedAt: start.Add(offset), ReceivedAfterNS: offset.Nanoseconds(),
			HasContent: true, ContentBytes: 1, TokenIDsPresent: true, GeneratedTokenCount: counts[index],
		}
	}
	return benchmark.RequestObservation{
		RunID: "run", RequestID: "req-000001", RequestStartedAt: &start,
		StreamEvents: events,
		TokenTiming:  benchmark.TokenTimingEvidence{Requested: true, Source: benchmark.TokenTimingSourceVLLM, CompletedThroughDone: true},
		Usage:        benchmark.TokenUsage{OutputTokens: outputTokens, TotalTokens: outputTokens, Source: "server_usage", Available: true},
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
		got.ITL.MeanMS, got.ITL.MinMS, got.ITL.MaxMS,
	}
	values = append(values, got.InterChunkLatency.ValuesMS...)
	values = append(values, got.ITL.ValuesMS...)
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("non-finite metric value: %v", value)
		}
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
