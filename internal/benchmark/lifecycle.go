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

var (
	ErrDrainTimeout = errors.New("benchmark drain timeout expired")
	ErrLoadDelivery = errors.New("open-loop schedule could not be faithfully delivered")
)

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
	Mode             LoadMode
	RunID            string
	RequestTemplate  Request
	Concurrency      int
	WarmupRequests   int
	MeasuredRequests int
	RequestTimeout   time.Duration
	DrainTimeout     time.Duration
	OpenLoop         OpenLoopPlan
}

type OpenLoopPlan struct {
	RequestRate float64
	Duration    time.Duration
	MaxInFlight int
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
	LoadMode             LoadMode
	Phase                RequestPhase
	Status               string
	StartedAt            *time.Time
	CompletedAt          *time.Time
	ElapsedNS            int64
	RequestedRequests    int
	RequestedConcurrency int
	WorkerCount          int
	MaxObservedActive    int
	Arrivals             []ArrivalRecord
	ArrivalCounts        ArrivalCounts
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
	LoadMode    LoadMode
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
	newTimer func(time.Duration) lifecycleTimer
}

type lifecycleTimer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type realLifecycleTimer struct {
	timer *time.Timer
}

func (t realLifecycleTimer) Chan() <-chan time.Time { return t.timer.C }
func (t realLifecycleTimer) Stop() bool             { return t.timer.Stop() }

func newRealLifecycleTimer(duration time.Duration) lifecycleTimer {
	return realLifecycleTimer{timer: time.NewTimer(duration)}
}

func NewLifecycleCoordinator(runner *Runner) *LifecycleCoordinator {
	return &LifecycleCoordinator{runner: runner, now: time.Now, newTimer: newRealLifecycleTimer}
}

func (c *LifecycleCoordinator) Run(ctx context.Context, plan LifecyclePlan) (LifecycleResult, error) {
	if err := c.validate(ctx, plan); err != nil {
		return LifecycleResult{}, err
	}
	mode := normalizedLoadMode(plan.Mode)
	var warmupSchedule, measurementSchedule []time.Duration
	var err error
	if mode == LoadModeOpenLoop {
		warmupSchedule, err = ArrivalOffsets(plan.OpenLoop.RequestRate, plan.WarmupRequests)
		if err != nil {
			return LifecycleResult{}, fmt.Errorf("prepare warmup arrival schedule: %w", err)
		}
		measurementSchedule, err = DurationArrivalOffsets(plan.OpenLoop.RequestRate, plan.OpenLoop.Duration)
		if err != nil {
			return LifecycleResult{}, fmt.Errorf("prepare measurement arrival schedule: %w", err)
		}
		plan.MeasuredRequests = len(measurementSchedule)
	}
	started := c.now()
	state := newLifecycleState(started)
	result := LifecycleResult{
		LoadMode:    mode,
		StartedAt:   started,
		Warmup:      skippedPhaseResult(mode, RequestPhaseWarmup, plan.WarmupRequests, plan.Concurrency),
		Measurement: skippedPhaseResult(mode, RequestPhaseMeasured, plan.MeasuredRequests, plan.Concurrency),
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
		markSkippedOpenLoopUnprocessed(&result)
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
		phasePlan := phaseExecutionPlan{
			lifecyclePlan: plan,
			phase:         RequestPhaseWarmup,
			requests:      plan.WarmupRequests,
			startedAt:     warmupStarted,
			onParentCancellation: func(at time.Time) error {
				result.Drain.ParentCancelled = true
				return enterStopAndDrain(at, "parent_cancelled_during_warmup")
			},
		}
		var warmup PhaseResult
		var warmupErr error
		if mode == LoadModeOpenLoop {
			phasePlan.schedule = warmupSchedule
			warmup, warmupErr = c.runOpenLoopPhase(ctx, phasePlan)
		} else {
			warmup, warmupErr = c.runClosedLoopPhase(ctx, phasePlan)
		}
		result.Warmup = warmup
		appendCancelledIDs(&result.Drain, warmup.Completed)
		if warmupErr != nil {
			markSkippedOpenLoopUnprocessed(&result)
			return c.finishLifecycle(state, result, warmupErr)
		}
	}

	measurementStarted := c.now()
	if err := state.transition(PhaseMeasurement, measurementStarted, "warmup_complete"); err != nil {
		return LifecycleResult{}, err
	}
	measurementPlan := phaseExecutionPlan{
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
	}
	var measurement PhaseResult
	var measurementErr error
	if mode == LoadModeOpenLoop {
		measurementPlan.requests = len(measurementSchedule)
		measurementPlan.schedule = measurementSchedule
		measurementPlan.deadline = timePointer(measurementStarted.Add(plan.OpenLoop.Duration))
		measurementPlan.onAdmissionStopped = func(at time.Time) error {
			return enterStopAndDrain(at, "measurement_duration_elapsed")
		}
		measurement, measurementErr = c.runOpenLoopPhase(ctx, measurementPlan)
	} else {
		measurement, measurementErr = c.runClosedLoopPhase(ctx, measurementPlan)
	}
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
	schedule             []time.Duration
	deadline             *time.Time
}

