package benchmark

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLifecycleWarmupCompletesBeforeMeasurementAndFailuresRemainSeparate(t *testing.T) {
	executor := &lifecycleRecordingExecutor{warmupRequested: 4, failures: map[string]error{"warmup-000002": errors.New("warmup fixture failure")}}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), lifecycleTestPlan(2, 4, 3, time.Second))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Warmup.Status != PhaseStatusFailed || result.Warmup.Outcomes.Succeeded != 3 || result.Warmup.Outcomes.RequestError != 1 {
		t.Fatalf("warmup = %+v", result.Warmup)
	}
	if result.Measurement.Status != PhaseStatusCompleted || result.Measurement.Outcomes.Succeeded != 3 || len(result.Measurement.Completed) != 3 {
		t.Fatalf("measurement = %+v", result.Measurement)
	}
	if result.Warmup.MaxObservedActive != 2 || result.Measurement.MaxObservedActive != 2 {
		t.Fatalf("max active warmup/measured = %d/%d", result.Warmup.MaxObservedActive, result.Measurement.MaxObservedActive)
	}
	if result.Warmup.CompletedAt == nil || result.Measurement.StartedAt == nil || result.Measurement.StartedAt.Before(*result.Warmup.CompletedAt) {
		t.Fatalf("phase timing overlaps: warmup=%+v measurement=%+v", result.Warmup, result.Measurement)
	}
	if executor.measurementBeforeWarmupComplete.Load() {
		t.Fatal("measurement started before all warmup calls returned")
	}
	assertTransitionPhases(t, result.Transitions, []LifecyclePhase{PhaseSetup, PhaseWarmup, PhaseMeasurement, PhaseStopAdmission, PhaseDrain, PhaseArtifacts})
	assertPhaseIDs(t, result.Warmup.Completed, "warmup-", 4)
	assertPhaseIDs(t, result.Measurement.Completed, "req-", 3)
}

func TestLifecycleZeroWarmupIsExplicitlySkipped(t *testing.T) {
	result, err := NewLifecycleCoordinator(NewRunner(&lifecycleRecordingExecutor{})).Run(context.Background(), lifecycleTestPlan(1, 0, 1, time.Second))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Warmup.Status != PhaseStatusSkipped || result.Warmup.RequestedRequests != 0 || len(result.Warmup.Completed) != 0 || result.Warmup.StartedAt == nil || result.Warmup.CompletedAt == nil {
		t.Fatalf("warmup = %+v", result.Warmup)
	}
	if result.Transitions[1].Phase != PhaseWarmup || result.Transitions[1].Reason != "warmup_skipped" {
		t.Fatalf("warmup transition = %+v", result.Transitions[1])
	}
}

func TestLifecycleDrainTimeoutCancelsAndClassifiesOutstandingRequests(t *testing.T) {
	executor := &lifecycleBlockingExecutor{started: make(chan string, 4)}
	plan := lifecycleTestPlan(2, 0, 2, 25*time.Millisecond)
	plan.RequestTimeout = time.Second
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), plan)
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("Run error = %v, want ErrDrainTimeout", err)
	}
	if !result.Drain.TimedOut || result.Drain.ParentCancelled || len(result.Drain.CancelledRequestIDs) != 2 {
		t.Fatalf("drain = %+v", result.Drain)
	}
	if result.Measurement.Outcomes.DrainTimeout != 2 || result.Measurement.Status != PhaseStatusFailed {
		t.Fatalf("measurement = %+v", result.Measurement)
	}
	if executor.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", executor.calls.Load())
	}
}

func TestLifecycleParentCancellationDuringWarmupSkipsMeasurementAndDrains(t *testing.T) {
	executor := &lifecycleBlockingExecutor{started: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result LifecycleResult
		err    error
	}, 1)
	go func() {
		result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(ctx, lifecycleTestPlan(2, 5, 3, time.Second))
		done <- struct {
			result LifecycleResult
			err    error
		}{result: result, err: err}
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			t.Fatal("warmup workers did not start")
		}
	}
	cancel()
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.Canceled) || !outcome.result.Drain.ParentCancelled {
			t.Fatalf("outcome error/drain = %v/%+v", outcome.err, outcome.result.Drain)
		}
		if outcome.result.Measurement.Status != PhaseStatusSkipped || len(outcome.result.Measurement.Completed) != 0 {
			t.Fatalf("measurement = %+v", outcome.result.Measurement)
		}
		if outcome.result.Warmup.Outcomes.ParentCancelled != 2 || executor.calls.Load() != 2 {
			t.Fatalf("warmup/calls = %+v/%d", outcome.result.Warmup.Outcomes, executor.calls.Load())
		}
		assertTransitionPhases(t, outcome.result.Transitions, []LifecyclePhase{PhaseSetup, PhaseWarmup, PhaseStopAdmission, PhaseDrain, PhaseArtifacts})
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not drain after cancellation")
	}
}

