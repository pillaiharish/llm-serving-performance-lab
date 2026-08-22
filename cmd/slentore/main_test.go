package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

func TestRunBenchAppliesCLIOverridesAndWritesArtifacts(t *testing.T) {
	fixture := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header should have been cleared, got %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload["model"] != "cli-model" || payload["temperature"] != float64(0) {
			t.Errorf("CLI overrides not sent: %v", payload)
		}
		options, ok := payload["stream_options"].(map[string]any)
		if !ok || options["include_usage"] != true || payload["stream"] != true {
			t.Errorf("stream options not sent: %v", payload)
		}
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	configPath := writeCLIConfig(t, `version: 1
endpoint:
  base_url: "`+server.URL+`/v1"
  api_key_env: "MISSING_FROM_TEST"
  model: "yaml-model"
request:
  prompt: "private CLI prompt"
  max_output_tokens: 8
  temperature: 1.5
runtime:
  timeout: "5s"
  drain_timeout: "9s"
capture:
  output_dir: "unused"
benchmark:
  warmup_requests: 2
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--config", configPath,
		"--model", "cli-model",
		"--temperature", "0",
		"--warmup-requests", "0",
		"--drain-timeout", "5s",
		"--api-key-env", "",
		"--token-timing", "disabled",
		"--output-dir", outputDirectory,
	}, &stdout, &stderr, func(string) (string, bool) {
		t.Fatal("environment lookup should not occur after clearing api_key_env")
		return "", false
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}

	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.Model != "cli-model" || metadata.Temperature != 0 || metadata.Workload.PromptBytes != len("private CLI prompt") {
		t.Fatalf("unexpected run metadata: %+v", metadata)
	}
	if metadata.SchemaVersion != 6 || metadata.Workload.Mode != "prompt" || metadata.TokenTiming.Mode != "disabled" || metadata.Load.Mode != benchmark.LoadModeClosedLoop || metadata.Load.ClosedLoop == nil || metadata.Measurement.Requested != 1 || metadata.Measurement.Attempted != 1 || metadata.Measurement.Successful != 1 || metadata.Warmup.Requested != 0 || metadata.Warmup.Status != benchmark.PhaseStatusSkipped || metadata.Drain.Timeout != "5s" || metadata.RunStatus != artifacts.RunStatusCompleted {
		t.Fatalf("unexpected run contract: %+v", metadata)
	}
	if metadata.Drain.CancelledRequestIDs == nil {
		t.Fatal("normal drain must persist an empty cancelled_request_ids array")
	}
	requestDirectory := filepath.Join(runDirectory, "measured", "requests", "req-000001")
	var observation benchmark.RequestObservation
	readJSON(t, filepath.Join(requestDirectory, "observation.json"), &observation)
	if observation.Error != "" || observation.FinishReason != "stop" || observation.StatusCode != http.StatusOK {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if observation.RunID != metadata.RunID || observation.RequestID != "req-000001" {
		t.Fatalf("observation identity = (%q, %q), metadata run ID = %q", observation.RunID, observation.RequestID, metadata.RunID)
	}
	if observation.HeadersAfterNS == nil || observation.FirstByteAfterNS == nil || observation.FirstStreamEventAfterNS == nil || observation.FirstContentAfterNS == nil || observation.LastContentAfterNS == nil || observation.CompletedAfterNS == nil {
		t.Fatalf("observation is missing relative timing evidence: %+v", observation)
	}
	if len(observation.StreamEvents) != 3 {
		t.Fatalf("stream events = %d, want 3", len(observation.StreamEvents))
	}
	for _, event := range observation.StreamEvents {
		if event.ReceivedAfterNS < 0 {
			t.Fatalf("stream event has negative relative offset: %+v", event)
		}
	}
	var requestMetrics metrics.RequestMetrics
	readJSON(t, filepath.Join(requestDirectory, "metrics.json"), &requestMetrics)
	if requestMetrics.RunID != metadata.RunID || requestMetrics.RequestID != observation.RequestID {
		t.Fatalf("metrics identity = (%q, %q), observation identity = (%q, %q)", requestMetrics.RunID, requestMetrics.RequestID, observation.RunID, observation.RequestID)
	}
	combined := stdout.String() + readText(t, filepath.Join(runDirectory, "run.json")) + readText(t, filepath.Join(requestDirectory, "observation.json")) + readText(t, filepath.Join(requestDirectory, "metrics.json"))
	for _, forbidden := range []string{"private CLI prompt", "hello", "MISSING_FROM_TEST"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("output/artifacts contain forbidden value %q", forbidden)
		}
	}
	if !strings.Contains(stdout.String(), "Artifacts:") || !strings.Contains(stdout.String(), metadata.RunID) {
		t.Fatalf("summary missing artifact path: %q", stdout.String())
	}
}

func TestRunBenchVLLMTokenTimingWritesAvailableRedactedITL(t *testing.T) {
	serverConfig := fakeserver.DefaultConfig()
	serverConfig.HeaderDelay = 0
	serverConfig.FirstContentDelay = 0
	serverConfig.ChunkInterval = time.Millisecond
	serverConfig.UsageDelay = 0
	serverConfig.DoneDelay = 0
	serverConfig.TokenEvidence = fakeserver.TokenEvidenceSingleton
	handler, err := fakeserver.NewHandler(serverConfig)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var requested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read request: %v", readErr)
		} else {
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("decode request: %v", err)
			} else if value, ok := payload["return_token_ids"].(bool); ok && value {
				requested.Store(true)
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "fake-model", "--prompt", "private token timing prompt",
		"--max-output-tokens", "4", "--token-timing", "vllm", "--timeout", "2s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 0 || stderr.Len() != 0 || !requested.Load() {
		t.Fatalf("exit=%d requested=%t stderr=%q stdout=%q", exit, requested.Load(), stderr.String(), stdout.String())
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.SchemaVersion != 6 || metadata.TokenTiming.Mode != "vllm" || metadata.TokenTiming.Source != benchmark.TokenTimingSourceVLLM {
		t.Fatalf("token timing metadata = %+v", metadata.TokenTiming)
	}
	requestDirectory := filepath.Join(runDirectory, "measured", "requests", "req-000001")
	var observation benchmark.RequestObservation
	readJSON(t, filepath.Join(requestDirectory, "observation.json"), &observation)
	if !observation.TokenTiming.Requested || !observation.TokenTiming.CompletedThroughDone || observation.TokenTiming.Source != benchmark.TokenTimingSourceVLLM {
		t.Fatalf("token timing observation = %+v", observation.TokenTiming)
	}
	observedTokens := 0
	for _, event := range observation.StreamEvents {
		observedTokens += event.GeneratedTokenCount
	}
	var requestMetrics metrics.RequestMetrics
	readJSON(t, filepath.Join(requestDirectory, "metrics.json"), &requestMetrics)
	if observedTokens != 4 || !requestMetrics.ITL.Available || requestMetrics.ITL.Count != 3 || len(requestMetrics.ITL.ValuesMS) != 3 {
		t.Fatalf("tokens/ITL = %d/%+v", observedTokens, requestMetrics.ITL)
	}
	combined := stdout.String() + stderr.String() + readTreeText(t, runDirectory)
	for _, forbidden := range []string{"987654300", "987654301", "987654321", "987654322", "private token timing prompt", "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("summary/artifacts retain forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"Token timing:        vllm", "True ITL:   available", "ITL samples: 3"} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("summary missing %q: %q", required, stdout.String())
		}
	}
}

func TestRunBenchPersistsNoContentFailure(t *testing.T) {
	fixture := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":0,\"total_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench",
		"--base-url", server.URL + "/v1",
		"--model", "model",
		"--prompt", "prompt",
		"--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var observation benchmark.RequestObservation
	requestDirectory := filepath.Join(runDirectory, "measured", "requests", "req-000001")
	readJSON(t, filepath.Join(requestDirectory, "observation.json"), &observation)
	if observation.Error != benchmark.ErrNoGeneratedContent.Error() {
		t.Fatalf("observation error = %q", observation.Error)
	}
	if observation.RunID == "" || observation.RequestID != "req-000001" || observation.CompletedAfterNS == nil {
		t.Fatalf("failure observation is missing identity or completion offset: %+v", observation)
	}
	var requestMetrics metrics.RequestMetrics
	readJSON(t, filepath.Join(requestDirectory, "metrics.json"), &requestMetrics)
	if requestMetrics.RunID != observation.RunID || requestMetrics.RequestID != observation.RequestID {
		t.Fatalf("failure artifact identities differ: observation=(%q, %q), metrics=(%q, %q)", observation.RunID, observation.RequestID, requestMetrics.RunID, requestMetrics.RequestID)
	}
	if !strings.Contains(stdout.String(), "Measured failed:     1") {
		t.Fatalf("summary does not report measured failure: %q", stdout.String())
	}
}

func TestRunBenchPreflightErrorsDoNotCreateArtifacts(t *testing.T) {
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench",
		"--base-url", "https://example.test/v1",
		"--model", "model",
		"--prompt", "prompt",
		"--api-key-env", "MISSING_API_KEY",
		"--output-dir", outputDirectory,
	}, &stdout, &stderr, func(string) (string, bool) { return "", false })
	if exitCode != 2 || !strings.Contains(stderr.String(), "MISSING_API_KEY") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if _, err := os.Stat(outputDirectory); !os.IsNotExist(err) {
		t.Fatalf("preflight failure created output directory: %v", err)
	}
}

func TestRunBenchConcurrentOverridesAndSchema4Counts(t *testing.T) {
	const (
		requestCount = 7
		workers      = 3
		secret       = "cli-concurrency-secret"
	)
	fixture := normalCLIFixture("generated-fragment")
	var active atomic.Int64
	var maximum atomic.Int64
	var calls atomic.Int64
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("unexpected Authorization header")
		}
		calls.Add(1)
		current := active.Add(1)
		updateTestMaximum(&maximum, current)
		defer active.Add(-1)
		if current == workers {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	configPath := writeCLIConfig(t, `version: 1
endpoint:
  base_url: "`+server.URL+`/v1"
  api_key_env: "TEST_BENCHMARK_KEY"
  model: "model"
request:
  prompt: "private concurrent prompt"
runtime:
  timeout: "5s"
capture:
  output_dir: "unused"
benchmark:
  concurrency: 1
  requests: 1
  safety:
    max_concurrency: 1
    max_requests: 1
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--config", configPath,
		"--concurrency", "3", "--requests", "7",
		"--max-concurrency", "3", "--max-requests", "7",
		"--output-dir", outputDirectory,
	}, &stdout, &stderr, func(name string) (string, bool) {
		if name != "TEST_BENCHMARK_KEY" {
			t.Fatalf("unexpected environment lookup %q", name)
		}
		return secret, true
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if calls.Load() != requestCount || maximum.Load() != workers {
		t.Fatalf("calls = %d, maximum active = %d", calls.Load(), maximum.Load())
	}

	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.Measurement.Requested != 7 || metadata.Measurement.Attempted != 7 || metadata.Measurement.Completed != 7 || metadata.Measurement.Successful != 7 || metadata.Measurement.RequestedConcurrency != 3 || metadata.Measurement.EffectiveWorkers != 3 || metadata.Measurement.MaxObservedActive != 3 {
		t.Fatalf("unexpected run metadata: %+v", metadata)
	}
	if metadata.SafetyLimits != (artifacts.SafetyLimits{MaxConcurrency: 3, MaxRequests: 7, MaxRequestRate: 10000, MaxInFlight: 256, MaxInputTokens: 131072, MaxOutputTokens: 32768}) {
		t.Fatalf("safety limits = %+v", metadata.SafetyLimits)
	}
	if metadata.ClientDiagnostics.NumCPU <= 0 || metadata.ClientDiagnostics.GOMAXPROCS <= 0 || metadata.ClientDiagnostics.GoVersion == "" || metadata.ClientDiagnostics.GOOS == "" || metadata.ClientDiagnostics.GOARCH == "" {
		t.Fatalf("client diagnostics = %+v", metadata.ClientDiagnostics)
	}
	requestEntries, err := os.ReadDir(filepath.Join(runDirectory, "measured", "requests"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(requestEntries) != requestCount {
		t.Fatalf("request directories = %d, want %d", len(requestEntries), requestCount)
	}
	if strings.Contains(stdout.String(), "Request:") {
		t.Fatalf("multi-request summary contains detailed request output: %q", stdout.String())
	}
	combined := stdout.String() + readTreeText(t, runDirectory)
	for _, forbidden := range []string{"private concurrent prompt", "generated-fragment", secret} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("summary/artifacts contain forbidden value %q", forbidden)
		}
	}
}

func TestRunBenchAdmissionErrorsDoNotCreateArtifacts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "explicit zero", args: []string{"--concurrency", "0"}, want: "benchmark.concurrency"},
		{name: "concurrency over ceiling", args: []string{"--concurrency", "3", "--max-concurrency", "2"}, want: "exceeds"},
		{name: "requests over ceiling", args: []string{"--requests", "4", "--max-requests", "3"}, want: "exceeds"},
		{name: "negative warmup", args: []string{"--warmup-requests", "-1"}, want: "warmup_requests"},
		{name: "warmup over ceiling", args: []string{"--warmup-requests", "4", "--max-requests", "3"}, want: "warmup_requests"},
		{name: "zero drain timeout", args: []string{"--drain-timeout", "0s"}, want: "drain_timeout"},
		{name: "invalid drain timeout", args: []string{"--drain-timeout", "later"}, want: "--drain-timeout"},
		{name: "invalid token timing", args: []string{"--token-timing", "automatic"}, want: "--token-timing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputDirectory := filepath.Join(t.TempDir(), "runs")
			args := []string{"bench", "--base-url", "http://127.0.0.1:1/v1", "--model", "model", "--prompt", "prompt", "--output-dir", outputDirectory}
			args = append(args, test.args...)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := run(args, &stdout, &stderr, os.LookupEnv); exitCode != 2 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
			}
			if _, err := os.Stat(outputDirectory); !os.IsNotExist(err) {
				t.Fatalf("admission failure created artifacts: %v", err)
			}
		})
	}
}

