package aggregate

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

func Calculate(input Input) (RunSummary, error) {
	measured, outcomeCounts, err := validateAndSelectMeasured(input)
	if err != nil {
		return RunSummary{}, err
	}
	successful := make([]Request, 0, outcomeCounts.Succeeded)
	for _, request := range measured {
		if request.Outcome == benchmark.OutcomeSucceeded {
			successful = append(successful, request)
		}
	}

	completionWindow := unavailableNumeric("s", "measurement completion window must be positive")
	completionSeconds := float64(input.Measurement.ElapsedNS) / float64(time.Second)
	if input.Measurement.ElapsedNS > 0 {
		completionWindow = availableNumeric(completionSeconds, "s")
	}
	summary := RunSummary{
		SchemaVersion: SchemaVersion,
		RunID:         input.RunID,
		RunStatus:     input.RunStatus,
		ErrorClass:    input.ErrorClass,
		Complete:      input.Complete,
		Workload: WorkloadSummary{
			Mode:                     input.Workload.Mode,
			InputTargetTokens:        copyIntPointer(input.Workload.InputTargetTokens),
			InputResolvedTokens:      copyIntPointer(input.Workload.InputResolvedTokens),
			RequestedOutputMaxTokens: input.Workload.RequestedOutputMaxTokens,
		},
		Counts: RequestCounts{
			RequestedOrPlanned: input.Measurement.Requested,
			Started:            len(measured),
			Completed:          len(measured),
			Successful:         outcomeCounts.Succeeded,
			Failed:             outcomeCounts.Failed(),
			RequestError:       outcomeCounts.RequestError,
			RequestTimeout:     outcomeCounts.RequestTimeout,
			ParentCancelled:    outcomeCounts.ParentCancelled,
			DrainTimeout:       outcomeCounts.DrainTimeout,
		},
		Durations: DurationSummary{
			CompletionWindow: completionWindow,
			AdmissionWindow:  unavailableNumeric("s", "not applicable to closed-loop load"),
		},
		RequestRates: RequestRateSummary{
			SuccessRate:                 calculateRatio(outcomeCounts.Succeeded, len(measured), "no measured requests started"),
			FailureRate:                 calculateRatio(outcomeCounts.Failed(), len(measured), "no measured requests started"),
			CompletedRequestThroughput:  calculateRate(int64(len(measured)), completionSeconds, "requests/s", "measurement completion window must be positive"),
			SuccessfulRequestThroughput: calculateRate(int64(outcomeCounts.Succeeded), completionSeconds, "requests/s", "measurement completion window must be positive"),
		},
	}

	if err := populateLoadSummary(&summary, input, measured); err != nil {
		return RunSummary{}, err
	}
	if err := populateMetricDistributions(&summary, successful); err != nil {
		return RunSummary{}, err
	}
	if summary.ICL, err = calculateIntervalSummary(successful, false); err != nil {
		return RunSummary{}, err
	}
	if summary.ITL, err = calculateITLSummary(successful); err != nil {
		return RunSummary{}, err
	}
	if summary.SchedulerLag, err = calculateSchedulerLag(input); err != nil {
		return RunSummary{}, err
	}
	summary.Tokens = calculateTokens(successful, completionSeconds)
	if summary.SLO, err = calculateSLO(successful, input.SLO, completionSeconds); err != nil {
		return RunSummary{}, err
	}
	return summary, nil
}