func (c *LifecycleCoordinator) runClosedLoopPhase(ctx context.Context, plan phaseExecutionPlan) (PhaseResult, error) {
	workerCount := plan.lifecyclePlan.Concurrency
	if plan.requests < workerCount {
		workerCount = plan.requests
	}
	result := PhaseResult{
		LoadMode:             LoadModeClosedLoop,
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
	var drainTimer lifecycleTimer
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
				remaining := drainTimeRemaining(boundary.observedAt, plan.drainTimeout, c.now())
				if remaining <= 0 {
					if active.Load() > 0 {
						cancelPhase(ErrDrainTimeout)
						phaseErr = ErrDrainTimeout
					}
				} else {
					drainTimer = c.newTimer(remaining)
					drainTimerCh = drainTimer.Chan()
				}
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
		case <-drainTimer.Chan():
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

type openLoopCollection struct {
	completed []CompletedRequest
	arrivals  []ArrivalRecord
	outcomes  OutcomeCounts
}

func (c *LifecycleCoordinator) runOpenLoopPhase(ctx context.Context, plan phaseExecutionPlan) (PhaseResult, error) {
	maxInFlight := plan.lifecyclePlan.OpenLoop.MaxInFlight
	result := PhaseResult{
		LoadMode:          LoadModeOpenLoop,
		Phase:             plan.phase,
		Status:            PhaseStatusCompleted,
		StartedAt:         timePointer(plan.startedAt),
		RequestedRequests: len(plan.schedule),
		Arrivals:          make([]ArrivalRecord, 0, len(plan.schedule)),
		Completed:         make([]CompletedRequest, 0, maxInFlight),
		ArrivalCounts:     ArrivalCounts{Planned: len(plan.schedule)},
	}

	phaseCtx, cancelPhase := context.WithCancelCause(ctx)
	defer cancelPhase(nil)
	semaphore := make(chan struct{}, maxInFlight)
	resultsCh := make(chan CompletedRequest, maxInFlight)
	collectionCh := make(chan openLoopCollection, 1)
	go func() {
		collection := openLoopCollection{
			completed: make([]CompletedRequest, 0, maxInFlight),
			arrivals:  make([]ArrivalRecord, 0, maxInFlight),
		}
		for completed := range resultsCh {
			collection.completed = append(collection.completed, completed)
			incrementOutcome(&collection.outcomes, completed.Outcome)
			if completed.Arrival != nil {
				collection.arrivals = append(collection.arrivals, *completed.Arrival)
			}
		}
		collectionCh <- collection
	}()

	var requests sync.WaitGroup
	var active atomic.Int64
	var maxActive atomic.Int64
	dropped := make([]ArrivalRecord, 0)
	processed := 0
	startRequest := func(sequence int, scheduledAt time.Time, scheduledAfterNS int64) bool {
		select {
		case semaphore <- struct{}{}:
		default:
			dropped = append(dropped, ArrivalRecord{
				Sequence: sequence, Phase: plan.phase, ScheduledAt: scheduledAt,
				ScheduledAfterNS: scheduledAfterNS, Disposition: ArrivalClientLimited,
			})
			return false
		}
		requestID, err := phaseRequestID(plan.phase, sequence)
		if err != nil {
			<-semaphore
			return false
		}
		requests.Add(1)
		current := active.Add(1)
		updateMaximum(&maxActive, current)
		go func() {
			defer requests.Done()
			request := plan.lifecyclePlan.RequestTemplate
			request.RunID = plan.lifecyclePlan.RunID
			request.RequestID = requestID
			requestCtx, cancelRequest := context.WithTimeout(phaseCtx, plan.lifecyclePlan.RequestTimeout)
			runResult := c.runner.RunRequest(requestCtx, request)
			outcome := classifyOutcome(ctx, requestCtx, runResult)
			cancelRequest()
			active.Add(-1)
			<-semaphore
			arrival := ArrivalRecord{
				Sequence: sequence, Phase: plan.phase, ScheduledAt: scheduledAt,
				ScheduledAfterNS: scheduledAfterNS, Disposition: ArrivalStarted,
				RequestID: stringPointer(requestID),
			}
			if runResult.Observation.RequestStartedAt != nil {
				actual := *runResult.Observation.RequestStartedAt
				actualAfter := actual.Sub(plan.startedAt).Nanoseconds()
				lag := actual.Sub(scheduledAt).Nanoseconds()
				arrival.ActualStartedAt = timePointer(actual)
				arrival.ActualStartedAfterNS = int64Pointer(actualAfter)
				arrival.SchedulerLagNS = int64Pointer(lag)
			}
			resultsCh <- CompletedRequest{Sequence: sequence, Phase: plan.phase, Outcome: outcome, Result: runResult, Arrival: &arrival}
		}()
		return true
	}

	var phaseErr error
	for index, offset := range plan.schedule {
		sequence := index + 1
		target := plan.startedAt.Add(offset)
		if !c.waitUntil(ctx, target) {
			break
		}
		observed := c.now()
		if plan.deadline != nil && !observed.Before(*plan.deadline) {
			for remaining := index; remaining < len(plan.schedule); remaining++ {
				remainingOffset := plan.schedule[remaining]
				dropped = append(dropped, ArrivalRecord{
					Sequence: remaining + 1, Phase: plan.phase,
					ScheduledAt: plan.startedAt.Add(remainingOffset), ScheduledAfterNS: remainingOffset.Nanoseconds(),
					Disposition: ArrivalSchedulerLimited,
				})
			}
			processed = len(plan.schedule)
			break
		}
		startRequest(sequence, target, offset.Nanoseconds())
		processed++
	}

	if ctx.Err() != nil {
		if plan.onParentCancellation != nil {
			if err := plan.onParentCancellation(c.now()); err != nil {
				phaseErr = err
			}
		}
		if phaseErr == nil {
			phaseErr = ctx.Err()
		}
	} else if plan.deadline != nil {
		if c.waitUntil(ctx, *plan.deadline) {
			if plan.onAdmissionStopped != nil {
				if err := plan.onAdmissionStopped(*plan.deadline); err != nil {
					phaseErr = err
					cancelPhase(err)
				}
			}
		} else {
			if plan.onParentCancellation != nil {
				if err := plan.onParentCancellation(c.now()); err != nil {
					phaseErr = err
				}
			}
			if phaseErr == nil {
				phaseErr = ctx.Err()
			}
		}
	}

	requestsDone := make(chan struct{})
	go func() {
		requests.Wait()
		close(resultsCh)
		close(requestsDone)
	}()

	if phaseErr == nil && plan.deadline != nil {
		remaining := drainTimeRemaining(*plan.deadline, plan.drainTimeout, c.now())
		if remaining <= 0 {
			if active.Load() > 0 {
				cancelPhase(ErrDrainTimeout)
				phaseErr = ErrDrainTimeout
			}
		} else {
			timer := c.newTimer(remaining)
			select {
			case <-requestsDone:
			case <-timer.Chan():
				if active.Load() > 0 {
					cancelPhase(ErrDrainTimeout)
					phaseErr = ErrDrainTimeout
				}
			case <-ctx.Done():
				if plan.onParentCancellation != nil {
					if err := plan.onParentCancellation(c.now()); err != nil {
						phaseErr = err
					}
				}
				if phaseErr == nil {
					phaseErr = ctx.Err()
				}
			}
			timer.Stop()
		}
	}
	<-requestsDone
	collection := <-collectionCh

	result.Completed = collection.completed
	result.Outcomes = collection.outcomes
	result.Arrivals = append(result.Arrivals, dropped...)
	result.Arrivals = append(result.Arrivals, collection.arrivals...)
	sort.Slice(result.Arrivals, func(left, right int) bool { return result.Arrivals[left].Sequence < result.Arrivals[right].Sequence })
	result.ArrivalCounts.Processed = len(result.Arrivals)
	for _, arrival := range result.Arrivals {
		switch arrival.Disposition {
		case ArrivalStarted:
			result.ArrivalCounts.Started++
		case ArrivalClientLimited:
			result.ArrivalCounts.ClientLimited++
		case ArrivalSchedulerLimited:
			result.ArrivalCounts.SchedulerLimited++
		}
	}
	result.ArrivalCounts.UnprocessedDueToCancellation = len(plan.schedule) - processed
	if result.ArrivalCounts.UnprocessedDueToCancellation < 0 {
		result.ArrivalCounts.UnprocessedDueToCancellation = 0
	}
	result.ArrivalCounts.MaxObservedInFlight = int(maxActive.Load())
	result.MaxObservedActive = int(maxActive.Load())
	completedAt := c.now()
	result.CompletedAt = timePointer(completedAt)
	result.ElapsedNS = completedAt.Sub(plan.startedAt).Nanoseconds()
	result.Status = phaseStatus(result.Outcomes, phaseErr)
	return result, phaseErr
}

func (c *LifecycleCoordinator) waitUntil(ctx context.Context, target time.Time) bool {
	for {
		remaining := target.Sub(c.now())
		if remaining <= 0 {
			return ctx.Err() == nil
		}
		timer := c.newTimer(remaining)
		select {
		case <-timer.Chan():
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return false
		}
	}
}

func drainTimeRemaining(stopAdmissionAt time.Time, timeout time.Duration, now time.Time) time.Duration {
	return stopAdmissionAt.Add(timeout).Sub(now)
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
	mode := normalizedLoadMode(plan.Mode)
	if plan.WarmupRequests < 0 {
		return fmt.Errorf("invalid lifecycle warmup request count")
	}
	switch mode {
	case LoadModeClosedLoop:
		if plan.Concurrency <= 0 || plan.MeasuredRequests <= 0 {
			return fmt.Errorf("invalid lifecycle request or concurrency counts")
		}
	case LoadModeOpenLoop:
		if plan.OpenLoop.RequestRate <= 0 || plan.OpenLoop.Duration <= 0 || plan.OpenLoop.MaxInFlight <= 0 {
			return fmt.Errorf("invalid open-loop lifecycle plan")
		}
	default:
		return fmt.Errorf("invalid lifecycle load mode %q", plan.Mode)
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

func skippedPhaseResult(mode LoadMode, phase RequestPhase, requested, concurrency int) PhaseResult {
	result := PhaseResult{LoadMode: mode, Phase: phase, Status: PhaseStatusSkipped, RequestedRequests: requested, RequestedConcurrency: concurrency, Completed: make([]CompletedRequest, 0)}
	if mode == LoadModeOpenLoop {
		result.RequestedConcurrency = 0
		result.Arrivals = make([]ArrivalRecord, 0)
		result.ArrivalCounts.Planned = requested
	}
	return result
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func int64Pointer(value int64) *int64 {
	copy := value
	return &copy
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func normalizedLoadMode(mode LoadMode) LoadMode {
	if mode == "" {
		return LoadModeClosedLoop
	}
	return mode
}

func markSkippedOpenLoopUnprocessed(result *LifecycleResult) {
	if result == nil || result.LoadMode != LoadModeOpenLoop {
		return
	}
	if result.Warmup.Status == PhaseStatusSkipped && result.Warmup.ArrivalCounts.Planned > 0 {
		result.Warmup.ArrivalCounts.UnprocessedDueToCancellation = result.Warmup.ArrivalCounts.Planned
	}
	if result.Measurement.Status == PhaseStatusSkipped && result.Measurement.ArrivalCounts.Planned > 0 {
		result.Measurement.ArrivalCounts.UnprocessedDueToCancellation = result.Measurement.ArrivalCounts.Planned
	}
}

func phaseReason(skipped bool, normal string) string {
	if skipped {
		return "warmup_skipped"
	}
	return normal
}
