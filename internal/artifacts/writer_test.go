package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

func TestWriterCreatesCompleteRedactedRunDirectory(t *testing.T) {
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	metadata := RunMetadata{
		SchemaVersion:            SchemaVersion,
		RunID:                    "20260816T120501Z-a31f00ff",
		RequestID:                "req-000001",
		SlentoreVersion:          "devel",
		CreatedAt:                time.Date(2026, 8, 16, 12, 5, 1, 0, time.UTC),
		Model:                    "test-model",
		BaseURL:                  "http://localhost:8000/v1",
		RequestedMaxOutputTokens: 64,
		Temperature:              0,
		Timeout:                  "2m0s",
		PromptBytes:              14,
		PromptSHA256:             "safe-hash-only",
	}
	observation := benchmark.RequestObservation{
		RequestID:         metadata.RequestID,
		StreamEvents:      []benchmark.StreamEvent{},
		Usage:             benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
		ResponseBodyBytes: 128,
		Error:             "stream completed without non-empty generated content",
	}
	requestMetrics := metrics.Calculate(observation)

	path, err := NewWriter(outputDirectory).Write(metadata, observation, requestMetrics)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != filepath.Join(outputDirectory, metadata.RunID) {
		t.Fatalf("path = %q", path)
	}

	wantFiles := map[string]bool{"run.json": true, "observation.json": true, "metrics.json": true}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != len(wantFiles) {
		t.Fatalf("entries = %v", entries)
	}
	combined := ""
	for _, entry := range entries {
		if !wantFiles[entry.Name()] {
			t.Fatalf("unexpected artifact %q", entry.Name())
		}
		encoded, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), err)
		}
		combined += string(encoded)
	}
	for _, forbidden := range []string{"raw private prompt", "generated private response", "api-secret-value", "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("artifacts contain forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{metadata.RunID, metadata.RequestID, metadata.PromptSHA256, "response_body_bytes"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("artifacts do not contain required value %q", required)
		}
	}

	if _, err := NewWriter(outputDirectory).Write(metadata, observation, requestMetrics); err == nil {
		t.Fatal("duplicate run directory unexpectedly succeeded")
	}
	temporaryDirectories, err := filepath.Glob(filepath.Join(outputDirectory, "."+metadata.RunID+"-*"))
	if err != nil || len(temporaryDirectories) != 0 {
		t.Fatalf("temporary directories remain: %v, err = %v", temporaryDirectories, err)
	}
}

func TestWriterRejectsUnsafeRunID(t *testing.T) {
	metadata := RunMetadata{RunID: "../outside"}
	_, err := NewWriter(t.TempDir()).Write(metadata, benchmark.RequestObservation{}, metrics.RequestMetrics{})
	if err == nil {
		t.Fatal("unsafe run ID unexpectedly succeeded")
	}
}
