package aggregate

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

var CSVColumns = []string{
	"schema_version", "run_id", "run_status", "error_class", "summary_complete", "load_mode",
	"workload_mode", "input_target_tokens", "input_resolved_tokens", "requested_output_max_tokens",
	"requested_or_planned", "started", "completed", "successful", "failed", "request_error", "request_timeout", "parent_cancelled", "drain_timeout",
	"completion_window_seconds", "request_success_rate", "request_failure_rate", "completed_requests_per_second", "successful_requests_per_second",
	"ttft_sample_count", "ttft_unavailable_count", "ttft_p50_ms", "ttft_p95_ms", "ttft_p99_ms",
	"tpot_sample_count", "tpot_unavailable_count", "tpot_p50_ms_per_token", "tpot_p95_ms_per_token", "tpot_p99_ms_per_token",
	"e2e_sample_count", "e2e_unavailable_count", "e2e_p50_ms", "e2e_p95_ms", "e2e_p99_ms",
	"icl_contributing_requests", "icl_interval_sample_count", "icl_request_mean_p50_ms", "icl_request_mean_p95_ms", "icl_request_mean_p99_ms", "icl_interval_p50_ms", "icl_interval_p95_ms", "icl_interval_p99_ms",
	"itl_sources", "itl_available_requests", "itl_unavailable_requests", "itl_interval_sample_count", "itl_request_mean_p50_ms", "itl_request_mean_p95_ms", "itl_request_mean_p99_ms", "itl_p50_ms", "itl_p95_ms", "itl_p99_ms",
	"usage_covered_requests", "successful_input_tokens", "successful_output_tokens", "successful_total_tokens", "input_tokens_per_second", "output_tokens_per_second", "total_tokens_per_second",
	"slo_evaluable_requests", "slo_good_requests", "slo_bad_requests", "slo_unevaluable_requests", "slo_pass_ratio_among_evaluable", "goodput_requests_per_second",
	"slo_ttft_threshold_ms", "slo_tpot_threshold_ms", "slo_e2e_threshold_ms",
	"requested_concurrency", "worker_count", "max_observed_active",
	"configured_request_rate", "admission_window_seconds", "max_in_flight", "planned_arrivals", "processed_arrivals", "started_arrivals", "client_limited", "scheduler_limited", "unprocessed_due_to_cancellation", "max_observed_in_flight",
	"planned_arrivals_per_second", "actual_starts_per_second", "delivery_ratio", "client_limited_ratio", "scheduler_limited_ratio", "cancellation_unprocessed_ratio", "scheduler_lag_sample_count", "scheduler_lag_p95_ms",
}