func TestRunBenchWarmupFailuresDoNotSuppressMeasurement(t *testing.T) {
	const (
		warmupRequests   = 4
		measuredRequests = 3
	)
	fixture := normalCLIFixture("private-generated-fragment")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		if call == 2 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, "fixed generic failure")
			return
		}
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "private warmup prompt",
		"--concurrency", "2", "--warmup-requests", "4", "--requests", "3", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if calls.Load() != warmupRequests+measuredRequests {
		t.Fatalf("calls = %d, want %d", calls.Load(), warmupRequests+measuredRequests)
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.RunStatus != artifacts.RunStatusFailed || metadata.Warmup.Attempted != warmupRequests || metadata.Warmup.Successful != 3 || metadata.Warmup.Failed != 1 || metadata.Measurement.Attempted != measuredRequests || metadata.Measurement.Successful != measuredRequests || metadata.Measurement.Failed != 0 {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.Warmup.Outcomes.RequestError != 1 || metadata.Measurement.Outcomes.Succeeded != measuredRequests {
		t.Fatalf("outcomes = warmup %+v measured %+v", metadata.Warmup.Outcomes, metadata.Measurement.Outcomes)
	}
	if metadata.Measurement.StartedAt == nil || metadata.Warmup.CompletedAt == nil || metadata.Measurement.StartedAt.Before(*metadata.Warmup.CompletedAt) {
		t.Fatalf("phase timing overlaps: warmup=%+v measurement=%+v", metadata.Warmup, metadata.Measurement)
	}
	if metadata.StopAdmission.StoppedAfterNS <= 0 || metadata.StopAdmission.Reason != "measured_request_limit_reached" {
		t.Fatalf("stop admission = %+v", metadata.StopAdmission)
	}
	for phase, want := range map[string]int{"warmup": warmupRequests, "measured": measuredRequests} {
		entries, err := os.ReadDir(filepath.Join(runDirectory, phase, "requests"))
		if err != nil || len(entries) != want {
			t.Fatalf("%s request directories = %d, err = %v", phase, len(entries), err)
		}
	}
	combined := stdout.String() + readTreeText(t, runDirectory)
	for _, forbidden := range []string{"private warmup prompt", "private-generated-fragment"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("summary/artifacts contain forbidden value %q", forbidden)
		}
	}
}

