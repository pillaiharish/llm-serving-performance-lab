package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/openai"
)

var version = "devel"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
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
	return runBench(args[1:], stdout, stderr, lookupEnv)
}

func runBench(args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
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

	flags.StringVar(&configPath, "config", "", "path to a version 1 YAML configuration file")
	flags.StringVar(&baseURL, "base-url", "", "OpenAI-compatible API root, normally ending in /v1")
	flags.StringVar(&model, "model", "", "model identifier")
	flags.StringVar(&prompt, "prompt", "", "single user prompt (never persisted)")
	flags.IntVar(&maxOutputTokens, "max-output-tokens", 0, "maximum output tokens")
	flags.Float64Var(&temperature, "temperature", 0, "sampling temperature")
	flags.StringVar(&timeoutText, "timeout", "", "request timeout, such as 120s")
	flags.StringVar(&outputDir, "output-dir", "", "artifact output directory")
	flags.StringVar(&apiKeyEnv, "api-key-env", "", "environment variable containing the API key")

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
	runID, err := benchmark.NewRunID()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	requestID, err := benchmark.RequestID(1)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	client, err := openai.NewClient(http.DefaultClient, resolved.Endpoint.BaseURL, apiKey)
	if err != nil {
		fmt.Fprintf(stderr, "error: create OpenAI client: %v\n", err)
		return 2
	}
	runner := benchmark.NewRunner(client)
	request := benchmark.Request{
		RequestID:       requestID,
		Model:           resolved.Endpoint.Model,
		Prompt:          resolved.Request.Prompt,
		MaxOutputTokens: resolved.Request.MaxOutputTokens,
		Temperature:     resolved.Request.Temperature,
	}

	createdAt := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), resolved.Runtime.Timeout)
	result := runner.RunRequest(ctx, request)
	cancel()

	requestMetrics := metrics.Calculate(result.Observation)
	promptHash := sha256.Sum256([]byte(resolved.Request.Prompt))
	metadata := artifacts.RunMetadata{
		SchemaVersion:            artifacts.SchemaVersion,
		RunID:                    runID,
		RequestID:                requestID,
		SlentoreVersion:          slentoreVersion(),
		CreatedAt:                createdAt,
		Model:                    resolved.Endpoint.Model,
		BaseURL:                  resolved.Endpoint.BaseURL,
		RequestedMaxOutputTokens: resolved.Request.MaxOutputTokens,
		Temperature:              resolved.Request.Temperature,
		Timeout:                  resolved.Runtime.Timeout.String(),
		PromptBytes:              len([]byte(resolved.Request.Prompt)),
		PromptSHA256:             hex.EncodeToString(promptHash[:]),
	}

	artifactPath, artifactErr := artifacts.NewWriter(resolved.Capture.OutputDir).Write(metadata, result.Observation, requestMetrics)
	printSummary(stdout, runID, result.Observation, requestMetrics, artifactPath)
	if artifactErr != nil {
		fmt.Fprintf(stderr, "error: write artifacts: %v\n", artifactErr)
		return 1
	}
	if result.Err != nil {
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
	fmt.Fprintln(writer, "Run one OpenAI-compatible streaming benchmark request.")
}

func printBenchUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore bench [--config path] [overrides]")
	fmt.Fprintln(writer, "Secrets are accepted only through --api-key-env.")
}

func printSummary(writer io.Writer, runID string, observation benchmark.RequestObservation, requestMetrics metrics.RequestMetrics, artifactPath string) {
	fmt.Fprintf(writer, "Run:        %s\n", runID)
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
	if artifactPath != "" {
		fmt.Fprintf(writer, "Artifacts:  %s\n", artifactPath)
	} else {
		fmt.Fprintln(writer, "Artifacts:  unavailable")
	}
}

func printScalar(writer io.Writer, label string, metric metrics.Scalar) {
	if metric.Available {
		fmt.Fprintf(writer, "%-12s%.3f %s\n", label+":", metric.Value, metric.Unit)
		return
	}
	fmt.Fprintf(writer, "%-12sunavailable (%s)\n", label+":", metric.Reason)
}