func validateAndSelectMeasured(input Input) ([]Request, benchmark.OutcomeCounts, error) {
	if input.RunID == "" {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("run ID is required")
	}
	if input.RunStatus == "" {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("run status is required")
	}
	if input.Measurement.Requested < 0 || input.Measurement.Attempted < 0 || input.Measurement.Completed < 0 || input.Measurement.ElapsedNS < 0 {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measurement counts and elapsed time must not be negative")
	}
	if input.Measurement.Attempted != input.Measurement.Completed {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measurement attempted and completed counts differ")
	}
	if input.Workload.Mode == "" || input.Workload.RequestedOutputMaxTokens <= 0 {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("workload summary input is invalid")
	}
	if err := validateSLOConfig(input.SLO); err != nil {
		return nil, benchmark.OutcomeCounts{}, err
	}

	measured := make([]Request, 0, input.Measurement.Completed)
	seenSequences := make(map[int]struct{}, input.Measurement.Completed)
	seenIDs := make(map[string]struct{}, input.Measurement.Completed)
	var outcomes benchmark.OutcomeCounts
	for _, request := range input.Requests {
		switch request.Phase {
		case benchmark.RequestPhaseWarmup:
			continue
		case benchmark.RequestPhaseMeasured:
		default:
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("request has invalid phase %q", request.Phase)
		}
		if request.Sequence <= 0 || request.Sequence > input.Measurement.Requested {
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measured request sequence %d is outside the requested range", request.Sequence)
		}
		if _, exists := seenSequences[request.Sequence]; exists {
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("duplicate measured request sequence %d", request.Sequence)
		}
		seenSequences[request.Sequence] = struct{}{}
		requestID, err := benchmark.RequestID(request.Sequence)
		if err != nil || request.Metrics.RunID != input.RunID || request.Metrics.RequestID != requestID {
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measured request identity does not match run and sequence")
		}
		if _, exists := seenIDs[request.Metrics.RequestID]; exists {
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("duplicate measured request ID %s", request.Metrics.RequestID)
		}
		seenIDs[request.Metrics.RequestID] = struct{}{}
		if !validOutcome(request.Outcome) {
			return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measured request %s has invalid outcome %q", request.Metrics.RequestID, request.Outcome)
		}
		incrementOutcome(&outcomes, request.Outcome)
		measured = append(measured, request)
	}
	if len(measured) != input.Measurement.Completed || outcomes != input.Measurement.Outcomes {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measured request artifacts disagree with lifecycle outcome totals")
	}
	if outcomes.Total() != len(measured) {
		return nil, benchmark.OutcomeCounts{}, fmt.Errorf("measured request outcomes do not classify every started request")
	}
	return measured, outcomes, nil
}

