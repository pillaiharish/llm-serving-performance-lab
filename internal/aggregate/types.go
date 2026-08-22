package aggregate

import (
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

const SchemaVersion = 7

const GoodputNoSLOReason = "no SLO configured"

type SLOConfig struct {
	TTFTMS *float64 `json:"ttft_ms,omitempty"`
	TPOTMS *float64 `json:"tpot_ms,omitempty"`
	E2EMS  *float64 `json:"e2e_ms,omitempty"`
}

func (c SLOConfig) Configured() bool {
	return c.TTFTMS != nil || c.TPOTMS != nil || c.E2EMS != nil
}

func SLOConfigFromDurations(ttft, tpot, e2e *time.Duration) SLOConfig {
	return SLOConfig{
		TTFTMS: durationMilliseconds(ttft),
		TPOTMS: durationMilliseconds(tpot),
		E2EMS:  durationMilliseconds(e2e),
	}
}

func durationMilliseconds(value *time.Duration) *float64 {
	if value == nil {
		return nil
	}
	milliseconds := float64(*value) / float64(time.Millisecond)
	return &milliseconds
}

type Input struct {
	RunID       string
	RunStatus   string
	ErrorClass  string
	Complete    bool
	Workload    WorkloadInput
	Load        LoadInput
	Measurement MeasurementInput
	Requests    []Request
	Arrivals    []benchmark.ArrivalRecord
	SLO         SLOConfig
}

type WorkloadInput struct {
	Mode                     string
	InputTargetTokens        *int
	InputResolvedTokens      *int
	RequestedOutputMaxTokens int
}

type LoadInput struct {
	Mode       benchmark.LoadMode
	ClosedLoop *ClosedLoopInput
	OpenLoop   *OpenLoopInput
}

type ClosedLoopInput struct {
	RequestedConcurrency int
	RequestedRequests    int
}

type OpenLoopInput struct {
	ConfiguredRequestRate float64
	Duration              time.Duration
	MaxInFlight           int
	PlannedArrivals       int
}

type MeasurementInput struct {
	Requested            int
	Attempted            int
	Completed            int
	ElapsedNS            int64
	Outcomes             benchmark.OutcomeCounts
	RequestedConcurrency int
	WorkerCount          int
	MaxObservedActive    int
	ArrivalCounts        *benchmark.ArrivalCounts
}

type Request struct {
	Sequence int
	Phase    benchmark.RequestPhase
	Outcome  benchmark.RequestOutcome
	Metrics  metrics.RequestMetrics
}

type RunSummary struct {
	SchemaVersion   int                   `json:"schema_version"`
	RunID           string                `json:"run_id"`
	RunStatus       string                `json:"run_status"`
	ErrorClass      string                `json:"error_class,omitempty"`
	Complete        bool                  `json:"complete"`
	Workload        WorkloadSummary       `json:"workload"`
	Load            LoadSummary           `json:"load"`
	Counts          RequestCounts         `json:"counts"`
	Durations       DurationSummary       `json:"durations"`
	RequestRates    RequestRateSummary    `json:"request_rates"`
	Latency         LatencySummary        `json:"latency"`
	PerRequestRates PerRequestRateSummary `json:"per_request_rates"`
	ICL             IntervalSummary       `json:"icl"`
	ITL             ITLSummary            `json:"itl"`
	SchedulerLag    Distribution          `json:"scheduler_lag_ms"`
	Tokens          TokenSummary          `json:"tokens"`
	SLO             SLOSummary            `json:"slo"`
}

type WorkloadSummary struct {
	Mode                     string `json:"mode"`
	InputTargetTokens        *int   `json:"input_target_tokens,omitempty"`
	InputResolvedTokens      *int   `json:"input_resolved_tokens,omitempty"`
	RequestedOutputMaxTokens int    `json:"requested_output_max_tokens"`
}

type LoadSummary struct {
	Mode       benchmark.LoadMode `json:"mode"`
	ClosedLoop *ClosedLoopSummary `json:"closed_loop,omitempty"`
	OpenLoop   *OpenLoopSummary   `json:"open_loop,omitempty"`
}

type ClosedLoopSummary struct {
	RequestedConcurrency int `json:"requested_concurrency"`
	WorkerCount          int `json:"worker_count"`
	MaxObservedActive    int `json:"max_observed_active"`
}

type OpenLoopSummary struct {
	ConfiguredRequestRate        float64 `json:"configured_request_rate"`
	AdmissionWindowSeconds       float64 `json:"admission_window_seconds"`
	MaxInFlight                  int     `json:"max_in_flight"`
	PlannedArrivals              int     `json:"planned_arrivals"`
	ProcessedArrivals            int     `json:"processed_arrivals"`
	StartedArrivals              int     `json:"started_arrivals"`
	ClientLimited                int     `json:"client_limited"`
	SchedulerLimited             int     `json:"scheduler_limited"`
	UnprocessedDueToCancellation int     `json:"unprocessed_due_to_cancellation"`
	MaxObservedInFlight          int     `json:"max_observed_in_flight"`
	PlannedArrivalRate           Rate    `json:"planned_arrival_rate"`
	ActualStartRate              Rate    `json:"actual_start_rate"`
	DeliveryRatio                Ratio   `json:"delivery_ratio"`
	ClientLimitedRatio           Ratio   `json:"client_limited_ratio"`
	SchedulerLimitedRatio        Ratio   `json:"scheduler_limited_ratio"`
	CancellationUnprocessedRatio Ratio   `json:"cancellation_unprocessed_ratio"`
}

type RequestCounts struct {
	RequestedOrPlanned int `json:"requested_or_planned"`
	Started            int `json:"started"`
	Completed          int `json:"completed"`
	Successful         int `json:"successful"`
	Failed             int `json:"failed"`
	RequestError       int `json:"request_error"`
	RequestTimeout     int `json:"request_timeout"`
	ParentCancelled    int `json:"parent_cancelled"`
	DrainTimeout       int `json:"drain_timeout"`
}

type DurationSummary struct {
	CompletionWindow Numeric `json:"completion_window_seconds"`
	AdmissionWindow  Numeric `json:"admission_window_seconds"`
}

type RequestRateSummary struct {
	SuccessRate                 Ratio `json:"request_success_rate"`
	FailureRate                 Ratio `json:"request_failure_rate"`
	CompletedRequestThroughput  Rate  `json:"completed_request_throughput"`
	SuccessfulRequestThroughput Rate  `json:"successful_request_throughput"`
}

type LatencySummary struct {
	TimeToHeaders Distribution `json:"time_to_headers"`
	TTFB          Distribution `json:"ttfb"`
	TTFT          Distribution `json:"ttft"`
	TTLT          Distribution `json:"ttlt"`
	E2E           Distribution `json:"e2e"`
	TPOT          Distribution `json:"tpot"`
}

type PerRequestRateSummary struct {
	OutputTokensPerSecond Distribution `json:"output_tokens_per_second"`
	DecodeTokensPerSecond Distribution `json:"decode_tokens_per_second"`
}

type IntervalSummary struct {
	EligibleSuccessfulRequests int          `json:"eligible_successful_requests"`
	ContributingRequests       int          `json:"contributing_requests"`
	UnavailableRequests        int          `json:"unavailable_requests"`
	RequestMeans               Distribution `json:"request_means_ms"`
	Intervals                  Distribution `json:"intervals_ms"`
}

type ITLSummary struct {
	EligibleSuccessfulRequests int          `json:"eligible_successful_requests"`
	AvailableRequests          int          `json:"available_requests"`
	UnavailableRequests        int          `json:"unavailable_requests"`
	AvailableSources           []string     `json:"available_sources"`
	RequestMeans               Distribution `json:"request_means_ms"`
	Intervals                  Distribution `json:"intervals_ms"`
}

type Distribution struct {
	Available        bool    `json:"available"`
	SampleCount      int     `json:"sample_count"`
	UnavailableCount int     `json:"unavailable_count"`
	Mean             float64 `json:"mean"`
	Min              float64 `json:"min"`
	P50              float64 `json:"p50"`
	P90              float64 `json:"p90"`
	P95              float64 `json:"p95"`
	P99              float64 `json:"p99"`
	Max              float64 `json:"max"`
	Unit             string  `json:"unit"`
	Reason           string  `json:"reason,omitempty"`
}

type Numeric struct {
	Available bool    `json:"available"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Reason    string  `json:"reason,omitempty"`
}

type Ratio struct {
	Available   bool    `json:"available"`
	Value       float64 `json:"value"`
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
	Unit        string  `json:"unit"`
	Reason      string  `json:"reason,omitempty"`
}

type Rate struct {
	Available          bool    `json:"available"`
	Value              float64 `json:"value"`
	Numerator          int64   `json:"numerator"`
	DenominatorSeconds float64 `json:"denominator_seconds"`
	Unit               string  `json:"unit"`
	Reason             string  `json:"reason,omitempty"`
}

type TokenSummary struct {
	Available              bool            `json:"available"`
	SuccessfulRequests     int             `json:"successful_requests"`
	UsageCoveredRequests   int             `json:"usage_covered_requests"`
	SuccessfulInputTokens  int64           `json:"successful_input_tokens"`
	SuccessfulOutputTokens int64           `json:"successful_output_tokens"`
	SuccessfulTotalTokens  int64           `json:"successful_total_tokens"`
	Throughput             TokenThroughput `json:"throughput"`
	Reason                 string          `json:"reason,omitempty"`
}

type TokenThroughput struct {
	Input  Rate `json:"input_tokens"`
	Output Rate `json:"output_tokens"`
	Total  Rate `json:"total_tokens"`
}

type SLOSummary struct {
	Configured          bool              `json:"configured"`
	TTFT                *SLOMetricSummary `json:"ttft,omitempty"`
	TPOT                *SLOMetricSummary `json:"tpot,omitempty"`
	E2E                 *SLOMetricSummary `json:"e2e,omitempty"`
	SuccessfulRequests  int               `json:"successful_requests"`
	EvaluableRequests   int               `json:"evaluable_requests"`
	GoodRequests        int               `json:"good_requests"`
	BadRequests         int               `json:"bad_requests"`
	UnevaluableRequests int               `json:"unevaluable_requests"`
	PassRatio           Ratio             `json:"pass_ratio_among_evaluable"`
	Goodput             Rate              `json:"goodput"`
}

type SLOMetricSummary struct {
	ThresholdMS                 float64 `json:"threshold_ms"`
	AvailableSuccessfulRequests int     `json:"available_successful_requests"`
	PassingRequests             int     `json:"passing_requests"`
	FailingRequests             int     `json:"failing_requests"`
	UnavailableRequests         int     `json:"unavailable_requests"`
}
