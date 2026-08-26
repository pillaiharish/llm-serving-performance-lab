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
	if args[0] == "sweep" {
		return runSweepContext(ctx, args[1:], stdout, stderr, lookupEnv)
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
	var values runFlagValues
	registerRunFlags(flags, &values)
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
	visited := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	resolved, err := resolveRunConfig(values, visited)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if _, err := benchmarkexec.ValidateStatic(resolved); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
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
	fmt.Fprintln(writer, "Usage: slentore <bench|sweep> [options]")
	fmt.Fprintln(writer, "Run one benchmark or a sequential deterministic experiment sweep.")
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
