package benchmark

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrDrainTimeout = errors.New("benchmark drain timeout expired")

type LifecyclePhase string

const (
	PhaseSetup         LifecyclePhase = "setup"
	PhaseWarmup        LifecyclePhase = "warmup"
	PhaseMeasurement   LifecyclePhase = "measurement"
	PhaseStopAdmission LifecyclePhase = "stop_admission"
	PhaseDrain         LifecyclePhase = "drain"
	PhaseArtifacts     LifecyclePhase = "artifacts"
)

type RequestPhase string

const (
	RequestPhaseWarmup   RequestPhase = "warmup"
	RequestPhaseMeasured RequestPhase = "measured"
)

type RequestOutcome string

const (
	OutcomeSucceeded       RequestOutcome = "succeeded"
	OutcomeRequestError    RequestOutcome = "request_error"
	OutcomeRequestTimeout  RequestOutcome = "request_timeout"
	OutcomeParentCancelled RequestOutcome = "parent_cancelled"
	OutcomeDrainTimeout    RequestOutcome = "drain_timeout"
)

const (
	PhaseStatusSkipped   = "skipped"
	PhaseStatusCompleted = "completed"
	PhaseStatusFailed    = "failed"
	PhaseStatusCancelled = "cancelled"
)

type LifecyclePlan struct {
	RunID            string
	RequestTemplate  Request
	Concurrency      int
	WarmupRequests   int
	MeasuredRequests int
	RequestTimeout   time.Duration
	DrainTimeout     time.Duration
}

type PhaseTransition struct {
	Sequence       int            `json:"sequence"`
	Phase          LifecyclePhase `json:"phase"`
	EnteredAt      time.Time      `json:"entered_at"`
	EnteredAfterNS int64          `json:"entered_after_ns"`
	Reason         string         `json:"reason"`
}

type OutcomeCounts struct {
	Succeeded       int `json:"succeeded"`
	RequestError    int `json:"request_error"`
	RequestTimeout  int `json:"request_timeout"`
	ParentCancelled int `json:"parent_cancelled"`
	DrainTimeout    int `json:"drain_timeout"`
}

func (c OutcomeCounts) Failed() int {
	return c.RequestError + c.RequestTimeout + c.ParentCancelled + c.DrainTimeout
}

func (c OutcomeCounts) Total() int {
	return c.Succeeded + c.Failed()
}

type PhaseResult struct {
	Phase                RequestPhase
	Status               string
	StartedAt            *time.Time
	CompletedAt          *time.Time
	ElapsedNS            int64
	RequestedRequests    int
	RequestedConcurrency int
	WorkerCount          int
	MaxObservedActive    int
	Outcomes             OutcomeCounts
	Completed            []CompletedRequest
}

type DrainResult struct {
	StartedAt           *time.Time
	CompletedAt         *time.Time
	ElapsedNS           int64
	Timeout             time.Duration
	TimedOut            bool
	ParentCancelled     bool
	CancelledRequestIDs []string
}

type LifecycleResult struct {
	StartedAt   time.Time
	CompletedAt time.Time
	ElapsedNS   int64
	Transitions []PhaseTransition
	Warmup      PhaseResult
	Measurement PhaseResult
	Drain       DrainResult
}

type LifecycleCoordinator struct {
	runner   *Runner
	now      func() time.Time
	newTimer func(time.Duration) *time.Timer
}

func NewLifecycleCoordinator(runner *Runner) *LifecycleCoordinator {
	return &LifecycleCoordinator{runner: runner, now: time.Now, newTimer: time.NewTimer}
}

