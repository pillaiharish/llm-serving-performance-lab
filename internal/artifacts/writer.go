package artifacts

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/workload"
)

const SchemaVersion = 5

const (
	RunStatusCompleted = "completed"
	RunStatusCancelled = "cancelled"
	RunStatusFailed    = "failed"
)

const ErrorClassLoadDelivery = "load_delivery_error"

type SafetyLimits struct {
	MaxConcurrency  int     `json:"max_concurrency"`
	MaxRequests     int     `json:"max_requests"`
	MaxRequestRate  float64 `json:"max_request_rate"`
	MaxInFlight     int     `json:"max_in_flight"`
	MaxInputTokens  int     `json:"max_input_tokens"`
	MaxOutputTokens int     `json:"max_output_tokens"`
}

type WorkloadMetadata struct {
	Mode         string                      `json:"mode"`
	Input        *WorkloadInputMetadata      `json:"input,omitempty"`
	Output       WorkloadOutputMetadata      `json:"output"`
	Builder      *workload.BuilderIdentity   `json:"builder,omitempty"`
	PromptBytes  int                         `json:"prompt_bytes"`
	PromptSHA256 string                      `json:"prompt_sha256"`
	Tokenizer    *workload.TokenizerIdentity `json:"tokenizer,omitempty"`
}

type WorkloadInputMetadata struct {
	Contract       string `json:"contract"`
	TargetTokens   int    `json:"target_tokens"`
	ResolvedTokens int    `json:"resolved_tokens"`
}

type WorkloadOutputMetadata struct {
	RequestedMaxTokens int `json:"requested_max_tokens"`
}

type LoadMetadata struct {
	Mode       benchmark.LoadMode      `json:"mode"`
	ClosedLoop *ClosedLoopLoadMetadata `json:"closed_loop,omitempty"`
	OpenLoop   *OpenLoopLoadMetadata   `json:"open_loop,omitempty"`
}

type ClosedLoopLoadMetadata struct {
	RequestedConcurrency int `json:"requested_concurrency"`
	RequestedRequests    int `json:"requested_requests"`
}

type OpenLoopLoadMetadata struct {
	RequestRate     float64 `json:"request_rate"`
	Duration        string  `json:"duration"`
	MaxInFlight     int     `json:"max_in_flight"`
	PlannedArrivals int     `json:"planned_arrivals"`
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
	Phase                benchmark.RequestPhase   `json:"phase"`
	Status               string                   `json:"status"`
	StartedAt            *time.Time               `json:"started_at"`
	CompletedAt          *time.Time               `json:"completed_at"`
	ElapsedNS            int64                    `json:"elapsed_ns"`
	Requested            int                      `json:"requested"`
	Attempted            int                      `json:"attempted"`
	Completed            int                      `json:"completed"`
	Successful           int                      `json:"successful"`
	Failed               int                      `json:"failed"`
	RequestedConcurrency int                      `json:"requested_concurrency,omitempty"`
	EffectiveWorkers     int                      `json:"effective_workers,omitempty"`
	MaxObservedActive    int                      `json:"max_observed_active_requests,omitempty"`
	Arrivals             *benchmark.ArrivalCounts `json:"arrivals,omitempty"`
	Outcomes             benchmark.OutcomeCounts  `json:"outcomes"`
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
	SchemaVersion     int                         `json:"schema_version"`
	RunID             string                      `json:"run_id"`
	SlentoreVersion   string                      `json:"slentore_version"`
	CreatedAt         time.Time                   `json:"created_at"`
	RunStatus         string                      `json:"run_status"`
	Error             string                      `json:"error,omitempty"`
	ErrorClass        string                      `json:"error_class,omitempty"`
	Model             string                      `json:"model"`
	BaseURL           string                      `json:"base_url"`
	Temperature       float64                     `json:"temperature"`
	RequestTimeout    string                      `json:"request_timeout"`
	Workload          WorkloadMetadata            `json:"workload"`
	SafetyLimits      SafetyLimits                `json:"safety_limits"`
	Load              LoadMetadata                `json:"load"`
	ClientDiagnostics benchmark.ClientDiagnostics `json:"client_diagnostics"`
	Lifecycle         LifecycleMetadata           `json:"lifecycle"`
	StopAdmission     StopAdmissionMetadata       `json:"stop_admission"`
	Warmup            PhaseMetadata               `json:"warmup"`
	Measurement       PhaseMetadata               `json:"measurement"`
	Drain             DrainMetadata               `json:"drain"`
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
	return w.WriteWithArrivals(metadata, requests, nil)
}

