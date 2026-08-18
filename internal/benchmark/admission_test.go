package benchmark

import (
	"strings"
	"testing"
)

func TestAdmitRunUsesExplicitCeilingsWithoutDerivingCapacity(t *testing.T) {
	diagnostics := ClientDiagnostics{NumCPU: 1, GOMAXPROCS: 1, GoVersion: "test-go", GOOS: "test-os", GOARCH: "test-arch"}
	decision, err := AdmitRun(AdmissionRequest{Concurrency: 512, Requests: 3, WarmupRequests: 2, MaxConcurrency: 512, MaxRequests: 3}, diagnostics)
	if err != nil {
		t.Fatalf("AdmitRun: %v", err)
	}
	if decision.WorkerCount != 3 || decision.MeasurementWorkerCount != 3 || decision.WarmupWorkerCount != 2 || decision.TransportWorkerLimit != 3 || decision.Diagnostics != diagnostics {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestAdmitRunRejectsInvalidOrOverCeilingValues(t *testing.T) {
	tests := []struct {
		name    string
		request AdmissionRequest
		want    string
	}{
		{name: "zero concurrency", request: AdmissionRequest{Concurrency: 0, Requests: 1, MaxConcurrency: 1, MaxRequests: 1}, want: "concurrency"},
		{name: "zero requests", request: AdmissionRequest{Concurrency: 1, Requests: 0, MaxConcurrency: 1, MaxRequests: 1}, want: "requests"},
		{name: "negative warmup", request: AdmissionRequest{Concurrency: 1, Requests: 1, WarmupRequests: -1, MaxConcurrency: 1, MaxRequests: 1}, want: "warmup_requests"},
		{name: "zero concurrency ceiling", request: AdmissionRequest{Concurrency: 1, Requests: 1, MaxConcurrency: 0, MaxRequests: 1}, want: "max_concurrency"},
		{name: "zero request ceiling", request: AdmissionRequest{Concurrency: 1, Requests: 1, MaxConcurrency: 1, MaxRequests: 0}, want: "max_requests"},
		{name: "concurrency exceeds ceiling", request: AdmissionRequest{Concurrency: 9, Requests: 9, MaxConcurrency: 8, MaxRequests: 10}, want: "raise the safety ceiling"},
		{name: "requests exceed ceiling", request: AdmissionRequest{Concurrency: 2, Requests: 11, MaxConcurrency: 8, MaxRequests: 10}, want: "raise the safety ceiling"},
		{name: "warmup exceeds ceiling", request: AdmissionRequest{Concurrency: 2, Requests: 2, WarmupRequests: 11, MaxConcurrency: 8, MaxRequests: 10}, want: "warmup_requests"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := AdmitRun(test.request, ClientDiagnostics{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAdmitRunSizesTransportForLargerWarmup(t *testing.T) {
	decision, err := AdmitRun(AdmissionRequest{Concurrency: 8, Requests: 1, WarmupRequests: 8, MaxConcurrency: 8, MaxRequests: 8}, ClientDiagnostics{})
	if err != nil {
		t.Fatalf("AdmitRun: %v", err)
	}
	if decision.WarmupWorkerCount != 8 || decision.MeasurementWorkerCount != 1 || decision.TransportWorkerLimit != 8 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestCollectClientDiagnostics(t *testing.T) {
	diagnostics := CollectClientDiagnostics()
	if diagnostics.NumCPU <= 0 || diagnostics.GOMAXPROCS <= 0 || diagnostics.GoVersion == "" || diagnostics.GOOS == "" || diagnostics.GOARCH == "" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}
