package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
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
	var sloTTFTText string
	var sloTPOTText string
	var sloE2EText string

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
	flags.StringVar(&sloTTFTText, "slo-ttft", "", "maximum successful-request TTFT, such as 800ms")
	flags.StringVar(&sloTPOTText, "slo-tpot", "", "maximum successful-request TPOT, such as 30ms")
	flags.StringVar(&sloE2EText, "slo-e2e", "", "maximum successful-request E2E latency, such as 5s")

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
	for _, sloFlag := range []struct {
		name   string
		text   string
		target **time.Duration
	}{
		{name: "slo-ttft", text: sloTTFTText, target: &overrides.SLOTTFT},
		{name: "slo-tpot", text: sloTPOTText, target: &overrides.SLOTPOT},
		{name: "slo-e2e", text: sloE2EText, target: &overrides.SLOE2E},
	} {
		if !visited[sloFlag.name] {
			continue
		}
		duration, parseErr := time.ParseDuration(sloFlag.text)
		if parseErr != nil {
			fmt.Fprintf(stderr, "error: --%s: %v\n", sloFlag.name, parseErr)
			return 2
		}
		*sloFlag.target = &duration
	}
	resolved.ApplyOverrides(overrides)
	apiKey, err := config.ResolveAPIKey(resolved.Endpoint.APIKeyEnv, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	result, err := benchmarkexec.Execute(ctx, benchmarkexec.Request{
		Config: resolved, APIKey: apiKey, ArtifactRoot: resolved.Capture.OutputDir, SlentoreVersion: slentoreVersion(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if benchmarkexec.StageOf(err) == benchmarkexec.FailureStatic {
			return 2
		}
		return 1
	}
	printLifecycleSummary(stdout, result.Metadata, result.Summary, result.RequestArtifacts, result.ArtifactPath)
	if !result.Successful() {
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
	fmt.Fprintln(writer, "Run a closed-loop or open-loop OpenAI-compatible streaming benchmark.")
}

func printBenchUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore bench [--config path] [overrides]")
	fmt.Fprintln(writer, "Secrets are accepted only through --api-key-env.")
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

func printLifecycleSummary(writer io.Writer, metadata artifacts.RunMetadata, summary aggregate.RunSummary, requestArtifacts []artifacts.RequestArtifact, artifactPath string) {
	fmt.Fprintf(writer, "Run:                         %s\n", summary.RunID)
	fmt.Fprintf(writer, "Run status:                  %s\n", summary.RunStatus)
	if summary.ErrorClass != "" {
		fmt.Fprintf(writer, "Run error class:             %s\n", summary.ErrorClass)
	}
	fmt.Fprintf(writer, "Summary complete:            %t\n", summary.Complete)
	fmt.Fprintf(writer, "Mode:                        %s\n", summary.Load.Mode)
	fmt.Fprintf(writer, "Workload mode:               %s\n", summary.Workload.Mode)
	fmt.Fprintf(writer, "Token timing:                %s\n", metadata.TokenTiming.Mode)
	if summary.Workload.InputTargetTokens != nil {
		fmt.Fprintf(writer, "Input target/resolved:       %d / %d tokens\n", *summary.Workload.InputTargetTokens, *summary.Workload.InputResolvedTokens)
	}
	fmt.Fprintf(writer, "Output max requested:        %d tokens\n", summary.Workload.RequestedOutputMaxTokens)
	fmt.Fprintf(writer, "Warmup successful:           %d / %d\n", metadata.Warmup.Successful, metadata.Warmup.Attempted)
	fmt.Fprintf(writer, "Measured successful:         %d / %d\n", summary.Counts.Successful, summary.Counts.Started)
	fmt.Fprintf(writer, "Measured failed:             %d (request=%d timeout=%d parent=%d drain=%d)\n", summary.Counts.Failed, summary.Counts.RequestError, summary.Counts.RequestTimeout, summary.Counts.ParentCancelled, summary.Counts.DrainTimeout)
	fmt.Fprintf(writer, "Drain timed out:             %t\n", metadata.Drain.TimedOut)
	fmt.Fprintf(writer, "Drain cancellations:         %d requests\n", metadata.Drain.CancelledRequests)
	printRatio(writer, "Request success rate", summary.RequestRates.SuccessRate)
	printDistributionTriplet(writer, "TTFT p50/p95/p99", summary.Latency.TTFT)
	printDistributionTriplet(writer, "TPOT p50/p95/p99", summary.Latency.TPOT)
	printDistributionTriplet(writer, "E2E p50/p95/p99", summary.Latency.E2E)
	printRate(writer, "Successful req throughput", summary.RequestRates.SuccessfulRequestThroughput)
	printRate(writer, "Output token throughput", summary.Tokens.Throughput.Output)
	fmt.Fprintf(writer, "True ITL requests:           %d / %d\n", summary.ITL.AvailableRequests, summary.ITL.EligibleSuccessfulRequests)
	printDistributionTriplet(writer, "ITL p50/p95/p99", summary.ITL.Intervals)
	if summary.Load.OpenLoop != nil {
		open := summary.Load.OpenLoop
		fmt.Fprintf(writer, "Offered rate:                %g requests/s\n", open.ConfiguredRequestRate)
		fmt.Fprintf(writer, "Planned/started arrivals:    %d / %d\n", open.PlannedArrivals, open.StartedArrivals)
		printRate(writer, "Actual start rate", open.ActualStartRate)
		printRatio(writer, "Delivery ratio", open.DeliveryRatio)
		fmt.Fprintf(writer, "Client-limited:              %d\n", open.ClientLimited)
		fmt.Fprintf(writer, "Scheduler-limited:           %d\n", open.SchedulerLimited)
		printDistributionValue(writer, "Scheduler lag p95", summary.SchedulerLag, summary.SchedulerLag.P95)
	}
	printSLOSummary(writer, summary.SLO)
	if metadata.Error != "" {
		fmt.Fprintf(writer, "Run error:                    %s\n", metadata.Error)
	}
	if summary.Counts.RequestedOrPlanned == 1 && summary.Counts.Successful == 1 {
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
	}
	if artifactPath != "" {
		fmt.Fprintf(writer, "Artifacts:                    %s\n", artifactPath)
	} else {
		fmt.Fprintln(writer, "Artifacts:                    unavailable")
	}
}

func printDistributionTriplet(writer io.Writer, label string, value aggregate.Distribution) {
	if !value.Available {
		fmt.Fprintf(writer, "%-29s unavailable (%s)\n", label+":", value.Reason)
		return
	}
	fmt.Fprintf(writer, "%-29s %.3f / %.3f / %.3f %s (n=%d)\n", label+":", value.P50, value.P95, value.P99, value.Unit, value.SampleCount)
}

func printDistributionValue(writer io.Writer, label string, distribution aggregate.Distribution, value float64) {
	if !distribution.Available {
		fmt.Fprintf(writer, "%-29s unavailable (%s)\n", label+":", distribution.Reason)
		return
	}
	fmt.Fprintf(writer, "%-29s %.3f %s\n", label+":", value, distribution.Unit)
}

func printRatio(writer io.Writer, label string, value aggregate.Ratio) {
	if !value.Available {
		fmt.Fprintf(writer, "%-29s unavailable (%s)\n", label+":", value.Reason)
		return
	}
	fmt.Fprintf(writer, "%-29s %.1f%% (%d/%d)\n", label+":", value.Value*100, value.Numerator, value.Denominator)
}

func printRate(writer io.Writer, label string, value aggregate.Rate) {
	if !value.Available {
		fmt.Fprintf(writer, "%-29s unavailable (%s)\n", label+":", value.Reason)
		return
	}
	fmt.Fprintf(writer, "%-29s %.3f %s\n", label+":", value.Value, value.Unit)
}

func printSLOSummary(writer io.Writer, summary aggregate.SLOSummary) {
	if summary.TTFT != nil {
		fmt.Fprintf(writer, "TTFT SLO:                    <= %.3f ms\n", summary.TTFT.ThresholdMS)
	}
	if summary.TPOT != nil {
		fmt.Fprintf(writer, "TPOT SLO:                    <= %.3f ms/token\n", summary.TPOT.ThresholdMS)
	}
	if summary.E2E != nil {
		fmt.Fprintf(writer, "E2E SLO:                     <= %.3f ms\n", summary.E2E.ThresholdMS)
	}
	if summary.Configured {
		fmt.Fprintf(writer, "SLO good/evaluable:          %d / %d\n", summary.GoodRequests, summary.EvaluableRequests)
	}
	printRate(writer, "Goodput", summary.Goodput)
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