func (w *Writer) WriteWithArrivals(metadata RunMetadata, requests []RequestArtifact, arrivals []benchmark.ArrivalRecord) (string, error) {
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
	orderedArrivals := append([]benchmark.ArrivalRecord(nil), arrivals...)
	sort.Slice(orderedArrivals, func(left, right int) bool {
		if orderedArrivals[left].Phase != orderedArrivals[right].Phase {
			return orderedArrivals[left].Phase < orderedArrivals[right].Phase
		}
		return orderedArrivals[left].Sequence < orderedArrivals[right].Sequence
	})
	if err := validateMetadata(metadata, warmupSummary, measuredSummary, ordered, orderedArrivals); err != nil {
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
	if metadata.Load.Mode == benchmark.LoadModeOpenLoop {
		warmupArrivals := make([]benchmark.ArrivalRecord, 0)
		measuredArrivals := make([]benchmark.ArrivalRecord, 0)
		for _, arrival := range orderedArrivals {
			if arrival.Phase == benchmark.RequestPhaseWarmup {
				warmupArrivals = append(warmupArrivals, arrival)
			} else {
				measuredArrivals = append(measuredArrivals, arrival)
			}
		}
		if err := writeJSONLines(filepath.Join(temporaryDirectory, "warmup", "arrivals.jsonl"), warmupArrivals); err != nil {
			return "", err
		}
		if err := writeJSONLines(filepath.Join(temporaryDirectory, "measured", "arrivals.jsonl"), measuredArrivals); err != nil {
			return "", err
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

func validateMetadata(metadata RunMetadata, warmupArtifacts, measuredArtifacts phaseArtifactSummary, requests []RequestArtifact, arrivals []benchmark.ArrivalRecord) error {
	if err := validateWorkload(metadata); err != nil {
		return err
	}
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
	if metadata.Measurement.Requested <= 0 || metadata.Warmup.Requested < 0 {
		return fmt.Errorf("invalid phase requested counts")
	}
	if metadata.SafetyLimits.MaxConcurrency <= 0 || metadata.SafetyLimits.MaxRequests <= 0 || math.IsNaN(metadata.SafetyLimits.MaxRequestRate) || math.IsInf(metadata.SafetyLimits.MaxRequestRate, 0) || metadata.SafetyLimits.MaxRequestRate <= 0 || metadata.SafetyLimits.MaxInFlight <= 0 || metadata.SafetyLimits.MaxInputTokens <= 0 || metadata.SafetyLimits.MaxOutputTokens <= 0 || metadata.Measurement.Requested > metadata.SafetyLimits.MaxRequests || metadata.Warmup.Requested > metadata.SafetyLimits.MaxRequests {
		return fmt.Errorf("invalid or exceeded safety limits")
	}
	switch metadata.Load.Mode {
	case benchmark.LoadModeClosedLoop:
		if metadata.Load.ClosedLoop == nil || metadata.Load.OpenLoop != nil || len(arrivals) != 0 || metadata.Warmup.Arrivals != nil || metadata.Measurement.Arrivals != nil {
			return fmt.Errorf("invalid closed-loop load metadata")
		}
		if metadata.Load.ClosedLoop.RequestedConcurrency != metadata.Measurement.RequestedConcurrency || metadata.Load.ClosedLoop.RequestedRequests != metadata.Measurement.Requested || metadata.Warmup.RequestedConcurrency != metadata.Measurement.RequestedConcurrency || metadata.Measurement.RequestedConcurrency > metadata.SafetyLimits.MaxConcurrency {
			return fmt.Errorf("closed-loop load metadata differs from phase metadata")
		}
		if err := validateClosedLoopPhase(metadata.Warmup, benchmark.RequestPhaseWarmup, warmupArtifacts, metadata.SafetyLimits); err != nil {
			return fmt.Errorf("warmup metadata: %w", err)
		}
		if err := validateClosedLoopPhase(metadata.Measurement, benchmark.RequestPhaseMeasured, measuredArtifacts, metadata.SafetyLimits); err != nil {
			return fmt.Errorf("measurement metadata: %w", err)
		}
	case benchmark.LoadModeOpenLoop:
		if metadata.Load.OpenLoop == nil || metadata.Load.ClosedLoop != nil || math.IsNaN(metadata.Load.OpenLoop.RequestRate) || math.IsInf(metadata.Load.OpenLoop.RequestRate, 0) || metadata.Load.OpenLoop.RequestRate <= 0 || metadata.Load.OpenLoop.RequestRate > metadata.SafetyLimits.MaxRequestRate || metadata.Load.OpenLoop.MaxInFlight <= 0 || metadata.Load.OpenLoop.MaxInFlight > metadata.SafetyLimits.MaxInFlight || metadata.Load.OpenLoop.PlannedArrivals != metadata.Measurement.Requested {
			return fmt.Errorf("invalid open-loop load metadata")
		}
		if duration, err := time.ParseDuration(metadata.Load.OpenLoop.Duration); err != nil || duration <= 0 {
			return fmt.Errorf("invalid open-loop duration")
		} else {
			planned, countErr := benchmark.PlannedArrivalCount(metadata.Load.OpenLoop.RequestRate, duration)
			if countErr != nil || planned != metadata.Load.OpenLoop.PlannedArrivals {
				return fmt.Errorf("open-loop planned arrival count does not match rate and duration")
			}
			if metadata.Measurement.StartedAt != nil && metadata.StopAdmission.Reason == "measurement_duration_elapsed" && !metadata.StopAdmission.StoppedAt.Equal(metadata.Measurement.StartedAt.Add(duration)) {
				return fmt.Errorf("open-loop stop-admission boundary does not match measurement duration")
			}
		}
		if !metadata.Drain.ParentCancelled && metadata.StopAdmission.Reason != "measurement_duration_elapsed" {
			return fmt.Errorf("normal open-loop run has invalid stop-admission reason")
		}
		requestBySequence := make(map[string]RequestArtifact, len(requests))
		for _, request := range requests {
			requestBySequence[fmt.Sprintf("%s:%d", request.Phase, request.Sequence)] = request
		}
		if err := validateOpenLoopPhase(metadata.Warmup, benchmark.RequestPhaseWarmup, warmupArtifacts, arrivals, requestBySequence, metadata.Load.OpenLoop.RequestRate, metadata.Load.OpenLoop.MaxInFlight); err != nil {
			return fmt.Errorf("warmup metadata: %w", err)
		}
		if err := validateOpenLoopPhase(metadata.Measurement, benchmark.RequestPhaseMeasured, measuredArtifacts, arrivals, requestBySequence, metadata.Load.OpenLoop.RequestRate, metadata.Load.OpenLoop.MaxInFlight); err != nil {
			return fmt.Errorf("measurement metadata: %w", err)
		}
	default:
		return fmt.Errorf("invalid load mode %q", metadata.Load.Mode)
	}
	if metadata.Drain.Timeout == "" || metadata.Drain.ElapsedNS < 0 || metadata.Drain.CancelledRequests != len(metadata.Drain.CancelledRequestIDs) {
		return fmt.Errorf("invalid drain metadata")
	}
	switch metadata.RunStatus {
	case RunStatusCompleted:
		if metadata.Error != "" || metadata.ErrorClass != "" || metadata.Warmup.Failed != 0 || metadata.Measurement.Failed != 0 || metadata.Drain.TimedOut || metadata.Drain.ParentCancelled {
			return fmt.Errorf("completed run has inconsistent status metadata")
		}
		if metadata.Load.Mode == benchmark.LoadModeClosedLoop && (metadata.Warmup.Attempted != metadata.Warmup.Requested || metadata.Measurement.Attempted != metadata.Measurement.Requested) {
			return fmt.Errorf("completed closed-loop run did not attempt every request")
		}
		if metadata.Load.Mode == benchmark.LoadModeOpenLoop && (metadata.Measurement.Arrivals.ClientLimited != 0 || metadata.Measurement.Arrivals.SchedulerLimited != 0 || metadata.Measurement.Arrivals.UnprocessedDueToCancellation != 0) {
			return fmt.Errorf("completed open-loop run did not faithfully deliver its schedule")
		}
	case RunStatusCancelled, RunStatusFailed:
		if metadata.Error == "" {
			return fmt.Errorf("non-completed run error is required")
		}
	default:
		return fmt.Errorf("invalid run status %q", metadata.RunStatus)
	}
	if metadata.ErrorClass == ErrorClassLoadDelivery {
		if metadata.Load.Mode != benchmark.LoadModeOpenLoop || metadata.RunStatus != RunStatusFailed || metadata.Measurement.Arrivals == nil || metadata.Measurement.Arrivals.ClientLimited+metadata.Measurement.Arrivals.SchedulerLimited == 0 || metadata.Drain.ParentCancelled {
			return fmt.Errorf("invalid load-delivery error classification")
		}
	}
	return nil
}

func validateWorkload(metadata RunMetadata) error {
	value := metadata.Workload
	if value.Output.RequestedMaxTokens <= 0 || value.Output.RequestedMaxTokens > metadata.SafetyLimits.MaxOutputTokens {
		return fmt.Errorf("invalid or unsafe requested workload output tokens")
	}
	if value.PromptBytes < 0 || len(value.PromptSHA256) != sha256HexLength {
		return fmt.Errorf("invalid workload prompt metadata")
	}
	if _, err := hex.DecodeString(value.PromptSHA256); err != nil {
		return fmt.Errorf("invalid workload prompt SHA-256")
	}
	switch value.Mode {
	case workload.ModePrompt:
		if value.PromptBytes <= 0 || value.Input != nil || value.Builder != nil || value.Tokenizer != nil {
			return fmt.Errorf("direct prompt workload contains token-length metadata")
		}
	case workload.ModeTokenLength:
		if value.Input == nil || value.Builder == nil || value.Tokenizer == nil {
			return fmt.Errorf("token-length workload metadata is incomplete")
		}
		if value.Input.Contract != workload.ContractRenderedChatInput || value.Input.TargetTokens <= 0 || value.Input.ResolvedTokens != value.Input.TargetTokens || value.Input.TargetTokens > metadata.SafetyLimits.MaxInputTokens {
			return fmt.Errorf("invalid token-length input metadata")
		}
		if value.Builder.Kind != workload.BuilderKindDeterministic || value.Builder.Version == "" {
			return fmt.Errorf("invalid workload builder identity")
		}
		identity := value.Tokenizer
		if identity.Adapter != "vllm_chat_render" || identity.AdapterVersion == "" || identity.Contract != workload.ContractRenderedChatInput || identity.Model != metadata.Model || identity.Source == "" || identity.ModelMaxLength <= 0 || len(identity.BehavioralFingerprintSHA256) != sha256HexLength {
			return fmt.Errorf("invalid tokenizer identity")
		}
		if _, err := hex.DecodeString(identity.BehavioralFingerprintSHA256); err != nil {
			return fmt.Errorf("invalid tokenizer behavioral fingerprint")
		}
		if value.Input.TargetTokens > maxInt()-value.Output.RequestedMaxTokens || value.Input.TargetTokens+value.Output.RequestedMaxTokens > identity.ModelMaxLength {
			return fmt.Errorf("workload exceeds tokenizer model context")
		}
	default:
		return fmt.Errorf("invalid workload mode %q", value.Mode)
	}
	return nil
}

const sha256HexLength = 64

func validatePhaseCommon(phase PhaseMetadata, wantPhase benchmark.RequestPhase, artifacts phaseArtifactSummary, safety SafetyLimits) error {
	if phase.Phase != wantPhase || phase.Requested < 0 || phase.Attempted < 0 || phase.Completed < 0 || phase.Successful < 0 || phase.Failed < 0 {
		return fmt.Errorf("invalid phase values")
	}
	if phase.Attempted > phase.Requested || phase.Attempted != phase.Completed || phase.Completed != artifacts.count || phase.Successful+phase.Failed != phase.Completed || phase.Outcomes.Total() != phase.Completed || phase.Outcomes.Succeeded != phase.Successful || phase.Outcomes.Failed() != phase.Failed || phase.Outcomes != artifacts.outcomes {
		return fmt.Errorf("inconsistent phase counts")
	}
	if phase.ElapsedNS < 0 || phase.Requested > safety.MaxRequests {
		return fmt.Errorf("invalid phase timing or safety")
	}
	return nil
}

func validateClosedLoopPhase(phase PhaseMetadata, wantPhase benchmark.RequestPhase, artifacts phaseArtifactSummary, safety SafetyLimits) error {
	if err := validatePhaseCommon(phase, wantPhase, artifacts, safety); err != nil {
		return err
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

func validateOpenLoopPhase(phase PhaseMetadata, wantPhase benchmark.RequestPhase, artifacts phaseArtifactSummary, arrivals []benchmark.ArrivalRecord, requests map[string]RequestArtifact, requestRate float64, maxInFlight int) error {
	if err := validatePhaseCommon(phase, wantPhase, artifacts, SafetyLimits{MaxRequests: maxInt()}); err != nil {
		return err
	}
	if phase.RequestedConcurrency != 0 || phase.EffectiveWorkers != 0 || phase.Arrivals == nil {
		return fmt.Errorf("open-loop phase contains closed-loop worker metadata or lacks arrival counts")
	}
	counts := *phase.Arrivals
	if counts.Planned != phase.Requested || counts.Processed < 0 || counts.Started < 0 || counts.ClientLimited < 0 || counts.SchedulerLimited < 0 || counts.UnprocessedDueToCancellation < 0 || counts.MaxObservedInFlight < 0 || counts.MaxObservedInFlight > maxInFlight || counts.MaxObservedInFlight > counts.Started {
		return fmt.Errorf("invalid arrival counts")
	}
	if wantPhase == benchmark.RequestPhaseWarmup && counts.SchedulerLimited != 0 {
		return fmt.Errorf("warmup cannot contain scheduler-limited arrivals")
	}
	phaseArrivals := make([]benchmark.ArrivalRecord, 0, counts.Processed)
	for _, arrival := range arrivals {
		if arrival.Phase == wantPhase {
			phaseArrivals = append(phaseArrivals, arrival)
		}
	}
	if counts.Processed != len(phaseArrivals) || counts.Processed != counts.Started+counts.ClientLimited+counts.SchedulerLimited || counts.Planned != counts.Processed+counts.UnprocessedDueToCancellation || counts.Started != artifacts.count || phase.Attempted != counts.Started {
		return fmt.Errorf("inconsistent arrival counts")
	}
	if phase.Status == benchmark.PhaseStatusSkipped {
		if len(phaseArrivals) != 0 || counts.Processed != 0 || counts.Started != 0 || counts.ClientLimited != 0 || counts.SchedulerLimited != 0 || (counts.UnprocessedDueToCancellation != 0 && counts.UnprocessedDueToCancellation != counts.Planned) {
			return fmt.Errorf("skipped open-loop phase contains arrivals")
		}
		return nil
	}
	if phase.StartedAt == nil {
		return fmt.Errorf("open-loop phase start is required")
	}
	if phase.Status != benchmark.PhaseStatusCompleted && phase.Status != benchmark.PhaseStatusFailed && phase.Status != benchmark.PhaseStatusCancelled {
		return fmt.Errorf("invalid phase status %q", phase.Status)
	}
	seen := make(map[int]struct{}, len(phaseArrivals))
	expectedOffsets, err := benchmark.ArrivalOffsets(requestRate, counts.Planned)
	if err != nil {
		return fmt.Errorf("derive arrival offsets: %w", err)
	}
	started := 0
	for _, arrival := range phaseArrivals {
		if arrival.Sequence <= 0 || arrival.Sequence > counts.Planned || arrival.ScheduledAfterNS < 0 || !arrival.ScheduledAt.Equal(phase.StartedAt.Add(time.Duration(arrival.ScheduledAfterNS))) {
			return fmt.Errorf("invalid arrival identity or scheduled timing")
		}
		if arrival.ScheduledAfterNS != expectedOffsets[arrival.Sequence-1].Nanoseconds() {
			return fmt.Errorf("arrival %d does not match the configured absolute schedule", arrival.Sequence)
		}
		if _, exists := seen[arrival.Sequence]; exists {
			return fmt.Errorf("duplicate arrival sequence %d", arrival.Sequence)
		}
		seen[arrival.Sequence] = struct{}{}
		request, requestExists := requests[fmt.Sprintf("%s:%d", wantPhase, arrival.Sequence)]
		switch arrival.Disposition {
		case benchmark.ArrivalStarted:
			started++
			if arrival.RequestID == nil || arrival.ActualStartedAt == nil || arrival.ActualStartedAfterNS == nil || arrival.SchedulerLagNS == nil || *arrival.SchedulerLagNS < 0 || !requestExists || *arrival.RequestID != request.Observation.RequestID || request.Observation.RequestStartedAt == nil || !arrival.ActualStartedAt.Equal(*request.Observation.RequestStartedAt) || *arrival.ActualStartedAfterNS != arrival.ActualStartedAt.Sub(*phase.StartedAt).Nanoseconds() || *arrival.SchedulerLagNS != arrival.ActualStartedAt.Sub(arrival.ScheduledAt).Nanoseconds() {
				return fmt.Errorf("started arrival %d has inconsistent request timing or identity", arrival.Sequence)
			}
		case benchmark.ArrivalClientLimited, benchmark.ArrivalSchedulerLimited:
			if arrival.RequestID != nil || arrival.ActualStartedAt != nil || arrival.ActualStartedAfterNS != nil || arrival.SchedulerLagNS != nil || requestExists {
				return fmt.Errorf("unstarted arrival %d contains request evidence", arrival.Sequence)
			}
		default:
			return fmt.Errorf("invalid arrival disposition %q", arrival.Disposition)
		}
	}
	if started != counts.Started {
		return fmt.Errorf("started arrival records differ from counts")
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

func writeJSONLines(path string, values []benchmark.ArrivalRecord) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = file.Close()
			return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
