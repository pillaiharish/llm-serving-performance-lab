# Day 7 Week 1 Report

## Summary

Week 1 built a local LLM serving performance lab around vLLM, an OpenAI-compatible API, and a small Qwen2.5 model on an NVIDIA GeForce RTX 5070 Ti.

The week started with basic serving validation and ended with benchmark evidence for concurrency and prompt/output length effects. Day 7 did not run new benchmarks. It consolidates the existing Day 1 through Day 6 evidence into one engineering report.

The most important result is not a single "best" number. The useful result is the measurement loop: start the server, smoke test it, run controlled synthetic benchmarks, preserve raw JSON/log evidence, parse results, plot trends, and write down what the data does and does not prove.

## What I built

- A local vLLM serving baseline for `models/Qwen/Qwen2.5-0.5B-Instruct`.
- An OpenAI-compatible smoke test for server liveness.
- A first concurrency benchmark at max concurrency 1, 2, and 4.
- A parser that converts vLLM benchmark JSON into CSV.
- Latency and throughput plots for the Day 2 benchmark data.
- Benchmark methodology and result interpretation docs.
- A prompt/output length sweep at fixed concurrency 4.
- A wider concurrency sweep at max concurrency 1, 2, 4, 8, and 16.

## Environment

| Item | Value |
|---|---|
| Machine | `admin1-MS-7D75` |
| GPU | NVIDIA GeForce RTX 5070 Ti, 16 GB |
| Model | `models/Qwen/Qwen2.5-0.5B-Instruct` |
| Server | vLLM OpenAI-compatible API |
| Python | 3.12.3 |
| PyTorch | 2.11.0+cu130 |
| vLLM | 0.22.0 |
| NVIDIA driver | 580.126.09 |
| CUDA reported by `nvidia-smi` | 13.0 |
| GPU capability | `sm_120` |

The first vLLM server attempt failed during engine initialization after selecting the FlashInfer sampler path. The server started successfully after exporting `VLLM_USE_FLASHINFER_SAMPLER=0` and setting max model length to 4096.

## Timeline

| Day | Work completed |
|---:|---|
| 1 | Created the repo identity, checked the GPU/Python/vLLM environment, started a vLLM server, documented the first startup failure, applied the FlashInfer sampler workaround, and passed an OpenAI-compatible smoke test. |
| 2 | Ran the first benchmark loop at concurrency 1, 2, and 4 with 128 configured input tokens, 64 output tokens, and 16 requests per run. |
| 3 | Parsed Day 2 raw JSON into CSV and generated the first latency/throughput plots. |
| 4 | Added benchmark methodology and interpretation docs to clarify what claims are supported. |
| 5 | Ran a prompt/output length sweep at fixed concurrency 4 for small, medium, and large synthetic workloads. |
| 6 | Ran a wider concurrency sweep at concurrency 1, 2, 4, 8, and 16 with 64 requests per run. |

## Key results

- Day 2/3 showed output throughput increasing from 438.30 tok/s at concurrency 1 to 1526.10 tok/s at concurrency 4, while E2E p95 increased from 148.70 ms to 170.16 ms.
- Day 5 showed larger workloads increasing E2E p95: 215.67 ms for small, 296.37 ms for medium, and 664.01 ms for large.
- Day 5 did not show monotonic TTFT behavior. Medium had the best TTFT p95 and output throughput in that single run, while small had the best E2E p95.
- Day 6 showed output throughput increasing from 477.07 tok/s at concurrency 1 to 6428.86 tok/s at concurrency 16.
- Day 6 also showed latency increasing: TTFT p95 went from 8.61 ms to 22.08 ms, and E2E p95 went from 134.51 ms to 160.22 ms.
- All reported Day 2, Day 5, and Day 6 runs completed with 0 failed requests.

## Day 2/3 concurrency baseline

The first benchmark used one model, one GPU, synthetic random prompts, 128 configured input tokens, 64 output tokens, and 16 total requests per run.

| Concurrency | Completed | Failed | Request throughput | TTFT p95 | E2E p95 | Output tok/s |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 16 | 0 | 6.85 req/s | 11.08 ms | 148.70 ms | 438.30 |
| 2 | 16 | 0 | 12.23 req/s | 11.60 ms | 164.94 ms | 782.64 |
| 4 | 16 | 0 | 23.85 req/s | 12.99 ms | 170.16 ms | 1526.10 |

