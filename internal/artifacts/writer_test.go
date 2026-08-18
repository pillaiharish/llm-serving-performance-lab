package artifacts

import (
	"context"
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

func TestWriterCreatesAtomicRedactedSchema3Lifecycle(t *testing.T) {
	outputDirectory := filepath.Join(t.TempDir(), "runs")
	metadata := testLifecycleMetadata(2, 2)
	requests := []RequestArtifact{
		testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 2, benchmark.OutcomeSucceeded, ""),
		testRequestArtifact(metadata.RunID, benchmark.RequestPhaseWarmup, 2, benchmark.OutcomeSucceeded, ""),
		testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, ""),
		testRequestArtifact(metadata.RunID, benchmark.RequestPhaseWarmup, 1, benchmark.OutcomeSucceeded, ""),
	}

	path, err := NewWriter(outputDirectory).Write(metadata, requests)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path != filepath.Join(outputDirectory, metadata.RunID) {
		t.Fatalf("path = %q", path)
	}
	for _, phasePath := range []string{"warmup", "measured"} {
		entries, err := os.ReadDir(filepath.Join(path, phasePath, "requests"))
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", phasePath, err)
		}
		if len(entries) != 2 {
			t.Fatalf("%s request entries = %v", phasePath, entries)
		}
	}

	combined := readArtifactText(t, filepath.Join(path, "run.json"))
	for _, request := range requests {
		phasePath := "measured"
		if request.Phase == benchmark.RequestPhaseWarmup {
			phasePath = "warmup"
		}
		requestDirectory := filepath.Join(path, phasePath, "requests", request.Observation.RequestID)
		combined += readArtifactText(t, filepath.Join(requestDirectory, "observation.json"))
		combined += readArtifactText(t, filepath.Join(requestDirectory, "metrics.json"))
	}
	for _, forbidden := range []string{"raw private prompt", "generated private response", "api-secret-value", "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("artifacts contain forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{metadata.RunID, "warmup-000001", "req-000001", "transitions", "outcomes", "drain"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("artifacts do not contain required value %q", required)
		}
	}

	var persisted RunMetadata
	readArtifactJSON(t, filepath.Join(path, "run.json"), &persisted)
	if persisted.SchemaVersion != 3 || persisted.Warmup.Completed != 2 || persisted.Measurement.Completed != 2 {
		t.Fatalf("persisted metadata = %+v", persisted)
	}
	if _, err := NewWriter(outputDirectory).Write(metadata, requests); err == nil {
		t.Fatal("duplicate run directory unexpectedly succeeded")
	}
}

func TestWriterAlwaysCreatesEmptyWarmupRoot(t *testing.T) {
	metadata := testLifecycleMetadata(0, 1)
	requests := []RequestArtifact{testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, "")}
	path, err := NewWriter(filepath.Join(t.TempDir(), "runs")).Write(metadata, requests)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(path, "warmup", "requests"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("warmup entries = %v, err = %v", entries, err)
	}
}

