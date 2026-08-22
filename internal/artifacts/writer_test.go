package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/workload"
)

func TestWriterCreatesAtomicRedactedSchema5Lifecycle(t *testing.T) {
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
	if persisted.SchemaVersion != 5 || persisted.Warmup.Completed != 2 || persisted.Measurement.Completed != 2 {
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

func TestWriterPersistsAndRejectsInconsistentTokenLengthMetadata(t *testing.T) {
	metadata := testLifecycleMetadata(0, 1)
	metadata.Workload = tokenLengthMetadata()
	request := testRequestArtifact(metadata.RunID, benchmark.RequestPhaseMeasured, 1, benchmark.OutcomeSucceeded, "")
	path, err := NewWriter(filepath.Join(t.TempDir(), "runs")).Write(metadata, []RequestArtifact{request})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var persisted RunMetadata
	readArtifactJSON(t, filepath.Join(path, "run.json"), &persisted)
	if persisted.Workload.Input == nil || persisted.Workload.Input.ResolvedTokens != 128 || persisted.Workload.Tokenizer == nil || persisted.Workload.Tokenizer.Revision != nil {
		t.Fatalf("persisted workload = %+v", persisted.Workload)
	}

	tests := []struct {
		name  string
		alter func(*RunMetadata)
	}{
		{name: "resolved mismatch", alter: func(value *RunMetadata) { value.Workload.Input.ResolvedTokens = 127 }},
		{name: "missing tokenizer", alter: func(value *RunMetadata) { value.Workload.Tokenizer = nil }},
		{name: "bad prompt hash", alter: func(value *RunMetadata) { value.Workload.PromptSHA256 = "not-a-hash" }},
		{name: "bad fingerprint", alter: func(value *RunMetadata) { value.Workload.Tokenizer.BehavioralFingerprintSHA256 = "bad" }},
		{name: "context exceeded", alter: func(value *RunMetadata) { value.Workload.Tokenizer.ModelMaxLength = 150 }},
		{name: "safety exceeded", alter: func(value *RunMetadata) { value.SafetyLimits.MaxInputTokens = 64 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := testLifecycleMetadata(0, 1)
			candidate.Workload = tokenLengthMetadata()
			test.alter(&candidate)
			if _, err := NewWriter(filepath.Join(t.TempDir(), "runs")).Write(candidate, []RequestArtifact{request}); err == nil {
				t.Fatal("inconsistent workload unexpectedly persisted")
			}
		})
	}
}

func tokenLengthMetadata() WorkloadMetadata {
	return WorkloadMetadata{
		Mode:         workload.ModeTokenLength,
		Input:        &WorkloadInputMetadata{Contract: workload.ContractRenderedChatInput, TargetTokens: 128, ResolvedTokens: 128},
		Output:       WorkloadOutputMetadata{RequestedMaxTokens: 32},
		Builder:      &workload.BuilderIdentity{Kind: workload.BuilderKindDeterministic, Version: workload.BuilderVersion},
		PromptBytes:  120,
		PromptSHA256: strings.Repeat("b", 64),
		Tokenizer: &workload.TokenizerIdentity{
			Adapter: "vllm_chat_render", AdapterVersion: "1", Contract: workload.ContractRenderedChatInput,
			Model: "test-model", Source: "http://localhost:8000/tokenize",
			BehavioralFingerprintSHA256: strings.Repeat("c", 64), ModelMaxLength: 512,
		},
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

func TestWriterPersistsAndValidatesOpenLoopArrivalEvidence(t *testing.T) {
	for _, disposition := range []benchmark.ArrivalDisposition{benchmark.ArrivalClientLimited, benchmark.ArrivalSchedulerLimited} {
		t.Run(string(disposition), func(t *testing.T) {
			metadata, requests, arrivals := testOpenLoopArtifacts(disposition)
			path, err := NewWriter(filepath.Join(t.TempDir(), "runs")).WriteWithArrivals(metadata, requests, arrivals)
			if err != nil {
				t.Fatalf("WriteWithArrivals: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(path, "measured", "requests"))
			if err != nil || len(entries) != 2 || entries[0].Name() != "req-000001" || entries[1].Name() != "req-000003" {
				t.Fatalf("request gap entries = %v, err = %v", entries, err)
			}
			lines := strings.Split(strings.TrimSpace(readArtifactText(t, filepath.Join(path, "measured", "arrivals.jsonl"))), "\n")
			if len(lines) != 3 {
				t.Fatalf("arrival lines = %d: %v", len(lines), lines)
			}
			var dropped benchmark.ArrivalRecord
			if err := json.Unmarshal([]byte(lines[1]), &dropped); err != nil {
				t.Fatalf("Unmarshal arrival: %v", err)
			}
			if dropped.Sequence != 2 || dropped.Disposition != disposition || dropped.RequestID != nil || dropped.ActualStartedAt != nil || dropped.SchedulerLagNS != nil {
				t.Fatalf("dropped arrival = %+v", dropped)
			}
			if warmup := readArtifactText(t, filepath.Join(path, "warmup", "arrivals.jsonl")); warmup != "" {
				t.Fatalf("skipped warmup arrivals = %q", warmup)
			}
		})
	}

	metadata, requests, arrivals := testOpenLoopArtifacts(benchmark.ArrivalClientLimited)
	tests := []struct {
		name     string
		metadata RunMetadata
		requests []RequestArtifact
		arrivals []benchmark.ArrivalRecord
	}{
		{name: "missing request for started arrival", metadata: metadata, requests: requests[:1], arrivals: arrivals},
		{name: "duplicate arrival sequence", metadata: metadata, requests: requests, arrivals: append(append([]benchmark.ArrivalRecord{}, arrivals...), arrivals[0])},
		{name: "unstarted arrival has actual time", metadata: metadata, requests: requests, arrivals: func() []benchmark.ArrivalRecord {
			copy := append([]benchmark.ArrivalRecord{}, arrivals...)
			actual := copy[1].ScheduledAt
			copy[1].ActualStartedAt = &actual
			return copy
		}()},
		{name: "negative scheduler lag", metadata: metadata, requests: requests, arrivals: func() []benchmark.ArrivalRecord {
			copy := append([]benchmark.ArrivalRecord{}, arrivals...)
			negative := int64(-1)
			copy[0].SchedulerLagNS = &negative
			return copy
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewWriter(filepath.Join(t.TempDir(), "runs")).WriteWithArrivals(test.metadata, test.requests, test.arrivals); err == nil {
				t.Fatal("inconsistent open-loop evidence unexpectedly persisted")
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
		SchemaVersion:   SchemaVersion,
		RunID:           "20260818T100000Z-a31f00ff",
		SlentoreVersion: "devel",
		CreatedAt:       started,
		RunStatus:       RunStatusCompleted,
		Model:           "test-model",
		BaseURL:         "http://localhost:8000/v1",
		Temperature:     0,
		RequestTimeout:  "2m0s",
		Workload:        WorkloadMetadata{Mode: "prompt", Output: WorkloadOutputMetadata{RequestedMaxTokens: 64}, PromptBytes: 14, PromptSHA256: strings.Repeat("a", 64)},
		SafetyLimits:    SafetyLimits{MaxConcurrency: 256, MaxRequests: 10000, MaxRequestRate: 10000, MaxInFlight: 256, MaxInputTokens: 131072, MaxOutputTokens: 32768},
		Load: LoadMetadata{Mode: benchmark.LoadModeClosedLoop, ClosedLoop: &ClosedLoopLoadMetadata{
			RequestedConcurrency: 2, RequestedRequests: measuredRequests,
		}},
		ClientDiagnostics: benchmark.ClientDiagnostics{NumCPU: 8, GOMAXPROCS: 8, GoVersion: "go1.26.6", GOOS: "darwin", GOARCH: "arm64"},
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

func testOpenLoopArtifacts(limited benchmark.ArrivalDisposition) (RunMetadata, []RequestArtifact, []benchmark.ArrivalRecord) {
	started := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	warmupAt := started.Add(time.Millisecond)
	measurementAt := started.Add(20 * time.Millisecond)
	stopAt := measurementAt.Add(time.Second)
	completedAt := stopAt.Add(100 * time.Millisecond)
	offsets, _ := benchmark.ArrivalOffsets(3, 3)
	arrivals := make([]benchmark.ArrivalRecord, 0, 3)
	requests := make([]RequestArtifact, 0, 2)
	for _, sequence := range []int{1, 3} {
		requestID, _ := benchmark.RequestID(sequence)
		scheduled := measurementAt.Add(offsets[sequence-1])
		actual := scheduled.Add(time.Millisecond)
		actualAfter := actual.Sub(measurementAt).Nanoseconds()
		lag := actual.Sub(scheduled).Nanoseconds()
		requestIDCopy := requestID
		arrivals = append(arrivals, benchmark.ArrivalRecord{
			Sequence: sequence, Phase: benchmark.RequestPhaseMeasured,
			ScheduledAt: scheduled, ScheduledAfterNS: offsets[sequence-1].Nanoseconds(),
			Disposition: benchmark.ArrivalStarted, RequestID: &requestIDCopy,
			ActualStartedAt: &actual, ActualStartedAfterNS: &actualAfter, SchedulerLagNS: &lag,
		})
		observation := benchmark.RequestObservation{
			RunID: "20260819T100000Z-a31f00ff", RequestID: requestID, RequestStartedAt: &actual,
			StreamEvents: []benchmark.StreamEvent{}, Usage: benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
		}
		requests = append(requests, RequestArtifact{Sequence: sequence, Phase: benchmark.RequestPhaseMeasured, Outcome: benchmark.OutcomeSucceeded, Observation: observation, Metrics: metrics.Calculate(observation)})
	}
	arrivals = append(arrivals, benchmark.ArrivalRecord{
		Sequence: 2, Phase: benchmark.RequestPhaseMeasured,
		ScheduledAt: measurementAt.Add(offsets[1]), ScheduledAfterNS: offsets[1].Nanoseconds(), Disposition: limited,
	})
	sort.Slice(arrivals, func(left, right int) bool { return arrivals[left].Sequence < arrivals[right].Sequence })
	clientLimited := 0
	schedulerLimited := 0
	if limited == benchmark.ArrivalClientLimited {
		clientLimited = 1
	} else {
		schedulerLimited = 1
	}
	warmupCounts := benchmark.ArrivalCounts{}
	measurementCounts := benchmark.ArrivalCounts{Planned: 3, Processed: 3, Started: 2, ClientLimited: clientLimited, SchedulerLimited: schedulerLimited, MaxObservedInFlight: 2}
	metadata := RunMetadata{
		SchemaVersion: SchemaVersion, RunID: "20260819T100000Z-a31f00ff", SlentoreVersion: "devel", CreatedAt: started,
		RunStatus: RunStatusFailed, Error: benchmark.ErrLoadDelivery.Error(), ErrorClass: ErrorClassLoadDelivery,
		Model: "test-model", BaseURL: "http://localhost:8000/v1", RequestTimeout: "2s",
		Workload:          WorkloadMetadata{Mode: "prompt", Output: WorkloadOutputMetadata{RequestedMaxTokens: 64}, PromptBytes: 14, PromptSHA256: strings.Repeat("a", 64)},
		SafetyLimits:      SafetyLimits{MaxConcurrency: 256, MaxRequests: 10000, MaxRequestRate: 10000, MaxInFlight: 256, MaxInputTokens: 131072, MaxOutputTokens: 32768},
		Load:              LoadMetadata{Mode: benchmark.LoadModeOpenLoop, OpenLoop: &OpenLoopLoadMetadata{RequestRate: 3, Duration: "1s", MaxInFlight: 2, PlannedArrivals: 3}},
		ClientDiagnostics: benchmark.ClientDiagnostics{NumCPU: 8, GOMAXPROCS: 8, GoVersion: "go1.25.5", GOOS: "darwin", GOARCH: "arm64"},
		Lifecycle: LifecycleMetadata{StartedAt: started, CompletedAt: completedAt, ElapsedNS: completedAt.Sub(started).Nanoseconds(), Transitions: []benchmark.PhaseTransition{
			{Sequence: 1, Phase: benchmark.PhaseSetup, EnteredAt: started, Reason: "lifecycle_started"},
			{Sequence: 2, Phase: benchmark.PhaseWarmup, EnteredAt: warmupAt, EnteredAfterNS: warmupAt.Sub(started).Nanoseconds(), Reason: "warmup_skipped"},
			{Sequence: 3, Phase: benchmark.PhaseMeasurement, EnteredAt: measurementAt, EnteredAfterNS: measurementAt.Sub(started).Nanoseconds(), Reason: "warmup_complete"},
			{Sequence: 4, Phase: benchmark.PhaseStopAdmission, EnteredAt: stopAt, EnteredAfterNS: stopAt.Sub(started).Nanoseconds(), Reason: "measurement_duration_elapsed"},
			{Sequence: 5, Phase: benchmark.PhaseDrain, EnteredAt: stopAt, EnteredAfterNS: stopAt.Sub(started).Nanoseconds(), Reason: "measurement_duration_elapsed"},
			{Sequence: 6, Phase: benchmark.PhaseArtifacts, EnteredAt: completedAt, EnteredAfterNS: completedAt.Sub(started).Nanoseconds(), Reason: "drain_complete"},
		}},
		StopAdmission: StopAdmissionMetadata{StoppedAt: stopAt, StoppedAfterNS: stopAt.Sub(started).Nanoseconds(), Reason: "measurement_duration_elapsed"},
		Warmup:        PhaseMetadata{Phase: benchmark.RequestPhaseWarmup, Status: benchmark.PhaseStatusSkipped, StartedAt: &warmupAt, CompletedAt: &warmupAt, Arrivals: &warmupCounts},
		Measurement:   PhaseMetadata{Phase: benchmark.RequestPhaseMeasured, Status: benchmark.PhaseStatusCompleted, StartedAt: &measurementAt, CompletedAt: &completedAt, ElapsedNS: completedAt.Sub(measurementAt).Nanoseconds(), Requested: 3, Attempted: 2, Completed: 2, Successful: 2, Outcomes: benchmark.OutcomeCounts{Succeeded: 2}, Arrivals: &measurementCounts},
		Drain:         DrainMetadata{StartedAt: &stopAt, CompletedAt: &completedAt, ElapsedNS: completedAt.Sub(stopAt).Nanoseconds(), Timeout: "2s", CancelledRequestIDs: []string{}},
	}
	return metadata, requests, arrivals
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