func populateLoadSummary(summary *RunSummary, input Input, measured []Request) error {
	measuredArrivals := make([]benchmark.ArrivalRecord, 0)
	for _, arrival := range input.Arrivals {
		switch arrival.Phase {
		case benchmark.RequestPhaseWarmup:
			continue
		case benchmark.RequestPhaseMeasured:
			measuredArrivals = append(measuredArrivals, arrival)
		default:
			return fmt.Errorf("arrival has invalid phase %q", arrival.Phase)
		}
	}

	summary.Load.Mode = input.Load.Mode
	switch input.Load.Mode {
	case benchmark.LoadModeClosedLoop:
		if input.Load.ClosedLoop == nil || input.Load.OpenLoop != nil || input.Measurement.ArrivalCounts != nil || len(measuredArrivals) != 0 {
			return fmt.Errorf("closed-loop aggregate input contains invalid load or arrival evidence")
		}
		closed := input.Load.ClosedLoop
		if closed.RequestedConcurrency <= 0 || closed.RequestedRequests != input.Measurement.Requested || input.Measurement.RequestedConcurrency != closed.RequestedConcurrency || input.Measurement.WorkerCount < 0 || input.Measurement.MaxObservedActive < 0 || input.Measurement.MaxObservedActive > input.Measurement.WorkerCount {
			return fmt.Errorf("closed-loop aggregate input is inconsistent")
		}
		summary.Load.ClosedLoop = &ClosedLoopSummary{
			RequestedConcurrency: closed.RequestedConcurrency,
			WorkerCount:          input.Measurement.WorkerCount,
			MaxObservedActive:    input.Measurement.MaxObservedActive,
		}
	case benchmark.LoadModeOpenLoop:
		if input.Load.OpenLoop == nil || input.Load.ClosedLoop != nil || input.Measurement.ArrivalCounts == nil {
			return fmt.Errorf("open-loop aggregate input is incomplete")
		}
		open := input.Load.OpenLoop
		counts := *input.Measurement.ArrivalCounts
		if math.IsNaN(open.ConfiguredRequestRate) || math.IsInf(open.ConfiguredRequestRate, 0) || open.ConfiguredRequestRate <= 0 || open.Duration <= 0 || open.MaxInFlight <= 0 || open.PlannedArrivals != input.Measurement.Requested || counts.Planned != open.PlannedArrivals {
			return fmt.Errorf("open-loop configuration is invalid")
		}
		planned, err := benchmark.PlannedArrivalCount(open.ConfiguredRequestRate, open.Duration)
		if err != nil || planned != open.PlannedArrivals {
			return fmt.Errorf("open-loop planned arrivals disagree with configured rate and duration")
		}
		if counts.Processed != len(measuredArrivals) || counts.Processed != counts.Started+counts.ClientLimited+counts.SchedulerLimited || counts.Planned != counts.Processed+counts.UnprocessedDueToCancellation || counts.Started != len(measured) || counts.MaxObservedInFlight < 0 || counts.MaxObservedInFlight > open.MaxInFlight || counts.MaxObservedInFlight > counts.Started {
			return fmt.Errorf("open-loop arrival counts are inconsistent")
		}
		requestsBySequence := make(map[int]Request, len(measured))
		for _, request := range measured {
			requestsBySequence[request.Sequence] = request
		}
		seen := make(map[int]struct{}, len(measuredArrivals))
		started := 0
		clientLimited := 0
		schedulerLimited := 0
		for _, arrival := range measuredArrivals {
			if arrival.Sequence <= 0 || arrival.Sequence > counts.Planned {
				return fmt.Errorf("open-loop arrival sequence is outside the planned range")
			}
			if _, exists := seen[arrival.Sequence]; exists {
				return fmt.Errorf("duplicate measured arrival sequence %d", arrival.Sequence)
			}
			seen[arrival.Sequence] = struct{}{}
			request, requestExists := requestsBySequence[arrival.Sequence]
			switch arrival.Disposition {
			case benchmark.ArrivalStarted:
				started++
				if !requestExists || arrival.RequestID == nil || *arrival.RequestID != request.Metrics.RequestID || arrival.SchedulerLagNS == nil {
					return fmt.Errorf("started arrival %d lacks matching request or scheduler-lag evidence", arrival.Sequence)
				}
				if *arrival.SchedulerLagNS < 0 {
					return fmt.Errorf("started arrival %d has negative scheduler lag", arrival.Sequence)
				}
			case benchmark.ArrivalClientLimited:
				clientLimited++
				if requestExists || arrival.RequestID != nil || arrival.SchedulerLagNS != nil {
					return fmt.Errorf("unstarted arrival %d contains request evidence", arrival.Sequence)
				}
			case benchmark.ArrivalSchedulerLimited:
				schedulerLimited++
				if requestExists || arrival.RequestID != nil || arrival.SchedulerLagNS != nil {
					return fmt.Errorf("unstarted arrival %d contains request evidence", arrival.Sequence)
				}
			default:
				return fmt.Errorf("arrival %d has invalid disposition %q", arrival.Sequence, arrival.Disposition)
			}
		}
		if started != counts.Started || clientLimited != counts.ClientLimited || schedulerLimited != counts.SchedulerLimited {
			return fmt.Errorf("open-loop arrival disposition records disagree with counts")
		}
		admissionSeconds := open.Duration.Seconds()
		summary.Durations.AdmissionWindow = availableNumeric(admissionSeconds, "s")
		summary.Load.OpenLoop = &OpenLoopSummary{
			ConfiguredRequestRate:        open.ConfiguredRequestRate,
			AdmissionWindowSeconds:       admissionSeconds,
			MaxInFlight:                  open.MaxInFlight,
			PlannedArrivals:              counts.Planned,
			ProcessedArrivals:            counts.Processed,
			StartedArrivals:              counts.Started,
			ClientLimited:                counts.ClientLimited,
			SchedulerLimited:             counts.SchedulerLimited,
			UnprocessedDueToCancellation: counts.UnprocessedDueToCancellation,
			MaxObservedInFlight:          counts.MaxObservedInFlight,
			PlannedArrivalRate:           calculateRate(int64(counts.Planned), admissionSeconds, "requests/s", "open-loop admission window must be positive"),
			ActualStartRate:              calculateRate(int64(counts.Started), admissionSeconds, "requests/s", "open-loop admission window must be positive"),
			DeliveryRatio:                calculateRatio(counts.Started, counts.Planned, "no open-loop arrivals were planned"),
			ClientLimitedRatio:           calculateRatio(counts.ClientLimited, counts.Planned, "no open-loop arrivals were planned"),
			SchedulerLimitedRatio:        calculateRatio(counts.SchedulerLimited, counts.Planned, "no open-loop arrivals were planned"),
			CancellationUnprocessedRatio: calculateRatio(counts.UnprocessedDueToCancellation, counts.Planned, "no open-loop arrivals were planned"),
		}
	default:
		return fmt.Errorf("unsupported load mode %q", input.Load.Mode)
	}
	return nil
}

