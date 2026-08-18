package fakeserver_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/openai"
)

func TestRunCoordinatorReachesEffectiveConcurrencyWithRealClient(t *testing.T) {
	tests := []struct {
		name        string
		requests    int
		concurrency int
	}{
		{name: "C1", requests: 6, concurrency: 1},
		{name: "C2", requests: 8, concurrency: 2},
		{name: "C8", requests: 16, concurrency: 8},
		{name: "N3C8", requests: 3, concurrency: 8},
		{name: "N100C8", requests: 100, concurrency: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workerCount := test.concurrency
			if test.requests < workerCount {
				workerCount = test.requests
			}
			config := fakeserver.DefaultConfig()
			config.HeaderDelay = 0
			config.FirstContentDelay = 0
			config.ChunkInterval = 0
			config.ContentChunks = 1
			config.UsageDelay = 0
			config.DoneDelay = 0
			fakeHandler, err := fakeserver.NewHandler(config)
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			tracker := newOverlapTrackingHandler(fakeHandler, workerCount)
			server := httptest.NewServer(tracker)
			defer server.Close()

			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.MaxIdleConns = workerCount
			transport.MaxIdleConnsPerHost = workerCount
			transport.MaxConnsPerHost = workerCount
			defer transport.CloseIdleConnections()
			client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			coordinator := benchmark.NewRunCoordinator(benchmark.NewRunner(client))
			result, err := coordinator.Run(context.Background(), benchmark.RunPlan{
				RunID: "run-real-client-" + test.name,
				RequestTemplate: benchmark.Request{
					Model:           "fake-model",
					Prompt:          "private integration prompt",
					MaxOutputTokens: 4,
					Temperature:     0,
				},
				Concurrency:    test.concurrency,
				Requests:       test.requests,
				RequestTimeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(result.Completed) != test.requests || tracker.calls.Load() != int64(test.requests) {
				t.Fatalf("completed/calls = %d/%d, want %d", len(result.Completed), tracker.calls.Load(), test.requests)
			}
			if result.WorkerCount != workerCount || result.MaxObservedActive != workerCount || tracker.maximum.Load() != int64(workerCount) {
				t.Fatalf("workers/coordinator max/handler max = %d/%d/%d, want %d", result.WorkerCount, result.MaxObservedActive, tracker.maximum.Load(), workerCount)
			}
			seen := make(map[string]struct{}, test.requests)
			for _, completed := range result.Completed {
				if completed.Result.Err != nil {
					t.Fatalf("request %d: %v", completed.Sequence, completed.Result.Err)
				}
				wantID := fmt.Sprintf("req-%06d", completed.Sequence)
				observation := completed.Result.Observation
				if observation.RequestID != wantID || observation.RunID != "run-real-client-"+test.name {
					t.Fatalf("request identity = (%q, %q), want (%q, %q)", observation.RunID, observation.RequestID, "run-real-client-"+test.name, wantID)
				}
				if _, exists := seen[observation.RequestID]; exists {
					t.Fatalf("duplicate request ID %q", observation.RequestID)
				}
				seen[observation.RequestID] = struct{}{}
			}
		})
	}
}

func TestLifecycleCoordinatorUsesSharedClientWithoutPhaseOverlap(t *testing.T) {
	const (
		warmupRequests   = 4
		measuredRequests = 3
		concurrency      = 2
	)
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = 2 * time.Millisecond
	config.ChunkInterval = time.Millisecond
	config.UsageDelay = 0
	config.DoneDelay = 0
	fakeHandler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	tracker := newPhaseTrackingHandler(fakeHandler, warmupRequests, concurrency)
	server := httptest.NewServer(tracker)
	defer server.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = concurrency
	transport.MaxIdleConnsPerHost = concurrency
	transport.MaxConnsPerHost = concurrency
	defer transport.CloseIdleConnections()
	client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(context.Background(), benchmark.LifecyclePlan{
		RunID: "run-lifecycle-real-client",
		RequestTemplate: benchmark.Request{
			Model: "fake-model", Prompt: "private integration prompt", MaxOutputTokens: 4, Temperature: 0,
		},
		Concurrency: concurrency, WarmupRequests: warmupRequests, MeasuredRequests: measuredRequests,
		RequestTimeout: 3 * time.Second, DrainTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tracker.calls.Load() != warmupRequests+measuredRequests || tracker.overlap.Load() {
		t.Fatalf("calls/phase overlap = %d/%v", tracker.calls.Load(), tracker.overlap.Load())
	}
	if tracker.warmupMaximum.Load() != concurrency || tracker.measuredMaximum.Load() != concurrency || result.Warmup.MaxObservedActive != concurrency || result.Measurement.MaxObservedActive != concurrency {
		t.Fatalf("handler/coordinator maxima = %d/%d/%d/%d", tracker.warmupMaximum.Load(), tracker.measuredMaximum.Load(), result.Warmup.MaxObservedActive, result.Measurement.MaxObservedActive)
	}
	if result.Warmup.CompletedAt == nil || result.Measurement.StartedAt == nil || result.Measurement.StartedAt.Before(*result.Warmup.CompletedAt) {
		t.Fatalf("phase timing overlaps: warmup=%+v measurement=%+v", result.Warmup, result.Measurement)
	}
	assertLifecycleIDs(t, result.Warmup.Completed, "warmup")
	assertLifecycleIDs(t, result.Measurement.Completed, "req")
	for _, completed := range append(append([]benchmark.CompletedRequest{}, result.Warmup.Completed...), result.Measurement.Completed...) {
		if completed.Result.Err != nil || completed.Outcome != benchmark.OutcomeSucceeded {
			t.Fatalf("request %s failed: outcome=%s err=%v", completed.Result.Observation.RequestID, completed.Outcome, completed.Result.Err)
		}
		observation := completed.Result.Observation
		if observation.RequestStartedAt == nil || observation.HeadersAfterNS == nil || observation.FirstByteAfterNS == nil || observation.FirstContentAfterNS == nil || observation.LastContentAfterNS == nil || observation.CompletedAfterNS == nil {
			t.Fatalf("missing timing evidence: %+v", observation)
		}
		calculated := metrics.Calculate(observation)
		for name, scalar := range map[string]metrics.Scalar{"ttfb": calculated.TTFB, "ttft": calculated.TTFT, "ttlt": calculated.TTLT, "e2e": calculated.E2E, "tpot": calculated.TPOT, "decode": calculated.DecodeTokensPerSecond} {
			if !scalar.Available || math.IsNaN(scalar.Value) || math.IsInf(scalar.Value, 0) {
				t.Fatalf("%s for %s = %+v", name, observation.RequestID, scalar)
			}
		}
	}
}

func TestLifecycleCoordinatorDrainTimeoutWithRealClientPreservesPartialEvidence(t *testing.T) {
	const concurrency = 2
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = 500 * time.Millisecond
	fakeHandler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	tracker := newOverlapTrackingHandler(fakeHandler, concurrency)
	server := httptest.NewServer(tracker)
	defer server.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = concurrency
	transport.MaxIdleConnsPerHost = concurrency
	transport.MaxConnsPerHost = concurrency
	defer transport.CloseIdleConnections()
	client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(context.Background(), benchmark.LifecyclePlan{
		RunID:           "run-lifecycle-drain-timeout",
		RequestTemplate: benchmark.Request{Model: "fake-model", Prompt: "private prompt", MaxOutputTokens: 4},
		Concurrency:     concurrency, MeasuredRequests: concurrency, RequestTimeout: 2 * time.Second, DrainTimeout: 30 * time.Millisecond,
	})
	if err == nil || !errors.Is(err, benchmark.ErrDrainTimeout) {
		t.Fatalf("Run error = %v", err)
	}
	if !result.Drain.TimedOut || result.Drain.ParentCancelled || result.Measurement.Outcomes.DrainTimeout != concurrency || len(result.Drain.CancelledRequestIDs) != concurrency {
		t.Fatalf("lifecycle result = %+v", result)
	}
	for _, completed := range result.Measurement.Completed {
		observation := completed.Result.Observation
		if completed.Outcome != benchmark.OutcomeDrainTimeout || observation.StatusCode != http.StatusOK || observation.HeadersAfterNS == nil || observation.CompletedAfterNS == nil || observation.FirstContentAfterNS != nil {
			t.Fatalf("partial result = %+v", completed)
		}
	}
}

func TestOpenLoopLifecycleUsesRealClientAndFakeServer(t *testing.T) {
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = 0
	config.ChunkInterval = 0
	config.ContentChunks = 1
	config.UsageDelay = 0
	config.DoneDelay = 0
	fakeHandler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		fakeHandler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 16
	transport.MaxConnsPerHost = 16
	defer transport.CloseIdleConnections()
	client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(context.Background(), benchmark.LifecyclePlan{
		Mode: benchmark.LoadModeOpenLoop, RunID: "run-open-real-client",
		RequestTemplate: benchmark.Request{Model: "fake-model", Prompt: "private integration prompt", MaxOutputTokens: 4},
		WarmupRequests:  4, RequestTimeout: time.Second, DrainTimeout: time.Second,
		OpenLoop: benchmark.OpenLoopPlan{RequestRate: 20, Duration: 500 * time.Millisecond, MaxInFlight: 16},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls.Load() != 14 || result.Warmup.ArrivalCounts.Started != 4 || result.Measurement.ArrivalCounts.Planned != 10 || result.Measurement.ArrivalCounts.Started != 10 || result.Measurement.ArrivalCounts.ClientLimited != 0 || result.Measurement.ArrivalCounts.SchedulerLimited != 0 {
		t.Fatalf("calls/results = %d/%+v/%+v", calls.Load(), result.Warmup.ArrivalCounts, result.Measurement.ArrivalCounts)
	}
	if result.Warmup.CompletedAt == nil || result.Measurement.StartedAt == nil || result.Measurement.StartedAt.Before(*result.Warmup.CompletedAt) {
		t.Fatalf("open-loop phases overlap: warmup=%+v measurement=%+v", result.Warmup, result.Measurement)
	}
	for _, arrival := range append(append([]benchmark.ArrivalRecord{}, result.Warmup.Arrivals...), result.Measurement.Arrivals...) {
		if arrival.Disposition != benchmark.ArrivalStarted || arrival.RequestID == nil || arrival.ActualStartedAt == nil || arrival.SchedulerLagNS == nil || *arrival.SchedulerLagNS < 0 {
			t.Fatalf("arrival evidence = %+v", arrival)
		}
	}
}

func TestOpenLoopRealClientIsClientLimitedWithoutQueue(t *testing.T) {
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 200 * time.Millisecond
	config.FirstContentDelay = 0
	config.ChunkInterval = 0
	config.ContentChunks = 1
	config.UsageDelay = 0
	config.DoneDelay = 0
	fakeHandler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		fakeHandler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxConnsPerHost = 2
	defer transport.CloseIdleConnections()
	client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(context.Background(), benchmark.LifecyclePlan{
		Mode: benchmark.LoadModeOpenLoop, RunID: "run-open-pressure",
		RequestTemplate: benchmark.Request{Model: "fake-model", Prompt: "private integration prompt", MaxOutputTokens: 4},
		RequestTimeout:  time.Second, DrainTimeout: time.Second,
		OpenLoop: benchmark.OpenLoopPlan{RequestRate: 100, Duration: 100 * time.Millisecond, MaxInFlight: 2},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	counts := result.Measurement.ArrivalCounts
	if calls.Load() != 2 || counts.Planned != 10 || counts.Started != 2 || counts.ClientLimited != 8 || counts.SchedulerLimited != 0 || counts.MaxObservedInFlight != 2 {
		t.Fatalf("calls/counts = %d/%+v", calls.Load(), counts)
	}
}

func TestOpenLoopRealClientDrainTimeoutCancelsOutstandingRequests(t *testing.T) {
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 500 * time.Millisecond
	fakeHandler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(fakeHandler)
	defer server.Close()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxConnsPerHost = 2
	defer transport.CloseIdleConnections()
	client, err := openai.NewClient(&http.Client{Transport: transport}, server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := benchmark.NewLifecycleCoordinator(benchmark.NewRunner(client)).Run(context.Background(), benchmark.LifecyclePlan{
		Mode: benchmark.LoadModeOpenLoop, RunID: "run-open-drain-timeout",
		RequestTemplate: benchmark.Request{Model: "fake-model", Prompt: "private integration prompt", MaxOutputTokens: 4},
		RequestTimeout:  2 * time.Second, DrainTimeout: 30 * time.Millisecond,
		OpenLoop: benchmark.OpenLoopPlan{RequestRate: 20, Duration: 100 * time.Millisecond, MaxInFlight: 2},
	})
	if !errors.Is(err, benchmark.ErrDrainTimeout) {
		t.Fatalf("Run error = %v, want drain timeout", err)
	}
	counts := result.Measurement.ArrivalCounts
	if !result.Drain.TimedOut || counts.Planned != 2 || counts.Started != 2 || counts.ClientLimited != 0 || result.Measurement.Outcomes.DrainTimeout != 2 || len(result.Drain.CancelledRequestIDs) != 2 {
		t.Fatalf("drain-timeout result = %+v", result)
	}
}

func assertLifecycleIDs(t *testing.T, completed []benchmark.CompletedRequest, prefix string) {
	t.Helper()
	seen := make(map[string]struct{}, len(completed))
	for _, request := range completed {
		want := fmt.Sprintf("%s-%06d", prefix, request.Sequence)
		if request.Result.Observation.RequestID != want {
			t.Fatalf("request ID = %q, want %q", request.Result.Observation.RequestID, want)
		}
		if _, exists := seen[want]; exists {
			t.Fatalf("duplicate request ID %q", want)
		}
		seen[want] = struct{}{}
	}
}

type phaseTrackingHandler struct {
	next            http.Handler
	warmupRequests  int64
	target          int64
	warmupRelease   chan struct{}
	measuredRelease chan struct{}
	warmupOnce      sync.Once
	measuredOnce    sync.Once
	calls           atomic.Int64
	warmupCompleted atomic.Int64
	warmupActive    atomic.Int64
	measuredActive  atomic.Int64
	warmupMaximum   atomic.Int64
	measuredMaximum atomic.Int64
	overlap         atomic.Bool
}

func newPhaseTrackingHandler(next http.Handler, warmupRequests, target int) *phaseTrackingHandler {
	return &phaseTrackingHandler{
		next: next, warmupRequests: int64(warmupRequests), target: int64(target),
		warmupRelease: make(chan struct{}), measuredRelease: make(chan struct{}),
	}
}

func (h *phaseTrackingHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	call := h.calls.Add(1)
	if call <= h.warmupRequests {
		current := h.warmupActive.Add(1)
		updateIntegrationMaximum(&h.warmupMaximum, current)
		defer func() {
			h.warmupActive.Add(-1)
			h.warmupCompleted.Add(1)
		}()
		if current == h.target {
			h.warmupOnce.Do(func() { close(h.warmupRelease) })
		}
		select {
		case <-h.warmupRelease:
			h.next.ServeHTTP(writer, request)
		case <-request.Context().Done():
		}
		return
	}
	if h.warmupCompleted.Load() != h.warmupRequests {
		h.overlap.Store(true)
	}
	current := h.measuredActive.Add(1)
	updateIntegrationMaximum(&h.measuredMaximum, current)
	defer h.measuredActive.Add(-1)
	if current == h.target {
		h.measuredOnce.Do(func() { close(h.measuredRelease) })
	}
	select {
	case <-h.measuredRelease:
		h.next.ServeHTTP(writer, request)
	case <-request.Context().Done():
	}
}

type overlapTrackingHandler struct {
	next    http.Handler
	target  int64
	release chan struct{}
	once    sync.Once
	active  atomic.Int64
	maximum atomic.Int64
	calls   atomic.Int64
}

func newOverlapTrackingHandler(next http.Handler, target int) *overlapTrackingHandler {
	return &overlapTrackingHandler{next: next, target: int64(target), release: make(chan struct{})}
}

func (h *overlapTrackingHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.calls.Add(1)
	current := h.active.Add(1)
	updateIntegrationMaximum(&h.maximum, current)
	defer h.active.Add(-1)
	if current == h.target {
		h.once.Do(func() { close(h.release) })
	}
	select {
	case <-h.release:
		h.next.ServeHTTP(writer, request)
	case <-request.Context().Done():
	}
}

func updateIntegrationMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}
