package artifacts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

func TestWriterCreatesAtomicRedactedSchema2Run(t *testing.T) {
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	metadata := testRunMetadata(2)
	metadata.RunStatus = RunStatusFailed
	metadata.Error = "1 of 2 attempted requests failed"
	metadata.RequestCounts.Successful = 1
	metadata.RequestCounts.Failed = 1
	requests := []RequestArtifact{
		testRequestArtifact(metadata.RunID, 2, "request two failed"),
		testRequestArtifact(metadata.RunID, 1, ""),
	}

	path, err := NewWriter(outputDirectory).Write(metadata, requests)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != filepath.Join(outputDirectory, metadata.RunID) {
		t.Fatalf("path = %q", path)
	}
	rootEntries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir root: %v", err)
	}
	if len(rootEntries) != 2 || rootEntries[0].Name() != "requests" || rootEntries[1].Name() != "run.json" {
		t.Fatalf("root entries = %v", rootEntries)
	}
	requestEntries, err := os.ReadDir(filepath.Join(path, "requests"))
	if err != nil {
		t.Fatalf("ReadDir requests: %v", err)
	}
	if len(requestEntries) != 2 || requestEntries[0].Name() != "req-000001" || requestEntries[1].Name() != "req-000002" {
		t.Fatalf("request entries = %v", requestEntries)
	}

	combined := readArtifactText(t, filepath.Join(path, "run.json"))
	for _, requestID := range []string{"req-000001", "req-000002"} {
		requestDirectory := filepath.Join(path, "requests", requestID)
		entries, err := os.ReadDir(requestDirectory)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", requestID, err)
		}
		if len(entries) != 2 || entries[0].Name() != "metrics.json" || entries[1].Name() != "observation.json" {
			t.Fatalf("%s entries = %v", requestID, entries)
		}
		combined += readArtifactText(t, filepath.Join(requestDirectory, "observation.json"))
		combined += readArtifactText(t, filepath.Join(requestDirectory, "metrics.json"))
	}
	for _, forbidden := range []string{"raw private prompt", "generated private response", "api-secret-value", "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("artifacts contain forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{metadata.RunID, "req-000001", "req-000002", metadata.PromptSHA256, "client_diagnostics", "run_elapsed_ns"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("artifacts do not contain required value %q", required)
		}
	}

	var persisted RunMetadata
	readArtifactJSON(t, filepath.Join(path, "run.json"), &persisted)
	if persisted.SchemaVersion != 2 || persisted.RequestCounts != metadata.RequestCounts || persisted.ClientDiagnostics != metadata.ClientDiagnostics {
		t.Fatalf("persisted metadata = %+v", persisted)
	}
	if _, err := NewWriter(outputDirectory).Write(metadata, requests); err == nil {
		t.Fatal("duplicate run directory unexpectedly succeeded")
	}
	temporaryDirectories, err := filepath.Glob(filepath.Join(outputDirectory, "."+metadata.RunID+"-*"))
	if err != nil || len(temporaryDirectories) != 0 {
		t.Fatalf("temporary directories remain: %v, err = %v", temporaryDirectories, err)
	}
}

func TestWriterRejectsUnsafeOrInconsistentInput(t *testing.T) {
	metadata := testRunMetadata(1)
	validRequest := testRequestArtifact(metadata.RunID, 1, "")
	tests := []struct {
		name     string
		metadata RunMetadata
		requests []RequestArtifact
	}{
		{name: "unsafe run ID", metadata: func() RunMetadata { value := metadata; value.RunID = "../outside"; return value }(), requests: []RequestArtifact{validRequest}},
		{name: "wrong schema", metadata: func() RunMetadata { value := metadata; value.SchemaVersion = 1; return value }(), requests: []RequestArtifact{validRequest}},
		{name: "inconsistent counts", metadata: func() RunMetadata { value := metadata; value.RequestCounts.Completed = 0; return value }(), requests: []RequestArtifact{validRequest}},
		{name: "identity mismatch", metadata: metadata, requests: []RequestArtifact{func() RequestArtifact { value := validRequest; value.Observation.RunID = "other"; return value }()}},
		{name: "duplicate ID", metadata: func() RunMetadata {
			value := metadata
			value.RequestCounts = RequestCounts{Requested: 2, Attempted: 2, Completed: 2, Successful: 2}
			value.EffectiveWorkers = 2
			return value
		}(), requests: []RequestArtifact{validRequest, validRequest}},
		{name: "sequence beyond requested count", metadata: metadata, requests: []RequestArtifact{testRequestArtifact(metadata.RunID, 2, "")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewWriter(t.TempDir()).Write(test.metadata, test.requests); err == nil {
				t.Fatal("Write unexpectedly succeeded")
			}
		})
	}
}

