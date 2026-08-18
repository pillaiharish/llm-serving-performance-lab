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
capture:
  output_dir: "unused"
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{
		"bench", "--config", configPath,
		"--model", "cli-model",
		"--temperature", "0",
		"--api-key-env", "",
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
	if metadata.Model != "cli-model" || metadata.Temperature != 0 || metadata.PromptBytes != len("private CLI prompt") {
		t.Fatalf("unexpected run metadata: %+v", metadata)
	}
	if metadata.SchemaVersion != 2 || metadata.RequestCounts != (artifacts.RequestCounts{Requested: 1, Attempted: 1, Completed: 1, Successful: 1}) || metadata.RunStatus != artifacts.RunStatusCompleted {
		t.Fatalf("unexpected run contract: %+v", metadata)
	}
	requestDirectory := filepath.Join(runDirectory, "requests", "req-000001")
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
	requestDirectory := filepath.Join(runDirectory, "requests", "req-000001")
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
	if !strings.Contains(stdout.String(), benchmark.ErrNoGeneratedContent.Error()) {
		t.Fatalf("summary does not report no-content failure: %q", stdout.String())
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

func TestRunBenchConcurrentOverridesAndSchema2Counts(t *testing.T) {
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
	wantCounts := artifacts.RequestCounts{Requested: 7, Attempted: 7, Completed: 7, Successful: 7}
	if metadata.RequestCounts != wantCounts || metadata.RequestedConcurrency != 3 || metadata.EffectiveWorkers != 3 || metadata.MaxObservedActive != 3 {
		t.Fatalf("unexpected run metadata: %+v", metadata)
	}
	if metadata.SafetyLimits != (artifacts.SafetyLimits{MaxConcurrency: 3, MaxRequests: 7}) {
		t.Fatalf("safety limits = %+v", metadata.SafetyLimits)
	}
	if metadata.ClientDiagnostics.NumCPU <= 0 || metadata.ClientDiagnostics.GOMAXPROCS <= 0 || metadata.ClientDiagnostics.GoVersion == "" || metadata.ClientDiagnostics.GOOS == "" || metadata.ClientDiagnostics.GOARCH == "" {
		t.Fatalf("client diagnostics = %+v", metadata.ClientDiagnostics)
	}
	requestEntries, err := os.ReadDir(filepath.Join(runDirectory, "requests"))
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
	if metadata.RunStatus != artifacts.RunStatusFailed || metadata.RequestCounts != (artifacts.RequestCounts{Requested: 5, Attempted: 5, Completed: 5, Successful: 4, Failed: 1}) {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.Error != "1 of 5 attempted requests failed" {
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
	if metadata.RequestCounts.Requested != 20 || metadata.RequestCounts.Attempted != workers || metadata.RequestCounts.Completed != workers || metadata.RequestCounts.Failed != workers {
		t.Fatalf("partial counts = %+v", metadata.RequestCounts)
	}
	entries, err := os.ReadDir(filepath.Join(runDirectory, "requests"))
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
