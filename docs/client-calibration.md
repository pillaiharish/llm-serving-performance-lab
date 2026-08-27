# Client calibration

`slentore calibrate-client` answers one deliberately narrow question:

> Can this Slentore client process deliver this tested load cleanly against a
> lightweight endpoint?

It does not measure GPU or model capacity. A configured safety ceiling is a
policy guardrail, not measured machine capacity, and the highest tested clean
point is not a universal safe maximum or a statement about untested values.
Calibration never changes `benchmark.safety` values or silently lowers a
requested load.

## Isolate the client process

Run `slentore-fake-server` as a separate process on loopback. Keeping the
fixture server out of the Slentore process prevents its heap and goroutines
from being counted as client evidence. Port zero asks the operating system for
an unused local port; read the actual address from the startup output.

```bash
build/slentore-fake-server \
  --listen 127.0.0.1:0 \
  --header-delay 0 \
  --first-content-delay 0 \
  --chunk-interval 0 \
  --usage-delay 0 \
  --done-delay 0
```

Closed-loop calibration:

```bash
build/slentore calibrate-client \
  --base-url http://127.0.0.1:<port>/v1 \
  --model fixture-model \
  --mode closed-loop \
  --concurrency-values 1,4,16 \
  --requests 100 \
  --output-dir calibration-artifacts
```

Open-loop calibration:

```bash
build/slentore calibrate-client \
  --base-url http://127.0.0.1:<port>/v1 \
  --model fixture-model \
  --mode open-loop \
  --request-rate-values 100,250,500 \
  --duration 2s \
  --max-in-flight 256 \
  --output-dir calibration-artifacts
```

One invocation uses one load mode and one load axis. Points run sequentially
through the ordinary experiment runner, and each point is a fresh schema-7
benchmark with the normal setup, optional warmup, measurement, stop-admission,
drain, summary, and artifact lifecycle. The workload is a fixed transient
direct prompt with one requested output token, temperature zero, and token
timing disabled. Prompt text is not persisted.

Authentication retains the normal indirection contract:

```bash
export VLLM_API_KEY='...'
build/slentore calibrate-client ... --api-key-env VLLM_API_KEY
```

The secret value is never accepted as a flag or stored in artifacts.

## Clean delivery

A closed-loop point is clean only when the child run completes normally, every
requested measured request is attempted and completes successfully, and no
request error, timeout, parent cancellation, or drain timeout occurs. There is
no latency threshold: a slow endpoint may still have been driven faithfully.

An open-loop point additionally requires every planned arrival to be
processed, delivery ratio one, and zero client-limited, scheduler-limited, or
cancellation-unprocessed arrivals. `client_limited` means the configured
in-flight guard was full. `scheduler_limited` means the local process did not
process an arrival before the admission deadline. Scheduler lag is always
reported for started arrivals but does not fail calibration by default.

An explicit inclusive budget can be added only for open loop:

```bash
--max-scheduler-lag-p95 5ms
```

The point is then clean only when scheduler-lag p95 is available and at most
the supplied value. GitHub CI intentionally does not use this option because
host timing is noisy.

A dirty point does not stop later points. Exit zero means every planned point
was evaluated and clean. Exit one means evidence was published but a point was
dirty or unevaluated, or execution was cancelled/failed. Exit two means input
was rejected before execution.

## Runtime evidence and artifacts

The client samples standard Go runtime evidence every 10 ms around each child
execution. It records start/end and sampled peak goroutine counts; start/end
and sampled peak heap allocation; sampled `HeapSys` and `Sys` peaks; and
deltas for total allocation, mallocs, frees, GC cycles, and total GC pause.
Initial and final readings are always included.

“Observed peak” is a sampled peak, not a mathematically guaranteed
instantaneous maximum. The sampler goroutine can contribute to the goroutine
peak and slightly affects allocation evidence. Calibration does not claim
portable CPU utilization, RSS, file-descriptor peak, or exact profiler
precision.

```text
<output-dir>/
├── experiments/<experiment-id>/       # experiment schema 1
│   └── runs/<run-id>/                  # ordinary run schema 7
└── calibrations/<calibration-id>/
    ├── calibration.json                # calibration schema 1
    └── summary.csv
```

The calibration report references rather than duplicates child observations.
It lists every tested value and does not publish a `max_capacity`, `safe_limit`,
or automatic recommendation. Calibration publication is staged and atomically
renamed. If it fails after the experiment was published, the valid experiment
is retained and reported rather than rolled back.
