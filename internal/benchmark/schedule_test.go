package benchmark

import (
	"math"
	"testing"
	"time"
)

func TestDurationArrivalOffsetsUseAbsoluteEpoch(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
		duration time.Duration
		want     []time.Duration
	}{
		{name: "integer", rate: 10, duration: time.Second, want: []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond, 600 * time.Millisecond, 700 * time.Millisecond, 800 * time.Millisecond, 900 * time.Millisecond}},
		{name: "fractional", rate: 2.5, duration: time.Second, want: []time.Duration{0, 400 * time.Millisecond, 800 * time.Millisecond}},
		{name: "sub-hertz", rate: 0.5, duration: 3 * time.Second, want: []time.Duration{0, 2 * time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offsets, err := DurationArrivalOffsets(test.rate, test.duration)
			if err != nil {
				t.Fatalf("DurationArrivalOffsets: %v", err)
			}
			if len(offsets) != len(test.want) {
				t.Fatalf("offset count = %d, want %d: %v", len(offsets), len(test.want), offsets)
			}
			for index := range offsets {
				if offsets[index] != test.want[index] || offsets[index] >= test.duration {
					t.Fatalf("offset %d = %s, want %s", index+1, offsets[index], test.want[index])
				}
				if index > 0 && offsets[index] < offsets[index-1] {
					t.Fatalf("offsets decrease: %v", offsets)
				}
			}
		})
	}
}

func TestScheduleValidationRejectsInvalidAndOverflowingInputs(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := PlannedArrivalCount(rate, time.Second); err == nil {
			t.Fatalf("rate %v unexpectedly accepted", rate)
		}
	}
	if _, err := PlannedArrivalCount(1, 0); err == nil {
		t.Fatal("zero duration unexpectedly accepted")
	}
	if _, err := PlannedArrivalCount(math.MaxFloat64, time.Duration(math.MaxInt64)); err == nil {
		t.Fatal("overflowing planned count unexpectedly accepted")
	}
	if _, err := ArrivalOffsets(1e-300, 2); err == nil {
		t.Fatal("overflowing arrival offset unexpectedly accepted")
	}
}

func TestAdmitOpenLoopUsesExplicitCeilings(t *testing.T) {
	decision, err := AdmitRun(AdmissionRequest{
		Mode: LoadModeOpenLoop, WarmupRequests: 4, RequestRate: 20, Duration: 500 * time.Millisecond, MaxInFlight: 16,
		MaxRequests: 100, MaxRequestRate: 100, MaxInFlightCeiling: 32,
	}, ClientDiagnostics{NumCPU: 1})
	if err != nil {
		t.Fatalf("AdmitRun: %v", err)
	}
	if decision.Mode != LoadModeOpenLoop || decision.PlannedArrivals != 10 || decision.TransportWorkerLimit != 16 {
		t.Fatalf("decision = %+v", decision)
	}

	for _, test := range []struct {
		name  string
		alter func(*AdmissionRequest)
	}{
		{name: "rate ceiling", alter: func(value *AdmissionRequest) { value.RequestRate = 101 }},
		{name: "in-flight ceiling", alter: func(value *AdmissionRequest) { value.MaxInFlight = 33 }},
		{name: "planned ceiling", alter: func(value *AdmissionRequest) { value.Duration = 6 * time.Second }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := AdmissionRequest{Mode: LoadModeOpenLoop, RequestRate: 20, Duration: time.Second, MaxInFlight: 16, MaxRequests: 100, MaxRequestRate: 100, MaxInFlightCeiling: 32}
			test.alter(&request)
			if _, err := AdmitRun(request, ClientDiagnostics{}); err == nil {
				t.Fatal("unsafe open-loop run unexpectedly admitted")
			}
		})
	}
}
