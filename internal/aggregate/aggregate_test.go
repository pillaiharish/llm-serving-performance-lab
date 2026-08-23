package aggregate

import (
	"encoding/csv"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

func TestSummarizeDistributionUsesNearestRankWithoutMutatingInput(t *testing.T) {
	tests := []struct {
		name      string
		values    []float64
		want      []float64
		available bool
	}{
		{name: "empty", values: nil},
		{name: "single", values: []float64{7}, want: []float64{7, 7, 7, 7}, available: true},
		{name: "two", values: []float64{20, 10}, want: []float64{10, 20, 20, 20}, available: true},
		{name: "four unsorted", values: []float64{40, 10, 30, 20}, want: []float64{20, 40, 40, 40}, available: true},
		{name: "odd duplicates and zero", values: []float64{5, 0, 5, 1, 5}, want: []float64{5, 5, 5, 5}, available: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := append([]float64(nil), test.values...)
			got, err := SummarizeDistribution(test.values, 3, "ms", "no samples")
			if err != nil {
				t.Fatalf("SummarizeDistribution: %v", err)
			}
			if !reflect.DeepEqual(test.values, before) {
				t.Fatalf("input mutated: got %v want %v", test.values, before)
			}
			if got.Available != test.available || got.UnavailableCount != 3 {
				t.Fatalf("distribution = %+v", got)
			}
			if !test.available {
				if got.SampleCount != 0 || got.Reason == "" {
					t.Fatalf("empty distribution = %+v", got)
				}
				return
			}
			values := []float64{got.P50, got.P90, got.P95, got.P99}
			if !reflect.DeepEqual(values, test.want) {
				t.Fatalf("percentiles = %v, want %v", values, test.want)
			}
		})
	}
}

func TestSummarizeDistributionRejectsNonFiniteSamples(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := SummarizeDistribution([]float64{value}, 0, "ms", "empty"); err == nil {
			t.Fatalf("accepted non-finite sample %v", value)
		}
	}
}

func TestCalculateExcludesWarmupAndFailedRequestsWithMetricSpecificAvailability(t *testing.T) {
	input := closedInput(4, 10*time.Second)
	input.Requests = []Request{
		warmupRequest(1, 10000),
		successRequest(1, 100, 10),
		successRequest(2, 200, 20),
		successRequest(3, 300, 30),
		failedRequest(4, benchmark.OutcomeRequestError, 5000),
	}
	input.Requests[2].Metrics.TPOT = unavailableMetric("ms/token")
	input.Requests[3].Metrics.ITL = unavailableITL()
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 3, RequestError: 1}
	input.Measurement.Attempted = 4
	input.Measurement.Completed = 4

	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.Counts.Started != 4 || got.Counts.Successful != 3 || got.Counts.Failed != 1 || got.Counts.RequestError != 1 {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if got.Latency.TTFT.SampleCount != 3 || got.Latency.TTFT.Min != 100 || got.Latency.TTFT.Max != 300 {
		t.Fatalf("TTFT = %+v", got.Latency.TTFT)
	}
	if got.Latency.TPOT.SampleCount != 2 || got.Latency.TPOT.UnavailableCount != 1 {
		t.Fatalf("TPOT = %+v", got.Latency.TPOT)
	}
	if got.ITL.AvailableRequests != 2 || got.ITL.UnavailableRequests != 1 {
		t.Fatalf("ITL = %+v", got.ITL)
	}
	if got.RequestRates.CompletedRequestThroughput.Value != 0.4 || got.RequestRates.SuccessfulRequestThroughput.Value != 0.3 {
		t.Fatalf("throughputs = %+v", got.RequestRates)
	}
}

func TestCalculateKeepsRequestAndIntervalWeightingDistinct(t *testing.T) {
	input := closedInput(2, time.Second)
	first := successRequest(1, 100, 10)
	second := successRequest(2, 100, 10)
	first.Metrics.ITL = availableITL([]float64{10, 10})
	second.Metrics.ITL = availableITL([]float64{100, 100, 100, 100})
	first.Metrics.InterChunkLatency = availableICL([]float64{10, 10})
	second.Metrics.InterChunkLatency = availableICL([]float64{100, 100, 100, 100})
	input.Requests = []Request{first, second}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 2}

	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	for name, summary := range map[string]IntervalSummary{
		"ICL": got.ICL,
		"ITL": {EligibleSuccessfulRequests: got.ITL.EligibleSuccessfulRequests, ContributingRequests: got.ITL.AvailableRequests, UnavailableRequests: got.ITL.UnavailableRequests, RequestMeans: got.ITL.RequestMeans, Intervals: got.ITL.Intervals},
	} {
		if summary.ContributingRequests != 2 || summary.Intervals.SampleCount != 6 || summary.RequestMeans.P50 != 10 || summary.Intervals.P50 != 100 {
			t.Fatalf("%s weighting = %+v", name, summary)
		}
	}
}

