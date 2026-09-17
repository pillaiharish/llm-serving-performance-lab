# A100 Qwen3-8B prefix-cache-OFF inference study

## Scope

This report documents a controlled, single-GPU inference study performed on
2026-09-16. It follows one deployment through closed-loop concurrency,
exploratory workload shapes, a fixed-output input-length control, and open-loop
arrival-rate saturation. Results apply only to the recorded model, software,
hardware, workload, and server configuration.

The compact source tables are in
[`evidence/a100-qwen3-8b-prefixoff-20260916/`](../../evidence/a100-qwen3-8b-prefixoff-20260916/).
Values shown for percentile columns are arithmetic means of the three
per-repetition summary percentiles, not percentiles pooled across requests.

## Environment and provenance

The server was vLLM 0.26.0 on one NVIDIA A100-SXM4-40GB (40,960 MiB), using
`Qwen/Qwen3-8B`, bfloat16 weights, automatic KV-cache dtype, TP/world size 1,
`max_model_len=32768`, `max_num_seqs=16`, and
`gpu_memory_utilization=0.90`. Generation configuration was `vllm`, thinking
was disabled, and prefix caching was explicitly disabled. The detailed
qualified environment and recovered model snapshot are recorded in
[`environment.md`](../../evidence/a100-qwen3-8b-prefixoff-20260916/environment.md).

Measurements used Slentore commit
`4a573b104970d7300ebebd136548e0c679fd4f74`, version
`v0.0.0-20260830170145-4a573b104970`. The later PR #26 commissioning code did
not generate the earlier measurements; it validated the qualified Slentore
binary/source identity and the live environment.

## Experimental controls

All canonical points used a deterministic token-length workload, temperature
zero, and three repetitions. Each closed-loop point measured 320 successful
requests with zero failures. Shape and fixed-output warmup discarded 4 ×
concurrency requests (32 at C8 and 64 at C16). The earlier concurrency sweep
used its own archived policy: 10 warmups at C1/C2, then 12, 24, 48, and 96 at
C4/C8/C16/C32. The open-loop study used 32 warmup requests and 45 seconds per
point with `max_in_flight=64`.

True ITL was requested from Slentore's
`vllm_return_token_ids_client_receive` source, which uses returned token
identity and client receive timing. It remains a client-visible interval, not
GPU kernel decode latency. Missing ITL observations are counted rather than
reconstructed.

## Why prefix caching was disabled

The deterministic workload can reuse prompt structure. Disabling prefix
caching removes cache reuse as an experimental variable for this study.
Startup configuration and before/after Prometheus metadata independently
recorded the disabled state. These results do not predict prefix-cache-ON
behavior.

## Experiment sequence

### 1. Closed-loop concurrency

The 512-token input / max-128-output workload produced exactly 41 output tokens
per successful request in all 18 canonical runs. Mean successful throughput
rose from 1.632 req/s at C1 to 11.377 req/s at C16, then changed only to 11.456
req/s at C32 (+0.7%). At the same time, mean TTFT p50 rose from 467 ms at C16
to 1,716 ms at C32, and E2E p50 rose from 1,408 ms to 2,797 ms. This is an
observed throughput plateau with sharply higher latency, not evidence that C16
is universally optimal or C32 is useless.

C32 intentionally exceeded the server's `max_num_seqs=16`. A client-side
`max_observed_active=32` records admitted client work; it does not show that
vLLM executed 32 sequences simultaneously. Likewise, Prometheus
`kv_cache_max_concurrency` is KV-capacity metadata, not observed request
concurrency. No GPU batching mechanism is inferred from these client-visible
results.

True-ITL coverage was complete at C2–C16, 958/960 at C1, and 944/960 at C32.
The full aggregate is in
[`concurrency-summary.csv`](../../evidence/a100-qwen3-8b-prefixoff-20260916/concurrency-summary.csv).

### 2. Exploratory workload shapes

The exploratory matrix crossed C8/C16 with requested input/max-output shapes
128/128, 512/128, 2048/128, 512/16, and 512/32. Requested maximum output was
not actual output: the 512/max16 and 512/max32 points produced exactly 16 and
32 tokens, while 512/max128 produced 41. Mean actual output at 128/max128 was
88.04 tokens at C8 and 81.70 at C16; at 2048/max128 it was 66.71 and 67.10.

Consequently, the max-128 rows are mixed workload-shape observations, not a
pure prefill/input-length comparison. That confound directly motivated the
fixed-32 experiment. The 30 canonical runs and complete 9,600/9,600 true-ITL
coverage are summarized in
[`shape-summary.csv`](../../evidence/a100-qwen3-8b-prefixoff-20260916/shape-summary.csv).

