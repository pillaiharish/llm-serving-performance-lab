package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
