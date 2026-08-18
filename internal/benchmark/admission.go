package benchmark

import (
	"fmt"
	"runtime"
)

type AdmissionRequest struct {
	Concurrency    int
	Requests       int
	MaxConcurrency int
	MaxRequests    int
}

type ClientDiagnostics struct {
	NumCPU     int    `json:"num_cpu"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
}

type AdmissionDecision struct {
	WorkerCount int
	Diagnostics ClientDiagnostics
}

func CollectClientDiagnostics() ClientDiagnostics {
	return ClientDiagnostics{
		NumCPU:     runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

// AdmitRun applies explicit user-configured safety ceilings. Machine
// diagnostics are returned for evidence only and are not used to derive or
// silently reduce concurrency.
func AdmitRun(request AdmissionRequest, diagnostics ClientDiagnostics) (AdmissionDecision, error) {
	if request.Concurrency <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.concurrency must be greater than zero")
	}
	if request.Requests <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.requests must be greater than zero")
	}
	if request.MaxConcurrency <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_concurrency must be greater than zero")
	}
	if request.MaxRequests <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_requests must be greater than zero")
	}
	if request.Concurrency > request.MaxConcurrency {
		return AdmissionDecision{}, fmt.Errorf("benchmark.concurrency %d exceeds benchmark.safety.max_concurrency %d; raise the safety ceiling explicitly to admit this run", request.Concurrency, request.MaxConcurrency)
	}
	if request.Requests > request.MaxRequests {
		return AdmissionDecision{}, fmt.Errorf("benchmark.requests %d exceeds benchmark.safety.max_requests %d; raise the safety ceiling explicitly to admit this run", request.Requests, request.MaxRequests)
	}

	workers := request.Concurrency
	if request.Requests < workers {
		workers = request.Requests
	}
	return AdmissionDecision{WorkerCount: workers, Diagnostics: diagnostics}, nil
}