func TestRunBenchDrainTimeoutPersistsAffectedMeasuredRequests(t *testing.T) {
	const workers = 2
	started := make(chan struct{})
	var active atomic.Int64
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if active.Add(1) == workers {
			once.Do(func() { close(started) })
		}
		defer active.Add(-1)
		select {
		case <-started:
			<-request.Context().Done()
		case <-request.Context().Done():
		}
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "private drain prompt",
		"--concurrency", "2", "--requests", "2", "--timeout", "2s", "--drain-timeout", "30ms", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.RunStatus != artifacts.RunStatusFailed || metadata.Error != benchmark.ErrDrainTimeout.Error() || !metadata.Drain.TimedOut || metadata.Drain.ParentCancelled || metadata.Drain.CancelledRequests != workers || metadata.Measurement.Outcomes.DrainTimeout != workers {
		t.Fatalf("metadata = %+v", metadata)
	}
	if strings.Join(metadata.Drain.CancelledRequestIDs, ",") != "req-000001,req-000002" {
		t.Fatalf("cancelled IDs = %v", metadata.Drain.CancelledRequestIDs)
	}
	for sequence := 1; sequence <= workers; sequence++ {
		requestID, _ := benchmark.RequestID(sequence)
		var observation benchmark.RequestObservation
		readJSON(t, filepath.Join(runDirectory, "measured", "requests", requestID, "observation.json"), &observation)
		if observation.RequestID != requestID || observation.RunID != metadata.RunID || observation.HeadersAfterNS == nil || observation.CompletedAfterNS == nil || observation.Error == "" {
			t.Fatalf("partial observation %s = %+v", requestID, observation)
		}
	}
	if !strings.Contains(stdout.String(), "Drain timed out:     true") || !strings.Contains(stdout.String(), "Drain cancellations: 2 requests") {
		t.Fatalf("drain summary = %q", stdout.String())
	}
	if strings.Contains(stdout.String()+readTreeText(t, runDirectory), "private drain prompt") {
		t.Fatal("drain artifacts contain prompt")
	}
}

