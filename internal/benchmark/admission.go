package benchmark

import (
	"fmt"
	"math"
	"runtime"
	"time"
)

type AdmissionRequest struct {
	Mode               LoadMode
	Concurrency        int
	Requests           int
	WarmupRequests     int
	RequestRate        float64
	Duration           time.Duration
	MaxInFlight        int
	MaxConcurrency     int
	MaxRequests        int
	MaxRequestRate     float64
	MaxInFlightCeiling int
}

type ClientDiagnostics struct {
	NumCPU     int    `json:"num_cpu"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
}

type AdmissionDecision struct {
	Mode                   LoadMode
	WorkerCount            int
	WarmupWorkerCount      int
	MeasurementWorkerCount int
	TransportWorkerLimit   int
	PlannedArrivals        int
	Diagnostics            ClientDiagnostics
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
	mode := request.Mode
	if mode == "" {
		mode = LoadModeClosedLoop
	}
	if request.WarmupRequests < 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.warmup_requests must not be negative")
	}
	if request.MaxRequests <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_requests must be greater than zero")
	}
	if request.WarmupRequests > request.MaxRequests {
		return AdmissionDecision{}, fmt.Errorf("benchmark.warmup_requests %d exceeds benchmark.safety.max_requests %d; raise the safety ceiling explicitly to admit this run", request.WarmupRequests, request.MaxRequests)
	}
	if mode == LoadModeOpenLoop {
		return admitOpenLoop(request, diagnostics)
	}
	if mode != LoadModeClosedLoop {
		return AdmissionDecision{}, fmt.Errorf("benchmark.mode must be closed_loop or open_loop")
	}
	if request.Concurrency <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.concurrency must be greater than zero")
	}
	if request.Requests <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.requests must be greater than zero")
	}
	if request.MaxConcurrency <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_concurrency must be greater than zero")
	}
	if request.Concurrency > request.MaxConcurrency {
		return AdmissionDecision{}, fmt.Errorf("benchmark.concurrency %d exceeds benchmark.safety.max_concurrency %d; raise the safety ceiling explicitly to admit this run", request.Concurrency, request.MaxConcurrency)
	}
	if request.Requests > request.MaxRequests {
		return AdmissionDecision{}, fmt.Errorf("benchmark.requests %d exceeds benchmark.safety.max_requests %d; raise the safety ceiling explicitly to admit this run", request.Requests, request.MaxRequests)
	}

	measurementWorkers := request.Concurrency
	if request.Requests < measurementWorkers {
		measurementWorkers = request.Requests
	}
	warmupWorkers := request.Concurrency
	if request.WarmupRequests == 0 {
		warmupWorkers = 0
	} else if request.WarmupRequests < warmupWorkers {
		warmupWorkers = request.WarmupRequests
	}
	transportLimit := measurementWorkers
	if warmupWorkers > transportLimit {
		transportLimit = warmupWorkers
	}
	return AdmissionDecision{
		Mode:                   LoadModeClosedLoop,
		WorkerCount:            measurementWorkers,
		WarmupWorkerCount:      warmupWorkers,
		MeasurementWorkerCount: measurementWorkers,
		TransportWorkerLimit:   transportLimit,
		Diagnostics:            diagnostics,
	}, nil
}

func admitOpenLoop(request AdmissionRequest, diagnostics ClientDiagnostics) (AdmissionDecision, error) {
	if math.IsNaN(request.RequestRate) || math.IsInf(request.RequestRate, 0) || request.RequestRate <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop.request_rate must be a finite number greater than zero")
	}
	if request.Duration <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop.duration must be greater than zero")
	}
	if request.MaxInFlight <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop.max_in_flight must be greater than zero")
	}
	if math.IsNaN(request.MaxRequestRate) || math.IsInf(request.MaxRequestRate, 0) || request.MaxRequestRate <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_request_rate must be a finite number greater than zero")
	}
	if request.MaxInFlightCeiling <= 0 {
		return AdmissionDecision{}, fmt.Errorf("benchmark.safety.max_in_flight must be greater than zero")
	}
	if request.RequestRate > request.MaxRequestRate {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop.request_rate %g exceeds benchmark.safety.max_request_rate %g; raise the safety ceiling explicitly to admit this run", request.RequestRate, request.MaxRequestRate)
	}
	if request.MaxInFlight > request.MaxInFlightCeiling {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop.max_in_flight %d exceeds benchmark.safety.max_in_flight %d; raise the safety ceiling explicitly to admit this run", request.MaxInFlight, request.MaxInFlightCeiling)
	}
	planned, err := PlannedArrivalCount(request.RequestRate, request.Duration)
	if err != nil {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop schedule: %w", err)
	}
	if planned > request.MaxRequests {
		return AdmissionDecision{}, fmt.Errorf("planned arrivals %d exceed benchmark.safety.max_requests %d; raise the safety ceiling explicitly to admit this run", planned, request.MaxRequests)
	}
	if _, err := DurationArrivalOffsets(request.RequestRate, request.Duration); err != nil {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop measurement schedule: %w", err)
	}
	if _, err := ArrivalOffsets(request.RequestRate, request.WarmupRequests); err != nil {
		return AdmissionDecision{}, fmt.Errorf("benchmark.open_loop warmup schedule: %w", err)
	}
	return AdmissionDecision{
		Mode:                 LoadModeOpenLoop,
		TransportWorkerLimit: request.MaxInFlight,
		PlannedArrivals:      planned,
		Diagnostics:          diagnostics,
	}, nil
}
