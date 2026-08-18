package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
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
	resolved.ApplyOverrides(overrides)
	if err := resolved.Validate(); err != nil {
		fmt.Fprintf(stderr, "error: invalid configuration: %v\n", err)
		return 2
	}

	apiKey, err := config.ResolveAPIKey(resolved.Endpoint.APIKeyEnv, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	diagnostics := benchmark.CollectClientDiagnostics()
	admission, err := benchmark.AdmitRun(benchmark.AdmissionRequest{
		Concurrency:    resolved.Benchmark.Concurrency,
		Requests:       resolved.Benchmark.Requests,
		MaxConcurrency: resolved.Benchmark.Safety.MaxConcurrency,
		MaxRequests:    resolved.Benchmark.Safety.MaxRequests,
	}, diagnostics)
	if err != nil {
		fmt.Fprintf(stderr, "error: client admission rejected: %v\n", err)
		return 2
	}
	runID, err := benchmark.NewRunID()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	httpClient, transport, err := newSharedHTTPClient(admission.WorkerCount)
	if err != nil {
		fmt.Fprintf(stderr, "error: create shared HTTP client: %v\n", err)
		return 2
	}
	defer transport.CloseIdleConnections()

	client, err := openai.NewClient(httpClient, resolved.Endpoint.BaseURL, apiKey)
	if err != nil {
		fmt.Fprintf(stderr, "error: create OpenAI client: %v\n", err)
		return 2
	}
	runner := benchmark.NewRunner(client)
	requestTemplate := benchmark.Request{
		RunID:           runID,
		Model:           resolved.Endpoint.Model,
		Prompt:          resolved.Request.Prompt,
		MaxOutputTokens: resolved.Request.MaxOutputTokens,
		Temperature:     resolved.Request.Temperature,
	}

	createdAt := time.Now().UTC()
	coordinator := benchmark.NewRunCoordinator(runner)
	runResult, runErr := coordinator.Run(ctx, benchmark.RunPlan{
		RunID:           runID,
		RequestTemplate: requestTemplate,
		Concurrency:     resolved.Benchmark.Concurrency,
		Requests:        resolved.Benchmark.Requests,
		RequestTimeout:  resolved.Runtime.Timeout,
	})
	if runResult.StartedAt.IsZero() {
		fmt.Fprintf(stderr, "error: coordinate benchmark run: %v\n", runErr)
		return 1
	}

	requestArtifacts := make([]artifacts.RequestArtifact, 0, len(runResult.Completed))
	counts := artifacts.RequestCounts{
		Requested: resolved.Benchmark.Requests,
		Attempted: len(runResult.Completed),
		Completed: len(runResult.Completed),
	}
	for _, completed := range runResult.Completed {
		if completed.Result.Err == nil {
			counts.Successful++
		} else {
			counts.Failed++
		}
		requestArtifacts = append(requestArtifacts, artifacts.RequestArtifact{
			Sequence:    completed.Sequence,
			Observation: completed.Result.Observation,
			Metrics:     metrics.Calculate(completed.Result.Observation),
		})
	}

	runStatus := artifacts.RunStatusCompleted
	runError := ""
	if runErr != nil {
		runStatus = artifacts.RunStatusFailed
		if errors.Is(runErr, context.Canceled) {
			runStatus = artifacts.RunStatusCancelled
		}
		runError = runErr.Error()
	} else if counts.Failed > 0 {
		runStatus = artifacts.RunStatusFailed
		runError = fmt.Sprintf("%d of %d attempted requests failed", counts.Failed, counts.Attempted)
	} else if counts.Attempted != counts.Requested {
		runStatus = artifacts.RunStatusFailed
		runError = fmt.Sprintf("attempted %d of %d requested requests", counts.Attempted, counts.Requested)
	}
	promptHash := sha256.Sum256([]byte(resolved.Request.Prompt))
	metadata := artifacts.RunMetadata{
		SchemaVersion:            artifacts.SchemaVersion,
		RunID:                    runID,
		SlentoreVersion:          slentoreVersion(),
		CreatedAt:                createdAt,
		RunStartedAt:             runResult.StartedAt,
		RunCompletedAt:           runResult.CompletedAt,
		RunElapsedNS:             runResult.ElapsedNS,
		RunStatus:                runStatus,
		Error:                    runError,
		Model:                    resolved.Endpoint.Model,
		BaseURL:                  resolved.Endpoint.BaseURL,
		RequestedMaxOutputTokens: resolved.Request.MaxOutputTokens,
		Temperature:              resolved.Request.Temperature,
		Timeout:                  resolved.Runtime.Timeout.String(),
		PromptBytes:              len([]byte(resolved.Request.Prompt)),
		PromptSHA256:             hex.EncodeToString(promptHash[:]),
		RequestedConcurrency:     resolved.Benchmark.Concurrency,
		EffectiveWorkers:         admission.WorkerCount,
		MaxObservedActive:        runResult.MaxObservedActive,
		SafetyLimits: artifacts.SafetyLimits{
			MaxConcurrency: resolved.Benchmark.Safety.MaxConcurrency,
			MaxRequests:    resolved.Benchmark.Safety.MaxRequests,
		},
		RequestCounts:     counts,
		ClientDiagnostics: admission.Diagnostics,
	}

	artifactPath, artifactErr := artifacts.NewWriter(resolved.Capture.OutputDir).Write(metadata, requestArtifacts)
	printRunSummary(stdout, runID, runResult, counts, requestArtifacts, artifactPath, runErr)
	if artifactErr != nil {
		fmt.Fprintf(stderr, "error: write artifacts: %v\n", artifactErr)
		return 1
	}
	if runErr != nil || counts.Failed > 0 || counts.Attempted != counts.Requested {
		return 1
	}
	return 0
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
	fmt.Fprintln(writer, "Run a closed-loop OpenAI-compatible streaming benchmark.")
}

func printBenchUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore bench [--config path] [overrides]")
	fmt.Fprintln(writer, "Secrets are accepted only through --api-key-env.")
}

func printRunSummary(writer io.Writer, runID string, runResult benchmark.RunResult, counts artifacts.RequestCounts, requestArtifacts []artifacts.RequestArtifact, artifactPath string, runErr error) {
	fmt.Fprintf(writer, "Run:        %s\n", runID)
	fmt.Fprintf(writer, "Requested:  %d requests\n", counts.Requested)
	fmt.Fprintf(writer, "Attempted:  %d requests\n", counts.Attempted)
	fmt.Fprintf(writer, "Completed:  %d requests\n", counts.Completed)
	fmt.Fprintf(writer, "Successful: %d requests\n", counts.Successful)
	fmt.Fprintf(writer, "Failed:     %d requests\n", counts.Failed)
	fmt.Fprintf(writer, "Concurrency: %d requested\n", runResult.RequestedConcurrency)
	fmt.Fprintf(writer, "Workers:    %d effective\n", runResult.WorkerCount)
	fmt.Fprintf(writer, "Max active: %d requests\n", runResult.MaxObservedActive)
	fmt.Fprintf(writer, "Run elapsed: %s\n", time.Duration(runResult.ElapsedNS))
	if runErr != nil {
		fmt.Fprintf(writer, "Run error:  %s\n", runErr)
	}
	if counts.Requested == 1 && len(requestArtifacts) == 1 {
		fmt.Fprintln(writer)
		printRequestSummary(writer, requestArtifacts[0].Observation, requestArtifacts[0].Metrics)
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
	fmt.Fprintf(writer, "ITL:        unavailable (%s)\n", requestMetrics.ITL.Reason)
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