func TestRunBenchMixedFailuresAttemptEveryRequest(t *testing.T) {
	fixture := normalCLIFixture("x")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		if call == 2 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, "generic failure")
			return
		}
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "prompt",
		"--concurrency", "2", "--requests", "5", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if calls.Load() != 5 {
		t.Fatalf("calls = %d, want 5", calls.Load())
	}
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(onlyRunDirectory(t, outputDirectory), "run.json"), &metadata)
	if metadata.RunStatus != artifacts.RunStatusFailed || metadata.Measurement.Requested != 5 || metadata.Measurement.Attempted != 5 || metadata.Measurement.Completed != 5 || metadata.Measurement.Successful != 4 || metadata.Measurement.Failed != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.Error != "1 of 5 attempted measured requests failed" {
		t.Fatalf("run error = %q", metadata.Error)
	}
}

func TestRunBenchCancellationPersistsCollectedSubset(t *testing.T) {
	const workers = 3
	started := make(chan struct{})
	release := make(chan struct{})
	var active atomic.Int64
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if active.Add(1) == workers {
			once.Do(func() { close(started) })
		}
		defer active.Add(-1)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
			close(release)
		case <-time.After(5 * time.Second):
			cancel()
			close(release)
		}
	}()
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runContext(ctx, []string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "prompt",
		"--concurrency", "3", "--requests", "20", "--timeout", "10s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	var metadata artifacts.RunMetadata
	runDirectory := onlyRunDirectory(t, outputDirectory)
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.RunStatus != artifacts.RunStatusCancelled || metadata.Error != context.Canceled.Error() {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.Measurement.Requested != 20 || metadata.Measurement.Attempted != workers || metadata.Measurement.Completed != workers || metadata.Measurement.Failed != workers {
		t.Fatalf("partial counts = %+v", metadata.Measurement)
	}
	entries, err := os.ReadDir(filepath.Join(runDirectory, "measured", "requests"))
	if err != nil || len(entries) != workers {
		t.Fatalf("partial request directories = %d, err = %v", len(entries), err)
	}
}

func TestRunRejectsDirectAPIKeyFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"bench", "--api-key", "secret"}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunBenchHealthyOpenLoopPersistsArrivalEvidence(t *testing.T) {
	fixture := normalCLIFixture("private-open-loop-content")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "private-open-loop-prompt",
		"--mode", "open-loop", "--request-rate", "20", "--duration", "250ms", "--max-in-flight", "16",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if calls.Load() != 5 {
		t.Fatalf("calls = %d, want 5", calls.Load())
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	if metadata.SchemaVersion != 6 || metadata.Load.Mode != benchmark.LoadModeOpenLoop || metadata.Load.OpenLoop == nil || metadata.Load.ClosedLoop != nil || metadata.Load.OpenLoop.RequestRate != 20 || metadata.Load.OpenLoop.Duration != "250ms" || metadata.Measurement.Arrivals == nil {
		t.Fatalf("open-loop metadata = %+v", metadata)
	}
	counts := *metadata.Measurement.Arrivals
	if counts.Planned != 5 || counts.Processed != 5 || counts.Started != 5 || counts.ClientLimited != 0 || counts.SchedulerLimited != 0 || counts.MaxObservedInFlight > 16 || metadata.RunStatus != artifacts.RunStatusCompleted || metadata.StopAdmission.Reason != "measurement_duration_elapsed" {
		t.Fatalf("arrival metadata = %+v, run status = %s", counts, metadata.RunStatus)
	}
	arrivalText := strings.TrimSpace(readText(t, filepath.Join(runDirectory, "measured", "arrivals.jsonl")))
	if len(strings.Split(arrivalText, "\n")) != 5 {
		t.Fatalf("arrival JSONL = %q", arrivalText)
	}
	if warmupText := readText(t, filepath.Join(runDirectory, "warmup", "arrivals.jsonl")); warmupText != "" {
		t.Fatalf("skipped warmup arrivals = %q", warmupText)
	}
	for sequence := 1; sequence <= 5; sequence++ {
		requestID, _ := benchmark.RequestID(sequence)
		var observation benchmark.RequestObservation
		readJSON(t, filepath.Join(runDirectory, "measured", "requests", requestID, "observation.json"), &observation)
		if observation.RequestStartedAt == nil {
			t.Fatalf("request %s lacks start evidence", requestID)
		}
	}
	combined := stdout.String() + readTreeText(t, runDirectory)
	for _, forbidden := range []string{"private-open-loop-prompt", "private-open-loop-content"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("open-loop output contains forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"Mode:                open_loop", "Request rate:        20/s", "Planned arrivals:    5", "Client-limited:      0"} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("summary %q lacks %q", stdout.String(), required)
		}
	}
}

func TestRunBenchTokenLengthWorkloadAcrossLoadModes(t *testing.T) {
	for _, test := range []struct {
		name          string
		args          []string
		wantRequests  int64
		tokenEvidence fakeserver.TokenEvidenceMode
		wantITL       bool
	}{
		{name: "closed-loop", args: []string{"--concurrency", "4", "--requests", "16", "--warmup-requests", "2"}, wantRequests: 18},
		{name: "closed-loop-vllm-token-timing", args: []string{"--concurrency", "2", "--requests", "4", "--warmup-requests", "1", "--token-timing", "vllm"}, wantRequests: 5, tokenEvidence: fakeserver.TokenEvidenceSingleton, wantITL: true},
		{name: "open-loop", args: []string{"--mode", "open-loop", "--request-rate", "20", "--duration", "250ms", "--max-in-flight", "8", "--warmup-requests", "2"}, wantRequests: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverConfig := fakeserver.DefaultConfig()
			serverConfig.Listen = "127.0.0.1:18080"
			serverConfig.HeaderDelay = 0
			serverConfig.FirstContentDelay = 0
			serverConfig.ChunkInterval = 0
			serverConfig.UsageDelay = 0
			serverConfig.DoneDelay = 0
			serverConfig.TokenizerFixture = true
			if test.tokenEvidence != "" {
				serverConfig.TokenEvidence = test.tokenEvidence
			}
			handler, err := fakeserver.NewHandler(serverConfig)
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			var tokenizerCalls atomic.Int64
			var requestCalls atomic.Int64
			var callsAtFirstRequest atomic.Int64
			callsAtFirstRequest.Store(-1)
			prompts := make(map[string]struct{})
			var promptsMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/tokenize" {
					tokenizerCalls.Add(1)
				} else if request.URL.Path == "/v1/chat/completions" {
					callsAtFirstRequest.CompareAndSwap(-1, tokenizerCalls.Load())
					requestCalls.Add(1)
					body, readErr := io.ReadAll(request.Body)
					if readErr != nil {
						t.Errorf("read request: %v", readErr)
					} else {
						request.Body = io.NopCloser(bytes.NewReader(body))
						var payload struct {
							Messages []struct {
								Content string `json:"content"`
							} `json:"messages"`
							MaxTokens      int   `json:"max_tokens"`
							ReturnTokenIDs *bool `json:"return_token_ids"`
						}
						if err := json.Unmarshal(body, &payload); err != nil || len(payload.Messages) != 1 || payload.MaxTokens != 32 {
							t.Errorf("request payload = %+v, err=%v", payload, err)
						} else {
							if (payload.ReturnTokenIDs != nil && *payload.ReturnTokenIDs) != test.wantITL {
								t.Errorf("return_token_ids = %v, want requested=%t", payload.ReturnTokenIDs, test.wantITL)
							}
							promptsMu.Lock()
							prompts[payload.Messages[0].Content] = struct{}{}
							promptsMu.Unlock()
						}
					}
				}
				handler.ServeHTTP(writer, request)
			}))
			defer server.Close()

			outputDirectory := filepath.Join(t.TempDir(), "runs")
			args := []string{"bench", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--workload-mode", "token-length", "--input-tokens", "128", "--tokenizer-adapter", "vllm", "--tokenizer-url", server.URL + "/tokenize", "--max-output-tokens", "32", "--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputDirectory}
			args = append(args, test.args...)
			var stdout, stderr bytes.Buffer
			if exit := run(args, &stdout, &stderr, os.LookupEnv); exit != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
			}
			if requestCalls.Load() != test.wantRequests || callsAtFirstRequest.Load() <= 0 || tokenizerCalls.Load() != callsAtFirstRequest.Load() {
				t.Fatalf("requests=%d tokenizer calls=%d first-request calls=%d", requestCalls.Load(), tokenizerCalls.Load(), callsAtFirstRequest.Load())
			}
			promptsMu.Lock()
			if len(prompts) != 1 {
				t.Fatalf("unique prompts = %d", len(prompts))
			}
			var generatedPrompt string
			for prompt := range prompts {
				generatedPrompt = prompt
			}
			promptsMu.Unlock()

			runDirectory := onlyRunDirectory(t, outputDirectory)
			var metadata artifacts.RunMetadata
			readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
			if metadata.SchemaVersion != 6 || metadata.Workload.Mode != "token_length" || metadata.Workload.Input == nil || metadata.Workload.Input.TargetTokens != 128 || metadata.Workload.Input.ResolvedTokens != 128 || metadata.Workload.Tokenizer == nil || metadata.Workload.Tokenizer.Contract != "rendered_chat_input" || len(metadata.Workload.Tokenizer.BehavioralFingerprintSHA256) != 64 {
				t.Fatalf("workload metadata = %+v", metadata.Workload)
			}
			var firstMetrics metrics.RequestMetrics
			readJSON(t, filepath.Join(runDirectory, "measured", "requests", "req-000001", "metrics.json"), &firstMetrics)
			if firstMetrics.ITL.Available != test.wantITL {
				t.Fatalf("ITL = %+v, want available=%t", firstMetrics.ITL, test.wantITL)
			}
			if text := readText(t, filepath.Join(runDirectory, "run.json")); strings.Contains(text, generatedPrompt) || strings.Contains(text, "private-key") {
				t.Fatal("run metadata persisted transient workload or secret")
			}
			if !strings.Contains(stdout.String(), "Input resolved:      128 tokens") || strings.Contains(stdout.String(), generatedPrompt) {
				t.Fatalf("unsafe or incomplete summary: %q", stdout.String())
			}
			if test.wantITL && !strings.Contains(stdout.String(), "Measured requests with true ITL: 4 / 4") {
				t.Fatalf("token timing summary missing measured availability count: %q", stdout.String())
			}
		})
	}
}

