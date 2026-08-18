package benchmark

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenLoopHealthyScheduleStartsEveryArrival(t *testing.T) {
	executor := &timedOpenLoopExecutor{delay: time.Millisecond}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), LifecyclePlan{
		Mode: LoadModeOpenLoop, RunID: "run-open", RequestTemplate: Request{RunID: "run-open"},
		RequestTimeout: time.Second, DrainTimeout: time.Second,
		OpenLoop: OpenLoopPlan{RequestRate: 50, Duration: 100 * time.Millisecond, MaxInFlight: 16},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	counts := result.Measurement.ArrivalCounts
	if counts.Planned != 5 || counts.Processed != 5 || counts.Started != 5 || counts.ClientLimited != 0 || counts.SchedulerLimited != 0 || counts.UnprocessedDueToCancellation != 0 || len(result.Measurement.Completed) != 5 {
		t.Fatalf("arrival counts = %+v, completed = %d", counts, len(result.Measurement.Completed))
	}
	for sequence, arrival := range result.Measurement.Arrivals {
		if arrival.Sequence != sequence+1 || arrival.Disposition != ArrivalStarted || arrival.RequestID == nil || arrival.ActualStartedAt == nil || arrival.SchedulerLagNS == nil || *arrival.SchedulerLagNS < 0 {
			t.Fatalf("arrival %d = %+v", sequence+1, arrival)
		}
	}
	if result.Measurement.CompletedAt.Before(result.Measurement.StartedAt.Add(100 * time.Millisecond)) {
		t.Fatalf("measurement ended before duration boundary: %+v", result.Measurement)
	}
}

func TestOpenLoopDoesNotQueueClientLimitedArrivals(t *testing.T) {
	executor := &timedOpenLoopExecutor{delay: 80 * time.Millisecond}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), LifecyclePlan{
		Mode: LoadModeOpenLoop, RunID: "run-pressure", RequestTemplate: Request{RunID: "run-pressure"},
		RequestTimeout: time.Second, DrainTimeout: time.Second,
		OpenLoop: OpenLoopPlan{RequestRate: 100, Duration: 50 * time.Millisecond, MaxInFlight: 2},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	counts := result.Measurement.ArrivalCounts
	if counts.Planned != 5 || counts.Started != 2 || counts.ClientLimited != 3 || counts.SchedulerLimited != 0 || counts.MaxObservedInFlight != 2 {
		t.Fatalf("arrival counts = %+v", counts)
	}
	if executor.calls.Load() != 2 {
		t.Fatalf("HTTP executions = %d, want only the two admitted arrivals", executor.calls.Load())
	}
	time.Sleep(20 * time.Millisecond)
	if executor.calls.Load() != 2 {
		t.Fatalf("dropped arrivals started later: calls = %d", executor.calls.Load())
	}
}