func TestWriterCleansStagingDirectoryAfterWriteFailure(t *testing.T) {
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	metadata := testRunMetadata(1)
	writer := NewWriter(outputDirectory)
	writer.writeFile = func(path string, value any) error {
		if filepath.Base(path) == "metrics.json" {
			return errors.New("injected metrics write failure")
		}
		return writeJSON(path, value)
	}

	if _, err := writer.Write(metadata, []RequestArtifact{testRequestArtifact(metadata.RunID, 1, "")}); err == nil || !strings.Contains(err.Error(), "injected metrics write failure") {
		t.Fatalf("Write error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDirectory, metadata.RunID)); !os.IsNotExist(err) {
		t.Fatalf("final directory exists after write failure: %v", err)
	}
	temporaryDirectories, err := filepath.Glob(filepath.Join(outputDirectory, "."+metadata.RunID+"-*"))
	if err != nil || len(temporaryDirectories) != 0 {
		t.Fatalf("temporary directories remain: %v, err = %v", temporaryDirectories, err)
	}
}
func testRunMetadata(requests int) RunMetadata {
	started := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	return RunMetadata{
		SchemaVersion:            SchemaVersion,
		RunID:                    "20260818T100000Z-a31f00ff",
		SlentoreVersion:          "devel",
		CreatedAt:                started,
		RunStartedAt:             started,
		RunCompletedAt:           started.Add(time.Second),
		RunElapsedNS:             time.Second.Nanoseconds(),
		RunStatus:                RunStatusCompleted,
		Model:                    "test-model",
		BaseURL:                  "http://localhost:8000/v1",
		RequestedMaxOutputTokens: 64,
		Temperature:              0,
		Timeout:                  "2m0s",
		PromptBytes:              14,
		PromptSHA256:             "safe-hash-only",
		RequestedConcurrency:     2,
		EffectiveWorkers:         min(2, requests),
		MaxObservedActive:        min(2, requests),
		SafetyLimits:             SafetyLimits{MaxConcurrency: 256, MaxRequests: 10000},
		RequestCounts:            RequestCounts{Requested: requests, Attempted: requests, Completed: requests, Successful: requests},
		ClientDiagnostics:        benchmark.ClientDiagnostics{NumCPU: 8, GOMAXPROCS: 8, GoVersion: "go1.26.6", GOOS: "darwin", GOARCH: "arm64"},
	}
}

func testRequestArtifact(runID string, sequence int, requestError string) RequestArtifact {
	requestID, _ := benchmark.RequestID(sequence)
	started := time.Date(2026, 8, 18, 10, 0, sequence, 0, time.UTC)
	completed := started.Add(100 * time.Millisecond)
	completedAfterNS := (100 * time.Millisecond).Nanoseconds()
	observation := benchmark.RequestObservation{
		RunID:            runID,
		RequestID:        requestID,
		RequestStartedAt: &started,
		CompletedAt:      &completed,
		CompletedAfterNS: &completedAfterNS,
		StreamEvents:     []benchmark.StreamEvent{},
		Usage:            benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
		Error:            requestError,
	}
	return RequestArtifact{Sequence: sequence, Observation: observation, Metrics: metrics.Calculate(observation)}
}

func readArtifactJSON(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("Unmarshal(%s): %v", path, err)
	}
}

func readArtifactText(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(encoded)
}