func TestCalculateOpenLoopUsesAdmissionAndCompletionDenominators(t *testing.T) {
	input := openInput(100, 10*time.Second, 1000, 800, 12*time.Second)
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 750, RequestError: 50}
	input.Requests = make([]Request, 0, 800)
	input.Arrivals = make([]benchmark.ArrivalRecord, 0, 1000)
	for sequence := 1; sequence <= 1000; sequence++ {
		if sequence <= 800 {
			outcome := benchmark.OutcomeSucceeded
			if sequence > 750 {
				outcome = benchmark.OutcomeRequestError
			}
			request := successRequest(sequence, 100, 10)
			request.Outcome = outcome
			input.Requests = append(input.Requests, request)
			input.Arrivals = append(input.Arrivals, startedArrival(sequence, request.Metrics.RequestID, time.Duration(sequence)*time.Microsecond))
		} else {
			input.Arrivals = append(input.Arrivals, droppedArrival(sequence, benchmark.ArrivalClientLimited))
		}
	}
	counts := input.Measurement.ArrivalCounts
	counts.ClientLimited = 200
	counts.Processed = 1000
	input.Measurement.Attempted = 800
	input.Measurement.Completed = 800

	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.Load.OpenLoop.PlannedArrivalRate.Value != 100 || got.Load.OpenLoop.ActualStartRate.Value != 80 || got.RequestRates.SuccessfulRequestThroughput.Value != 62.5 {
		t.Fatalf("rate denominators = open=%+v completion=%+v", got.Load.OpenLoop, got.RequestRates)
	}
	if got.Load.OpenLoop.DeliveryRatio.Value != .8 || got.Load.OpenLoop.ClientLimitedRatio.Value != .2 {
		t.Fatalf("delivery = %+v", got.Load.OpenLoop)
	}
}

func TestCalculateClientLimitedRunKeepsLatencyAndFailureStatus(t *testing.T) {
	input := openInput(100, time.Second, 100, 20, time.Second)
	input.RunStatus = "failed"
	input.ErrorClass = "load_delivery_error"
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 20}
	for sequence := 1; sequence <= 100; sequence++ {
		if sequence <= 20 {
			request := successRequest(sequence, 100, 10)
			input.Requests = append(input.Requests, request)
			input.Arrivals = append(input.Arrivals, startedArrival(sequence, request.Metrics.RequestID, time.Millisecond))
		} else {
			input.Arrivals = append(input.Arrivals, droppedArrival(sequence, benchmark.ArrivalClientLimited))
		}
	}
	input.Measurement.ArrivalCounts.ClientLimited = 80
	input.Measurement.ArrivalCounts.Processed = 100
	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.RunStatus != "failed" || got.ErrorClass != "load_delivery_error" || got.Load.OpenLoop.DeliveryRatio.Value != .2 || got.Load.OpenLoop.ClientLimitedRatio.Value != .8 || got.Latency.TTFT.SampleCount != 20 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestCalculateRequiresCompleteUsageCoverageForTokenThroughput(t *testing.T) {
	input := closedInput(2, 2*time.Second)
	input.Requests = []Request{successRequest(1, 100, 10), successRequest(2, 100, 10)}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 2}
	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate complete: %v", err)
	}
	if !got.Tokens.Available || got.Tokens.UsageCoveredRequests != 2 || got.Tokens.SuccessfulOutputTokens != 4 || got.Tokens.Throughput.Output.Value != 2 {
		t.Fatalf("complete tokens = %+v", got.Tokens)
	}

	input.Requests[1].Metrics.TokenUsage = benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable}
	got, err = Calculate(input)
	if err != nil {
		t.Fatalf("Calculate partial: %v", err)
	}
	if got.Tokens.Available || got.Tokens.UsageCoveredRequests != 1 || !strings.Contains(got.Tokens.Reason, "unavailable for 1") || got.Tokens.Throughput.Output.Available {
		t.Fatalf("partial tokens = %+v", got.Tokens)
	}
}

