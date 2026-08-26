// Package benchmarkexec owns the complete execution path for one benchmark
// run. CLI commands and higher-level experiment orchestration both use this
// package so the lifecycle and artifact semantics have one implementation.
package benchmarkexec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/openai"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/workload"
)

type FailureStage string

const (
	FailureStatic      FailureStage = "static_preflight"
	FailurePreparation FailureStage = "workload_preparation"
	FailureRuntime     FailureStage = "runtime"
)

type ExecutionError struct {
	Stage FailureStage
	Op    string
	Err   error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func StageOf(err error) FailureStage {
	var executionError *ExecutionError
	if errors.As(err, &executionError) {
		return executionError.Stage
	}
	return FailureRuntime
}

type Request struct {
	Config          config.Config
	APIKey          string
	ArtifactRoot    string
	SlentoreVersion string
}

type Result struct {
	Metadata         artifacts.RunMetadata
	Summary          aggregate.RunSummary
	RequestArtifacts []artifacts.RequestArtifact
	ArtifactPath     string
}

func (r Result) Successful() bool {
	return r.Metadata.RunStatus == artifacts.RunStatusCompleted
}

// ValidateStatic applies the complete run-level configuration and admission
// policy without creating clients, contacting endpoints, or writing artifacts.
func ValidateStatic(resolved config.Config) (benchmark.AdmissionDecision, error) {
	if err := resolved.Validate(); err != nil {
		return benchmark.AdmissionDecision{}, fmt.Errorf("invalid configuration: %w", err)
	}
	admission, err := benchmark.AdmitRun(admissionRequest(resolved), benchmark.CollectClientDiagnostics())
	if err != nil {
		return benchmark.AdmissionDecision{}, fmt.Errorf("client admission rejected: %w", err)
	}
	return admission, nil
}

// Execute performs exactly one independent benchmark run and publishes its
// ordinary schema-7 artifact tree beneath Request.ArtifactRoot.
func Execute(ctx context.Context, request Request) (Result, error) {
	resolved := request.Config
	admission, err := ValidateStatic(resolved)
	if err != nil {
		return Result{}, executionFailure(FailureStatic, "validate benchmark run", err)
	}
	if request.ArtifactRoot == "" {
		return Result{}, executionFailure(FailureStatic, "validate benchmark run", fmt.Errorf("artifact root is required"))
	}

	httpClient, transport, err := newSharedHTTPClient(admission.TransportWorkerLimit)
	if err != nil {
		return Result{}, executionFailure(FailureStatic, "create shared HTTP client", err)
	}
	defer transport.CloseIdleConnections()

	prepared, err := prepareWorkload(ctx, resolved, httpClient, request.APIKey)
	if err != nil {
		return Result{}, executionFailure(FailurePreparation, "prepare workload", err)
	}
	runID, err := benchmark.NewRunID()
	if err != nil {
		return Result{}, executionFailure(FailureStatic, "generate run ID", err)
	}
	client, err := openai.NewClientWithOptions(httpClient, resolved.Endpoint.BaseURL, request.APIKey, openai.ClientOptions{
		TokenEvidenceMode: openai.TokenEvidenceMode(resolved.Benchmark.TokenTiming.Mode),
	})
	if err != nil {
		return Result{}, executionFailure(FailureStatic, "create OpenAI client", err)
	}

	createdAt := time.Now().UTC()
	requestTemplate := benchmark.Request{
		RunID:           runID,
		Model:           resolved.Endpoint.Model,
		Prompt:          prepared.Prompt,
		MaxOutputTokens: prepared.RequestedOutputTokens,
		Temperature:     resolved.Request.Temperature,
	}
	lifecycleResult, runErr := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(ctx, benchmark.LifecyclePlan{
		Mode:             admission.Mode,
		RunID:            runID,
		RequestTemplate:  requestTemplate,
		Concurrency:      resolved.Benchmark.Concurrency,
		WarmupRequests:   resolved.Benchmark.WarmupRequests,
		MeasuredRequests: resolved.Benchmark.Requests,
		RequestTimeout:   resolved.Runtime.Timeout,
		DrainTimeout:     resolved.Runtime.DrainTimeout,
		OpenLoop: benchmark.OpenLoopPlan{
			RequestRate: resolved.Benchmark.OpenLoop.RequestRate,
			Duration:    resolved.Benchmark.OpenLoop.Duration,
			MaxInFlight: resolved.Benchmark.OpenLoop.MaxInFlight,
		},
	})
	if lifecycleResult.StartedAt.IsZero() {
		return Result{}, executionFailure(FailureRuntime, "coordinate benchmark run", runErr)
	}

	requestArtifacts := lifecycleRequestArtifacts(lifecycleResult)
	warmupMetadata := phaseMetadata(lifecycleResult.Warmup)
	measurementMetadata := phaseMetadata(lifecycleResult.Measurement)
	runStatus, runError, runErrorClass := classifyRun(lifecycleResult, runErr, warmupMetadata, measurementMetadata)
	metadata := artifacts.RunMetadata{
		SchemaVersion:   artifacts.SchemaVersion,
		RunID:           runID,
		SlentoreVersion: request.SlentoreVersion,
		CreatedAt:       createdAt,
		RunStatus:       runStatus,
		Error:           runError,
		ErrorClass:      runErrorClass,
		Model:           resolved.Endpoint.Model,
		BaseURL:         resolved.Endpoint.BaseURL,
		Temperature:     resolved.Request.Temperature,
		RequestTimeout:  resolved.Runtime.Timeout.String(),
		Workload:        workloadMetadata(prepared),
		TokenTiming:     tokenTimingMetadata(resolved.Benchmark.TokenTiming.Mode),
		SLO:             aggregate.SLOConfigFromDurations(resolved.Benchmark.SLO.TTFT, resolved.Benchmark.SLO.TPOT, resolved.Benchmark.SLO.E2E),
		SafetyLimits: artifacts.SafetyLimits{
			MaxConcurrency:  resolved.Benchmark.Safety.MaxConcurrency,
			MaxRequests:     resolved.Benchmark.Safety.MaxRequests,
			MaxRequestRate:  resolved.Benchmark.Safety.MaxRequestRate,
			MaxInFlight:     resolved.Benchmark.Safety.MaxInFlight,
			MaxInputTokens:  resolved.Benchmark.Safety.MaxInputTokens,
			MaxOutputTokens: resolved.Benchmark.Safety.MaxOutputTokens,
		},
		Load:              loadMetadata(resolved, admission.PlannedArrivals),
		ClientDiagnostics: admission.Diagnostics,
		Lifecycle: artifacts.LifecycleMetadata{
			StartedAt:   lifecycleResult.StartedAt,
			CompletedAt: lifecycleResult.CompletedAt,
			ElapsedNS:   lifecycleResult.ElapsedNS,
			Transitions: lifecycleResult.Transitions,
		},
		StopAdmission: stopAdmissionMetadata(lifecycleResult.Transitions),
		Warmup:        warmupMetadata,
		Measurement:   measurementMetadata,
		Drain: artifacts.DrainMetadata{
			StartedAt:           lifecycleResult.Drain.StartedAt,
			CompletedAt:         lifecycleResult.Drain.CompletedAt,
			ElapsedNS:           lifecycleResult.Drain.ElapsedNS,
			Timeout:             lifecycleResult.Drain.Timeout.String(),
			TimedOut:            lifecycleResult.Drain.TimedOut,
			ParentCancelled:     lifecycleResult.Drain.ParentCancelled,
			CancelledRequests:   len(lifecycleResult.Drain.CancelledRequestIDs),
			CancelledRequestIDs: lifecycleResult.Drain.CancelledRequestIDs,
		},
	}

	arrivals := lifecycleArrivals(lifecycleResult)
	runSummary, err := artifacts.CalculateSummary(metadata, requestArtifacts, arrivals)
	if err != nil {
		return Result{}, executionFailure(FailureRuntime, "aggregate run summary", err)
	}
	artifactPath, err := artifacts.NewWriter(request.ArtifactRoot).WriteWithArrivals(metadata, requestArtifacts, arrivals)
	if err != nil {
		return Result{}, executionFailure(FailureRuntime, "write artifacts", err)
	}
	return Result{Metadata: metadata, Summary: runSummary, RequestArtifacts: requestArtifacts, ArtifactPath: artifactPath}, nil
}

func executionFailure(stage FailureStage, op string, err error) error {
	if err == nil {
		err = fmt.Errorf("operation failed without an error")
	}
	return &ExecutionError{Stage: stage, Op: op, Err: err}
}

func admissionRequest(resolved config.Config) benchmark.AdmissionRequest {
	return benchmark.AdmissionRequest{
		Mode:               benchmark.LoadMode(resolved.Benchmark.Mode),
		Concurrency:        resolved.Benchmark.Concurrency,
		Requests:           resolved.Benchmark.Requests,
		WarmupRequests:     resolved.Benchmark.WarmupRequests,
		RequestRate:        resolved.Benchmark.OpenLoop.RequestRate,
		Duration:           resolved.Benchmark.OpenLoop.Duration,
		MaxInFlight:        resolved.Benchmark.OpenLoop.MaxInFlight,
		MaxConcurrency:     resolved.Benchmark.Safety.MaxConcurrency,
		MaxRequests:        resolved.Benchmark.Safety.MaxRequests,
		MaxRequestRate:     resolved.Benchmark.Safety.MaxRequestRate,
		MaxInFlightCeiling: resolved.Benchmark.Safety.MaxInFlight,
	}
}

func classifyRun(result benchmark.LifecycleResult, runErr error, warmup, measurement artifacts.PhaseMetadata) (string, string, string) {
	if runErr != nil {
		status := artifacts.RunStatusFailed
		if errors.Is(runErr, context.Canceled) || (result.Drain.ParentCancelled && !errors.Is(runErr, benchmark.ErrDrainTimeout)) {
			status = artifacts.RunStatusCancelled
		}
		return status, runErr.Error(), ""
	}
	loadLimited := result.LoadMode == benchmark.LoadModeOpenLoop && !result.Drain.ParentCancelled && (result.Measurement.ArrivalCounts.ClientLimited > 0 || result.Measurement.ArrivalCounts.SchedulerLimited > 0)
	if loadLimited {
		return artifacts.RunStatusFailed, benchmark.ErrLoadDelivery.Error(), artifacts.ErrorClassLoadDelivery
	}
	if warmup.Failed > 0 {
		return artifacts.RunStatusFailed, fmt.Sprintf("%d of %d attempted warmup requests failed", warmup.Failed, warmup.Attempted), ""
	}
	if measurement.Failed > 0 {
		return artifacts.RunStatusFailed, fmt.Sprintf("%d of %d attempted measured requests failed", measurement.Failed, measurement.Attempted), ""
	}
	if result.LoadMode == benchmark.LoadModeClosedLoop && (warmup.Attempted != warmup.Requested || measurement.Attempted != measurement.Requested) {
		return artifacts.RunStatusFailed, "lifecycle did not attempt every requested warmup and measured request", ""
	}
	return artifacts.RunStatusCompleted, "", ""
}

func newSharedHTTPClient(connectionLimit int) (*http.Client, *http.Transport, error) {
	if connectionLimit <= 0 {
		return nil, nil, fmt.Errorf("shared HTTP connection limit must be greater than zero")
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, nil, fmt.Errorf("default HTTP transport has unexpected type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	if transport.MaxIdleConns < connectionLimit {
		transport.MaxIdleConns = connectionLimit
	}
	transport.MaxIdleConnsPerHost = connectionLimit
	transport.MaxConnsPerHost = connectionLimit
	return &http.Client{Transport: transport}, transport, nil
}

func prepareWorkload(ctx context.Context, resolved config.Config, httpClient *http.Client, apiKey string) (workload.PreparedWorkload, error) {
	if resolved.Workload.Mode == config.WorkloadModePrompt {
		return workload.PreparePrompt(resolved.Request.Prompt, resolved.Request.MaxOutputTokens), nil
	}
	prepareContext, cancel := context.WithTimeout(ctx, resolved.Runtime.Timeout)
	defer cancel()
	tokenizer, err := workload.NewVLLMTokenizer(httpClient, resolved.Workload.Tokenizer.URL, apiKey, resolved.Endpoint.Model)
	if err != nil {
		return workload.PreparedWorkload{}, err
	}
	if err := tokenizer.Initialize(prepareContext); err != nil {
		return workload.PreparedWorkload{}, err
	}
	return workload.NewDeterministicBuilder(tokenizer).Build(prepareContext, workload.WorkloadSpec{
		TargetInputTokens:     resolved.Workload.InputTokens,
		RequestedOutputTokens: resolved.Request.MaxOutputTokens,
		MaxInputTokens:        resolved.Benchmark.Safety.MaxInputTokens,
	})
}

func stopAdmissionMetadata(transitions []benchmark.PhaseTransition) artifacts.StopAdmissionMetadata {
	for _, transition := range transitions {
		if transition.Phase == benchmark.PhaseStopAdmission {
			return artifacts.StopAdmissionMetadata{StoppedAt: transition.EnteredAt, StoppedAfterNS: transition.EnteredAfterNS, Reason: transition.Reason}
		}
	}
	return artifacts.StopAdmissionMetadata{}
}

func phaseMetadata(result benchmark.PhaseResult) artifacts.PhaseMetadata {
	attempted := len(result.Completed)
	metadata := artifacts.PhaseMetadata{
		Phase: result.Phase, Status: result.Status, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, ElapsedNS: result.ElapsedNS,
		Requested: result.RequestedRequests, Attempted: attempted, Completed: attempted, Successful: result.Outcomes.Succeeded, Failed: result.Outcomes.Failed(),
		RequestedConcurrency: result.RequestedConcurrency, EffectiveWorkers: result.WorkerCount, MaxObservedActive: result.MaxObservedActive, Outcomes: result.Outcomes,
	}
	if result.LoadMode == benchmark.LoadModeOpenLoop {
		counts := result.ArrivalCounts
		metadata.Arrivals = &counts
		metadata.RequestedConcurrency = 0
		metadata.EffectiveWorkers = 0
		metadata.MaxObservedActive = 0
	}
	return metadata
}

func lifecycleRequestArtifacts(result benchmark.LifecycleResult) []artifacts.RequestArtifact {
	requestArtifacts := make([]artifacts.RequestArtifact, 0, len(result.Warmup.Completed)+len(result.Measurement.Completed))
	for _, phase := range [][]benchmark.CompletedRequest{result.Warmup.Completed, result.Measurement.Completed} {
		for _, completed := range phase {
			requestArtifacts = append(requestArtifacts, artifacts.RequestArtifact{Sequence: completed.Sequence, Phase: completed.Phase, Outcome: completed.Outcome, Observation: completed.Result.Observation, Metrics: metrics.Calculate(completed.Result.Observation)})
		}
	}
	return requestArtifacts
}

func lifecycleArrivals(result benchmark.LifecycleResult) []benchmark.ArrivalRecord {
	if result.LoadMode != benchmark.LoadModeOpenLoop {
		return nil
	}
	arrivals := make([]benchmark.ArrivalRecord, 0, len(result.Warmup.Arrivals)+len(result.Measurement.Arrivals))
	arrivals = append(arrivals, result.Warmup.Arrivals...)
	return append(arrivals, result.Measurement.Arrivals...)
}

func loadMetadata(resolved config.Config, plannedArrivals int) artifacts.LoadMetadata {
	if resolved.Benchmark.Mode == config.LoadModeOpenLoop {
		return artifacts.LoadMetadata{Mode: benchmark.LoadModeOpenLoop, OpenLoop: &artifacts.OpenLoopLoadMetadata{RequestRate: resolved.Benchmark.OpenLoop.RequestRate, Duration: resolved.Benchmark.OpenLoop.Duration.String(), MaxInFlight: resolved.Benchmark.OpenLoop.MaxInFlight, PlannedArrivals: plannedArrivals}}
	}
	return artifacts.LoadMetadata{Mode: benchmark.LoadModeClosedLoop, ClosedLoop: &artifacts.ClosedLoopLoadMetadata{RequestedConcurrency: resolved.Benchmark.Concurrency, RequestedRequests: resolved.Benchmark.Requests}}
}

func workloadMetadata(prepared workload.PreparedWorkload) artifacts.WorkloadMetadata {
	metadata := artifacts.WorkloadMetadata{Mode: prepared.Mode, Output: artifacts.WorkloadOutputMetadata{RequestedMaxTokens: prepared.RequestedOutputTokens}, Builder: prepared.Builder, PromptBytes: prepared.PromptBytes, PromptSHA256: prepared.PromptSHA256, Tokenizer: prepared.Tokenizer}
	if prepared.Mode == workload.ModeTokenLength {
		metadata.Input = &artifacts.WorkloadInputMetadata{Contract: prepared.InputContract, TargetTokens: prepared.TargetInputTokens, ResolvedTokens: prepared.ResolvedInputTokens}
	}
	return metadata
}

func tokenTimingMetadata(mode config.TokenTimingMode) artifacts.TokenTimingMetadata {
	metadata := artifacts.TokenTimingMetadata{Mode: string(mode)}
	if mode == config.TokenTimingVLLM {
		metadata.Source = benchmark.TokenTimingSourceVLLM
	}
	return metadata
}
