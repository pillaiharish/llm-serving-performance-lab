# Week 1 Benchmark Findings

This document summarizes the main Week 1 benchmark findings for the LLM Serving Performance Lab.

These are local synthetic benchmark results for one model, one GPU, and a small set of request shapes. They are useful for comparing runs inside this repo, but they are not production capacity claims.

## Environment

| Item | Value |
|---|---|
| GPU | NVIDIA GeForce RTX 5070 Ti, 16 GB |
| Model | `models/Qwen/Qwen2.5-0.5B-Instruct` |
| Server | vLLM OpenAI-compatible API |
| Main metrics | TTFT, TPOT, ITL, E2E latency, request throughput, output token throughput |

## Day 2/3 concurrency baseline

The first benchmark tested max concurrency 1, 2, and 4 with 128 configured input tokens, 64 output tokens, and 16 requests per run.

| Concurrency | Failed | E2E p95 | Output tok/s |
|---:|---:|---:|---:|
| 1 | 0 | 148.70 ms | 438.30 |
| 2 | 0 | 164.94 ms | 782.64 |
| 4 | 0 | 170.16 ms | 1526.10 |

Finding: output token throughput increased strongly from concurrency 1 to 4, while E2E p95 increased moderately. This supported testing higher concurrency later, but did not prove an optimal serving point.

## Day 5 length sweep

The length sweep kept concurrency fixed at 4 and tested three configured input/output sizes.

| Case | Configured input | Configured output | Failed | TTFT p95 | E2E p95 | Output tok/s |
|---|---:|---:|---:|---:|---:|---:|
| small | 128 | 64 | 0 | 84.80 ms | 215.67 ms | 1566.47 |
| medium | 512 | 128 | 0 | 31.50 ms | 296.37 ms | 1742.67 |
| large | 2048 | 256 | 0 | 92.10 ms | 664.01 ms | 1568.42 |

Finding: larger workloads increased E2E latency. TTFT did not increase monotonically in this single run. Medium had the best TTFT p95 and output throughput, but small had the best E2E p95. This does not support claiming a globally best length configuration.

## Day 6 wide concurrency sweep

The wider concurrency sweep tested max concurrency 1, 2, 4, 8, and 16 with 128 configured input tokens, 64 output tokens, and 64 requests per run.

| Concurrency | Failed | TTFT p95 | E2E p95 | Request throughput | Output tok/s |
|---:|---:|---:|---:|---:|---:|
| 1 | 0 | 8.61 ms | 134.51 ms | 7.45 req/s | 477.07 |
| 2 | 0 | 10.25 ms | 140.30 ms | 14.37 req/s | 919.96 |
| 4 | 0 | 12.42 ms | 143.84 ms | 28.02 req/s | 1792.99 |
| 8 | 0 | 16.12 ms | 148.93 ms | 54.92 req/s | 3515.04 |
| 16 | 0 | 22.08 ms | 160.22 ms | 100.45 req/s | 6428.86 |

Finding: throughput increased strongly with concurrency. Latency also increased. Concurrency 16 was the highest-throughput tested point, but it is not a production capacity claim.

## Supported conclusions

- The local vLLM serving and benchmark loop works.
- Higher concurrency improved throughput in the tested range.
- Higher concurrency also increased TTFT p95 and E2E p95.
- Longer prompt/output workloads increased E2E latency.
- All Week 1 benchmark runs reported here completed with 0 failed requests.

## Unsupported conclusions

- These results do not prove production readiness.
- These results do not prove concurrency 16 is production-safe capacity.
- These results do not prove a globally optimal concurrency or length configuration.
- These results do not cover bursty traffic, mixed workloads, real prompt datasets, or repeated-run variance.

## Next benchmark work

- Test concurrency 32, 64, and 128.
- Define an explicit latency SLO before choosing a serving target.
- Add repeated runs and variance/error bars.
- Track GPU utilization.
- Benchmark RAG-under-load, including retrieval plus generation.