func TestCalculateSLOGoodputAndUnevaluableRequests(t *testing.T) {
	input := closedInput(100, 10*time.Second)
	input.SLO = SLOConfig{TTFTMS: floatPointer(800), TPOTMS: floatPointer(30)}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 100}
	for sequence := 1; sequence <= 100; sequence++ {
		request := successRequest(sequence, 500, 20)
		if sequence == 1 {
			request.Metrics.TTFT.Value = 800
			request.Metrics.TPOT.Value = 30
		}
		if sequence > 75 && sequence <= 90 {
			request.Metrics.TTFT.Value = 900
		}
		if sequence > 90 {
			request.Metrics.TPOT = unavailableMetric("ms/token")
		}
		input.Requests = append(input.Requests, request)
	}
	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.SLO.EvaluableRequests != 90 || got.SLO.GoodRequests != 75 || got.SLO.BadRequests != 15 || got.SLO.UnevaluableRequests != 10 || got.SLO.Goodput.Value != 7.5 || got.SLO.PassRatio.Value != float64(75)/90 {
		t.Fatalf("SLO = %+v", got.SLO)
	}
	if got.SLO.TPOT.AvailableSuccessfulRequests != 90 || got.SLO.TPOT.UnavailableRequests != 10 {
		t.Fatalf("TPOT SLO = %+v", got.SLO.TPOT)
	}
}

func TestCalculateZeroCompletionWindowKeepsRatesFiniteAndUnavailable(t *testing.T) {
	input := closedInput(1, 0)
	input.Requests = []Request{successRequest(1, 100, 10)}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 1}
	input.SLO = SLOConfig{TTFTMS: floatPointer(100)}

	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	for name, rate := range map[string]Rate{
		"completed throughput":  got.RequestRates.CompletedRequestThroughput,
		"successful throughput": got.RequestRates.SuccessfulRequestThroughput,
		"token throughput":      got.Tokens.Throughput.Output,
		"goodput":               got.SLO.Goodput,
	} {
		if rate.Available || math.IsNaN(rate.Value) || math.IsInf(rate.Value, 0) || rate.Reason == "" {
			t.Fatalf("%s = %+v", name, rate)
		}
	}
}

func TestCalculateGoodputExcludesFailedRequestsAndNoSLOIsUnavailable(t *testing.T) {
	input := closedInput(100, 10*time.Second)
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 90, RequestError: 10}
	for sequence := 1; sequence <= 100; sequence++ {
		request := successRequest(sequence, 500, 20)
		if sequence > 75 && sequence <= 90 {
			request.Metrics.TTFT.Value = 900
		}
		if sequence > 90 {
			request = failedRequest(sequence, benchmark.OutcomeRequestError, 1)
		}
		input.Requests = append(input.Requests, request)
	}
	without, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate without SLO: %v", err)
	}
	if without.SLO.Goodput.Available || without.SLO.Goodput.Reason != GoodputNoSLOReason {
		t.Fatalf("no-SLO goodput = %+v", without.SLO.Goodput)
	}
	input.SLO = SLOConfig{TTFTMS: floatPointer(800)}
	with, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate with SLO: %v", err)
	}
	if with.SLO.GoodRequests != 75 || with.SLO.Goodput.Value != 7.5 {
		t.Fatalf("goodput = %+v", with.SLO)
	}
}

func TestCalculateSchedulerLagIncludesStartedFailuresOnly(t *testing.T) {
	input := openInput(5, time.Second, 5, 4, time.Second)
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 3, RequestError: 1}
	lags := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 100 * time.Millisecond}
	for index, lag := range lags {
		request := successRequest(index+1, 100, 10)
		if index == 3 {
			request.Outcome = benchmark.OutcomeRequestError
		}
		input.Requests = append(input.Requests, request)
		input.Arrivals = append(input.Arrivals, startedArrival(index+1, request.Metrics.RequestID, lag))
	}
	input.Arrivals = append(input.Arrivals, droppedArrival(5, benchmark.ArrivalClientLimited))
	input.Measurement.ArrivalCounts.ClientLimited = 1
	input.Measurement.ArrivalCounts.Processed = 5
	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.SchedulerLag.SampleCount != 4 || got.SchedulerLag.P50 != 2 || got.SchedulerLag.P90 != 100 || got.SchedulerLag.P95 != 100 || got.SchedulerLag.P99 != 100 {
		t.Fatalf("scheduler lag = %+v", got.SchedulerLag)
	}
}