func populateMetricDistributions(summary *RunSummary, successful []Request) error {
	var err error
	summary.Latency.TimeToHeaders, err = scalarDistribution(successful, "time to headers", "ms", func(value metrics.RequestMetrics) metrics.Scalar { return value.TimeToHeaders })
	if err != nil {
		return err
	}
	summary.Latency.TTFB, err = scalarDistribution(successful, "TTFB", "ms", func(value metrics.RequestMetrics) metrics.Scalar { return value.TTFB })
	if err != nil {
		return err
	}
	summary.Latency.TTFT, err = scalarDistribution(successful, "TTFT", "ms", func(value metrics.RequestMetrics) metrics.Scalar { return value.TTFT })
	if err != nil {
		return err
	}
	summary.Latency.TTLT, err = scalarDistribution(successful, "TTLT", "ms", func(value metrics.RequestMetrics) metrics.Scalar { return value.TTLT })
	if err != nil {
		return err
	}
	summary.Latency.E2E, err = scalarDistribution(successful, "E2E", "ms", func(value metrics.RequestMetrics) metrics.Scalar { return value.E2E })
	if err != nil {
		return err
	}
	summary.Latency.TPOT, err = scalarDistribution(successful, "TPOT", "ms/token", func(value metrics.RequestMetrics) metrics.Scalar { return value.TPOT })
	if err != nil {
		return err
	}
	summary.PerRequestRates.OutputTokensPerSecond, err = scalarDistribution(successful, "output tokens per second", "tokens/s", func(value metrics.RequestMetrics) metrics.Scalar { return value.OutputTokensPerSecond })
	if err != nil {
		return err
	}
	summary.PerRequestRates.DecodeTokensPerSecond, err = scalarDistribution(successful, "decode tokens per second", "tokens/s", func(value metrics.RequestMetrics) metrics.Scalar { return value.DecodeTokensPerSecond })
	return err
}

func scalarDistribution(successful []Request, name, unit string, extract func(metrics.RequestMetrics) metrics.Scalar) (Distribution, error) {
	samples := make([]float64, 0, len(successful))
	for _, request := range successful {
		value := extract(request.Metrics)
		if !value.Available {
			continue
		}
		if value.Unit != unit || !finiteNonNegative(value.Value) {
			return Distribution{}, fmt.Errorf("request %s has invalid available %s metric", request.Metrics.RequestID, name)
		}
		samples = append(samples, value.Value)
	}
	reason := fmt.Sprintf("no successful measured requests had available %s", name)
	return SummarizeDistribution(samples, len(successful)-len(samples), unit, reason)
}

