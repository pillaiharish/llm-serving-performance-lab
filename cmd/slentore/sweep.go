package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
)

type runFlagValues struct {
	configPath, baseURL, model, prompt, timeoutText, outputDir, apiKeyEnv               string
	maxOutputTokens, concurrency, requests, maxConcurrency, maxRequests, warmupRequests int
	temperature, requestRate, maxRequestRateCeiling                                     float64
	drainTimeoutText, modeText, durationText                                            string
	maxInFlight, maxInFlightCeiling                                                     int
	workloadModeText, tokenizerAdapterText, tokenizerURL, tokenTimingText               string
	inputTokens, maxInputTokensCeiling, maxOutputTokensCeiling                          int
	sloTTFTText, sloTPOTText, sloE2EText                                                string
	concurrencyValues, requestRateValues, inputTokenValues, outputTokenValues           string
	maxSweepPoints                                                                      int
}

func runSweepContext(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	flags := flag.NewFlagSet("slentore sweep", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printSweepUsage(flags.Output()) }
	var values runFlagValues
	registerSweepFlags(flags, &values)
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
	resolved, err := resolveSweepConfig(values, visited)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	plan, err := experiment.BuildPlan(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "error: invalid experiment: %v\n", err)
		return 2
	}
	apiKey, err := config.ResolveAPIKey(resolved.Endpoint.APIKeyEnv, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	runner := experiment.NewRunner()
	result, err := runner.Run(ctx, experiment.RunRequest{
		Plan: plan, APIKey: apiKey, OutputRoot: resolved.Capture.OutputDir, SlentoreVersion: slentoreVersion(),
		ExperimentStarted: func(id string) {
			fmt.Fprintf(table, "Experiment:\t%s\n", id)
			printSweepHeader(table, resolved.Benchmark.Mode)
		},
		PointCompleted: func(record experiment.PointRecord, child *benchmarkexec.Result) {
			printSweepPoint(table, resolved.Benchmark.Mode, record, child)
		},
	})
	_ = table.Flush()
	if err != nil {
		fmt.Fprintf(stderr, "error: publish experiment: %v\n", err)
		return 1
	}
	printExperimentSummary(stdout, result)
	if !result.Successful() {
		return 1
	}
	return 0
}

func registerRunFlags(flags *flag.FlagSet, values *runFlagValues) {
	flags.StringVar(&values.configPath, "config", "", "path to a version 1 YAML configuration file")
	flags.StringVar(&values.baseURL, "base-url", "", "OpenAI-compatible API root, normally ending in /v1")
	flags.StringVar(&values.model, "model", "", "model identifier")
	flags.StringVar(&values.prompt, "prompt", "", "single user prompt (never persisted)")
	flags.IntVar(&values.maxOutputTokens, "max-output-tokens", 0, "maximum output tokens")
	flags.Float64Var(&values.temperature, "temperature", 0, "sampling temperature")
	flags.StringVar(&values.timeoutText, "timeout", "", "request timeout, such as 120s")
	flags.StringVar(&values.outputDir, "output-dir", "", "artifact output directory")
	flags.StringVar(&values.apiKeyEnv, "api-key-env", "", "environment variable containing the API key")
	flags.IntVar(&values.concurrency, "concurrency", 0, "simultaneously active requests")
	flags.IntVar(&values.requests, "requests", 0, "total requests to attempt")
	flags.IntVar(&values.maxConcurrency, "max-concurrency", 0, "client admission ceiling for concurrency")
	flags.IntVar(&values.maxRequests, "max-requests", 0, "client admission ceiling for total requests")
	flags.IntVar(&values.warmupRequests, "warmup-requests", 0, "warmup requests to attempt before measurement")
	flags.StringVar(&values.drainTimeoutText, "drain-timeout", "", "maximum drain duration after measured admission stops")
	flags.StringVar(&values.modeText, "mode", "", "load mode: closed-loop or open-loop")
	flags.Float64Var(&values.requestRate, "request-rate", 0, "open-loop offered arrivals per second")
	flags.StringVar(&values.durationText, "duration", "", "open-loop measurement duration, such as 30s")
	flags.IntVar(&values.maxInFlight, "max-in-flight", 0, "open-loop admitted request bound")
	flags.Float64Var(&values.maxRequestRateCeiling, "max-request-rate-ceiling", 0, "open-loop request-rate safety ceiling")
	flags.IntVar(&values.maxInFlightCeiling, "max-in-flight-ceiling", 0, "open-loop in-flight safety ceiling")
	flags.StringVar(&values.workloadModeText, "workload-mode", "", "workload mode: prompt or token-length")
	flags.IntVar(&values.inputTokens, "input-tokens", 0, "target rendered chat input tokens")
	flags.StringVar(&values.tokenizerAdapterText, "tokenizer-adapter", "", "tokenizer adapter: vllm")
	flags.StringVar(&values.tokenizerURL, "tokenizer-url", "", "exact vLLM /tokenize endpoint")
	flags.IntVar(&values.maxInputTokensCeiling, "max-input-tokens-ceiling", 0, "client safety ceiling for target input tokens")
	flags.IntVar(&values.maxOutputTokensCeiling, "max-output-tokens-ceiling", 0, "client safety ceiling for requested output tokens")
	flags.StringVar(&values.tokenTimingText, "token-timing", "", "token timing evidence: disabled or vllm")
	flags.StringVar(&values.sloTTFTText, "slo-ttft", "", "maximum successful-request TTFT, such as 800ms")
	flags.StringVar(&values.sloTPOTText, "slo-tpot", "", "maximum successful-request TPOT, such as 30ms")
	flags.StringVar(&values.sloE2EText, "slo-e2e", "", "maximum successful-request E2E latency, such as 5s")
}