func (c *LifecycleCoordinator) Run(ctx context.Context, plan LifecyclePlan) (LifecycleResult, error) {
	if err := c.validate(ctx, plan); err != nil {
		return LifecycleResult{}, err
	}
	started := c.now()
	state := newLifecycleState(started)
	result := LifecycleResult{
		StartedAt:   started,
		Warmup:      skippedPhaseResult(RequestPhaseWarmup, plan.WarmupRequests, plan.Concurrency),
		Measurement: skippedPhaseResult(RequestPhaseMeasured, plan.MeasuredRequests, plan.Concurrency),
		Drain:       DrainResult{Timeout: plan.DrainTimeout, CancelledRequestIDs: make([]string, 0)},
	}

	enterStopAndDrain := func(at time.Time, reason string) error {
		if state.current == PhaseStopAdmission || state.current == PhaseDrain {
			return nil
		}
		if err := state.transition(PhaseStopAdmission, at, reason); err != nil {
			return err
		}
		if err := state.transition(PhaseDrain, at, reason); err != nil {
			return err
		}
		observed := at
		result.Drain.StartedAt = &observed
		return nil
	}

	if ctx.Err() != nil {
		at := c.now()
		if err := enterStopAndDrain(at, "parent_cancelled_during_setup"); err != nil {
			return LifecycleResult{}, err
		}
		result.Drain.ParentCancelled = true
		return c.finishLifecycle(state, result, ctx.Err())
	}

	warmupStarted := c.now()
	if err := state.transition(PhaseWarmup, warmupStarted, phaseReason(plan.WarmupRequests == 0, "warmup_started")); err != nil {
		return LifecycleResult{}, err
	}
	if plan.WarmupRequests == 0 {
		result.Warmup.StartedAt = timePointer(warmupStarted)
		result.Warmup.CompletedAt = timePointer(warmupStarted)
	} else {
		warmup, warmupErr := c.runPhase(ctx, phaseExecutionPlan{
			lifecyclePlan: plan,
			phase:         RequestPhaseWarmup,
			requests:      plan.WarmupRequests,
			startedAt:     warmupStarted,
			onParentCancellation: func(at time.Time) error {
				result.Drain.ParentCancelled = true
				return enterStopAndDrain(at, "parent_cancelled_during_warmup")
			},
		})
		result.Warmup = warmup
		appendCancelledIDs(&result.Drain, warmup.Completed)
		if warmupErr != nil {
			return c.finishLifecycle(state, result, warmupErr)
		}
	}

	measurementStarted := c.now()
	if err := state.transition(PhaseMeasurement, measurementStarted, "warmup_complete"); err != nil {
		return LifecycleResult{}, err
	}
	measurement, measurementErr := c.runPhase(ctx, phaseExecutionPlan{
		lifecyclePlan: plan,
		phase:         RequestPhaseMeasured,
		requests:      plan.MeasuredRequests,
		startedAt:     measurementStarted,
		drainTimeout:  plan.DrainTimeout,
		onAdmissionStopped: func(at time.Time) error {
			return enterStopAndDrain(at, "measured_request_limit_reached")
		},
		onParentCancellation: func(at time.Time) error {
			result.Drain.ParentCancelled = true
			return enterStopAndDrain(at, "parent_cancelled_during_measurement")
		},
	})
	result.Measurement = measurement
	result.Drain.TimedOut = errors.Is(measurementErr, ErrDrainTimeout)
	if ctx.Err() != nil {
		result.Drain.ParentCancelled = true
	}
	appendCancelledIDs(&result.Drain, measurement.Completed)
	return c.finishLifecycle(state, result, measurementErr)
}

func (c *LifecycleCoordinator) finishLifecycle(state *lifecycleState, result LifecycleResult, runErr error) (LifecycleResult, error) {
	completed := c.now()
	if result.Drain.StartedAt != nil {
		result.Drain.CompletedAt = timePointer(completed)
		result.Drain.ElapsedNS = completed.Sub(*result.Drain.StartedAt).Nanoseconds()
	}
	if state.current != PhaseDrain {
		if err := state.transition(PhaseStopAdmission, completed, "phase_complete"); err != nil {
			return LifecycleResult{}, err
		}
		if err := state.transition(PhaseDrain, completed, "phase_complete"); err != nil {
			return LifecycleResult{}, err
		}
		result.Drain.StartedAt = timePointer(completed)
		result.Drain.CompletedAt = timePointer(completed)
	}
	if err := state.transition(PhaseArtifacts, completed, "drain_complete"); err != nil {
		return LifecycleResult{}, err
	}
	result.CompletedAt = completed
	result.ElapsedNS = completed.Sub(result.StartedAt).Nanoseconds()
	result.Transitions = append([]PhaseTransition(nil), state.transitions...)
	sort.Strings(result.Drain.CancelledRequestIDs)
	return result, runErr
}

type phaseExecutionPlan struct {
	lifecyclePlan        LifecyclePlan
	phase                RequestPhase
	requests             int
	startedAt            time.Time
	drainTimeout         time.Duration
	onAdmissionStopped   func(time.Time) error
	onParentCancellation func(time.Time) error
}

