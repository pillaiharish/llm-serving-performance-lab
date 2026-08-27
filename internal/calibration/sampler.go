package calibration

import (
	"runtime"
	"sync"
	"time"
)

type runtimeSample struct {
	observedAt time.Time
	goroutines int
	mem        runtime.MemStats
}

type Sampler struct {
	Interval time.Duration
	Now      func() time.Time
}

func NewSampler() Sampler {
	return Sampler{Interval: DefaultSampleInterval, Now: time.Now}
}

func (s Sampler) Measure(operation func()) ResourceEvidence {
	if s.Interval <= 0 {
		s.Interval = DefaultSampleInterval
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	start := captureRuntimeSample(s.Now)
	peak := start
	samples := 1
	stop := make(chan struct{})
	done := make(chan struct{})
	var lock sync.Mutex
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := captureRuntimeSample(s.Now)
				lock.Lock()
				updatePeak(&peak, current)
				samples++
				lock.Unlock()
			case <-stop:
				return
			}
		}
	}()

	operation()
	close(stop)
	<-done
	end := captureRuntimeSample(s.Now)
	lock.Lock()
	updatePeak(&peak, end)
	samples++
	lock.Unlock()

	return ResourceEvidence{
		StartedAt:                  start.observedAt.UTC(),
		CompletedAt:                end.observedAt.UTC(),
		ElapsedNS:                  end.observedAt.Sub(start.observedAt).Nanoseconds(),
		SampleIntervalNS:           s.Interval.Nanoseconds(),
		Samples:                    samples,
		GoroutinesStart:            start.goroutines,
		GoroutinesObservedPeak:     peak.goroutines,
		GoroutinesEnd:              end.goroutines,
		HeapAllocStartBytes:        start.mem.HeapAlloc,
		HeapAllocObservedPeakBytes: peak.mem.HeapAlloc,
		HeapAllocEndBytes:          end.mem.HeapAlloc,
		HeapSysObservedPeakBytes:   peak.mem.HeapSys,
		SysObservedPeakBytes:       peak.mem.Sys,
		TotalAllocDeltaBytes:       monotonicDelta(end.mem.TotalAlloc, start.mem.TotalAlloc),
		MallocsDelta:               monotonicDelta(end.mem.Mallocs, start.mem.Mallocs),
		FreesDelta:                 monotonicDelta(end.mem.Frees, start.mem.Frees),
		NumGCDelta:                 monotonicDelta32(end.mem.NumGC, start.mem.NumGC),
		GCPauseTotalDeltaNS:        monotonicDelta(end.mem.PauseTotalNs, start.mem.PauseTotalNs),
	}
}

func captureRuntimeSample(now func() time.Time) runtimeSample {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return runtimeSample{observedAt: now(), goroutines: runtime.NumGoroutine(), mem: memory}
}

func updatePeak(peak *runtimeSample, current runtimeSample) {
	if current.goroutines > peak.goroutines {
		peak.goroutines = current.goroutines
	}
	if current.mem.HeapAlloc > peak.mem.HeapAlloc {
		peak.mem.HeapAlloc = current.mem.HeapAlloc
	}
	if current.mem.HeapSys > peak.mem.HeapSys {
		peak.mem.HeapSys = current.mem.HeapSys
	}
	if current.mem.Sys > peak.mem.Sys {
		peak.mem.Sys = current.mem.Sys
	}
}

func monotonicDelta(end, start uint64) uint64 {
	if end < start {
		return 0
	}
	return end - start
}

func monotonicDelta32(end, start uint32) uint32 {
	if end < start {
		return 0
	}
	return end - start
}