This supported a narrow conclusion: within the tested range, higher concurrency improved throughput while moderately increasing latency. It did not prove production capacity or a global optimum.

## Day 5 length sweep

Day 5 kept concurrency fixed at 4 and changed configured input/output length.

| Case | Configured input | Configured output | Observed input/request | Failed | TTFT p95 | E2E p95 | Output tok/s |
|---|---:|---:|---:|---:|---:|---:|---:|
| small | 128 | 64 | 157 | 0 | 84.80 ms | 215.67 ms | 1566.47 |
| medium | 512 | 128 | 541 | 0 | 31.50 ms | 296.37 ms | 1742.67 |
| large | 2048 | 256 | 2077 | 0 | 92.10 ms | 664.01 ms | 1568.42 |

Larger workloads increased E2E latency. That is expected because longer outputs require more decode work, and longer inputs add prefill work.

TTFT did not increase monotonically in this single run. Medium had the best TTFT p95 and output throughput, but small had the best E2E p95. This does not support claiming a globally best length configuration.

## Day 6 wide concurrency sweep

Day 6 kept the request shape fixed at 128 configured input tokens, 64 output tokens, and 64 prompts per run, then increased max concurrency.

| Concurrency | Completed | Failed | Request throughput | TTFT p95 | E2E p95 | Output tok/s |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 64 | 0 | 7.45 req/s | 8.61 ms | 134.51 ms | 477.07 |
| 2 | 64 | 0 | 14.37 req/s | 10.25 ms | 140.30 ms | 919.96 |
| 4 | 64 | 0 | 28.02 req/s | 12.42 ms | 143.84 ms | 1792.99 |
| 8 | 64 | 0 | 54.92 req/s | 16.12 ms | 148.93 ms | 3515.04 |
| 16 | 64 | 0 | 100.45 req/s | 22.08 ms | 160.22 ms | 6428.86 |

Throughput increased strongly as concurrency increased. Latency also increased. Concurrency 16 was the highest-throughput tested point and still had 0 failed requests in this synthetic run.

This does not prove concurrency 16 is production capacity. It only says concurrency 16 was the highest-throughput tested point for this model, GPU, request shape, and single-run benchmark.

## What the results suggest

- The basic serving and benchmark loop works on this local machine.
- Increasing concurrency improved GPU utilization and total output throughput in the tested range.
- Higher throughput came with higher TTFT p95 and E2E p95.
- Longer prompt/output workloads increased E2E latency.
- Output throughput alone is not enough to choose a serving configuration.
- TTFT, TPOT, ITL, E2E latency, failures, and request throughput need to be read together.
- Configured random input length is not always equal to observed tokenized input length because chat formatting and benchmark prompt construction add tokens.

## What the results do not prove

- They do not prove production readiness.
- They do not prove production-safe capacity.
- They do not prove concurrency 16 is globally optimal.
- They do not prove behavior under real user traffic.
- They do not prove behavior under mixed prompt/output lengths.
- They do not include repeated runs, variance, or error bars.
- They do not include GPU utilization tracking.
- They do not identify a breaking point yet.

## Engineering lessons

- Benchmark reports need raw evidence, parsed summaries, plots, and written interpretation.
- Smoke tests prove API liveness, not answer quality or serving capacity.
- A small successful benchmark is useful, but only as a baseline.
- "Best" depends on the metric: TTFT, E2E latency, output throughput, and failure rate can point in different directions.
- A capacity claim needs an explicit latency SLO and tests beyond the first successful high-concurrency point.
- Methodology docs make benchmark numbers harder to misread.

## Portfolio value

This week shows the start of an AI infrastructure portfolio repo, not a chatbot demo.

The repo demonstrates:

- local GPU environment validation
- vLLM server startup and debugging
- OpenAI-compatible API smoke testing
- synthetic benchmark execution
- raw result preservation
- CSV parsing and plot generation
- conservative technical interpretation
- awareness of unsupported claims and benchmark limitations

## Next week plan

- Run breaking-point tests at concurrency 32, 64, and 128.
- Define an explicit latency SLO before choosing any serving target.
- Add repeated runs to estimate variance.
- Track GPU utilization during benchmark runs.
- Test mixed prompt/output lengths instead of one fixed synthetic shape.
- Start RAG-under-load experiments to measure retrieval and generation behavior together.
- Compare synthetic benchmark behavior with more realistic prompt datasets.
