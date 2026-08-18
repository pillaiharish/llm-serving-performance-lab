package benchmark

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type RunPlan struct {
	RunID           string
	RequestTemplate Request
	Concurrency     int
	Requests        int
	RequestTimeout  time.Duration
}

type CompletedRequest struct {
	Sequence int
	Phase    RequestPhase
	Outcome  RequestOutcome
	Result   Result
}

type RunResult struct {
	StartedAt            time.Time
	CompletedAt          time.Time
	ElapsedNS            int64
	RequestedRequests    int
	RequestedConcurrency int
	WorkerCount          int
	MaxObservedActive    int
	Completed            []CompletedRequest
}

type RunCoordinator struct {
	runner *Runner
	now    func() time.Time
}

func NewRunCoordinator(runner *Runner) *RunCoordinator {
	return &RunCoordinator{runner: runner, now: time.Now}
}

// Run executes one closed-loop workload. Completed contains results in
// completion order; request identity is carried independently by each result.
func (c *RunCoordinator) Run(ctx context.Context, plan RunPlan) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, fmt.Errorf("run context is required")
	}
	if c == nil || c.runner == nil {
		return RunResult{}, fmt.Errorf("run coordinator has no benchmark runner")
	}
	if plan.RunID == "" {
		return RunResult{}, fmt.Errorf("run ID is required")
	}
	if plan.Concurrency <= 0 {
		return RunResult{}, fmt.Errorf("run concurrency must be greater than zero")
	}
	if plan.Requests <= 0 {
		return RunResult{}, fmt.Errorf("run request count must be greater than zero")
	}
	if plan.RequestTimeout <= 0 {
		return RunResult{}, fmt.Errorf("per-request timeout must be greater than zero")
	}

	started := c.now()
	lifecycle := &LifecycleCoordinator{runner: c.runner, now: c.now, newTimer: time.NewTimer}
	phaseResult, runErr := lifecycle.runPhase(ctx, phaseExecutionPlan{
		lifecyclePlan: LifecyclePlan{
			RunID:            plan.RunID,
			RequestTemplate:  plan.RequestTemplate,
			Concurrency:      plan.Concurrency,
			MeasuredRequests: plan.Requests,
			RequestTimeout:   plan.RequestTimeout,
			DrainTimeout:     plan.RequestTimeout,
		},
		phase:     RequestPhaseMeasured,
		requests:  plan.Requests,
		startedAt: started,
	})
	completedAt := started
	if phaseResult.CompletedAt != nil {
		completedAt = *phaseResult.CompletedAt
	}
	runResult := RunResult{
		StartedAt:            started,
		CompletedAt:          completedAt,
		ElapsedNS:            phaseResult.ElapsedNS,
		RequestedRequests:    plan.Requests,
		RequestedConcurrency: plan.Concurrency,
		WorkerCount:          phaseResult.WorkerCount,
		MaxObservedActive:    phaseResult.MaxObservedActive,
		Completed:            phaseResult.Completed,
	}
	return runResult, runErr
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
