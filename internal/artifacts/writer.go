package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
)

const SchemaVersion = 1

type RunMetadata struct {
	SchemaVersion            int       `json:"schema_version"`
	RunID                    string    `json:"run_id"`
	RequestID                string    `json:"request_id"`
	SlentoreVersion          string    `json:"slentore_version"`
	CreatedAt                time.Time `json:"created_at"`
	Model                    string    `json:"model"`
	BaseURL                  string    `json:"base_url"`
	RequestedMaxOutputTokens int       `json:"requested_max_output_tokens"`
	Temperature              float64   `json:"temperature"`
	Timeout                  string    `json:"timeout"`
	PromptBytes              int       `json:"prompt_bytes"`
	PromptSHA256             string    `json:"prompt_sha256"`
}

type Writer struct {
	outputDir string
}

func NewWriter(outputDir string) *Writer {
	return &Writer{outputDir: outputDir}
}

// Write stages a complete run directory and atomically renames it into place.
// All encoding and filesystem work happens after request measurement.
func (w *Writer) Write(metadata RunMetadata, observation benchmark.RequestObservation, requestMetrics metrics.RequestMetrics) (string, error) {
	if w == nil || w.outputDir == "" {
		return "", fmt.Errorf("artifact output directory is required")
	}
	if metadata.RunID == "" || filepath.Base(metadata.RunID) != metadata.RunID || metadata.RunID == "." {
		return "", fmt.Errorf("invalid run ID")
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

	files := []struct {
		name  string
		value any
	}{
		{name: "run.json", value: metadata},
		{name: "observation.json", value: observation},
		{name: "metrics.json", value: requestMetrics},
	}
	for _, file := range files {
		encoded, err := json.MarshalIndent(file.value, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode %s: %w", file.name, err)
		}
		encoded = append(encoded, '\n')
		path := filepath.Join(temporaryDirectory, file.name)
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", file.name, err)
		}
	}

	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		return "", fmt.Errorf("commit artifact run directory: %w", err)
	}
	committed = true
	return finalDirectory, nil
}
