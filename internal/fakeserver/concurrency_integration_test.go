package fakeserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
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