func (c *LifecycleCoordinator) runPhase(ctx context.Context, plan phaseExecutionPlan) (PhaseResult, error) {
	workerCount := plan.lifecyclePlan.Concurrency
	if plan.requests < workerCount {
		workerCount = plan.requests
	}
	result := PhaseResult{
		Phase:                plan.phase,
		Status:               PhaseStatusCompleted,
		StartedAt:            timePointer(plan.startedAt),
		RequestedRequests:    plan.requests,
		RequestedConcurrency: plan.lifecyclePlan.Concurrency,
		WorkerCount:          workerCount,
		Completed:            make([]CompletedRequest, 0, workerCount),
	}

	phaseCtx, cancelPhase := context.WithCancelCause(ctx)
	defer cancelPhase(nil)
	resultsCh := make(chan CompletedRequest, workerCount)
	finalClaimCh := make(chan finalClaimBoundary, 1)
	var workers sync.WaitGroup
	var nextSequence atomic.Int64
	var active atomic.Int64
	var maxActive atomic.Int64

	worker := func() {
		defer workers.Done()
		for {
			sequence, ok := claimNext(phaseCtx, &nextSequence, plan.requests)
			if !ok {
				return
			}
			requestID, err := phaseRequestID(plan.phase, sequence)
			if err != nil {
				return
			}
			if plan.phase == RequestPhaseMeasured && sequence == plan.requests {
				boundary := finalClaimBoundary{observedAt: c.now(), admissionStopped: make(chan struct{})}
				finalClaimCh <- boundary
				<-boundary.admissionStopped
			}
			request := plan.lifecyclePlan.RequestTemplate
			request.RunID = plan.lifecyclePlan.RunID
			request.RequestID = requestID
			requestCtx, cancelRequest := context.WithTimeout(phaseCtx, plan.lifecyclePlan.RequestTimeout)
			current := active.Add(1)
			updateMaximum(&maxActive, current)
			runResult := c.runner.RunRequest(requestCtx, request)
			active.Add(-1)
			outcome := classifyOutcome(ctx, requestCtx, runResult)
			cancelRequest()
			resultsCh <- CompletedRequest{Sequence: sequence, Phase: plan.phase, Outcome: outcome, Result: runResult}
		}
	}

	workers.Add(workerCount)
	for index := 0; index < workerCount; index++ {
		go worker()
	}
	go func() {
		workers.Wait()
		close(resultsCh)
	}()

	var phaseErr error
	parentDone := ctx.Done()
	var drainTimer *time.Timer
	var drainTimerCh <-chan time.Time
	finalClaimSeen := plan.phase != RequestPhaseMeasured
	resultsOpen := true
	for resultsOpen || !finalClaimSeen {
		select {
		case boundary := <-finalClaimCh:
			if finalClaimSeen {
				close(boundary.admissionStopped)
				continue
			}
			finalClaimSeen = true
			if plan.onAdmissionStopped != nil {
				if err := plan.onAdmissionStopped(boundary.observedAt); err != nil {
					cancelPhase(err)
					phaseErr = err
				}
			}
			if plan.drainTimeout > 0 {
				drainTimer = c.newTimer(plan.drainTimeout)
				drainTimerCh = drainTimer.C
			}
			close(boundary.admissionStopped)
		case <-parentDone:
			parentDone = nil
			if plan.onParentCancellation != nil {
				if err := plan.onParentCancellation(c.now()); err != nil && phaseErr == nil {
					phaseErr = err
				}
			}
			if phaseErr == nil {
				phaseErr = ctx.Err()
			}
			if plan.phase == RequestPhaseMeasured && !finalClaimSeen {
				finalClaimSeen = true
			}
		case <-drainTimerCh:
			drainTimerCh = nil
			if active.Load() > 0 {
				cancelPhase(ErrDrainTimeout)
				phaseErr = ErrDrainTimeout
			}
		case completed, ok := <-resultsCh:
			if !ok {
				resultsOpen = false
				resultsCh = nil
				continue
			}
			result.Completed = append(result.Completed, completed)
			incrementOutcome(&result.Outcomes, completed.Outcome)
		}
	}
	if ctx.Err() != nil && parentDone != nil {
		if plan.onParentCancellation != nil {
			if err := plan.onParentCancellation(c.now()); err != nil && phaseErr == nil {
				phaseErr = err
			}
		}
		if phaseErr == nil {
			phaseErr = ctx.Err()
		}
	}
	if drainTimer != nil && !drainTimer.Stop() {
		select {
		case <-drainTimer.C:
		default:
		}
	}
	completedAt := c.now()
	result.CompletedAt = timePointer(completedAt)
	result.ElapsedNS = completedAt.Sub(plan.startedAt).Nanoseconds()
	result.MaxObservedActive = int(maxActive.Load())
	result.Status = phaseStatus(result.Outcomes, phaseErr)
	return result, phaseErr
}

type finalClaimBoundary struct {
	observedAt       time.Time
	admissionStopped chan struct{}
}

