package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

const SchemaVersion = 2

const (
	RunStatusCompleted = "completed"
	RunStatusCancelled = "cancelled"
	RunStatusFailed    = "failed"
)

type SafetyLimits struct {
	MaxConcurrency int `json:"max_concurrency"`
	MaxRequests    int `json:"max_requests"`
}

type RequestCounts struct {
	Requested  int `json:"requested"`
	Attempted  int `json:"attempted"`
	Completed  int `json:"completed"`
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
}

type RunMetadata struct {
	SchemaVersion            int                         `json:"schema_version"`
	RunID                    string                      `json:"run_id"`
	SlentoreVersion          string                      `json:"slentore_version"`
	CreatedAt                time.Time                   `json:"created_at"`
	RunStartedAt             time.Time                   `json:"run_started_at"`
	RunCompletedAt           time.Time                   `json:"run_completed_at"`
	RunElapsedNS             int64                       `json:"run_elapsed_ns"`
	RunStatus                string                      `json:"run_status"`
	Error                    string                      `json:"error,omitempty"`
	Model                    string                      `json:"model"`
	BaseURL                  string                      `json:"base_url"`
	RequestedMaxOutputTokens int                         `json:"requested_max_output_tokens"`
	Temperature              float64                     `json:"temperature"`
	Timeout                  string                      `json:"timeout"`
	PromptBytes              int                         `json:"prompt_bytes"`
	PromptSHA256             string                      `json:"prompt_sha256"`
	RequestedConcurrency     int                         `json:"requested_concurrency"`
	EffectiveWorkers         int                         `json:"effective_workers"`
	MaxObservedActive        int                         `json:"max_observed_active_requests"`
	SafetyLimits             SafetyLimits                `json:"safety_limits"`
	RequestCounts            RequestCounts               `json:"request_counts"`
	ClientDiagnostics        benchmark.ClientDiagnostics `json:"client_diagnostics"`
}

type RequestArtifact struct {
	Sequence    int
	Observation benchmark.RequestObservation
	Metrics     metrics.RequestMetrics
}

type Writer struct {
	outputDir string
	writeFile func(string, any) error
}

func NewWriter(outputDir string) *Writer {
	return &Writer{outputDir: outputDir, writeFile: writeJSON}
}

// Write stages a complete multi-request run directory and atomically renames
// it into place. All encoding and filesystem work happens after measurement.
func (w *Writer) Write(metadata RunMetadata, requests []RequestArtifact) (string, error) {
	if w == nil || w.outputDir == "" {
		return "", fmt.Errorf("artifact output directory is required")
	}
	if metadata.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("artifact schema version must be %d", SchemaVersion)
	}
	if metadata.RunID == "" || filepath.Base(metadata.RunID) != metadata.RunID || metadata.RunID == "." {
		return "", fmt.Errorf("invalid run ID")
	}
	if err := validateMetadata(metadata, len(requests)); err != nil {
		return "", err
	}

	ordered := append([]RequestArtifact(nil), requests...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Sequence < ordered[right].Sequence })
	seen := make(map[string]struct{}, len(ordered))
	for _, request := range ordered {
		if err := validateRequestArtifact(metadata.RunID, metadata.RequestCounts.Requested, request, seen); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(w.outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create artifact output directory: %w", err)
	}
	finalDirectory := filepath.Join(w.outputDir, metadata.RunID)
	if _, err := os.Lstat(finalDirectory); err == nil {
		return "", fmt.Errorf("artifact run directory already exists: %s", finalDirectory)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect artifact run directory: %w", err)
	}

	temporaryDirectory, err := os.MkdirTemp(w.outputDir, "."+metadata.RunID+"-")
	if err != nil {
		return "", fmt.Errorf("create temporary artifact directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporaryDirectory)
		}
	}()

	writeFile := w.writeFile
	if writeFile == nil {
		writeFile = writeJSON
	}
	if err := writeFile(filepath.Join(temporaryDirectory, "run.json"), metadata); err != nil {
		return "", err
	}
	requestsDirectory := filepath.Join(temporaryDirectory, "requests")
	if err := os.Mkdir(requestsDirectory, 0o755); err != nil {
		return "", fmt.Errorf("create requests artifact directory: %w", err)
	}
	for _, request := range ordered {
		requestDirectory := filepath.Join(requestsDirectory, request.Observation.RequestID)
		if err := os.Mkdir(requestDirectory, 0o755); err != nil {
			return "", fmt.Errorf("create request artifact directory %s: %w", request.Observation.RequestID, err)
		}
		if err := writeFile(filepath.Join(requestDirectory, "observation.json"), request.Observation); err != nil {
			return "", err
		}
		if err := writeFile(filepath.Join(requestDirectory, "metrics.json"), request.Metrics); err != nil {
			return "", err
		}
	}

	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		return "", fmt.Errorf("commit artifact run directory: %w", err)
	}
	committed = true
	return finalDirectory, nil
}

