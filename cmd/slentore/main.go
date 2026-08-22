package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/openai"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/workload"
)

var version = "devel"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	return runContext(context.Background(), args, stdout, stderr, lookupEnv)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	if len(args) == 0 {
		printRootUsage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printRootUsage(stdout)
		return 0
	}
	if args[0] != "bench" {
		fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		printRootUsage(stderr)
		return 2
	}
	return runBenchContext(ctx, args[1:], stdout, stderr, lookupEnv)
}

func runBench(args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	return runBenchContext(context.Background(), args, stdout, stderr, lookupEnv)
}

func runBenchContext(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	flags := flag.NewFlagSet("slentore bench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printBenchUsage(flags.Output()) }

	var configPath string
	var baseURL string
	var model string
	var prompt string
	var maxOutputTokens int
	var temperature float64
	var timeoutText string
	var outputDir string
	var apiKeyEnv string
	var concurrency int
	var requests int
	var maxConcurrency int
	var maxRequests int
	var warmupRequests int
	var drainTimeoutText string
	var modeText string
	var requestRate float64
	var durationText string
	var maxInFlight int
	var maxRequestRateCeiling float64
	var maxInFlightCeiling int
	var workloadModeText string
	var inputTokens int
	var tokenizerAdapterText string
	var tokenizerURL string
	var maxInputTokensCeiling int
	var maxOutputTokensCeiling int
	var tokenTimingText string

	flags.StringVar(&configPath, "config", "", "path to a version 1 YAML configuration file")
	flags.StringVar(&baseURL, "base-url", "", "OpenAI-compatible API root, normally ending in /v1")
	flags.StringVar(&model, "model", "", "model identifier")
	flags.StringVar(&prompt, "prompt", "", "single user prompt (never persisted)")
	flags.IntVar(&maxOutputTokens, "max-output-tokens", 0, "maximum output tokens")
	flags.Float64Var(&temperature, "temperature", 0, "sampling temperature")
	flags.StringVar(&timeoutText, "timeout", "", "request timeout, such as 120s")
	flags.StringVar(&outputDir, "output-dir", "", "artifact output directory")
	flags.StringVar(&apiKeyEnv, "api-key-env", "", "environment variable containing the API key")
	flags.IntVar(&concurrency, "concurrency", 0, "simultaneously active requests")
	flags.IntVar(&requests, "requests", 0, "total requests to attempt")
	flags.IntVar(&maxConcurrency, "max-concurrency", 0, "client admission ceiling for concurrency")
	flags.IntVar(&maxRequests, "max-requests", 0, "client admission ceiling for total requests")
	flags.IntVar(&warmupRequests, "warmup-requests", 0, "warmup requests to attempt before measurement")
	flags.StringVar(&drainTimeoutText, "drain-timeout", "", "maximum drain duration after measured admission stops")
	flags.StringVar(&modeText, "mode", "", "load mode: closed-loop or open-loop")
	flags.Float64Var(&requestRate, "request-rate", 0, "open-loop offered arrivals per second")
	flags.StringVar(&durationText, "duration", "", "open-loop measurement duration, such as 30s")
	flags.IntVar(&maxInFlight, "max-in-flight", 0, "open-loop admitted request bound")
	flags.Float64Var(&maxRequestRateCeiling, "max-request-rate-ceiling", 0, "open-loop request-rate safety ceiling")
	flags.IntVar(&maxInFlightCeiling, "max-in-flight-ceiling", 0, "open-loop in-flight safety ceiling")
	flags.StringVar(&workloadModeText, "workload-mode", "", "workload mode: prompt or token-length")
	flags.IntVar(&inputTokens, "input-tokens", 0, "target rendered chat input tokens")
	flags.StringVar(&tokenizerAdapterText, "tokenizer-adapter", "", "tokenizer adapter: vllm")
	flags.StringVar(&tokenizerURL, "tokenizer-url", "", "exact vLLM /tokenize endpoint")
	flags.IntVar(&maxInputTokensCeiling, "max-input-tokens-ceiling", 0, "client safety ceiling for target input tokens")
	flags.IntVar(&maxOutputTokensCeiling, "max-output-tokens-ceiling", 0, "client safety ceiling for requested output tokens")
	flags.StringVar(&tokenTimingText, "token-timing", "", "token timing evidence: disabled or vllm")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "error: unexpected positional arguments: %v\n", flags.Args())
		return 2
	}

	resolved, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	visited := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	overrides := config.Overrides{}
	if visited["base-url"] {
		overrides.BaseURL = &baseURL
	}
	if visited["model"] {
		overrides.Model = &model
	}
	if visited["prompt"] {
		overrides.Prompt = &prompt
	}
	if visited["workload-mode"] {
		mode, parseErr := parseCLIWorkloadMode(workloadModeText)
		if parseErr != nil {
			fmt.Fprintf(stderr, "error: --workload-mode: %v\n", parseErr)
			return 2
		}
		overrides.WorkloadMode = &mode
	}
	if visited["input-tokens"] {
		overrides.InputTokens = &inputTokens
	}
	if visited["tokenizer-adapter"] {
		adapter, parseErr := parseCLITokenizerAdapter(tokenizerAdapterText)
		if parseErr != nil {
			fmt.Fprintf(stderr, "error: --tokenizer-adapter: %v\n", parseErr)
			return 2
		}
		overrides.TokenizerAdapter = &adapter
	}
	if visited["tokenizer-url"] {
		overrides.TokenizerURL = &tokenizerURL
	}
	if visited["max-output-tokens"] {
		overrides.MaxOutputTokens = &maxOutputTokens
	}
	if visited["temperature"] {
		overrides.Temperature = &temperature
	}
	if visited["output-dir"] {
		overrides.OutputDir = &outputDir
	}
	if visited["api-key-env"] {
		overrides.APIKeyEnv = &apiKeyEnv
	}
	if visited["timeout"] {
		timeout, err := time.ParseDuration(timeoutText)
		if err != nil {
			fmt.Fprintf(stderr, "error: --timeout: %v\n", err)
			return 2
		}
		overrides.Timeout = &timeout
	}
	if visited["concurrency"] {
		overrides.Concurrency = &concurrency
	}
	if visited["requests"] {
		overrides.Requests = &requests
	}
	if visited["max-concurrency"] {
		overrides.MaxConcurrency = &maxConcurrency
	}
	if visited["max-requests"] {
		overrides.MaxRequests = &maxRequests
	}
	if visited["warmup-requests"] {
		overrides.WarmupRequests = &warmupRequests
	}
	if visited["drain-timeout"] {
		drainTimeout, err := time.ParseDuration(drainTimeoutText)
		if err != nil {
			fmt.Fprintf(stderr, "error: --drain-timeout: %v\n", err)
			return 2
		}
		overrides.DrainTimeout = &drainTimeout
	}
	if visited["mode"] {
		mode, err := parseCLILoadMode(modeText)
		if err != nil {
			fmt.Fprintf(stderr, "error: --mode: %v\n", err)
			return 2
		}
		overrides.Mode = &mode
	}
	if visited["request-rate"] {
		overrides.RequestRate = &requestRate
	}
	if visited["duration"] {
		duration, err := time.ParseDuration(durationText)
		if err != nil {
			fmt.Fprintf(stderr, "error: --duration: %v\n", err)
			return 2
		}
		overrides.Duration = &duration
	}
	if visited["max-in-flight"] {
		overrides.MaxInFlight = &maxInFlight
	}
	if visited["max-request-rate-ceiling"] {
		overrides.MaxRequestRate = &maxRequestRateCeiling
	}
	if visited["max-in-flight-ceiling"] {
		overrides.MaxInFlightCeiling = &maxInFlightCeiling
	}
	if visited["max-input-tokens-ceiling"] {
		overrides.MaxInputTokens = &maxInputTokensCeiling
	}
	if visited["max-output-tokens-ceiling"] {
		overrides.MaxOutputTokensCeiling = &maxOutputTokensCeiling
	}
	if visited["token-timing"] {
		mode, parseErr := parseCLITokenTimingMode(tokenTimingText)
		if parseErr != nil {
			fmt.Fprintf(stderr, "error: --token-timing: %v\n", parseErr)
			return 2
		}
		overrides.TokenTimingMode = &mode
	}
	resolved.ApplyOverrides(overrides)
	if err := resolved.Validate(); err != nil {
		fmt.Fprintf(stderr, "error: invalid configuration: %v\n", err)
		return 2
	}

	diagnostics := benchmark.CollectClientDiagnostics()
	admission, err := benchmark.AdmitRun(benchmark.AdmissionRequest{
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
	}, diagnostics)
	if err != nil {
		fmt.Fprintf(stderr, "error: client admission rejected: %v\n", err)
		return 2
	}
	apiKey, err := config.ResolveAPIKey(resolved.Endpoint.APIKeyEnv, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	httpClient, transport, err := newSharedHTTPClient(admission.TransportWorkerLimit)
	if err != nil {
		fmt.Fprintf(stderr, "error: create shared HTTP client: %v\n", err)
		return 2
	}
	defer transport.CloseIdleConnections()
	prepared, err := prepareWorkload(ctx, resolved, httpClient, apiKey)
	if err != nil {
		fmt.Fprintf(stderr, "error: prepare workload: %v\n", err)
		return 1
	}
	runID, err := benchmark.NewRunID()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	client, err := openai.NewClientWithOptions(httpClient, resolved.Endpoint.BaseURL, apiKey, openai.ClientOptions{
		TokenEvidenceMode: openai.TokenEvidenceMode(resolved.Benchmark.TokenTiming.Mode),
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: create OpenAI client: %v\n", err)
		return 2
	}
	runner := benchmark.NewRunner(client)
	requestTemplate := benchmark.Request{
		RunID:           runID,
		Model:           resolved.Endpoint.Model,
		Prompt:          prepared.Prompt,
		MaxOutputTokens: prepared.RequestedOutputTokens,
		Temperature:     resolved.Request.Temperature,
	}

	createdAt := time.Now().UTC()
	coordinator := benchmark.NewLifecycleCoordinator(runner)
	lifecycleResult, runErr := coordinator.Run(ctx, benchmark.LifecyclePlan{
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
		fmt.Fprintf(stderr, "error: coordinate benchmark run: %v\n", runErr)
		return 1
	}

	requestArtifacts := lifecycleRequestArtifacts(lifecycleResult)
	warmupMetadata := phaseMetadata(lifecycleResult.Warmup)
	measurementMetadata := phaseMetadata(lifecycleResult.Measurement)

	runStatus := artifacts.RunStatusCompleted
	runError := ""
	runErrorClass := ""
	loadLimited := lifecycleResult.LoadMode == benchmark.LoadModeOpenLoop && !lifecycleResult.Drain.ParentCancelled && (lifecycleResult.Measurement.ArrivalCounts.ClientLimited > 0 || lifecycleResult.Measurement.ArrivalCounts.SchedulerLimited > 0)
	if runErr != nil {
		runStatus = artifacts.RunStatusFailed
		if errors.Is(runErr, context.Canceled) || (lifecycleResult.Drain.ParentCancelled && !errors.Is(runErr, benchmark.ErrDrainTimeout)) {
			runStatus = artifacts.RunStatusCancelled
		}
		runError = runErr.Error()
	} else if loadLimited {
		runStatus = artifacts.RunStatusFailed
		runError = benchmark.ErrLoadDelivery.Error()
		runErrorClass = artifacts.ErrorClassLoadDelivery
	} else if warmupMetadata.Failed > 0 {
		runStatus = artifacts.RunStatusFailed
		runError = fmt.Sprintf("%d of %d attempted warmup requests failed", warmupMetadata.Failed, warmupMetadata.Attempted)
	} else if measurementMetadata.Failed > 0 {
		runStatus = artifacts.RunStatusFailed
		runError = fmt.Sprintf("%d of %d attempted measured requests failed", measurementMetadata.Failed, measurementMetadata.Attempted)
	} else if lifecycleResult.LoadMode == benchmark.LoadModeClosedLoop && (warmupMetadata.Attempted != warmupMetadata.Requested || measurementMetadata.Attempted != measurementMetadata.Requested) {
		runStatus = artifacts.RunStatusFailed
		runError = "lifecycle did not attempt every requested warmup and measured request"
	}
	metadata := artifacts.RunMetadata{
		SchemaVersion:   artifacts.SchemaVersion,
		RunID:           runID,
		SlentoreVersion: slentoreVersion(),
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

	artifactPath, artifactErr := artifacts.NewWriter(resolved.Capture.OutputDir).WriteWithArrivals(metadata, requestArtifacts, lifecycleArrivals(lifecycleResult))
	printLifecycleSummary(stdout, runID, lifecycleResult, metadata.Workload, metadata.TokenTiming, metadata.Load, warmupMetadata, measurementMetadata, requestArtifacts, artifactPath, runErr, runErrorClass)
	if artifactErr != nil {
		fmt.Fprintf(stderr, "error: write artifacts: %v\n", artifactErr)
		return 1
	}
	if runErr != nil || loadLimited || warmupMetadata.Failed > 0 || measurementMetadata.Failed > 0 || (lifecycleResult.LoadMode == benchmark.LoadModeClosedLoop && (warmupMetadata.Attempted != warmupMetadata.Requested || measurementMetadata.Attempted != measurementMetadata.Requested)) {
		return 1
	}
	return 0
}

func stopAdmissionMetadata(transitions []benchmark.PhaseTransition) artifacts.StopAdmissionMetadata {
	for _, transition := range transitions {
		if transition.Phase == benchmark.PhaseStopAdmission {
			return artifacts.StopAdmissionMetadata{
				StoppedAt:      transition.EnteredAt,
				StoppedAfterNS: transition.EnteredAfterNS,
				Reason:         transition.Reason,
			}
		}
	}
	return artifacts.StopAdmissionMetadata{}
}

func slentoreVersion() string {
	if version != "" && version != "devel" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

func printRootUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore bench [options]")
	fmt.Fprintln(writer, "Run a closed-loop or open-loop OpenAI-compatible streaming benchmark.")
}

func printBenchUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore bench [--config path] [overrides]")
	fmt.Fprintln(writer, "Secrets are accepted only through --api-key-env.")
}

func phaseMetadata(result benchmark.PhaseResult) artifacts.PhaseMetadata {
	attempted := len(result.Completed)
	metadata := artifacts.PhaseMetadata{
		Phase:                result.Phase,
		Status:               result.Status,
		StartedAt:            result.StartedAt,
		CompletedAt:          result.CompletedAt,
		ElapsedNS:            result.ElapsedNS,
		Requested:            result.RequestedRequests,
		Attempted:            attempted,
		Completed:            attempted,
		Successful:           result.Outcomes.Succeeded,
		Failed:               result.Outcomes.Failed(),
		RequestedConcurrency: result.RequestedConcurrency,
		EffectiveWorkers:     result.WorkerCount,
		MaxObservedActive:    result.MaxObservedActive,
		Outcomes:             result.Outcomes,
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
			requestArtifacts = append(requestArtifacts, artifacts.RequestArtifact{
				Sequence:    completed.Sequence,
				Phase:       completed.Phase,
				Outcome:     completed.Outcome,
				Observation: completed.Result.Observation,
				Metrics:     metrics.Calculate(completed.Result.Observation),
			})
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
	arrivals = append(arrivals, result.Measurement.Arrivals...)
	return arrivals
}

func loadMetadata(resolved config.Config, plannedArrivals int) artifacts.LoadMetadata {
	if resolved.Benchmark.Mode == config.LoadModeOpenLoop {
		return artifacts.LoadMetadata{
			Mode: benchmark.LoadModeOpenLoop,
			OpenLoop: &artifacts.OpenLoopLoadMetadata{
				RequestRate:     resolved.Benchmark.OpenLoop.RequestRate,
				Duration:        resolved.Benchmark.OpenLoop.Duration.String(),
				MaxInFlight:     resolved.Benchmark.OpenLoop.MaxInFlight,
				PlannedArrivals: plannedArrivals,
			},
		}
	}
	return artifacts.LoadMetadata{
		Mode: benchmark.LoadModeClosedLoop,
		ClosedLoop: &artifacts.ClosedLoopLoadMetadata{
			RequestedConcurrency: resolved.Benchmark.Concurrency,
			RequestedRequests:    resolved.Benchmark.Requests,
		},
	}
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

func workloadMetadata(prepared workload.PreparedWorkload) artifacts.WorkloadMetadata {
	metadata := artifacts.WorkloadMetadata{
		Mode:         prepared.Mode,
		Output:       artifacts.WorkloadOutputMetadata{RequestedMaxTokens: prepared.RequestedOutputTokens},
		Builder:      prepared.Builder,
		PromptBytes:  prepared.PromptBytes,
		PromptSHA256: prepared.PromptSHA256,
		Tokenizer:    prepared.Tokenizer,
	}
	if prepared.Mode == workload.ModeTokenLength {
		metadata.Input = &artifacts.WorkloadInputMetadata{
			Contract:       prepared.InputContract,
			TargetTokens:   prepared.TargetInputTokens,
			ResolvedTokens: prepared.ResolvedInputTokens,
		}
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

func parseCLILoadMode(value string) (config.LoadMode, error) {
	switch value {
	case "closed-loop":
		return config.LoadModeClosedLoop, nil
	case "open-loop":
		return config.LoadModeOpenLoop, nil
	default:
		return "", fmt.Errorf("must be closed-loop or open-loop")
	}
}

func parseCLIWorkloadMode(value string) (config.WorkloadMode, error) {
	switch value {
	case "prompt":
		return config.WorkloadModePrompt, nil
	case "token-length":
		return config.WorkloadModeTokenLength, nil
	default:
		return "", fmt.Errorf("must be prompt or token-length")
	}
}

func parseCLITokenizerAdapter(value string) (config.TokenizerAdapter, error) {
	if value != "vllm" {
		return "", fmt.Errorf("must be vllm")
	}
	return config.TokenizerAdapterVLLM, nil
}

func parseCLITokenTimingMode(value string) (config.TokenTimingMode, error) {
	switch value {
	case "disabled":
		return config.TokenTimingDisabled, nil
	case "vllm":
		return config.TokenTimingVLLM, nil
	default:
		return "", fmt.Errorf("must be disabled or vllm")
	}
}

func printLifecycleSummary(writer io.Writer, runID string, lifecycle benchmark.LifecycleResult, prepared artifacts.WorkloadMetadata, tokenTiming artifacts.TokenTimingMetadata, load artifacts.LoadMetadata, warmup, measurement artifacts.PhaseMetadata, requestArtifacts []artifacts.RequestArtifact, artifactPath string, runErr error, runErrorClass string) {
	fmt.Fprintf(writer, "Run:                 %s\n", runID)
	fmt.Fprintf(writer, "Workload mode:       %s\n", prepared.Mode)
	fmt.Fprintf(writer, "Token timing:        %s\n", tokenTiming.Mode)
	if prepared.Input != nil && prepared.Tokenizer != nil {
		fmt.Fprintf(writer, "Input target:        %d tokens\n", prepared.Input.TargetTokens)
		fmt.Fprintf(writer, "Input resolved:      %d tokens\n", prepared.Input.ResolvedTokens)
		fmt.Fprintf(writer, "Input contract:      %s\n", prepared.Input.Contract)
		fmt.Fprintf(writer, "Tokenizer:           %s/%s\n", prepared.Tokenizer.Adapter, prepared.Tokenizer.Model)
	}
	fmt.Fprintf(writer, "Output max requested: %d tokens\n", prepared.Output.RequestedMaxTokens)
	fmt.Fprintf(writer, "Prompt bytes:        %d\n", prepared.PromptBytes)
	fmt.Fprintf(writer, "Prompt SHA256:       %s\n", prepared.PromptSHA256)
	fmt.Fprintf(writer, "Mode:                %s\n", lifecycle.LoadMode)
	if lifecycle.LoadMode == benchmark.LoadModeOpenLoop {
		counts := lifecycle.Measurement.ArrivalCounts
		fmt.Fprintf(writer, "Request rate:        %g/s\n", load.OpenLoop.RequestRate)
		fmt.Fprintf(writer, "Duration:            %s\n", load.OpenLoop.Duration)
		fmt.Fprintf(writer, "Planned arrivals:    %d\n", counts.Planned)
		fmt.Fprintf(writer, "Processed arrivals:  %d\n", counts.Processed)
		fmt.Fprintf(writer, "Started requests:    %d\n", counts.Started)
		fmt.Fprintf(writer, "Client-limited:      %d\n", counts.ClientLimited)
		fmt.Fprintf(writer, "Scheduler-limited:   %d\n", counts.SchedulerLimited)
		fmt.Fprintf(writer, "Maximum in flight:   %d\n", counts.MaxObservedInFlight)
	}
	fmt.Fprintf(writer, "Warmup requested:    %d\n", warmup.Requested)
	fmt.Fprintf(writer, "Warmup attempted:    %d\n", warmup.Attempted)
	fmt.Fprintf(writer, "Warmup successful:   %d\n", warmup.Successful)
	fmt.Fprintf(writer, "Warmup failed:       %d\n", warmup.Failed)
	fmt.Fprintf(writer, "Measured requested:  %d\n", measurement.Requested)
	fmt.Fprintf(writer, "Measured attempted:  %d\n", measurement.Attempted)
	fmt.Fprintf(writer, "Measured successful: %d\n", measurement.Successful)
	fmt.Fprintf(writer, "Measured failed:     %d\n", measurement.Failed)
	if lifecycle.LoadMode == benchmark.LoadModeClosedLoop {
		fmt.Fprintf(writer, "Concurrency:         %d requested\n", measurement.RequestedConcurrency)
		fmt.Fprintf(writer, "Warmup workers:      %d effective\n", warmup.EffectiveWorkers)
		fmt.Fprintf(writer, "Measured workers:    %d effective\n", measurement.EffectiveWorkers)
		fmt.Fprintf(writer, "Warmup max active:   %d requests\n", warmup.MaxObservedActive)
		fmt.Fprintf(writer, "Measured max active: %d requests\n", measurement.MaxObservedActive)
	}
	fmt.Fprintf(writer, "Drain timeout:       %s\n", lifecycle.Drain.Timeout)
	fmt.Fprintf(writer, "Drain timed out:     %t\n", lifecycle.Drain.TimedOut)
	fmt.Fprintf(writer, "Drain cancellations: %d requests\n", len(lifecycle.Drain.CancelledRequestIDs))
	fmt.Fprintf(writer, "Lifecycle elapsed:   %s\n", time.Duration(lifecycle.ElapsedNS))
	fmt.Fprintf(writer, "Measurement elapsed: %s\n", time.Duration(measurement.ElapsedNS))
	if runErr != nil {
		fmt.Fprintf(writer, "Run error:            %s\n", runErr)
	}
	if runErrorClass != "" {
		fmt.Fprintf(writer, "Run error class:      %s\n", runErrorClass)
	}
	if measurement.Requested == 1 && measurement.Successful == 1 {
		var measured *artifacts.RequestArtifact
		for index := range requestArtifacts {
			if requestArtifacts[index].Phase == benchmark.RequestPhaseMeasured && requestArtifacts[index].Outcome == benchmark.OutcomeSucceeded {
				measured = &requestArtifacts[index]
				break
			}
		}
		if measured != nil {
			fmt.Fprintln(writer)
			printRequestSummary(writer, measured.Observation, measured.Metrics)
		}
	} else if tokenTiming.Mode == "vllm" && measurement.Successful > 0 {
		available := 0
		for _, request := range requestArtifacts {
			if request.Phase == benchmark.RequestPhaseMeasured && request.Outcome == benchmark.OutcomeSucceeded && request.Metrics.ITL.Available {
				available++
			}
		}
		fmt.Fprintf(writer, "Measured requests with true ITL: %d / %d\n", available, measurement.Successful)
	}
	if artifactPath != "" {
		fmt.Fprintf(writer, "Artifacts:  %s\n", artifactPath)
	} else {
		fmt.Fprintln(writer, "Artifacts:  unavailable")
	}
}

func printRequestSummary(writer io.Writer, observation benchmark.RequestObservation, requestMetrics metrics.RequestMetrics) {
	fmt.Fprintf(writer, "Request:    %s\n", observation.RequestID)
	if observation.StatusCode == 0 {
		fmt.Fprintln(writer, "Status:     unavailable")
	} else {
		fmt.Fprintf(writer, "Status:     %d\n", observation.StatusCode)
	}
	if observation.Usage.Available {
		fmt.Fprintf(writer, "Input:      %d tokens\n", observation.Usage.InputTokens)
		fmt.Fprintf(writer, "Output:     %d tokens\n", observation.Usage.OutputTokens)
	} else {
		fmt.Fprintln(writer, "Input:      unavailable (server usage not supplied)")
		fmt.Fprintln(writer, "Output:     unavailable (server usage not supplied)")
	}
	fmt.Fprintln(writer)
	printScalar(writer, "Headers", requestMetrics.TimeToHeaders)
	printScalar(writer, "TTFB", requestMetrics.TTFB)
	printScalar(writer, "TTFT", requestMetrics.TTFT)
	printScalar(writer, "TTLT", requestMetrics.TTLT)
	printScalar(writer, "E2E", requestMetrics.E2E)
	printScalar(writer, "TPOT", requestMetrics.TPOT)
	if requestMetrics.InterChunkLatency.Available {
		fmt.Fprintf(writer, "ICL mean:   %.3f ms\n", requestMetrics.InterChunkLatency.MeanMS)
	} else {
		fmt.Fprintf(writer, "ICL mean:   unavailable (%s)\n", requestMetrics.InterChunkLatency.Reason)
	}
	if requestMetrics.ITL.Available {
		fmt.Fprintln(writer, "True ITL:   available")
		fmt.Fprintf(writer, "ITL source: %s\n", requestMetrics.ITL.Source)
		fmt.Fprintf(writer, "ITL mean:   %.3f ms\n", requestMetrics.ITL.MeanMS)
		fmt.Fprintf(writer, "ITL samples: %d\n", requestMetrics.ITL.Count)
	} else {
		fmt.Fprintf(writer, "True ITL:   unavailable (%s)\n", requestMetrics.ITL.Reason)
	}
	if observation.Error != "" {
		fmt.Fprintf(writer, "Error:      %s\n", observation.Error)
	}
}

func printScalar(writer io.Writer, label string, metric metrics.Scalar) {
	if metric.Available {
		fmt.Fprintf(writer, "%-12s%.3f %s\n", label+":", metric.Value, metric.Unit)
		return
	}
	fmt.Fprintf(writer, "%-12sunavailable (%s)\n", label+":", metric.Reason)
}