func TestOpenLoopSchedulerLimitedIsDeterministic(t *testing.T) {
	start := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	clock := &overshootingClock{now: start, overshoot: 2 * time.Second}
	executor := &clockedOpenLoopExecutor{now: clock.Now}
	coordinator := NewLifecycleCoordinator(NewRunner(executor))
	coordinator.now = clock.Now
	coordinator.newTimer = clock.NewTimer
	result, err := coordinator.Run(context.Background(), LifecyclePlan{
		Mode: LoadModeOpenLoop, RunID: "run-scheduler-limited", RequestTemplate: Request{RunID: "run-scheduler-limited"},
		RequestTimeout: time.Second, DrainTimeout: 10 * time.Second,
		OpenLoop: OpenLoopPlan{RequestRate: 10, Duration: time.Second, MaxInFlight: 2},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	counts := result.Measurement.ArrivalCounts
	if counts.Planned != 10 || counts.Started != 1 || counts.SchedulerLimited != 9 || counts.ClientLimited != 0 || executor.calls.Load() != 1 {
		t.Fatalf("counts = %+v, calls = %d", counts, executor.calls.Load())
	}
	var stopped *PhaseTransition
	for index := range result.Transitions {
		if result.Transitions[index].Phase == PhaseStopAdmission {
			stopped = &result.Transitions[index]
		}
	}
	measurementStart := *result.Measurement.StartedAt
	if stopped == nil || stopped.Reason != "measurement_duration_elapsed" || !stopped.EnteredAt.Equal(measurementStart.Add(time.Second)) {
		t.Fatalf("stop admission = %+v, measurement start = %s", stopped, measurementStart)
	}
}

func TestOpenLoopCancellationAccountsForUnprocessedArrivals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := &cancelingOpenLoopExecutor{cancel: cancel}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(ctx, LifecyclePlan{
		Mode: LoadModeOpenLoop, RunID: "run-cancel", RequestTemplate: Request{RunID: "run-cancel"},
		RequestTimeout: time.Second, DrainTimeout: time.Second,
		OpenLoop: OpenLoopPlan{RequestRate: 100, Duration: time.Second, MaxInFlight: 4},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	counts := result.Measurement.ArrivalCounts
	if counts.Planned != 100 || counts.Processed+counts.UnprocessedDueToCancellation != counts.Planned || counts.UnprocessedDueToCancellation == 0 {
		t.Fatalf("cancellation counts = %+v", counts)
	}
}

func TestDrainTimeoutIsAnchoredToStopAdmissionBoundary(t *testing.T) {
	boundary := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	if got := drainTimeRemaining(boundary, 10*time.Second, boundary.Add(3*time.Second)); got != 7*time.Second {
		t.Fatalf("remaining = %s, want 7s", got)
	}
	if got := drainTimeRemaining(boundary, 10*time.Second, boundary.Add(11*time.Second)); got > 0 {
		t.Fatalf("expired boundary remaining = %s, want non-positive", got)
	}
}

func TestExplicitClosedLoopModePreservesC4N16Semantics(t *testing.T) {
	executor := &timedOpenLoopExecutor{delay: 5 * time.Millisecond}
	result, err := NewLifecycleCoordinator(NewRunner(executor)).Run(context.Background(), LifecyclePlan{
		Mode: LoadModeClosedLoop, RunID: "run-closed-regression", RequestTemplate: Request{RunID: "run-closed-regression"},
		Concurrency: 4, MeasuredRequests: 16, RequestTimeout: time.Second, DrainTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.LoadMode != LoadModeClosedLoop || result.Measurement.RequestedRequests != 16 || result.Measurement.WorkerCount != 4 || result.Measurement.MaxObservedActive != 4 || len(result.Measurement.Completed) != 16 || executor.calls.Load() != 16 || len(result.Measurement.Arrivals) != 0 {
		t.Fatalf("closed-loop regression result = %+v, calls = %d", result.Measurement, executor.calls.Load())
	}
}

type timedOpenLoopExecutor struct {
	delay time.Duration
	calls atomic.Int64
}

func (e *timedOpenLoopExecutor) Execute(ctx context.Context, _ Request, observation *RequestObservation) error {
	e.calls.Add(1)
	started := time.Now()
	observation.RequestStartedAt = &started
	timer := time.NewTimer(e.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type clockedOpenLoopExecutor struct {
	now   func() time.Time
	calls atomic.Int64
}

func (e *clockedOpenLoopExecutor) Execute(_ context.Context, _ Request, observation *RequestObservation) error {
	e.calls.Add(1)
	started := e.now()
	observation.RequestStartedAt = &started
	observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
	return nil
}

type cancelingOpenLoopExecutor struct {
	cancel context.CancelFunc
	once   sync.Once
}

func (e *cancelingOpenLoopExecutor) Execute(ctx context.Context, _ Request, observation *RequestObservation) error {
	started := time.Now()
	observation.RequestStartedAt = &started
	e.once.Do(e.cancel)
	<-ctx.Done()
	return ctx.Err()
}

type fakeLifecycleTimer struct {
	channel <-chan time.Time
}

func (t fakeLifecycleTimer) Chan() <-chan time.Time { return t.channel }
func (t fakeLifecycleTimer) Stop() bool             { return true }

type overshootingClock struct {
	mu        sync.Mutex
	now       time.Time
	overshoot time.Duration
	timers    int
}

func (c *overshootingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *overshootingClock) NewTimer(duration time.Duration) lifecycleTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers++
	channel := make(chan time.Time, 1)
	if c.timers == 1 {
		c.now = c.now.Add(duration + c.overshoot)
		channel <- c.now
	}
	return fakeLifecycleTimer{channel: channel}
}