### 3. Fixed-output prefill isolation

This controlled comparison held requested and actual output at exactly 32
tokens while varying input length across 128, 512, and 2,048 tokens at C8 and
C16. All 18 runs completed 320/320 requests, with 5,760/5,760 true-ITL
coverage.

At C8, mean successful request throughput fell from 14.762 to 9.250 to 3.902
req/s as input length increased, while mean input throughput rose from 1,889 to
4,736 to 7,992 tokens/s. At C16, the corresponding values were 24.212, 12.563,
and 4.275 req/s, but 3,099, 6,432, and 8,756 input tokens/s. Request/s alone is
therefore insufficient for comparing different input sizes. Concurrency
tolerance and latency tails also changed materially with prompt length: the
2,048-token C16 point had mean TTFT p99 of 2,888 ms and E2E p99 of 5,786 ms.

These are client-visible observations. They do not identify a GPU kernel-level
cause. See
[`prefill32-summary.csv`](../../evidence/a100-qwen3-8b-prefixoff-20260916/prefill32-summary.csv).

### 4. Open-loop saturation

The open-loop phase used 512 input tokens, requested and actual output of 32
tokens, rates 6, 8, 10, 11, 12, 13, 14, and 16 req/s, and deliberately
counterbalanced repetition orders. From 6 through 12 req/s, delivery was
complete, no arrivals were client-limited, and mean TTFT p95 stayed between 91
and 102 ms.

At 13 req/s, delivery remained complete and client limiting remained zero, but
mean maximum in-flight rose to 49, TTFT p95 to 2,609 ms, and E2E p95 to 3,771
ms. This is the clearest observed saturation transition: queue/in-flight
pressure and client-visible tail latency expanded before the configured client
admission ceiling was reached.

At 14 and 16 req/s, mean delivery ratios were 0.953 and 0.829, maximum
in-flight reached the configured ceiling of 64, and mean client-limited counts
were 29.7 and 123.0. All started requests succeeded and true-ITL coverage was
complete. `scheduler_limited` remained zero at every rate; the incomplete
delivery at high rates is recorded as client admission limiting, not scheduler
limiting. The high-rate child statuses are therefore expected evidence rather
than invalid points.

This transition does not establish a universal 12 req/s A100 capacity. It is
specific to this workload and server configuration. See
[`openloop-summary.csv`](../../evidence/a100-qwen3-8b-prefixoff-20260916/openloop-summary.csv).

## Cross-experiment observations

The experiment sequence matters:

1. The concurrency sweep located an observed throughput plateau and growing
   latency under a fixed observed 41-token decode.
2. Shape exploration showed that a max-output request is only a ceiling and
   exposed varying decode lengths.
3. Holding actual output at 32 tokens produced a cleaner input-length control
   and showed why both request and token throughput are needed.
4. Open-loop load then separated complete arrival delivery with exploding
   latency at 13 req/s from client-ceiling effects at 14 and 16 req/s.

These associations do not establish causality. Server internals and GPU kernel
behavior were not experimentally isolated.

## What the evidence does not prove

This study does not establish a universal A100 capacity, an optimal
concurrency, the value of C32 for another objective, or a causal batching or
kernel explanation. It does not generalize to another model, GPU, vLLM
version, quantization, prompt distribution, output distribution, or prefix
cache setting. Client active/in-flight counts are not server active-sequence
counts, true ITL is not GPU kernel latency, and warmup completion does not prove
thermal equilibrium.

No comparison to GuideLLM, AIPerf, `vllm bench`, or prefix-cache-ON data is made
here; cross-tool calibration belongs in a separate study.

## Reproducibility and raw-evidence policy

The five immutable local archives were verified against external SHA-256
sidecars. Raw archives are not committed because they include request-level
payloads and generated content, full metrics and telemetry, logs, and endpoint
details. Exact archive digests, compact derivatives, aggregation semantics,
and audit results are published in the evidence directory. The raw archives
remain the source of truth.

## Known evidence limitations

The concurrency package's internal manifest includes a digest entry for
itself. That single entry cannot verify after the manifest is finalized; all
13,069 other entries match, and the outer archive matches its external
sidecar. The package was not rewritten. The exact model snapshot is persisted
in the open-loop environment archive rather than independently duplicated in
each earlier phase archive. See
[`evidence-audit.md`](../../evidence/a100-qwen3-8b-prefixoff-20260916/evidence-audit.md)
for the complete boundary.
