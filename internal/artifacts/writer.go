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

const SchemaVersion = 3

const (
	RunStatusCompleted = "completed"
	RunStatusCancelled = "cancelled"
	RunStatusFailed    = "failed"
)

type SafetyLimits struct {
	MaxConcurrency int `json:"max_concurrency"`
	MaxRequests    int `json:"max_requests"`
}

type LifecycleMetadata struct {
	StartedAt   time.Time                   `json:"started_at"`
	CompletedAt time.Time                   `json:"completed_at"`
	ElapsedNS   int64                       `json:"elapsed_ns"`
	Transitions []benchmark.PhaseTransition `json:"transitions"`
}

type StopAdmissionMetadata struct {
	StoppedAt      time.Time `json:"stopped_at"`
	StoppedAfterNS int64     `json:"stopped_after_ns"`
	Reason         string    `json:"reason"`
}

type PhaseMetadata struct {
	Phase                benchmark.RequestPhase  `json:"phase"`
	Status               string                  `json:"status"`
	StartedAt            *time.Time              `json:"started_at"`
	CompletedAt          *time.Time              `json:"completed_at"`
	ElapsedNS            int64                   `json:"elapsed_ns"`
	Requested            int                     `json:"requested"`
	Attempted            int                     `json:"attempted"`
	Completed            int                     `json:"completed"`
	Successful           int                     `json:"successful"`
	Failed               int                     `json:"failed"`
	RequestedConcurrency int                     `json:"requested_concurrency"`
	EffectiveWorkers     int                     `json:"effective_workers"`
	MaxObservedActive    int                     `json:"max_observed_active_requests"`
	Outcomes             benchmark.OutcomeCounts `json:"outcomes"`
}

type DrainMetadata struct {
	StartedAt           *time.Time `json:"started_at"`
	CompletedAt         *time.Time `json:"completed_at"`
	ElapsedNS           int64      `json:"elapsed_ns"`
	Timeout             string     `json:"timeout"`
	TimedOut            bool       `json:"timed_out"`
	ParentCancelled     bool       `json:"parent_cancelled"`
	CancelledRequests   int        `json:"cancelled_requests"`
	CancelledRequestIDs []string   `json:"cancelled_request_ids"`
}

type RunMetadata struct {
	SchemaVersion            int                         `json:"schema_version"`
	RunID                    string                      `json:"run_id"`
	SlentoreVersion          string                      `json:"slentore_version"`
	CreatedAt                time.Time                   `json:"created_at"`
	RunStatus                string                      `json:"run_status"`
	Error                    string                      `json:"error,omitempty"`
	Model                    string                      `json:"model"`
	BaseURL                  string                      `json:"base_url"`
	RequestedMaxOutputTokens int                         `json:"requested_max_output_tokens"`
	Temperature              float64                     `json:"temperature"`
	RequestTimeout           string                      `json:"request_timeout"`
	PromptBytes              int                         `json:"prompt_bytes"`
	PromptSHA256             string                      `json:"prompt_sha256"`
	SafetyLimits             SafetyLimits                `json:"safety_limits"`
	ClientDiagnostics        benchmark.ClientDiagnostics `json:"client_diagnostics"`
	Lifecycle                LifecycleMetadata           `json:"lifecycle"`
	StopAdmission            StopAdmissionMetadata       `json:"stop_admission"`
	Warmup                   PhaseMetadata               `json:"warmup"`
	Measurement              PhaseMetadata               `json:"measurement"`
	Drain                    DrainMetadata               `json:"drain"`
}