func MarshalCSV(summary RunSummary) ([]byte, error) {
	record := CSVRecord(summary)
	if len(record) != len(CSVColumns) {
		return nil, fmt.Errorf("summary CSV record has %d fields, want %d", len(record), len(CSVColumns))
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(CSVColumns); err != nil {
		return nil, fmt.Errorf("write summary CSV header: %w", err)
	}
	if err := writer.Write(record); err != nil {
		return nil, fmt.Errorf("write summary CSV row: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush summary CSV: %w", err)
	}
	return buffer.Bytes(), nil
}

func CSVRecord(summary RunSummary) []string {
	record := []string{
		strconv.Itoa(summary.SchemaVersion), summary.RunID, summary.RunStatus, summary.ErrorClass, strconv.FormatBool(summary.Complete), string(summary.Load.Mode),
		summary.Workload.Mode, optionalInt(summary.Workload.InputTargetTokens), optionalInt(summary.Workload.InputResolvedTokens), strconv.Itoa(summary.Workload.RequestedOutputMaxTokens),
		strconv.Itoa(summary.Counts.RequestedOrPlanned), strconv.Itoa(summary.Counts.Started), strconv.Itoa(summary.Counts.Completed), strconv.Itoa(summary.Counts.Successful), strconv.Itoa(summary.Counts.Failed), strconv.Itoa(summary.Counts.RequestError), strconv.Itoa(summary.Counts.RequestTimeout), strconv.Itoa(summary.Counts.ParentCancelled), strconv.Itoa(summary.Counts.DrainTimeout),
		availableNumericCSV(summary.Durations.CompletionWindow), availableRatioCSV(summary.RequestRates.SuccessRate), availableRatioCSV(summary.RequestRates.FailureRate), availableRateCSV(summary.RequestRates.CompletedRequestThroughput), availableRateCSV(summary.RequestRates.SuccessfulRequestThroughput),
		strconv.Itoa(summary.Latency.TTFT.SampleCount), strconv.Itoa(summary.Latency.TTFT.UnavailableCount), distributionValue(summary.Latency.TTFT, summary.Latency.TTFT.P50), distributionValue(summary.Latency.TTFT, summary.Latency.TTFT.P95), distributionValue(summary.Latency.TTFT, summary.Latency.TTFT.P99),
		strconv.Itoa(summary.Latency.TPOT.SampleCount), strconv.Itoa(summary.Latency.TPOT.UnavailableCount), distributionValue(summary.Latency.TPOT, summary.Latency.TPOT.P50), distributionValue(summary.Latency.TPOT, summary.Latency.TPOT.P95), distributionValue(summary.Latency.TPOT, summary.Latency.TPOT.P99),
		strconv.Itoa(summary.Latency.E2E.SampleCount), strconv.Itoa(summary.Latency.E2E.UnavailableCount), distributionValue(summary.Latency.E2E, summary.Latency.E2E.P50), distributionValue(summary.Latency.E2E, summary.Latency.E2E.P95), distributionValue(summary.Latency.E2E, summary.Latency.E2E.P99),
		strconv.Itoa(summary.ICL.ContributingRequests), strconv.Itoa(summary.ICL.Intervals.SampleCount), distributionValue(summary.ICL.RequestMeans, summary.ICL.RequestMeans.P50), distributionValue(summary.ICL.RequestMeans, summary.ICL.RequestMeans.P95), distributionValue(summary.ICL.RequestMeans, summary.ICL.RequestMeans.P99), distributionValue(summary.ICL.Intervals, summary.ICL.Intervals.P50), distributionValue(summary.ICL.Intervals, summary.ICL.Intervals.P95), distributionValue(summary.ICL.Intervals, summary.ICL.Intervals.P99),
		strings.Join(summary.ITL.AvailableSources, ";"), strconv.Itoa(summary.ITL.AvailableRequests), strconv.Itoa(summary.ITL.UnavailableRequests), strconv.Itoa(summary.ITL.Intervals.SampleCount), distributionValue(summary.ITL.RequestMeans, summary.ITL.RequestMeans.P50), distributionValue(summary.ITL.RequestMeans, summary.ITL.RequestMeans.P95), distributionValue(summary.ITL.RequestMeans, summary.ITL.RequestMeans.P99), distributionValue(summary.ITL.Intervals, summary.ITL.Intervals.P50), distributionValue(summary.ITL.Intervals, summary.ITL.Intervals.P95), distributionValue(summary.ITL.Intervals, summary.ITL.Intervals.P99),
		strconv.Itoa(summary.Tokens.UsageCoveredRequests), availableTokenTotal(summary.Tokens, summary.Tokens.SuccessfulInputTokens), availableTokenTotal(summary.Tokens, summary.Tokens.SuccessfulOutputTokens), availableTokenTotal(summary.Tokens, summary.Tokens.SuccessfulTotalTokens), availableRateCSV(summary.Tokens.Throughput.Input), availableRateCSV(summary.Tokens.Throughput.Output), availableRateCSV(summary.Tokens.Throughput.Total),
		strconv.Itoa(summary.SLO.EvaluableRequests), strconv.Itoa(summary.SLO.GoodRequests), strconv.Itoa(summary.SLO.BadRequests), strconv.Itoa(summary.SLO.UnevaluableRequests), availableRatioCSV(summary.SLO.PassRatio), availableRateCSV(summary.SLO.Goodput),
		sloThreshold(summary.SLO.TTFT), sloThreshold(summary.SLO.TPOT), sloThreshold(summary.SLO.E2E),
	}
	if summary.Load.ClosedLoop != nil {
		record = append(record, strconv.Itoa(summary.Load.ClosedLoop.RequestedConcurrency), strconv.Itoa(summary.Load.ClosedLoop.WorkerCount), strconv.Itoa(summary.Load.ClosedLoop.MaxObservedActive))
	} else {
		record = append(record, "", "", "")
	}
	if summary.Load.OpenLoop != nil {
		open := summary.Load.OpenLoop
		record = append(record,
			formatFloat(open.ConfiguredRequestRate), formatFloat(open.AdmissionWindowSeconds), strconv.Itoa(open.MaxInFlight), strconv.Itoa(open.PlannedArrivals), strconv.Itoa(open.ProcessedArrivals), strconv.Itoa(open.StartedArrivals), strconv.Itoa(open.ClientLimited), strconv.Itoa(open.SchedulerLimited), strconv.Itoa(open.UnprocessedDueToCancellation), strconv.Itoa(open.MaxObservedInFlight),
			availableRateCSV(open.PlannedArrivalRate), availableRateCSV(open.ActualStartRate), availableRatioCSV(open.DeliveryRatio), availableRatioCSV(open.ClientLimitedRatio), availableRatioCSV(open.SchedulerLimitedRatio), availableRatioCSV(open.CancellationUnprocessedRatio), strconv.Itoa(summary.SchedulerLag.SampleCount), distributionValue(summary.SchedulerLag, summary.SchedulerLag.P95),
		)
	} else {
		for range 18 {
			record = append(record, "")
		}
	}
	return record
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func distributionValue(distribution Distribution, value float64) string {
	if !distribution.Available {
		return ""
	}
	return formatFloat(value)
}

func availableNumericCSV(value Numeric) string {
	if !value.Available {
		return ""
	}
	return formatFloat(value.Value)
}

func availableRatioCSV(value Ratio) string {
	if !value.Available {
		return ""
	}
	return formatFloat(value.Value)
}

func availableRateCSV(value Rate) string {
	if !value.Available {
		return ""
	}
	return formatFloat(value.Value)
}

func availableTokenTotal(summary TokenSummary, value int64) string {
	if !summary.Available {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func sloThreshold(value *SLOMetricSummary) string {
	if value == nil {
		return ""
	}
	return formatFloat(value.ThresholdMS)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
