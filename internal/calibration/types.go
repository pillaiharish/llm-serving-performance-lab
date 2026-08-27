package calibration

import (
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
)

const SchemaVersion = 1

const DefaultSampleInterval = 10 * time.Millisecond

const (
	ReasonRunNotCompleted             = "run_not_completed"
	ReasonRequestsNotFullyAttempted   = "requests_not_fully_attempted"
	ReasonRequestFailuresPresent      = "request_failures_present"
	ReasonPlannedArrivalsNotProcessed = "planned_arrivals_not_processed"
	ReasonDeliveryRatioBelowOne       = "delivery_ratio_below_one"
	ReasonClientLimited               = "client_limited"
	ReasonSchedulerLimited            = "scheduler_limited"
	ReasonCancellationUnprocessed     = "cancellation_unprocessed"
	ReasonSchedulerLagUnavailable     = "scheduler_lag_p95_unavailable"
	ReasonSchedulerLagExceeded        = "scheduler_lag_p95_exceeded"
)

type ResourceEvidence struct {
	StartedAt                  time.Time `json:"started_at"`
	CompletedAt                time.Time `json:"completed_at"`
	ElapsedNS                  int64     `json:"elapsed_ns"`
	SampleIntervalNS           int64     `json:"sample_interval_ns"`
	Samples                    int       `json:"samples"`
	GoroutinesStart            int       `json:"goroutines_start"`
	GoroutinesObservedPeak     int       `json:"goroutines_observed_peak"`
	GoroutinesEnd              int       `json:"goroutines_end"`
	HeapAllocStartBytes        uint64    `json:"heap_alloc_start_bytes"`
	HeapAllocObservedPeakBytes uint64    `json:"heap_alloc_observed_peak_bytes"`
	HeapAllocEndBytes          uint64    `json:"heap_alloc_end_bytes"`
	HeapSysObservedPeakBytes   uint64    `json:"heap_sys_observed_peak_bytes"`
	SysObservedPeakBytes       uint64    `json:"sys_observed_peak_bytes"`
	TotalAllocDeltaBytes       uint64    `json:"total_alloc_delta_bytes"`
	MallocsDelta               uint64    `json:"mallocs_delta"`
	FreesDelta                 uint64    `json:"frees_delta"`
	NumGCDelta                 uint32    `json:"num_gc_delta"`
	GCPauseTotalDeltaNS        uint64    `json:"gc_pause_total_delta_ns"`
}

type Point struct {
	PointIndex                  int                          `json:"point_index"`
	PointID                     string                       `json:"point_id"`
	PointStatus                 string                       `json:"point_status"`
	RequestedConcurrency        *int                         `json:"requested_concurrency,omitempty"`
	ConfiguredRequestRate       *float64                     `json:"configured_request_rate,omitempty"`
	ChildRunID                  *string                      `json:"child_run_id,omitempty"`
	ChildRunPath                *string                      `json:"child_run_path,omitempty"`
	RunStatus                   *string                      `json:"run_status,omitempty"`
	DeliveryClean               *bool                        `json:"delivery_clean"`
	DeliveryReasons             []string                     `json:"delivery_reasons"`
	Counts                      *aggregate.RequestCounts     `json:"counts,omitempty"`
	SuccessfulRequestThroughput *aggregate.Rate              `json:"successful_request_throughput,omitempty"`
	ClosedLoop                  *aggregate.ClosedLoopSummary `json:"closed_loop,omitempty"`
	OpenLoop                    *aggregate.OpenLoopSummary   `json:"open_loop,omitempty"`
	SchedulerLag                *aggregate.Distribution      `json:"scheduler_lag_p95_ms,omitempty"`
	Resource                    *ResourceEvidence            `json:"resource,omitempty"`
}

type Manifest struct {
	CalibrationSchemaVersion int                          `json:"calibration_schema_version"`
	CalibrationID            string                       `json:"calibration_id"`
	CreatedAt                time.Time                    `json:"created_at"`
	CompletedAt              time.Time                    `json:"completed_at"`
	Status                   string                       `json:"status"`
	Complete                 bool                         `json:"complete"`
	LoadMode                 config.LoadMode              `json:"load_mode"`
	ConcurrencyValues        []int                        `json:"concurrency_values"`
	RequestRateValues        []float64                    `json:"request_rate_values"`
	MaxSchedulerLagP95MS     *float64                     `json:"max_scheduler_lag_p95_ms,omitempty"`
	ExperimentID             string                       `json:"experiment_id"`
	ExperimentPath           string                       `json:"experiment_path"`
	ClientDiagnostics        *benchmark.ClientDiagnostics `json:"client_diagnostics,omitempty"`
	PlannedPoints            int                          `json:"planned_points"`
	EvaluatedPoints          int                          `json:"evaluated_points"`
	CleanPoints              int                          `json:"clean_points"`
	DirtyPoints              int                          `json:"dirty_points"`
	UnevaluatedPoints        int                          `json:"unevaluated_points"`
	Points                   []Point                      `json:"points"`
}

type Result struct {
	Manifest       Manifest
	ArtifactPath   string
	Experiment     experiment.Result
	ExperimentPath string
}

func (r Result) Successful() bool {
	return r.Manifest.Status == experiment.StatusCompleted && r.Manifest.Complete && r.Manifest.PlannedPoints > 0 && r.Manifest.CleanPoints == r.Manifest.PlannedPoints && r.Manifest.DirtyPoints == 0 && r.Manifest.UnevaluatedPoints == 0
}