func TestWriterRejectsInvalidPhaseIdentityAndCounts(t *testing.T) {
	metadata := testLifecycleMetadata(1, 1)
	validWarmup := testRequestArtifact(metadata.RunID, benchmark.RequestPhaseWarmup, 1, benchmark.OutcomeSucceeded, "")
	validMeasured := testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, "")
	tests := []struct {
		name     string
		metadata RunMetadata
		requests []RequestArtifact
	}{
		{name: "wrong schema", metadata: func() RunMetadata { value := metadata; value.SchemaVersion = 2; return value }(), requests: []RequestArtifact{validWarmup, validMeasured}},
		{name: "phase identity mismatch", metadata: metadata, requests: []RequestArtifact{func() RequestArtifact {
			value := validWarmup
			value.Observation.RequestID = "req-000001"
			value.Metrics.RequestID = "req-000001"
			return value
		}(), validMeasured}},
		{name: "duplicate measured ID", metadata: func() RunMetadata {
			value := metadata
			value.Measurement.Requested = 2
			value.Measurement.Attempted = 2
			value.Measurement.Completed = 2
			value.Measurement.Successful = 2
			value.Measurement.EffectiveWorkers = 2
			value.Measurement.Outcomes.Succeeded = 2
			return value
		}(), requests: []RequestArtifact{validWarmup, validMeasured, validMeasured}},
		{name: "inconsistent outcome", metadata: func() RunMetadata {
			value := metadata
			value.Measurement.Outcomes = benchmark.OutcomeCounts{RequestError: 1}
			return value
		}(), requests: []RequestArtifact{validWarmup, validMeasured}},
		{name: "request outcome differs from metadata", metadata: metadata, requests: []RequestArtifact{validWarmup, func() RequestArtifact {
			value := validMeasured
			value.Outcome = benchmark.OutcomeRequestError
			value.Observation.Error = "request failed"
			return value
		}()}},
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
	metadata := testLifecycleMetadata(0, 1)
	writer := NewWriter(outputDirectory)
	writer.writeFile = func(path string, value any) error {
		if filepath.Base(path) == "metrics.json" {
			return errors.New("injected metrics write failure")
		}
		return writeJSON(path, value)
	}
	request := testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, "")
	if _, err := writer.Write(metadata, []RequestArtifact{request}); err == nil || !strings.Contains(err.Error(), "injected metrics write failure") {
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

func TestWriterAcceptsFailureCancellationAndDrainTimeoutLifecycleEvidence(t *testing.T) {
	tests := []struct {
		name     string
		metadata RunMetadata
		requests []RequestArtifact
	}{
		{
			name: "warmup failure with successful measurement",
			metadata: func() RunMetadata {
				value := testLifecycleMetadata(1, 1)
				value.RunStatus = RunStatusFailed
				value.Error = "1 of 1 attempted warmup requests failed"
				value.Warmup.Status = benchmark.PhaseStatusFailed
				value.Warmup.Successful = 0
				value.Warmup.Failed = 1
				value.Warmup.Outcomes = benchmark.OutcomeCounts{RequestError: 1}
				return value
			}(),
			requests: []RequestArtifact{
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseWarmup, 1, benchmark.OutcomeRequestError, "warmup failed"),
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, ""),
			},
		},
		{
			name: "partial parent cancellation",
			metadata: func() RunMetadata {
				value := testLifecycleMetadata(0, 3)
				value.RunStatus = RunStatusCancelled
				value.Error = context.Canceled.Error()
				value.Measurement.Status = benchmark.PhaseStatusCancelled
				value.Measurement.Attempted = 2
				value.Measurement.Completed = 2
				value.Measurement.Successful = 0
				value.Measurement.Failed = 2
				value.Measurement.Outcomes = benchmark.OutcomeCounts{ParentCancelled: 2}
				value.Drain.ParentCancelled = true
				value.Drain.CancelledRequests = 2
				value.Drain.CancelledRequestIDs = []string{"req-000001", "req-000002"}
				return value
			}(),
			requests: []RequestArtifact{
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseMeasured, 2, benchmark.OutcomeParentCancelled, "context canceled"),
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeParentCancelled, "context canceled"),
			},
		},
		{
			name: "drain timeout",
			metadata: func() RunMetadata {
				value := testLifecycleMetadata(0, 2)
				value.RunStatus = RunStatusFailed
				value.Error = benchmark.ErrDrainTimeout.Error()
				value.Measurement.Status = benchmark.PhaseStatusFailed
				value.Measurement.Successful = 0
				value.Measurement.Failed = 2
				value.Measurement.Outcomes = benchmark.OutcomeCounts{DrainTimeout: 2}
				value.Drain.TimedOut = true
				value.Drain.CancelledRequests = 2
				value.Drain.CancelledRequestIDs = []string{"req-000001", "req-000002"}
				return value
			}(),
			requests: []RequestArtifact{
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeDrainTimeout, benchmark.ErrDrainTimeout.Error()),
				testRequestArtifact("20260818T100000Z-a31f00ff", benchmark.RequestPhaseMeasured, 2, benchmark.OutcomeDrainTimeout, benchmark.ErrDrainTimeout.Error()),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := NewWriter(filepath.Join(t.TempDir(), "runs")).Write(test.metadata, test.requests)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			var persisted RunMetadata
			readArtifactJSON(t, filepath.Join(path, "run.json"), &persisted)
			if persisted.RunStatus != test.metadata.RunStatus || persisted.Warmup.Outcomes != test.metadata.Warmup.Outcomes || persisted.Measurement.Outcomes != test.metadata.Measurement.Outcomes || persisted.Drain.CancelledRequests != test.metadata.Drain.CancelledRequests {
				t.Fatalf("persisted metadata = %+v", persisted)
			}
		})
	}
}

