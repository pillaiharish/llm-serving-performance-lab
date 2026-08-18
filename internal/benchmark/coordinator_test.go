package benchmark

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCoordinatorConcurrencyAndIdentityInvariants(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		requests    int
		wantActive  int
	}{
		{name: "C1", concurrency: 1, requests: 4, wantActive: 1},
		{name: "C2", concurrency: 2, requests: 8, wantActive: 2},
		{name: "C8", concurrency: 8, requests: 16, wantActive: 8},
		{name: "requests below concurrency", concurrency: 8, requests: 3, wantActive: 3},
		{name: "one hundred requests", concurrency: 8, requests: 100, wantActive: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newBarrierExecutor(test.wantActive)
			result, err := NewRunCoordinator(NewRunner(executor)).Run(context.Background(), testRunPlan(test.concurrency, test.requests, time.Second))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(result.Completed) != test.requests || result.WorkerCount != test.wantActive || result.MaxObservedActive != test.wantActive || int(executor.maxActive.Load()) != test.wantActive {
				t.Fatalf("run result = %+v, executor max = %d", result, executor.maxActive.Load())
			}
			seen := make(map[string]bool, test.requests)
			for _, completed := range result.Completed {
				if completed.Result.Err != nil {
					t.Fatalf("%s failed: %v", completed.Result.Observation.RequestID, completed.Result.Err)
				}
				requestID := completed.Result.Observation.RequestID
				if seen[requestID] {
					t.Fatalf("duplicate request ID %s", requestID)
				}
				seen[requestID] = true
			}
			for sequence := 1; sequence <= test.requests; sequence++ {
				requestID, _ := RequestID(sequence)
				if !seen[requestID] {
					t.Fatalf("missing request ID %s", requestID)
				}
			}
		})
	}
}

func TestRunCoordinatorPreservesCompletionOrderIndependentlyFromIdentity(t *testing.T) {
	executor := &delayExecutor{delays: map[string]time.Duration{"req-000001": 60 * time.Millisecond, "req-000002": 5 * time.Millisecond}}
	result, err := NewRunCoordinator(NewRunner(executor)).Run(context.Background(), testRunPlan(2, 2, time.Second))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := result.Completed[0].Result.Observation.RequestID; got != "req-000002" {
		t.Fatalf("first completed request = %s, want req-000002", got)
	}
	if got := result.Completed[1].Result.Observation.RequestID; got != "req-000001" {
		t.Fatalf("second completed request = %s, want req-000001", got)
	}
}

func TestRunCoordinatorContinuesAfterRequestFailures(t *testing.T) {
	executor := &selectiveExecutor{failures: map[string]error{"req-000002": errors.New("fixture failure"), "req-000005": errors.New("fixture failure")}}
	result, err := NewRunCoordinator(NewRunner(executor)).Run(context.Background(), testRunPlan(3, 10, time.Second))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	failed := 0
	for _, completed := range result.Completed {
		if completed.Result.Err != nil {
			failed++
		}
	}
	if len(result.Completed) != 10 || failed != 2 || !executor.saw("req-000010") {
		t.Fatalf("completed = %d, failed = %d, IDs = %v", len(result.Completed), failed, executor.requestIDs)
	}
}

func TestRunCoordinatorPerRequestTimeoutDoesNotStopLaterClaims(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{}, 16)}
	result, err := NewRunCoordinator(NewRunner(executor)).Run(context.Background(), testRunPlan(2, 6, 10*time.Millisecond))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Completed) != 6 {
		t.Fatalf("completed = %d, want 6", len(result.Completed))
	}
	for _, completed := range result.Completed {
		if !errors.Is(completed.Result.Err, context.DeadlineExceeded) {
			t.Fatalf("request error = %v, want deadline exceeded", completed.Result.Err)
		}
	}
}

func TestRunCoordinatorCancellationDrainsActiveResultsAndStopsClaims(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{}, 16)}
	coordinator := NewRunCoordinator(NewRunner(executor))
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result RunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := coordinator.Run(ctx, testRunPlan(3, 100, time.Second))
		done <- outcome{result: result, err: err}
	}()
	for index := 0; index < 3; index++ {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || len(got.result.Completed) != 3 || got.result.MaxObservedActive != 3 {
			t.Fatalf("outcome = %+v, err = %v", got.result, got.err)
		}
		for _, completed := range got.result.Completed {
			if !errors.Is(completed.Result.Err, context.Canceled) {
				t.Fatalf("request error = %v, want context canceled", completed.Result.Err)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not return after cancellation")
	}
}

func testRunPlan(concurrency, requests int, timeout time.Duration) RunPlan {
	return RunPlan{
		RunID: "run-test",
		RequestTemplate: Request{
			Model:           "model",
			Prompt:          "private prompt",
			MaxOutputTokens: 4,
			Temperature:     0,
		},
		Concurrency:    concurrency,
		Requests:       requests,
		RequestTimeout: timeout,
	}
}

type barrierExecutor struct {
	target    int64
	active    atomic.Int64
	maxActive atomic.Int64
	release   chan struct{}
	once      sync.Once
}

func newBarrierExecutor(target int) *barrierExecutor {
	return &barrierExecutor{target: int64(target), release: make(chan struct{})}
}

func (e *barrierExecutor) Execute(ctx context.Context, _ Request, observation *RequestObservation) error {
	current := e.active.Add(1)
	updateMaximum(&e.maxActive, current)
	defer e.active.Add(-1)
	if current == e.target {
		e.once.Do(func() { close(e.release) })
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
	return nil
}

type delayExecutor struct {
	delays map[string]time.Duration
}

func (e *delayExecutor) Execute(ctx context.Context, request Request, observation *RequestObservation) error {
	timer := time.NewTimer(e.delays[request.RequestID])
	defer timer.Stop()
	select {
	case <-timer.C:
		observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type selectiveExecutor struct {
	mu         sync.Mutex
	failures   map[string]error
	requestIDs []string
}

func (e *selectiveExecutor) Execute(_ context.Context, request Request, observation *RequestObservation) error {
	e.mu.Lock()
	e.requestIDs = append(e.requestIDs, request.RequestID)
	err := e.failures[request.RequestID]
	e.mu.Unlock()
	if err != nil {
		return err
	}
	observation.StreamEvents = append(observation.StreamEvents, StreamEvent{Sequence: 1, HasContent: true, ContentBytes: 1})
	return nil
}

func (e *selectiveExecutor) saw(requestID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, candidate := range e.requestIDs {
		if candidate == requestID {
			return true
		}
	}
	return false
}

type blockingExecutor struct {
	started chan struct{}
}

func (e *blockingExecutor) Execute(ctx context.Context, _ Request, _ *RequestObservation) error {
	e.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}
