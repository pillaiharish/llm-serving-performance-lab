package artifacts

import (
	"encoding/json"
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
	started := time.Date(2026, 8, 16, 12, 5, 1, 0, time.UTC)
	headers := started.Add(40 * time.Millisecond)
	firstByte := started.Add(45 * time.Millisecond)
	firstContent := started.Add(100 * time.Millisecond)
	completed := started.Add(200 * time.Millisecond)
	headersAfterNS := (40 * time.Millisecond).Nanoseconds()
	firstByteAfterNS := (45 * time.Millisecond).Nanoseconds()
	firstContentAfterNS := (100 * time.Millisecond).Nanoseconds()
	completedAfterNS := (200 * time.Millisecond).Nanoseconds()
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
		RunID:                   metadata.RunID,
		RequestID:               metadata.RequestID,
		RequestStartedAt:        &started,
		HeadersReceivedAt:       &headers,
		HeadersAfterNS:          &headersAfterNS,
		FirstByteAt:             &firstByte,
		FirstByteAfterNS:        &firstByteAfterNS,
		FirstStreamEventAt:      &firstContent,
		FirstStreamEventAfterNS: &firstContentAfterNS,
		FirstContentAt:          &firstContent,
		FirstContentAfterNS:     &firstContentAfterNS,
		LastContentAt:           &firstContent,
		LastContentAfterNS:      &firstContentAfterNS,
		CompletedAt:             &completed,
		CompletedAfterNS:        &completedAfterNS,
		StreamEvents: []benchmark.StreamEvent{{
			Sequence:        1,
			ReceivedAt:      firstContent,
			ReceivedAfterNS: firstContentAfterNS,
			HasContent:      true,
			ContentBytes:    8,
		}},
		Usage:             benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
		ResponseBodyBytes: 128,
		Error:             "unexpected EOF before [DONE]",
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
	for _, required := range []string{"headers_after_ns", "first_byte_after_ns", "first_stream_event_after_ns", "first_content_after_ns", "last_content_after_ns", "completed_after_ns", "received_after_ns"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("artifacts do not contain relative timing field %q", required)
		}
	}

	var persistedObservation benchmark.RequestObservation
	readArtifactJSON(t, filepath.Join(path, "observation.json"), &persistedObservation)
	var persistedMetrics metrics.RequestMetrics
	readArtifactJSON(t, filepath.Join(path, "metrics.json"), &persistedMetrics)
	if persistedObservation.RunID != metadata.RunID || persistedObservation.RequestID != metadata.RequestID {
		t.Fatalf("observation identity = (%q, %q)", persistedObservation.RunID, persistedObservation.RequestID)
	}
	if persistedMetrics.RunID != metadata.RunID || persistedMetrics.RequestID != metadata.RequestID {
		t.Fatalf("metrics identity = (%q, %q)", persistedMetrics.RunID, persistedMetrics.RequestID)
	}

	if _, err := NewWriter(outputDirectory).Write(metadata, observation, requestMetrics); err == nil {
		t.Fatal("duplicate run directory unexpectedly succeeded")
	}
	temporaryDirectories, err := filepath.Glob(filepath.Join(outputDirectory, "."+metadata.RunID+"-*"))
	if err != nil || len(temporaryDirectories) != 0 {
		t.Fatalf("temporary directories remain: %v, err = %v", temporaryDirectories, err)
	}
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

func TestWriterRejectsUnsafeRunID(t *testing.T) {
	metadata := RunMetadata{RunID: "../outside"}
	_, err := NewWriter(t.TempDir()).Write(metadata, benchmark.RequestObservation{}, metrics.RequestMetrics{})
	if err == nil {
		t.Fatal("unsafe run ID unexpectedly succeeded")
	}
}