func registerSweepFlags(flags *flag.FlagSet, values *runFlagValues) {
	registerRunFlags(flags, values)
	flags.StringVar(&values.concurrencyValues, "concurrency-values", "", "ordered comma-separated closed-loop concurrency values")
	flags.StringVar(&values.requestRateValues, "request-rate-values", "", "ordered comma-separated open-loop request rates")
	flags.StringVar(&values.inputTokenValues, "input-token-values", "", "ordered comma-separated token-length input values")
	flags.StringVar(&values.outputTokenValues, "output-token-values", "", "ordered comma-separated requested output maxima")
	flags.IntVar(&values.maxSweepPoints, "max-sweep-points", 0, "maximum admitted Cartesian sweep points")
}

func resolveRunConfig(values runFlagValues, visited map[string]bool) (config.Config, error) {
	resolved, err := config.Load(values.configPath)
	if err != nil {
		return config.Config{}, err
	}
	overrides := config.Overrides{}
	if visited["base-url"] {
		overrides.BaseURL = &values.baseURL
	}
	if visited["model"] {
		overrides.Model = &values.model
	}
	if visited["prompt"] {
		overrides.Prompt = &values.prompt
	}
	if visited["max-output-tokens"] {
		overrides.MaxOutputTokens = &values.maxOutputTokens
	}
	if visited["temperature"] {
		overrides.Temperature = &values.temperature
	}
	if visited["output-dir"] {
		overrides.OutputDir = &values.outputDir
	}
	if visited["api-key-env"] {
		overrides.APIKeyEnv = &values.apiKeyEnv
	}
	if visited["concurrency"] {
		overrides.Concurrency = &values.concurrency
	}
	if visited["requests"] {
		overrides.Requests = &values.requests
	}
	if visited["max-concurrency"] {
		overrides.MaxConcurrency = &values.maxConcurrency
	}
	if visited["max-requests"] {
		overrides.MaxRequests = &values.maxRequests
	}
	if visited["warmup-requests"] {
		overrides.WarmupRequests = &values.warmupRequests
	}
	if visited["request-rate"] {
		overrides.RequestRate = &values.requestRate
	}
	if visited["max-in-flight"] {
		overrides.MaxInFlight = &values.maxInFlight
	}
	if visited["max-request-rate-ceiling"] {
		overrides.MaxRequestRate = &values.maxRequestRateCeiling
	}
	if visited["max-in-flight-ceiling"] {
		overrides.MaxInFlightCeiling = &values.maxInFlightCeiling
	}
	if visited["input-tokens"] {
		overrides.InputTokens = &values.inputTokens
	}
	if visited["tokenizer-url"] {
		overrides.TokenizerURL = &values.tokenizerURL
	}
	if visited["max-input-tokens-ceiling"] {
		overrides.MaxInputTokens = &values.maxInputTokensCeiling
	}
	if visited["max-output-tokens-ceiling"] {
		overrides.MaxOutputTokensCeiling = &values.maxOutputTokensCeiling
	}

	if visited["timeout"] {
		value, parseErr := time.ParseDuration(values.timeoutText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--timeout: %w", parseErr)
		}
		overrides.Timeout = &value
	}
	if visited["drain-timeout"] {
		value, parseErr := time.ParseDuration(values.drainTimeoutText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--drain-timeout: %w", parseErr)
		}
		overrides.DrainTimeout = &value
	}
	if visited["duration"] {
		value, parseErr := time.ParseDuration(values.durationText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--duration: %w", parseErr)
		}
		overrides.Duration = &value
	}
	if visited["mode"] {
		value, parseErr := parseCLILoadMode(values.modeText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--mode: %w", parseErr)
		}
		overrides.Mode = &value
	}
	if visited["workload-mode"] {
		value, parseErr := parseCLIWorkloadMode(values.workloadModeText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--workload-mode: %w", parseErr)
		}
		overrides.WorkloadMode = &value
	}
	if visited["tokenizer-adapter"] {
		value, parseErr := parseCLITokenizerAdapter(values.tokenizerAdapterText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--tokenizer-adapter: %w", parseErr)
		}
		overrides.TokenizerAdapter = &value
	}
	if visited["token-timing"] {
		value, parseErr := parseCLITokenTimingMode(values.tokenTimingText)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--token-timing: %w", parseErr)
		}
		overrides.TokenTimingMode = &value
	}
	for _, item := range []struct {
		name, text string
		target     **time.Duration
	}{
		{name: "slo-ttft", text: values.sloTTFTText, target: &overrides.SLOTTFT},
		{name: "slo-tpot", text: values.sloTPOTText, target: &overrides.SLOTPOT},
		{name: "slo-e2e", text: values.sloE2EText, target: &overrides.SLOE2E},
	} {
		if !visited[item.name] {
			continue
		}
		value, parseErr := time.ParseDuration(item.text)
		if parseErr != nil {
			return config.Config{}, fmt.Errorf("--%s: %w", item.name, parseErr)
		}
		*item.target = &value
	}
	resolved.ApplyOverrides(overrides)
	return resolved, nil
}