func TestTokenLengthPreparationCompletesBeforeLifecycleTiming(t *testing.T) {
	serverConfig := fakeserver.DefaultConfig()
	serverConfig.HeaderDelay = 0
	serverConfig.FirstContentDelay = 5 * time.Millisecond
	serverConfig.ChunkInterval = 0
	serverConfig.UsageDelay = 0
	serverConfig.DoneDelay = 0
	serverConfig.TokenizerFixture = true
	handler, err := fakeserver.NewHandler(serverConfig)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var lastTokenizerResponse atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tokenize" {
			time.Sleep(15 * time.Millisecond)
			handler.ServeHTTP(writer, request)
			lastTokenizerResponse.Store(time.Now().UnixNano())
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "fixture-model",
		"--workload-mode", "token-length", "--input-tokens", "64", "--tokenizer-adapter", "vllm", "--tokenizer-url", server.URL + "/tokenize",
		"--max-output-tokens", "16", "--concurrency", "1", "--requests", "1", "--timeout", "2s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	lastTokenization := time.Unix(0, lastTokenizerResponse.Load())
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	var observation benchmark.RequestObservation
	readJSON(t, filepath.Join(runDirectory, "measured", "requests", "req-000001", "observation.json"), &observation)
	if !metadata.Lifecycle.StartedAt.After(lastTokenization) || observation.RequestStartedAt == nil || !observation.RequestStartedAt.After(lastTokenization) {
		t.Fatalf("tokenization=%s lifecycle=%s request=%v", lastTokenization, metadata.Lifecycle.StartedAt, observation.RequestStartedAt)
	}
	if observation.FirstContentAfterNS == nil || *observation.FirstContentAfterNS >= (100*time.Millisecond).Nanoseconds() {
		t.Fatalf("request timing includes preflight delay: %+v", observation)
	}
}