func calculateIntervalSummary(successful []Request, token bool) (IntervalSummary, error) {
	requestMeans := make([]float64, 0, len(successful))
	intervals := make([]float64, 0)
	for _, request := range successful {
		available := request.Metrics.InterChunkLatency.Available
		count := request.Metrics.InterChunkLatency.Count
		mean := request.Metrics.InterChunkLatency.MeanMS
		values := request.Metrics.InterChunkLatency.ValuesMS
		name := "ICL"
		if token {
			available = request.Metrics.ITL.Available
			count = request.Metrics.ITL.Count
			mean = request.Metrics.ITL.MeanMS
			values = request.Metrics.ITL.ValuesMS
			name = "ITL"
		}
		if !available {
			continue
		}
		if count <= 0 || len(values) != count || !finiteNonNegative(mean) {
			return IntervalSummary{}, fmt.Errorf("request %s has invalid available %s structure", request.Metrics.RequestID, name)
		}
		for _, value := range values {
			if !finiteNonNegative(value) {
				return IntervalSummary{}, fmt.Errorf("request %s has invalid %s interval", request.Metrics.RequestID, name)
			}
		}
		requestMeans = append(requestMeans, mean)
		intervals = append(intervals, values...)
	}
	label := "ICL"
	if token {
		label = "ITL"
	}
	means, err := SummarizeDistribution(requestMeans, len(successful)-len(requestMeans), "ms", fmt.Sprintf("no successful measured requests had available %s", label))
	if err != nil {
		return IntervalSummary{}, err
	}
	pooled, err := SummarizeDistribution(intervals, 0, "ms", fmt.Sprintf("no proven %s intervals were available", label))
	if err != nil {
		return IntervalSummary{}, err
	}
	return IntervalSummary{
		EligibleSuccessfulRequests: len(successful),
		ContributingRequests:       len(requestMeans),
		UnavailableRequests:        len(successful) - len(requestMeans),
		RequestMeans:               means,
		Intervals:                  pooled,
	}, nil
}