func resolveSweepConfig(values runFlagValues, visited map[string]bool) (config.Config, error) {
	resolved, err := resolveRunConfig(values, visited)
	if err != nil {
		return config.Config{}, err
	}

	if visited["concurrency-values"] {
		resolved.Experiment.ConcurrencyValues, err = parseIntValues(values.concurrencyValues, "--concurrency-values")
		if err != nil {
			return config.Config{}, err
		}
	}
	if visited["request-rate-values"] {
		resolved.Experiment.RequestRateValues, err = parseFloatValues(values.requestRateValues, "--request-rate-values")
		if err != nil {
			return config.Config{}, err
		}
	}
	if visited["input-token-values"] {
		resolved.Experiment.InputTokenValues, err = parseIntValues(values.inputTokenValues, "--input-token-values")
		if err != nil {
			return config.Config{}, err
		}
	}
	if visited["output-token-values"] {
		resolved.Experiment.OutputTokenValues, err = parseIntValues(values.outputTokenValues, "--output-token-values")
		if err != nil {
			return config.Config{}, err
		}
	}
	if visited["max-sweep-points"] {
		resolved.Experiment.Safety.MaxPoints = values.maxSweepPoints
	}
	for _, conflict := range []struct {
		scalar, axis string
		active       bool
	}{
		{scalar: "concurrency", axis: "concurrency-values", active: len(resolved.Experiment.ConcurrencyValues) > 0},
		{scalar: "request-rate", axis: "request-rate-values", active: len(resolved.Experiment.RequestRateValues) > 0},
		{scalar: "input-tokens", axis: "input-token-values", active: len(resolved.Experiment.InputTokenValues) > 0},
		{scalar: "max-output-tokens", axis: "output-token-values", active: len(resolved.Experiment.OutputTokenValues) > 0},
	} {
		if conflict.active && visited[conflict.scalar] {
			return config.Config{}, fmt.Errorf("--%s conflicts with active --%s/experiment axis", conflict.scalar, conflict.axis)
		}
	}
	return resolved, nil
}