type RequestArtifact struct {
	Sequence    int
	Phase       benchmark.RequestPhase
	Outcome     benchmark.RequestOutcome
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

// Write stages a complete lifecycle run and atomically renames it into place.
// All encoding and filesystem work happens after request measurement and drain.
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

	ordered := append([]RequestArtifact(nil), requests...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Phase != ordered[right].Phase {
			return ordered[left].Phase < ordered[right].Phase
		}
		return ordered[left].Sequence < ordered[right].Sequence
	})
	warmupSummary, measuredSummary, err := validateRequests(metadata, ordered)
	if err != nil {
		return "", err
	}
	if err := validateMetadata(metadata, warmupSummary, measuredSummary); err != nil {
		return "", err
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
	warmupRoot := filepath.Join(temporaryDirectory, "warmup", "requests")
	measuredRoot := filepath.Join(temporaryDirectory, "measured", "requests")
	for _, root := range []string{warmupRoot, measuredRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("create phase artifact directory: %w", err)
		}
	}
	for _, request := range ordered {
		root := measuredRoot
		if request.Phase == benchmark.RequestPhaseWarmup {
			root = warmupRoot
		}
		requestDirectory := filepath.Join(root, request.Observation.RequestID)
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

type phaseArtifactSummary struct {
	count    int
	outcomes benchmark.OutcomeCounts
}

func validateRequests(metadata RunMetadata, requests []RequestArtifact) (phaseArtifactSummary, phaseArtifactSummary, error) {
	seen := make(map[string]struct{}, len(requests))
	var warmupSummary phaseArtifactSummary
	var measuredSummary phaseArtifactSummary
	for _, request := range requests {
		var requested int
		var wantID string
		var err error
		var summary *phaseArtifactSummary
		switch request.Phase {
		case benchmark.RequestPhaseWarmup:
			requested = metadata.Warmup.Requested
			wantID, err = benchmark.WarmupRequestID(request.Sequence)
			summary = &warmupSummary
		case benchmark.RequestPhaseMeasured:
			requested = metadata.Measurement.Requested
			wantID, err = benchmark.RequestID(request.Sequence)
			summary = &measuredSummary
		default:
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("invalid request artifact phase %q", request.Phase)
		}
		if err != nil || request.Sequence <= 0 || request.Sequence > requested {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("request artifact sequence is outside its phase range")
		}
		observation := request.Observation
		if observation.RunID != metadata.RunID || observation.RequestID != wantID || filepath.Base(observation.RequestID) != observation.RequestID {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("request artifact identity does not match run, phase, and sequence")
		}
		if request.Metrics.RunID != observation.RunID || request.Metrics.RequestID != observation.RequestID {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("request observation and metrics identities differ for %s", observation.RequestID)
		}
		key := string(request.Phase) + ":" + observation.RequestID
		if _, exists := seen[key]; exists {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("duplicate request ID %s in phase %s", observation.RequestID, request.Phase)
		}
		seen[key] = struct{}{}
		if !validOutcome(request.Outcome) {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("invalid request outcome %q", request.Outcome)
		}
		if request.Outcome == benchmark.OutcomeSucceeded && observation.Error != "" {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("successful request %s contains an observation error", observation.RequestID)
		}
		if request.Outcome != benchmark.OutcomeSucceeded && observation.Error == "" {
			return phaseArtifactSummary{}, phaseArtifactSummary{}, fmt.Errorf("failed request %s has no observation error", observation.RequestID)
		}
		summary.count++
		incrementArtifactOutcome(&summary.outcomes, request.Outcome)
	}
	return warmupSummary, measuredSummary, nil
}

func validateMetadata(metadata RunMetadata, warmupArtifacts, measuredArtifacts phaseArtifactSummary) error {
	if metadata.Lifecycle.ElapsedNS < 0 || metadata.Lifecycle.CompletedAt.Before(metadata.Lifecycle.StartedAt) {
		return fmt.Errorf("invalid lifecycle timing metadata")
	}
	if len(metadata.Lifecycle.Transitions) == 0 || metadata.Lifecycle.Transitions[0].Phase != benchmark.PhaseSetup || metadata.Lifecycle.Transitions[len(metadata.Lifecycle.Transitions)-1].Phase != benchmark.PhaseArtifacts {
		return fmt.Errorf("invalid lifecycle transitions")
	}
	var stopAdmission *benchmark.PhaseTransition
	for index, transition := range metadata.Lifecycle.Transitions {
		if transition.Sequence != index+1 || transition.EnteredAfterNS < 0 || transition.EnteredAt.Before(metadata.Lifecycle.StartedAt) {
			return fmt.Errorf("invalid lifecycle transition timing or sequence")
		}
		if transition.Phase == benchmark.PhaseStopAdmission {
			copy := transition
			stopAdmission = &copy
		}
	}
	if stopAdmission == nil || metadata.StopAdmission.StoppedAt != stopAdmission.EnteredAt || metadata.StopAdmission.StoppedAfterNS != stopAdmission.EnteredAfterNS || metadata.StopAdmission.Reason != stopAdmission.Reason {
		return fmt.Errorf("stop-admission metadata does not match lifecycle transition")
	}
	if err := validatePhaseMetadata(metadata.Warmup, benchmark.RequestPhaseWarmup, warmupArtifacts, metadata.SafetyLimits); err != nil {
		return fmt.Errorf("warmup metadata: %w", err)
	}
	if err := validatePhaseMetadata(metadata.Measurement, benchmark.RequestPhaseMeasured, measuredArtifacts, metadata.SafetyLimits); err != nil {
		return fmt.Errorf("measurement metadata: %w", err)
	}
	if metadata.Measurement.Requested <= 0 || metadata.Warmup.Requested < 0 {
		return fmt.Errorf("invalid phase requested counts")
	}
	if metadata.Warmup.RequestedConcurrency != metadata.Measurement.RequestedConcurrency {
		return fmt.Errorf("phase requested concurrency differs")
	}
	if metadata.SafetyLimits.MaxConcurrency <= 0 || metadata.SafetyLimits.MaxRequests <= 0 || metadata.Measurement.RequestedConcurrency > metadata.SafetyLimits.MaxConcurrency || metadata.Measurement.Requested > metadata.SafetyLimits.MaxRequests || metadata.Warmup.Requested > metadata.SafetyLimits.MaxRequests {
		return fmt.Errorf("invalid or exceeded safety limits")
	}
	if metadata.Drain.Timeout == "" || metadata.Drain.ElapsedNS < 0 || metadata.Drain.CancelledRequests != len(metadata.Drain.CancelledRequestIDs) {
		return fmt.Errorf("invalid drain metadata")
	}
	switch metadata.RunStatus {
	case RunStatusCompleted:
		if metadata.Error != "" || metadata.Warmup.Failed != 0 || metadata.Measurement.Failed != 0 || metadata.Warmup.Attempted != metadata.Warmup.Requested || metadata.Measurement.Attempted != metadata.Measurement.Requested || metadata.Drain.TimedOut || metadata.Drain.ParentCancelled {
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

func validatePhaseMetadata(phase PhaseMetadata, wantPhase benchmark.RequestPhase, artifacts phaseArtifactSummary, safety SafetyLimits) error {
	if phase.Phase != wantPhase || phase.Requested < 0 || phase.Attempted < 0 || phase.Completed < 0 || phase.Successful < 0 || phase.Failed < 0 {
		return fmt.Errorf("invalid phase values")
	}
	if phase.Attempted > phase.Requested || phase.Attempted != phase.Completed || phase.Completed != artifacts.count || phase.Successful+phase.Failed != phase.Completed || phase.Outcomes.Total() != phase.Completed || phase.Outcomes.Succeeded != phase.Successful || phase.Outcomes.Failed() != phase.Failed || phase.Outcomes != artifacts.outcomes {
		return fmt.Errorf("inconsistent phase counts")
	}
	if phase.ElapsedNS < 0 || phase.Requested > safety.MaxRequests {
		return fmt.Errorf("invalid phase timing or safety")
	}
	if phase.RequestedConcurrency <= 0 {
		return fmt.Errorf("invalid phase requested concurrency")
	}
	if phase.Status == benchmark.PhaseStatusSkipped {
		if phase.Attempted != 0 || phase.EffectiveWorkers != 0 || phase.MaxObservedActive != 0 {
			return fmt.Errorf("invalid skipped phase")
		}
		return nil
	}
	if phase.Status != benchmark.PhaseStatusCompleted && phase.Status != benchmark.PhaseStatusFailed && phase.Status != benchmark.PhaseStatusCancelled {
		return fmt.Errorf("invalid phase status %q", phase.Status)
	}
	expectedWorkers := phase.RequestedConcurrency
	if phase.Requested < expectedWorkers {
		expectedWorkers = phase.Requested
	}
	if phase.RequestedConcurrency <= 0 || phase.EffectiveWorkers != expectedWorkers || phase.MaxObservedActive < 0 || phase.MaxObservedActive > phase.EffectiveWorkers {
		return fmt.Errorf("invalid phase concurrency")
	}
	return nil
}

func incrementArtifactOutcome(counts *benchmark.OutcomeCounts, outcome benchmark.RequestOutcome) {
	switch outcome {
	case benchmark.OutcomeSucceeded:
		counts.Succeeded++
	case benchmark.OutcomeRequestError:
		counts.RequestError++
	case benchmark.OutcomeRequestTimeout:
		counts.RequestTimeout++
	case benchmark.OutcomeParentCancelled:
		counts.ParentCancelled++
	case benchmark.OutcomeDrainTimeout:
		counts.DrainTimeout++
	}
}

func validOutcome(outcome benchmark.RequestOutcome) bool {
	switch outcome {
	case benchmark.OutcomeSucceeded, benchmark.OutcomeRequestError, benchmark.OutcomeRequestTimeout, benchmark.OutcomeParentCancelled, benchmark.OutcomeDrainTimeout:
		return true
	default:
		return false
	}
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
