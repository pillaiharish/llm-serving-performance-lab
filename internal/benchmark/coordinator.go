package benchmark

import (
	"context"
	"fmt"
	"sync"
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

	workerCount := plan.Concurrency
	if plan.Requests < workerCount {
		workerCount = plan.Requests
	}
	started := c.now()
	runResult := RunResult{
		StartedAt:            started,
		RequestedRequests:    plan.Requests,
		RequestedConcurrency: plan.Concurrency,
		WorkerCount:          workerCount,
		Completed:            make([]CompletedRequest, 0, workerCount),
	}

	resultsCh := make(chan CompletedRequest, workerCount)
	var workers sync.WaitGroup
	var nextSequence atomic.Int64
	var active atomic.Int64
	var maxActive atomic.Int64

	worker := func() {
		defer workers.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			sequence := int(nextSequence.Add(1))
			if sequence > plan.Requests {
				return
			}
			requestID, err := RequestID(sequence)
			if err != nil {
				return
			}
			request := plan.RequestTemplate
			request.RunID = plan.RunID
			request.RequestID = requestID

			requestContext, cancel := context.WithTimeout(ctx, plan.RequestTimeout)
			current := active.Add(1)
			updateMaximum(&maxActive, current)
			result := c.runner.RunRequest(requestContext, request)
			active.Add(-1)
			cancel()

			// Once RunRequest returns, its evidence must reach the collector even
			// when the parent context has been canceled.
			resultsCh <- CompletedRequest{Sequence: sequence, Result: result}
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

	for completed := range resultsCh {
		runResult.Completed = append(runResult.Completed, completed)
	}
	completedAt := c.now()
	runResult.CompletedAt = completedAt
	runResult.ElapsedNS = completedAt.Sub(started).Nanoseconds()
	runResult.MaxObservedActive = int(maxActive.Load())

	if len(runResult.Completed) < plan.Requests && ctx.Err() != nil {
		return runResult, ctx.Err()
	}
	return runResult, nil
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