func TestLifecycleParentCancellationDuringMeasuredDrainWinsBeforeDrainTimeout(t *testing.T) {
	executor := &lifecycleBlockingExecutor{started: make(chan string, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result LifecycleResult
		err    error
	}, 1)
	go func() {
		result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(ctx, lifecycleTestPlan(2, 0, 2, time.Second))
		done <- struct {
			result LifecycleResult
			err    error
		}{result: result, err: err}
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			t.Fatal("measurement workers did not start")
		}
	}
	cancel()
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) || outcome.result.Drain.TimedOut || !outcome.result.Drain.ParentCancelled {
		t.Fatalf("outcome error/drain = %v/%+v", outcome.err, outcome.result.Drain)
	}
	if outcome.result.Measurement.Outcomes.ParentCancelled != 2 || outcome.result.Measurement.Outcomes.DrainTimeout != 0 {
		t.Fatalf("measurement outcomes = %+v", outcome.result.Measurement.Outcomes)
	}
}

func TestLifecycleCancellationDuringMeasurementStopsFurtherClaims(t *testing.T) {
	executor := &lifecycleBlockingExecutor{started: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result LifecycleResult
		err    error
	}, 1)
	go func() {
		result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(ctx, lifecycleTestPlan(2, 0, 10, time.Second))
		done <- struct {
			result LifecycleResult
			err    error
		}{result: result, err: err}
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			t.Fatal("measurement workers did not start")
		}
	}
	cancel()
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) || executor.calls.Load() != 2 || len(outcome.result.Measurement.Completed) != 2 {
		t.Fatalf("error/calls/measurement = %v/%d/%+v", outcome.err, executor.calls.Load(), outcome.result.Measurement)
	}
	if outcome.result.Measurement.Outcomes.ParentCancelled != 2 || outcome.result.Measurement.Status != PhaseStatusCancelled {
		t.Fatalf("measurement = %+v", outcome.result.Measurement)
	}
	assertTransitionPhases(t, outcome.result.Transitions, []LifecyclePhase{PhaseSetup, PhaseWarmup, PhaseMeasurement, PhaseStopAdmission, PhaseDrain, PhaseArtifacts})
}

func TestLifecycleAlreadyCancelledDuringSetupSkipsRequestPhases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &lifecycleRecordingExecutor{}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(ctx, lifecycleTestPlan(2, 2, 3, time.Second))
	if !errors.Is(err, context.Canceled) || !result.Drain.ParentCancelled {
		t.Fatalf("error/drain = %v/%+v", err, result.Drain)
	}
	if result.Warmup.Status != PhaseStatusSkipped || result.Measurement.Status != PhaseStatusSkipped || result.Warmup.WorkerCount != 0 || result.Measurement.WorkerCount != 0 {
		t.Fatalf("phases = warmup %+v measurement %+v", result.Warmup, result.Measurement)
	}
	assertTransitionPhases(t, result.Transitions, []LifecyclePhase{PhaseSetup, PhaseStopAdmission, PhaseDrain, PhaseArtifacts})
}

func TestLifecyclePerRequestTimeoutsDoNotStopLaterClaims(t *testing.T) {
	executor := &lifecycleBlockingExecutor{started: make(chan string, 4)}
	plan := lifecycleTestPlan(1, 0, 3, time.Second)
	plan.RequestTimeout = 10 * time.Millisecond
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executor.calls.Load() != 3 || result.Measurement.Outcomes.RequestTimeout != 3 || result.Measurement.Status != PhaseStatusFailed {
		t.Fatalf("calls/measurement = %d/%+v", executor.calls.Load(), result.Measurement)
	}
}

func TestLifecycleDrainWaitsForActiveRequests(t *testing.T) {
	executor := &releasableLifecycleExecutor{started: make(chan struct{}, 2), release: make(chan struct{})}
	done := make(chan struct {
		result LifecycleResult
		err    error
	}, 1)
	go func() {
		result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), lifecycleTestPlan(2, 0, 2, time.Second))
		done <- struct {
			result LifecycleResult
			err    error
		}{result: result, err: err}
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			t.Fatal("measured request did not start")
		}
	}
	select {
	case <-done:
		t.Fatal("lifecycle returned before active requests drained")
	default:
	}
	close(executor.release)
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Measurement.Outcomes.Succeeded != 2 || outcome.result.Drain.TimedOut {
			t.Fatalf("outcome = %+v, err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not finish after active requests drained")
	}
}