func TestCalculatePartialRunPreservesRequestedAndActualCounts(t *testing.T) {
	input := closedInput(100, 4*time.Second)
	input.Complete = false
	input.RunStatus = "cancelled"
	input.Measurement.Attempted = 40
	input.Measurement.Completed = 40
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 30, ParentCancelled: 10}
	for sequence := 1; sequence <= 40; sequence++ {
		request := successRequest(sequence, 100, 10)
		if sequence > 30 {
			request.Outcome = benchmark.OutcomeParentCancelled
		}
		input.Requests = append(input.Requests, request)
	}
	got, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.Complete || got.Counts.RequestedOrPlanned != 100 || got.Counts.Started != 40 || got.Counts.ParentCancelled != 10 {
		t.Fatalf("partial summary = %+v", got)
	}
}

func TestCalculateRejectsStructuralInconsistencyAndITLSourceMixing(t *testing.T) {
	input := closedInput(2, time.Second)
	first := successRequest(1, 100, 10)
	second := successRequest(2, 100, 10)
	input.Requests = []Request{first, second}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 2}
	duplicate := input
	duplicate.Requests = []Request{first, first}
	if _, err := Calculate(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	input.Requests[1].Metrics.ITL.Source = "server_decode_time"
	if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("source error = %v", err)
	}
}

func TestCalculateRejectsOpenLoopArrivalDispositionCountSwaps(t *testing.T) {
	for _, test := range []struct {
		name            string
		metadataLimited benchmark.ArrivalDisposition
		rawLimited      benchmark.ArrivalDisposition
	}{
		{name: "metadata client-limited raw scheduler-limited", metadataLimited: benchmark.ArrivalClientLimited, rawLimited: benchmark.ArrivalSchedulerLimited},
		{name: "metadata scheduler-limited raw client-limited", metadataLimited: benchmark.ArrivalSchedulerLimited, rawLimited: benchmark.ArrivalClientLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := openInput(10, time.Second, 10, 2, time.Second)
			input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 2}
			for sequence := 1; sequence <= 2; sequence++ {
				request := successRequest(sequence, 100, 10)
				input.Requests = append(input.Requests, request)
				input.Arrivals = append(input.Arrivals, startedArrival(sequence, request.Metrics.RequestID, time.Millisecond))
			}
			for sequence := 3; sequence <= 10; sequence++ {
				input.Arrivals = append(input.Arrivals, droppedArrival(sequence, test.rawLimited))
			}
			input.Measurement.ArrivalCounts.Processed = 10
			if test.metadataLimited == benchmark.ArrivalClientLimited {
				input.Measurement.ArrivalCounts.ClientLimited = 8
			} else {
				input.Measurement.ArrivalCounts.SchedulerLimited = 8
			}
			if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "arrival disposition records disagree with counts") {
				t.Fatalf("Calculate error = %v", err)
			}
		})
	}
}