func (c *LifecycleCoordinator) validate(ctx context.Context, plan LifecyclePlan) error {
	if ctx == nil {
		return fmt.Errorf("lifecycle context is required")
	}
	if c == nil || c.runner == nil {
		return fmt.Errorf("lifecycle coordinator has no benchmark runner")
	}
	if c.now == nil || c.newTimer == nil {
		return fmt.Errorf("lifecycle coordinator clock is required")
	}
	if plan.RunID == "" {
		return fmt.Errorf("run ID is required")
	}
	if plan.Concurrency <= 0 || plan.MeasuredRequests <= 0 || plan.WarmupRequests < 0 {
		return fmt.Errorf("invalid lifecycle request or concurrency counts")
	}
	if plan.RequestTimeout <= 0 || plan.DrainTimeout <= 0 {
		return fmt.Errorf("request and drain timeouts must be greater than zero")
	}
	return nil
}

type lifecycleState struct {
	startedAt   time.Time
	current     LifecyclePhase
	transitions []PhaseTransition
}

func newLifecycleState(startedAt time.Time) *lifecycleState {
	return &lifecycleState{
		startedAt:   startedAt,
		current:     PhaseSetup,
		transitions: []PhaseTransition{{Sequence: 1, Phase: PhaseSetup, EnteredAt: startedAt, EnteredAfterNS: 0, Reason: "lifecycle_started"}},
	}
}

func (s *lifecycleState) transition(next LifecyclePhase, observed time.Time, reason string) error {
	if !legalTransition(s.current, next) {
		return fmt.Errorf("illegal lifecycle transition %s -> %s", s.current, next)
	}
	s.current = next
	s.transitions = append(s.transitions, PhaseTransition{
		Sequence:       len(s.transitions) + 1,
		Phase:          next,
		EnteredAt:      observed,
		EnteredAfterNS: observed.Sub(s.startedAt).Nanoseconds(),
		Reason:         reason,
	})
	return nil
}

func legalTransition(current, next LifecyclePhase) bool {
	switch current {
	case PhaseSetup:
		return next == PhaseWarmup || next == PhaseStopAdmission
	case PhaseWarmup:
		return next == PhaseMeasurement || next == PhaseStopAdmission
	case PhaseMeasurement:
		return next == PhaseStopAdmission
	case PhaseStopAdmission:
		return next == PhaseDrain
	case PhaseDrain:
		return next == PhaseArtifacts
	default:
		return false
	}
}

func claimNext(ctx context.Context, sequence *atomic.Int64, total int) (int, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	for {
		current := sequence.Load()
		if current >= int64(total) {
			return 0, false
		}
		if sequence.CompareAndSwap(current, current+1) {
			return int(current + 1), true
		}
		if ctx.Err() != nil {
			return 0, false
		}
	}
}

func phaseRequestID(phase RequestPhase, sequence int) (string, error) {
	if phase == RequestPhaseWarmup {
		return WarmupRequestID(sequence)
	}
	return RequestID(sequence)
}

func classifyOutcome(parent, request context.Context, result Result) RequestOutcome {
	if result.Err == nil {
		return OutcomeSucceeded
	}
	cause := context.Cause(request)
	if errors.Is(cause, ErrDrainTimeout) {
		return OutcomeDrainTimeout
	}
	if parent.Err() != nil {
		return OutcomeParentCancelled
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(result.Err, context.DeadlineExceeded) {
		return OutcomeRequestTimeout
	}
	return OutcomeRequestError
}

func incrementOutcome(counts *OutcomeCounts, outcome RequestOutcome) {
	switch outcome {
	case OutcomeSucceeded:
		counts.Succeeded++
	case OutcomeRequestError:
		counts.RequestError++
	case OutcomeRequestTimeout:
		counts.RequestTimeout++
	case OutcomeParentCancelled:
		counts.ParentCancelled++
	case OutcomeDrainTimeout:
		counts.DrainTimeout++
	}
}

func phaseStatus(outcomes OutcomeCounts, phaseErr error) string {
	if errors.Is(phaseErr, context.Canceled) || errors.Is(phaseErr, context.DeadlineExceeded) {
		return PhaseStatusCancelled
	}
	if phaseErr != nil || outcomes.Failed() > 0 {
		return PhaseStatusFailed
	}
	return PhaseStatusCompleted
}

func appendCancelledIDs(drain *DrainResult, completed []CompletedRequest) {
	for _, request := range completed {
		if request.Outcome == OutcomeDrainTimeout || request.Outcome == OutcomeParentCancelled {
			drain.CancelledRequestIDs = append(drain.CancelledRequestIDs, request.Result.Observation.RequestID)
		}
	}
}

func skippedPhaseResult(phase RequestPhase, requested, concurrency int) PhaseResult {
	return PhaseResult{Phase: phase, Status: PhaseStatusSkipped, RequestedRequests: requested, RequestedConcurrency: concurrency, Completed: make([]CompletedRequest, 0)}
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func phaseReason(skipped bool, normal string) string {
	if skipped {
		return "warmup_skipped"
	}
	return normal
}