func TestClaimNextNeverAdvancesPastLimit(t *testing.T) {
	var sequence atomic.Int64
	for want := 1; want <= 3; want++ {
		got, ok := claimNext(context.Background(), &sequence, 3)
		if !ok || got != want {
			t.Fatalf("claim = %d/%v, want %d/true", got, ok, want)
		}
	}
	if got, ok := claimNext(context.Background(), &sequence, 3); ok || got != 0 || sequence.Load() != 3 {
		t.Fatalf("past-limit claim = %d/%v, sequence=%d", got, ok, sequence.Load())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := claimNext(cancelled, &sequence, 4); ok || sequence.Load() != 3 {
		t.Fatalf("cancelled claim advanced sequence to %d", sequence.Load())
	}
}

func TestLifecycleStateRejectsIllegalTransitions(t *testing.T) {
	started := time.Now()
	state := newLifecycleState(started)
	if err := state.transition(PhaseMeasurement, started, "illegal"); err == nil {
		t.Fatal("setup -> measurement unexpectedly succeeded")
	}
	if err := state.transition(PhaseWarmup, started, "warmup"); err != nil {
		t.Fatalf("setup -> warmup: %v", err)
	}
	if err := state.transition(PhaseArtifacts, started, "illegal"); err == nil {
		t.Fatal("warmup -> artifacts unexpectedly succeeded")
	}
}

func TestLifecycleStateAcceptsEveryLegalTransition(t *testing.T) {
	tests := []struct {
		name   string
		phases []LifecyclePhase
	}{
		{name: "normal", phases: []LifecyclePhase{PhaseWarmup, PhaseMeasurement, PhaseStopAdmission, PhaseDrain, PhaseArtifacts}},
		{name: "cancel setup", phases: []LifecyclePhase{PhaseStopAdmission, PhaseDrain, PhaseArtifacts}},
		{name: "cancel warmup", phases: []LifecyclePhase{PhaseWarmup, PhaseStopAdmission, PhaseDrain, PhaseArtifacts}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			state := newLifecycleState(started)
			for _, phase := range test.phases {
				if err := state.transition(phase, started, "test"); err != nil {
					t.Fatalf("transition to %s: %v", phase, err)
				}
			}
		})
	}
}

func lifecycleTestPlan(concurrency, warmup, measured int, drainTimeout time.Duration) LifecyclePlan {
	return LifecyclePlan{
		RunID:           "run-lifecycle-test",
		RequestTemplate: Request{Model: "model", Prompt: "private prompt", MaxOutputTokens: 4, Temperature: 0},
		Concurrency:     concurrency, WarmupRequests: warmup, MeasuredRequests: measured,
		RequestTimeout: time.Second, DrainTimeout: drainTimeout,
	}
}

type lifecycleRecordingExecutor struct {
	mu                              sync.Mutex
	warmupRequested                 int
	warmupCompleted                 int
	failures                        map[string]error
	measurementBeforeWarmupComplete atomic.Bool
}

func (e *lifecycleRecordingExecutor) Execute(ctx context.Context, request Request, observation *RequestObservation) error {
	isWarmup := strings.HasPrefix(request.RequestID, "warmup-")
	if !isWarmup {
		e.mu.Lock()
		if e.warmupCompleted != e.warmupRequested {
			e.measurementBeforeWarmupComplete.Store(true)
		}
		e.mu.Unlock()
	}
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	err := e.failures[request.RequestID]
	if isWarmup {
		e.warmupCompleted++
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
	return nil
}

type lifecycleBlockingExecutor struct {
	started chan string
	calls   atomic.Int64
}

func (e *lifecycleBlockingExecutor) Execute(ctx context.Context, request Request, _ *RequestObservation) error {
	e.calls.Add(1)
	e.started <- request.RequestID
	<-ctx.Done()
	return ctx.Err()
}

type releasableLifecycleExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *releasableLifecycleExecutor) Execute(ctx context.Context, _ Request, observation *RequestObservation) error {
	e.started <- struct{}{}
	select {
	case <-e.release:
		observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func assertTransitionPhases(t *testing.T, transitions []PhaseTransition, want []LifecyclePhase) {
	t.Helper()
	if len(transitions) != len(want) {
		t.Fatalf("transitions = %+v, want phases %v", transitions, want)
	}
	for index, phase := range want {
		if transitions[index].Phase != phase || transitions[index].Sequence != index+1 {
			t.Fatalf("transition %d = %+v, want phase %s", index, transitions[index], phase)
		}
		if index > 0 && transitions[index].EnteredAfterNS < transitions[index-1].EnteredAfterNS {
			t.Fatalf("transition offsets decrease: %+v", transitions)
		}
	}
}

func assertPhaseIDs(t *testing.T, completed []CompletedRequest, prefix string, count int) {
	t.Helper()
	seen := make(map[string]bool, count)
	for _, request := range completed {
		id := request.Result.Observation.RequestID
		if !strings.HasPrefix(id, prefix) {
			t.Fatalf("request ID = %q, want prefix %q", id, prefix)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("unique IDs = %v, want %d", seen, count)
	}
}