func calculateITLSummary(successful []Request) (ITLSummary, error) {
	base, err := calculateIntervalSummary(successful, true)
	if err != nil {
		return ITLSummary{}, err
	}
	sourceSet := make(map[string]struct{})
	for _, request := range successful {
		if !request.Metrics.ITL.Available {
			continue
		}
		if request.Metrics.ITL.Source != benchmark.TokenTimingSourceVLLM {
			return ITLSummary{}, fmt.Errorf("request %s has incompatible available ITL source %q", request.Metrics.RequestID, request.Metrics.ITL.Source)
		}
		sourceSet[request.Metrics.ITL.Source] = struct{}{}
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return ITLSummary{
		EligibleSuccessfulRequests: base.EligibleSuccessfulRequests,
		AvailableRequests:          base.ContributingRequests,
		UnavailableRequests:        base.UnavailableRequests,
		AvailableSources:           sources,
		RequestMeans:               base.RequestMeans,
		Intervals:                  base.Intervals,
	}, nil
}

func calculateSchedulerLag(input Input) (Distribution, error) {
	if input.Load.Mode != benchmark.LoadModeOpenLoop {
		return SummarizeDistribution(nil, 0, "ms", "not applicable to closed-loop load")
	}
	samples := make([]float64, 0)
	started := 0
	for _, arrival := range input.Arrivals {
		if arrival.Phase != benchmark.RequestPhaseMeasured || arrival.Disposition != benchmark.ArrivalStarted {
			continue
		}
		started++
		if arrival.SchedulerLagNS == nil {
			continue
		}
		if *arrival.SchedulerLagNS < 0 {
			return Distribution{}, fmt.Errorf("scheduler lag must not be negative")
		}
		samples = append(samples, float64(*arrival.SchedulerLagNS)/float64(time.Millisecond))
	}
	return SummarizeDistribution(samples, started-len(samples), "ms", "no measured started arrivals had scheduler-lag evidence")
}

func calculateTokens(successful []Request, completionSeconds float64) TokenSummary {
	result := TokenSummary{SuccessfulRequests: len(successful)}
	reason := ""
	var inputTokens, outputTokens, totalTokens int64
	for _, request := range successful {
		usage := request.Metrics.TokenUsage
		if !usage.Available {
			continue
		}
		result.UsageCoveredRequests++
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.InputTokens > maxInt()-usage.OutputTokens || usage.InputTokens+usage.OutputTokens != usage.TotalTokens {
			reason = fmt.Sprintf("server token usage is inconsistent for successful request %s", request.Metrics.RequestID)
			continue
		}
		nextInput, inputOK := addInt64(inputTokens, int64(usage.InputTokens))
		nextOutput, outputOK := addInt64(outputTokens, int64(usage.OutputTokens))
		nextTotal, totalOK := addInt64(totalTokens, int64(usage.TotalTokens))
		if !inputOK || !outputOK || !totalOK {
			reason = "successful server token usage totals overflow int64"
			continue
		}
		inputTokens = nextInput
		outputTokens = nextOutput
		totalTokens = nextTotal
	}
	if len(successful) == 0 {
		reason = "no successful measured requests"
	} else if reason == "" && result.UsageCoveredRequests != len(successful) {
		reason = fmt.Sprintf("server token usage unavailable for %d successful requests", len(successful)-result.UsageCoveredRequests)
	} else if reason == "" && completionSeconds <= 0 {
		reason = "measurement completion window must be positive"
	}
	if reason != "" {
		result.Reason = reason
		result.Throughput = unavailableTokenThroughput(reason)
		return result
	}
	result.Available = true
	result.SuccessfulInputTokens = inputTokens
	result.SuccessfulOutputTokens = outputTokens
	result.SuccessfulTotalTokens = totalTokens
	result.Throughput = TokenThroughput{
		Input:  calculateRate(inputTokens, completionSeconds, "tokens/s", "measurement completion window must be positive"),
		Output: calculateRate(outputTokens, completionSeconds, "tokens/s", "measurement completion window must be positive"),
		Total:  calculateRate(totalTokens, completionSeconds, "tokens/s", "measurement completion window must be positive"),
	}
	return result
}

func calculateSLO(successful []Request, config SLOConfig, completionSeconds float64) (SLOSummary, error) {
	if err := validateSLOConfig(config); err != nil {
		return SLOSummary{}, err
	}
	result := SLOSummary{Configured: config.Configured(), SuccessfulRequests: len(successful)}
	if !config.Configured() {
		result.PassRatio = calculateRatio(0, 0, GoodputNoSLOReason)
		result.Goodput = calculateRate(0, 0, "requests/s", GoodputNoSLOReason)
		return result, nil
	}
	if config.TTFTMS != nil {
		result.TTFT = &SLOMetricSummary{ThresholdMS: *config.TTFTMS}
	}
	if config.TPOTMS != nil {
		result.TPOT = &SLOMetricSummary{ThresholdMS: *config.TPOTMS}
	}
	if config.E2EMS != nil {
		result.E2E = &SLOMetricSummary{ThresholdMS: *config.E2EMS}
	}
	for _, request := range successful {
		evaluable := true
		passedAll := true
		if result.TTFT != nil {
			evaluateSLOMetric(result.TTFT, request.Metrics.TTFT, &evaluable, &passedAll)
		}
		if result.TPOT != nil {
			evaluateSLOMetric(result.TPOT, request.Metrics.TPOT, &evaluable, &passedAll)
		}
		if result.E2E != nil {
			evaluateSLOMetric(result.E2E, request.Metrics.E2E, &evaluable, &passedAll)
		}
		if !evaluable {
			result.UnevaluableRequests++
		} else if passedAll {
			result.EvaluableRequests++
			result.GoodRequests++
		} else {
			result.EvaluableRequests++
			result.BadRequests++
		}
	}
	result.PassRatio = calculateRatio(result.GoodRequests, result.EvaluableRequests, "no successful measured requests were evaluable against every configured SLO")
	result.Goodput = calculateRate(int64(result.GoodRequests), completionSeconds, "requests/s", "measurement completion window must be positive")
	return result, nil
}

func evaluateSLOMetric(result *SLOMetricSummary, value metrics.Scalar, evaluable, passedAll *bool) {
	if !value.Available {
		result.UnavailableRequests++
		*evaluable = false
		return
	}
	result.AvailableSuccessfulRequests++
	if value.Value <= result.ThresholdMS {
		result.PassingRequests++
	} else {
		result.FailingRequests++
		*passedAll = false
	}
}

func validateSLOConfig(config SLOConfig) error {
	values := []struct {
		name  string
		value *float64
	}{
		{name: "TTFT", value: config.TTFTMS},
		{name: "TPOT", value: config.TPOTMS},
		{name: "E2E", value: config.E2EMS},
	}
	for _, item := range values {
		name, value := item.name, item.value
		if value != nil && (!finiteNonNegative(*value) || *value <= 0) {
			return fmt.Errorf("%s SLO threshold must be finite and greater than zero", name)
		}
	}
	return nil
}

func calculateRatio(numerator, denominator int, unavailableReason string) Ratio {
	result := Ratio{Numerator: numerator, Denominator: denominator, Unit: "ratio"}
	if denominator <= 0 {
		result.Reason = unavailableReason
		return result
	}
	result.Available = true
	result.Value = float64(numerator) / float64(denominator)
	return result
}

func calculateRate(numerator int64, denominatorSeconds float64, unit, unavailableReason string) Rate {
	result := Rate{Numerator: numerator, DenominatorSeconds: denominatorSeconds, Unit: unit}
	if denominatorSeconds <= 0 || math.IsNaN(denominatorSeconds) || math.IsInf(denominatorSeconds, 0) {
		result.Reason = unavailableReason
		return result
	}
	result.Available = true
	result.Value = float64(numerator) / denominatorSeconds
	return result
}

func unavailableTokenThroughput(reason string) TokenThroughput {
	return TokenThroughput{
		Input:  calculateRate(0, 0, "tokens/s", reason),
		Output: calculateRate(0, 0, "tokens/s", reason),
		Total:  calculateRate(0, 0, "tokens/s", reason),
	}
}

func availableNumeric(value float64, unit string) Numeric {
	return Numeric{Available: true, Value: value, Unit: unit}
}
func unavailableNumeric(unit, reason string) Numeric { return Numeric{Unit: unit, Reason: reason} }
func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
func copyIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func maxInt() int { return int(^uint(0) >> 1) }

func addInt64(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func validOutcome(outcome benchmark.RequestOutcome) bool {
	switch outcome {
	case benchmark.OutcomeSucceeded, benchmark.OutcomeRequestError, benchmark.OutcomeRequestTimeout, benchmark.OutcomeParentCancelled, benchmark.OutcomeDrainTimeout:
		return true
	default:
		return false
	}
}

func incrementOutcome(counts *benchmark.OutcomeCounts, outcome benchmark.RequestOutcome) {
	switch outcome {
	case benchmark.OutcomeSucceeded:
		counts.Succeeded++
	case benchmark.OutcomeRequestError:
		counts.RequestError++
	case benchmark.OutcomeRequestTimeout:
		counts.RequestTimeout++
	case benchmark.OutcomeParentCancelled:
		counts.ParentCancelled++
	case benchmark.OutcomeDrainTimeout:
		counts.DrainTimeout++
	}
}
