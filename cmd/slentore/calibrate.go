package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/calibration"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
)

const calibrationPrompt = "Slentore client calibration."

func runCalibrationContext(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	flags := flag.NewFlagSet("slentore calibrate-client", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printCalibrationUsage(flags.Output()) }
	var values runFlagValues
	var maxSchedulerLagText string
	registerCalibrationFlags(flags, &values, &maxSchedulerLagText)
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
	resolved, err := resolveCalibrationConfig(values, visited)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	var maxSchedulerLag *time.Duration
	if visited["max-scheduler-lag-p95"] {
		parsed, parseErr := time.ParseDuration(maxSchedulerLagText)
		if parseErr != nil {
			fmt.Fprintf(stderr, "error: --max-scheduler-lag-p95: %v\n", parseErr)
			return 2
		}
		maxSchedulerLag = &parsed
	}
	plan, err := experiment.BuildPlan(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "error: invalid calibration experiment: %v\n", err)
		return 2
	}
	if err := calibration.ValidatePlan(plan, maxSchedulerLag); err != nil {
		fmt.Fprintf(stderr, "error: invalid client calibration: %v\n", err)
		return 2
	}
	apiKey, err := config.ResolveAPIKey(resolved.Endpoint.APIKeyEnv, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	runner := calibration.NewRunner()
	result, err := runner.Run(ctx, calibration.RunRequest{
		Plan: plan, APIKey: apiKey, OutputRoot: resolved.Capture.OutputDir, SlentoreVersion: slentoreVersion(), MaxSchedulerLagP95: maxSchedulerLag,
		CalibrationStarted: func(id string) { fmt.Fprintf(table, "Calibration:\t%s\n", id) },
		ExperimentStarted:  func(id string) { fmt.Fprintf(table, "Experiment:\t%s\n", id) },
		PointCompleted: func(point calibration.Point) {
			value := "-"
			if point.RequestedConcurrency != nil {
				value = fmt.Sprintf("%d", *point.RequestedConcurrency)
			} else if point.ConfiguredRequestRate != nil {
				value = fmt.Sprintf("%g", *point.ConfiguredRequestRate)
			}
			clean := "unevaluated"
			if point.DeliveryClean != nil {
				clean = fmt.Sprintf("%t", *point.DeliveryClean)
			}
			fmt.Fprintf(table, "Point %d:\tload=%s\tstatus=%s\tclean=%s\treasons=%s\n", point.PointIndex, value, point.PointStatus, clean, strings.Join(point.DeliveryReasons, ","))
		},
	})
	_ = table.Flush()
	if err != nil {
		fmt.Fprintf(stderr, "error: client calibration: %v\n", err)
		if result.ExperimentPath != "" {
			fmt.Fprintf(stderr, "Experiment artifacts remain available: %s\n", result.ExperimentPath)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Calibration status:          %s\n", result.Manifest.Status)
	fmt.Fprintf(stdout, "Points clean/evaluated:      %d / %d\n", result.Manifest.CleanPoints, result.Manifest.EvaluatedPoints)
	fmt.Fprintf(stdout, "Points unevaluated:          %d\n", result.Manifest.UnevaluatedPoints)
	fmt.Fprintf(stdout, "Experiment artifacts:        %s\n", result.ExperimentPath)
	fmt.Fprintf(stdout, "Calibration artifacts:       %s\n", result.ArtifactPath)
	if !result.Successful() {
		return 1
	}
	return 0
}

func registerCalibrationFlags(flags *flag.FlagSet, values *runFlagValues, maxSchedulerLagText *string) {
	flags.StringVar(&values.configPath, "config", "", "path to a version 1 YAML configuration file")
	flags.StringVar(&values.baseURL, "base-url", "", "OpenAI-compatible API root, normally ending in /v1")
	flags.StringVar(&values.model, "model", "", "model identifier")
	flags.StringVar(&values.apiKeyEnv, "api-key-env", "", "environment variable containing the API key")
	flags.StringVar(&values.modeText, "mode", "", "load mode: closed-loop or open-loop")
	flags.StringVar(&values.concurrencyValues, "concurrency-values", "", "ordered comma-separated closed-loop concurrency values")
	flags.StringVar(&values.requestRateValues, "request-rate-values", "", "ordered comma-separated open-loop request rates")
	flags.IntVar(&values.requests, "requests", 0, "closed-loop measured requests per point")
	flags.StringVar(&values.durationText, "duration", "", "open-loop measurement duration, such as 2s")
	flags.IntVar(&values.maxInFlight, "max-in-flight", 0, "open-loop admitted request bound")
	flags.IntVar(&values.warmupRequests, "warmup-requests", 0, "warmup requests per point")
	flags.StringVar(&values.timeoutText, "timeout", "", "request timeout, such as 120s")
	flags.StringVar(&values.drainTimeoutText, "drain-timeout", "", "maximum drain duration")
	flags.StringVar(&values.outputDir, "output-dir", "", "artifact output directory")
	flags.IntVar(&values.maxConcurrency, "max-concurrency", 0, "client admission ceiling for concurrency")
	flags.IntVar(&values.maxRequests, "max-requests", 0, "client admission ceiling for total requests")
	flags.Float64Var(&values.maxRequestRateCeiling, "max-request-rate-ceiling", 0, "open-loop request-rate safety ceiling")
	flags.IntVar(&values.maxInFlightCeiling, "max-in-flight-ceiling", 0, "open-loop in-flight safety ceiling")
	flags.IntVar(&values.maxSweepPoints, "max-sweep-points", 0, "maximum admitted calibration points")
	flags.StringVar(maxSchedulerLagText, "max-scheduler-lag-p95", "", "optional inclusive open-loop scheduler-lag p95 budget")
}

func resolveCalibrationConfig(values runFlagValues, visited map[string]bool) (config.Config, error) {
	resolved, err := resolveSweepConfig(values, visited)
	if err != nil {
		return config.Config{}, err
	}
	if resolved.Workload.Mode != config.WorkloadModePrompt {
		return config.Config{}, fmt.Errorf("client calibration requires workload.mode prompt")
	}
	if len(resolved.Experiment.InputTokenValues) != 0 || len(resolved.Experiment.OutputTokenValues) != 0 {
		return config.Config{}, fmt.Errorf("client calibration forbids input/output token axes")
	}
	resolved.Request.Prompt = calibrationPrompt
	resolved.Request.MaxOutputTokens = 1
	resolved.Request.Temperature = 0
	resolved.Benchmark.TokenTiming.Mode = config.TokenTimingDisabled
	resolved.Benchmark.SLO = config.SLO{}
	return resolved, nil
}

func printCalibrationUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore calibrate-client [--config path] --mode <mode> --<load-axis>-values list [options]")
	fmt.Fprintln(writer, "Measure delivery fidelity for explicitly tested client loads; no capacity limit is inferred.")
}