func TestRunBenchOpenLoopClientLimitedFailsWithoutQueueing(t *testing.T) {
	fixture := normalCLIFixture("pressure-content")
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = io.WriteString(writer, fixture)
		case <-request.Context().Done():
		}
	}))
	defer server.Close()

	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "pressure-prompt",
		"--mode", "open-loop", "--request-rate", "100", "--duration", "100ms", "--max-in-flight", "2",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	if exitCode != 1 {
		t.Fatalf("exit = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 admitted requests and no catch-up", calls.Load())
	}
	runDirectory := onlyRunDirectory(t, outputDirectory)
	var metadata artifacts.RunMetadata
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	counts := metadata.Measurement.Arrivals
	if metadata.RunStatus != artifacts.RunStatusFailed || metadata.ErrorClass != artifacts.ErrorClassLoadDelivery || counts == nil || counts.Planned != 10 || counts.Started != 2 || counts.ClientLimited != 8 || counts.SchedulerLimited != 0 || counts.MaxObservedInFlight != 2 {
		t.Fatalf("pressure metadata = %+v", metadata)
	}
	if !strings.Contains(stdout.String(), "Run error class:      load_delivery_error") {
		t.Fatalf("summary = %q", stdout.String())
	}
}

func TestRunBenchOpenLoopCancellationPersistsUnprocessedCount(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(5 * time.Second):
			cancel()
		}
	}()
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runContext(ctx, []string{
		"bench", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "cancel-prompt",
		"--mode", "open-loop", "--request-rate", "100", "--duration", "1s", "--max-in-flight", "4",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputDirectory,
	}, &stdout, &stderr, os.LookupEnv)
	close(release)
	if exitCode != 1 {
		t.Fatalf("exit = %d, stderr = %q, stdout = %q", exitCode, stderr.String(), stdout.String())
	}
	var metadata artifacts.RunMetadata
	runDirectory := onlyRunDirectory(t, outputDirectory)
	readJSON(t, filepath.Join(runDirectory, "run.json"), &metadata)
	counts := metadata.Measurement.Arrivals
	if metadata.RunStatus != artifacts.RunStatusCancelled || metadata.Error != context.Canceled.Error() || counts == nil || counts.Planned != 100 || counts.Processed+counts.UnprocessedDueToCancellation != counts.Planned || counts.UnprocessedDueToCancellation == 0 {
		t.Fatalf("cancellation metadata = %+v", metadata)
	}
	if lines := strings.TrimSpace(readText(t, filepath.Join(runDirectory, "measured", "arrivals.jsonl"))); lines == "" {
		t.Fatal("processed cancellation arrival was not persisted")
	}
}