func parseIntValues(raw, flagName string) ([]int, error) {
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%s contains an empty value", flagName)
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%s value %q: %w", flagName, part, err)
		}
		values = append(values, value)
	}
	return values, nil
}

func parseFloatValues(raw, flagName string) ([]float64, error) {
	parts := strings.Split(raw, ",")
	values := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%s contains an empty value", flagName)
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q: %w", flagName, part, err)
		}
		values = append(values, value)
	}
	return values, nil
}

func printSweepUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore sweep [--config path] --<axis>-values list [overrides]")
	fmt.Fprintln(writer, "Execute explicit sweep points sequentially as independent benchmark runs.")
}

func printSweepHeader(writer io.Writer, mode config.LoadMode) {
	if mode == config.LoadModeOpenLoop {
		fmt.Fprintln(writer, "Point\tRate\tInput\tOutput\tStatus\tDelivery\tStart/s\tTTFT p95\tSchedLag p95")
		return
	}
	fmt.Fprintln(writer, "Point\tC\tInput\tOutput\tStatus\tTTFT p95\tTPOT p95\tReq/s\tGoodput")
}

func printSweepPoint(writer io.Writer, mode config.LoadMode, record experiment.PointRecord, child *benchmarkexec.Result) {
	status := record.PointStatus
	if child != nil {
		status = child.Summary.RunStatus
	}
	point := strings.TrimPrefix(record.PointID, "point-")
	input := optionalCLIInt(record.Parameters.InputTokens)
	if mode == config.LoadModeOpenLoop {
		delivery, startRate, ttft, lag := "-", "-", "-", "-"
		if child != nil {
			if child.Summary.Load.OpenLoop != nil {
				delivery = formatRatio(child.Summary.Load.OpenLoop.DeliveryRatio)
				startRate = formatRate(child.Summary.Load.OpenLoop.ActualStartRate)
			}
			ttft = formatDistributionP95(child.Summary.Latency.TTFT)
			lag = formatDistributionP95(child.Summary.SchedulerLag)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", point, optionalCLIFloat(record.Parameters.RequestRate), input, record.Parameters.RequestedOutputTokens, status, delivery, startRate, ttft, lag)
		return
	}
	ttft, tpot, requestRate, goodput := "-", "-", "-", "-"
	if child != nil {
		ttft = formatDistributionP95(child.Summary.Latency.TTFT)
		tpot = formatDistributionP95(child.Summary.Latency.TPOT)
		requestRate = formatRate(child.Summary.RequestRates.SuccessfulRequestThroughput)
		goodput = formatRate(child.Summary.SLO.Goodput)
	}
	fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", point, optionalCLIInt(record.Parameters.Concurrency), input, record.Parameters.RequestedOutputTokens, status, ttft, tpot, requestRate, goodput)
}

func printExperimentSummary(writer io.Writer, result experiment.Result) {
	manifest := result.Manifest
	fmt.Fprintf(writer, "Experiment status:           %s\n", manifest.Status)
	fmt.Fprintf(writer, "Points executed/planned:     %d / %d\n", manifest.ExecutedPoints, manifest.PlannedPoints)
	fmt.Fprintf(writer, "Child completed/failed:      %d / %d\n", manifest.ChildRunsCompleted, manifest.ChildRunsFailed)
	fmt.Fprintf(writer, "Artifacts:                   %s\n", result.ArtifactPath)
}

func optionalCLIInt(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}
func optionalCLIFloat(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}
func formatDistributionP95(value aggregate.Distribution) string {
	if !value.Available {
		return "-"
	}
	return strconv.FormatFloat(value.P95, 'f', 3, 64)
}
func formatRate(value aggregate.Rate) string {
	if !value.Available {
		return "-"
	}
	return strconv.FormatFloat(value.Value, 'f', 3, 64)
}
func formatRatio(value aggregate.Ratio) string {
	if !value.Available {
		return "-"
	}
	return strconv.FormatFloat(value.Value, 'f', 3, 64)
}