func testLifecycleMetadata(warmupRequests, measuredRequests int) RunMetadata {
	started := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	warmupStatus := benchmark.PhaseStatusCompleted
	var warmupStarted *time.Time
	var warmupCompleted *time.Time
	if warmupRequests == 0 {
		warmupStatus = benchmark.PhaseStatusSkipped
	} else {
		warmupStarted = timePointer(started.Add(time.Millisecond))
		warmupCompleted = timePointer(started.Add(10 * time.Millisecond))
	}
	warmupWorkers := min(2, warmupRequests)
	measurementWorkers := min(2, measuredRequests)
	return RunMetadata{
		SchemaVersion:            SchemaVersion,
		RunID:                    "20260818T100000Z-a31f00ff",
		SlentoreVersion:          "devel",
		CreatedAt:                started,
		RunStatus:                RunStatusCompleted,
		Model:                    "test-model",
		BaseURL:                  "http://localhost:8000/v1",
		RequestedMaxOutputTokens: 64,
		Temperature:              0,
		RequestTimeout:           "2m0s",
		PromptBytes:              14,
		PromptSHA256:             "safe-hash-only",
		SafetyLimits:             SafetyLimits{MaxConcurrency: 256, MaxRequests: 10000},
		ClientDiagnostics:        benchmark.ClientDiagnostics{NumCPU: 8, GOMAXPROCS: 8, GoVersion: "go1.26.6", GOOS: "darwin", GOARCH: "arm64"},
		Lifecycle: LifecycleMetadata{
			StartedAt:   started,
			CompletedAt: started.Add(time.Second),
			ElapsedNS:   time.Second.Nanoseconds(),
			Transitions: []benchmark.PhaseTransition{
				{Sequence: 1, Phase: benchmark.PhaseSetup, EnteredAt: started, Reason: "lifecycle_started"},
				{Sequence: 2, Phase: benchmark.PhaseWarmup, EnteredAt: started.Add(time.Millisecond), EnteredAfterNS: time.Millisecond.Nanoseconds(), Reason: "warmup_started"},
				{Sequence: 3, Phase: benchmark.PhaseMeasurement, EnteredAt: started.Add(20 * time.Millisecond), EnteredAfterNS: (20 * time.Millisecond).Nanoseconds(), Reason: "warmup_complete"},
				{Sequence: 4, Phase: benchmark.PhaseStopAdmission, EnteredAt: started.Add(30 * time.Millisecond), EnteredAfterNS: (30 * time.Millisecond).Nanoseconds(), Reason: "measured_request_limit_reached"},
				{Sequence: 5, Phase: benchmark.PhaseDrain, EnteredAt: started.Add(30 * time.Millisecond), EnteredAfterNS: (30 * time.Millisecond).Nanoseconds(), Reason: "measured_request_limit_reached"},
				{Sequence: 6, Phase: benchmark.PhaseArtifacts, EnteredAt: started.Add(time.Second), EnteredAfterNS: time.Second.Nanoseconds(), Reason: "drain_complete"},
			},
		},
		StopAdmission: StopAdmissionMetadata{
			StoppedAt: started.Add(30 * time.Millisecond), StoppedAfterNS: (30 * time.Millisecond).Nanoseconds(), Reason: "measured_request_limit_reached",
		},
		Warmup: PhaseMetadata{
			Phase: benchmark.RequestPhaseWarmup, Status: warmupStatus, StartedAt: warmupStarted, CompletedAt: warmupCompleted,
			Requested: warmupRequests, Attempted: warmupRequests, Completed: warmupRequests, Successful: warmupRequests,
			RequestedConcurrency: 2, EffectiveWorkers: warmupWorkers, MaxObservedActive: warmupWorkers,
			Outcomes: benchmark.OutcomeCounts{Succeeded: warmupRequests},
		},
		Measurement: PhaseMetadata{
			Phase: benchmark.RequestPhaseMeasured, Status: benchmark.PhaseStatusCompleted, StartedAt: timePointer(started.Add(20 * time.Millisecond)), CompletedAt: timePointer(started.Add(time.Second)), ElapsedNS: (980 * time.Millisecond).Nanoseconds(),
			Requested: measuredRequests, Attempted: measuredRequests, Completed: measuredRequests, Successful: measuredRequests,
			RequestedConcurrency: 2, EffectiveWorkers: measurementWorkers, MaxObservedActive: measurementWorkers,
			Outcomes: benchmark.OutcomeCounts{Succeeded: measuredRequests},
		},
		Drain: DrainMetadata{
			StartedAt: timePointer(started.Add(30 * time.Millisecond)), CompletedAt: timePointer(started.Add(time.Second)), ElapsedNS: (970 * time.Millisecond).Nanoseconds(), Timeout: "2m0s", CancelledRequestIDs: []string{},
		},
	}
}

func testRequestArtifact(runID string, phase benchmark.RequestPhase, sequence int, outcome benchmark.RequestOutcome, requestError string) RequestArtifact {
	requestID, _ := benchmark.RequestID(sequence)
	if phase == benchmark.RequestPhaseWarmup {
		requestID, _ = benchmark.WarmupRequestID(sequence)
	}
	started := time.Date(2026, 8, 18, 10, 0, sequence, 0, time.UTC)
	completed := started.Add(100 * time.Millisecond)
	completedAfterNS := (100 * time.Millisecond).Nanoseconds()
	observation := benchmark.RequestObservation{
		RunID: runID, RequestID: requestID, RequestStartedAt: &started, CompletedAt: &completed, CompletedAfterNS: &completedAfterNS,
		StreamEvents: []benchmark.StreamEvent{}, Usage: benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable}, Error: requestError,
	}
	return RequestArtifact{Sequence: sequence, Phase: phase, Outcome: outcome, Observation: observation, Metrics: metrics.Calculate(observation)}
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

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