func TestRunBenchRejectsAmbiguousOrUnsafeOpenLoopBeforeArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "mode not inferred", args: []string{"--request-rate", "10", "--duration", "1s", "--max-in-flight", "2"}, want: "incompatible"},
		{name: "closed fields in open mode", args: []string{"--mode", "open-loop", "--request-rate", "10", "--duration", "1s", "--max-in-flight", "2", "--requests", "10"}, want: "incompatible"},
		{name: "rate ceiling", args: []string{"--mode", "open-loop", "--request-rate", "101", "--duration", "1s", "--max-in-flight", "2", "--max-request-rate-ceiling", "100"}, want: "exceeds"},
		{name: "planned ceiling", args: []string{"--mode", "open-loop", "--request-rate", "100", "--duration", "2s", "--max-in-flight", "2", "--max-requests", "100"}, want: "planned arrivals"},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputDirectory := filepath.Join(t.TempDir(), "runs")
			args := []string{"bench", "--base-url", "http://127.0.0.1:1/v1", "--model", "model", "--prompt", "prompt", "--output-dir", outputDirectory}
			args = append(args, test.args...)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := run(args, &stdout, &stderr, os.LookupEnv); exitCode != 2 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
			}
			if _, err := os.Stat(outputDirectory); !os.IsNotExist(err) {
				t.Fatalf("preflight created artifacts: %v", err)
			}
		})
	}
}

func onlyRunDirectory(t *testing.T, outputDirectory string) string {
	t.Helper()
	entries, err := os.ReadDir(outputDirectory)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("run entries = %v", entries)
	}
	return filepath.Join(outputDirectory, entries[0].Name())
}

func writeCLIConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func normalCLIFixture(content string) string {
	return "data: {\"choices\":[{\"delta\":{\"content\":\"" + content + "\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
}

func updateTestMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func readTreeText(t *testing.T, root string) string {
	t.Helper()
	var combined strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		combined.Write(content)
		return nil
	})
	if err != nil {
		t.Fatalf("read artifact tree: %v", err)
	}
	return combined.String()
}