func validateMetadata(metadata RunMetadata, requestArtifacts int) error {
	counts := metadata.RequestCounts
	if counts.Requested <= 0 || counts.Attempted < 0 || counts.Completed < 0 || counts.Successful < 0 || counts.Failed < 0 {
		return fmt.Errorf("invalid request counts")
	}
	if counts.Attempted > counts.Requested || counts.Completed != requestArtifacts || counts.Attempted != counts.Completed || counts.Successful+counts.Failed != counts.Completed {
		return fmt.Errorf("inconsistent request counts")
	}
	expectedWorkers := metadata.RequestedConcurrency
	if counts.Requested < expectedWorkers {
		expectedWorkers = counts.Requested
	}
	if metadata.RequestedConcurrency <= 0 || metadata.EffectiveWorkers != expectedWorkers {
		return fmt.Errorf("invalid concurrency metadata")
	}
	if metadata.MaxObservedActive < 0 || metadata.MaxObservedActive > metadata.EffectiveWorkers {
		return fmt.Errorf("invalid maximum observed active requests")
	}
	if metadata.SafetyLimits.MaxConcurrency <= 0 || metadata.SafetyLimits.MaxRequests <= 0 {
		return fmt.Errorf("invalid safety limits")
	}
	if metadata.RequestedConcurrency > metadata.SafetyLimits.MaxConcurrency || counts.Requested > metadata.SafetyLimits.MaxRequests {
		return fmt.Errorf("run exceeds recorded safety limits")
	}
	if metadata.RunElapsedNS < 0 {
		return fmt.Errorf("run elapsed time must not be negative")
	}
	switch metadata.RunStatus {
	case RunStatusCompleted:
		if counts.Attempted != counts.Requested || counts.Failed != 0 || metadata.Error != "" {
			return fmt.Errorf("completed run has inconsistent status metadata")
		}
	case RunStatusCancelled, RunStatusFailed:
		if metadata.Error == "" {
			return fmt.Errorf("non-completed run error is required")
		}
	default:
		return fmt.Errorf("invalid run status %q", metadata.RunStatus)
	}
	return nil
}

func validateRequestArtifact(runID string, requestedRequests int, request RequestArtifact, seen map[string]struct{}) error {
	if request.Sequence <= 0 || request.Sequence > requestedRequests {
		return fmt.Errorf("request artifact sequence must be within the requested range")
	}
	wantID, err := benchmark.RequestID(request.Sequence)
	if err != nil {
		return err
	}
	observation := request.Observation
	if observation.RunID != runID || observation.RequestID != wantID || filepath.Base(observation.RequestID) != observation.RequestID {
		return fmt.Errorf("request artifact identity does not match run/sequence")
	}
	if request.Metrics.RunID != observation.RunID || request.Metrics.RequestID != observation.RequestID {
		return fmt.Errorf("request observation and metrics identities differ for %s", observation.RequestID)
	}
	if _, exists := seen[observation.RequestID]; exists {
		return fmt.Errorf("duplicate request ID %s", observation.RequestID)
	}
	seen[observation.RequestID] = struct{}{}
	return nil
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