func TestMarshalCSVUsesEmptyUnavailableFieldsAndDeterministicColumns(t *testing.T) {
	input := closedInput(1, time.Second)
	request := successRequest(1, 100, 10)
	request.Metrics.TPOT = unavailableMetric("ms/token")
	input.Requests = []Request{request}
	input.Measurement.Outcomes = benchmark.OutcomeCounts{Succeeded: 1}
	summary, err := Calculate(input)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	encoded, err := MarshalCSV(summary)
	if err != nil {
		t.Fatalf("MarshalCSV: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(encoded))).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(rows) != 2 || !reflect.DeepEqual(rows[0], CSVColumns) || len(rows[1]) != len(CSVColumns) {
		t.Fatalf("CSV rows = %+v", rows)
	}
	columns := make(map[string]string, len(rows[0]))
	for index, name := range rows[0] {
		columns[name] = rows[1][index]
	}
	if columns["tpot_p95_ms_per_token"] != "" || columns["goodput_requests_per_second"] != "" || columns["ttft_p95_ms"] != "100" {
		t.Fatalf("CSV columns = %+v", columns)
	}
}

func closedInput(requested int, elapsed time.Duration) Input {
	return Input{
		RunID: "run-aggregate", RunStatus: "completed", Complete: true,
		Workload:    WorkloadInput{Mode: "prompt", RequestedOutputMaxTokens: 64},
		Load:        LoadInput{Mode: benchmark.LoadModeClosedLoop, ClosedLoop: &ClosedLoopInput{RequestedConcurrency: 4, RequestedRequests: requested}},
		Measurement: MeasurementInput{Requested: requested, Attempted: requested, Completed: requested, ElapsedNS: elapsed.Nanoseconds(), RequestedConcurrency: 4, WorkerCount: min(4, requested), MaxObservedActive: min(4, requested)},
	}
}

func openInput(rate float64, duration time.Duration, planned, started int, completion time.Duration) Input {
	counts := &benchmark.ArrivalCounts{Planned: planned, Processed: started, Started: started, MaxObservedInFlight: min(started, 16)}
	return Input{
		RunID: "run-aggregate", RunStatus: "completed", Complete: true,
		Workload:    WorkloadInput{Mode: "prompt", RequestedOutputMaxTokens: 64},
		Load:        LoadInput{Mode: benchmark.LoadModeOpenLoop, OpenLoop: &OpenLoopInput{ConfiguredRequestRate: rate, Duration: duration, MaxInFlight: 16, PlannedArrivals: planned}},
		Measurement: MeasurementInput{Requested: planned, Attempted: started, Completed: started, ElapsedNS: completion.Nanoseconds(), ArrivalCounts: counts},
	}
}

func successRequest(sequence int, ttft, tpot float64) Request {
	requestID, _ := benchmark.RequestID(sequence)
	requestMetrics := metrics.RequestMetrics{
		RunID: "run-aggregate", RequestID: requestID,
		TimeToHeaders: availableMetric(1, "ms"), TTFB: availableMetric(2, "ms"), TTFT: availableMetric(ttft, "ms"), TTLT: availableMetric(ttft+10, "ms"), E2E: availableMetric(ttft+20, "ms"), TPOT: availableMetric(tpot, "ms/token"),
		OutputTokensPerSecond: availableMetric(20, "tokens/s"), DecodeTokensPerSecond: availableMetric(25, "tokens/s"),
		InterChunkLatency: availableICL([]float64{10, 20}), ITL: availableITL([]float64{10, 20}),
		TokenUsage: benchmark.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5, Source: "server_usage", Available: true},
	}
	return Request{Sequence: sequence, Phase: benchmark.RequestPhaseMeasured, Outcome: benchmark.OutcomeSucceeded, Metrics: requestMetrics}
}

func failedRequest(sequence int, outcome benchmark.RequestOutcome, partialTTFT float64) Request {
	request := successRequest(sequence, partialTTFT, 10)
	request.Outcome = outcome
	return request
}

func warmupRequest(sequence int, ttft float64) Request {
	request := successRequest(sequence, ttft, 10)
	request.Phase = benchmark.RequestPhaseWarmup
	requestID, _ := benchmark.WarmupRequestID(sequence)
	request.Metrics.RequestID = requestID
	return request
}

func availableMetric(value float64, unit string) metrics.Scalar {
	return metrics.Scalar{Available: true, Value: value, Unit: unit}
}
func unavailableMetric(unit string) metrics.Scalar {
	return metrics.Scalar{Unit: unit, Reason: "unavailable fixture"}
}

func availableICL(values []float64) metrics.InterChunkLatency {
	return metrics.InterChunkLatency{Available: true, Count: len(values), MeanMS: mean(values), MinMS: minimum(values), MaxMS: maximum(values), ValuesMS: append([]float64(nil), values...)}
}

func availableITL(values []float64) metrics.InterTokenLatency {
	return metrics.InterTokenLatency{Available: true, Source: benchmark.TokenTimingSourceVLLM, Count: len(values), MeanMS: mean(values), MinMS: minimum(values), MaxMS: maximum(values), ValuesMS: append([]float64(nil), values...)}
}

func unavailableITL() metrics.InterTokenLatency {
	return metrics.InterTokenLatency{Source: benchmark.TokenTimingSourceUnavailable, ValuesMS: []float64{}, Reason: "unavailable fixture"}
}

func startedArrival(sequence int, requestID string, lag time.Duration) benchmark.ArrivalRecord {
	return benchmark.ArrivalRecord{Sequence: sequence, Phase: benchmark.RequestPhaseMeasured, Disposition: benchmark.ArrivalStarted, RequestID: &requestID, SchedulerLagNS: int64Pointer(lag.Nanoseconds())}
}

func droppedArrival(sequence int, disposition benchmark.ArrivalDisposition) benchmark.ArrivalRecord {
	return benchmark.ArrivalRecord{Sequence: sequence, Phase: benchmark.RequestPhaseMeasured, Disposition: disposition}
}

func mean(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}
func minimum(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
func maximum(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
func floatPointer(value float64) *float64 { return &value }
func int64Pointer(value int64) *int64     { return &value }
