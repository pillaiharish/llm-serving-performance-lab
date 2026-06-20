# Day 6 Wide Concurrency Sweep Report

## Summary

Date: 2026-06-20
Model: models/Qwen/Qwen2.5-0.5B-Instruct
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB
Server: vLLM OpenAI-compatible API
Benchmark type: synthetic random prompt benchmark
Input length: 128
Output length: 64
Prompts per run: 64

## Goal

Measure how increasing max concurrency affects latency and throughput for the same request shape.

Day 6 keeps input length, output length, model, server settings, and prompt count fixed. Only max concurrency changes.

The tested concurrency values are:

```text
1, 2, 4, 8, 16
```

## Experiment cases

| Case | Configured input length | Configured output length | Prompts | Max concurrency |
| ---- | ----------------------: | -----------------------: | ------: | --------------: |
| c1   |                     128 |                       64 |      64 |               1 |
| c2   |                     128 |                       64 |      64 |               2 |
| c4   |                     128 |                       64 |      64 |               4 |
| c8   |                     128 |                       64 |      64 |               8 |
| c16  |                     128 |                       64 |      64 |              16 |

## Smoke test

The pre-benchmark smoke test completed before the Day 6 benchmark run.

```text
results/day06/logs/pre_benchmark_smoke.txt
```

## Result files

Raw benchmark JSON files:

```text
results/day06/raw/day06_c1_i128_o64_n64.json
results/day06/raw/day06_c2_i128_o64_n64.json
results/day06/raw/day06_c4_i128_o64_n64.json
results/day06/raw/day06_c8_i128_o64_n64.json
results/day06/raw/day06_c16_i128_o64_n64.json
```

Benchmark logs:

```text
results/day06/logs/bench_c1_i128_o64_n64.txt
results/day06/logs/bench_c2_i128_o64_n64.txt
results/day06/logs/bench_c4_i128_o64_n64.txt
results/day06/logs/bench_c8_i128_o64_n64.txt
results/day06/logs/bench_c16_i128_o64_n64.txt
```

Parsed summary:

```text
results/day06/day06_summary.csv
```

Plots:

```text
plots/day06/day06_concurrency_vs_ttft_p95.png
plots/day06/day06_concurrency_vs_e2e_p95.png
plots/day06/day06_concurrency_vs_output_tps.png
plots/day06/day06_concurrency_vs_request_tps.png
```

## Results summary

| Concurrency | Completed | Failed | TTFT p50 | TTFT p95 | TPOT p50 | ITL p50 |   E2E p95 | Request throughput | Output tok/s | Notes                                         |
| ----------: | --------: | -----: | -------: | -------: | -------: | ------: | --------: | -----------------: | -----------: | --------------------------------------------- |
|           1 |        64 |      0 |  8.09 ms |  8.61 ms |  2.00 ms | 2.00 ms | 134.51 ms |         7.45 req/s |       477.07 | Lowest latency, lowest throughput             |
|           2 |        64 |      0 |  8.15 ms | 10.25 ms |  2.07 ms | 2.07 ms | 140.30 ms |        14.37 req/s |       919.96 | Throughput roughly doubled from c1            |
|           4 |        64 |      0 | 10.36 ms | 12.42 ms |  2.09 ms | 2.09 ms | 143.84 ms |        28.02 req/s |      1792.99 | Better throughput with small latency increase |
|           8 |        64 |      0 | 12.05 ms | 16.12 ms |  2.10 ms | 2.11 ms | 148.93 ms |        54.92 req/s |      3515.04 | Throughput continued scaling                  |
|          16 |        64 |      0 | 20.68 ms | 22.08 ms |  2.19 ms | 2.19 ms | 160.22 ms |       100.45 req/s |      6428.86 | Highest tested throughput, still 0 failures   |

## Interpretation

The Day 6 sweep shows the expected throughput-latency tradeoff.

As concurrency increased from 1 to 16, request throughput increased from 7.45 req/s to 100.45 req/s. Output token throughput increased from 477.07 tok/s to 6428.86 tok/s.

Latency also increased. TTFT p95 increased from 8.61 ms at concurrency 1 to 22.08 ms at concurrency 16. E2E p95 increased from 134.51 ms to 160.22 ms.

This means higher concurrency improved GPU utilization and total throughput, but each request paid some additional latency.

Concurrency 16 had the highest throughput among the tested values and still had 0 failed requests in this synthetic benchmark. However, this does not prove that concurrency 16 is production capacity or globally optimal. It only means concurrency 16 was the best tested throughput point for this specific model, GPU, request shape, and single-run benchmark.

TPOT and ITL stayed fairly stable from concurrency 1 to 8, then increased slightly at concurrency 16. This suggests decode cost per token remained mostly stable until higher concurrency added more scheduling or batching pressure.

## Supported conclusion

For this single synthetic benchmark with 128 input tokens, 64 output tokens, and 64 prompts per run, increasing concurrency from 1 to 16 increased throughput significantly while causing a moderate increase in TTFT p95 and E2E p95.

Concurrency 16 was the highest-throughput tested point and did not produce failed requests.

## Unsupported conclusions

This experiment does not prove:

* concurrency 16 is production-safe capacity
* concurrency 16 is globally optimal
* the GPU can safely serve arbitrary real-world traffic at concurrency 16
* the same behavior will hold for longer prompts
* the same behavior will hold for longer outputs
* the same behavior will hold under bursty traffic or mixed workloads

## What worked

* The sweep ran successfully for concurrency 1, 2, 4, 8, and 16.
* All runs completed with 0 failed requests.
* Raw JSON and text logs were saved.
* Throughput scaled strongly as concurrency increased.
* Latency increased, but did not explode in this tested range.
* The result gives a better picture than the earlier 1, 2, 4-only benchmark.

## What failed / what is weak

* This is still one run per concurrency value.
* The request shape is small and fixed at 128 input tokens and 64 output tokens.
* There is no concurrency 32, 64, or 128 test yet.
* There is no explicit latency SLO yet.
* There is no GPU utilization log attached.
* Synthetic random prompts are not the same as real application traffic.
* The benchmark does not identify the breaking point yet.

## What I learned

* Higher concurrency can improve GPU utilization and output token throughput.
* Throughput alone is not enough; TTFT and E2E p95 must be checked together.
* In this run, concurrency 16 gave the highest throughput while keeping failures at 0.
* A safe serving number should not be chosen from the highest successful test alone.
* To find a real capacity target, I need to test higher concurrency until latency degrades sharply, failures appear, or throughput plateaus.
* After finding the first unstable point, a safer advertised serving target should be lower than the breaking point, such as 30–50% below it depending on the latency SLO.

## Next actions

Day 7 will consolidate Week 1 results into a weekly report.

Future benchmark work should include:

* concurrency 32, 64, and 128
* repeated runs
* p50, p90, p95, and p99 comparison
* GPU utilization tracking
* mixed input/output lengths
* real prompt datasets
* clear latency SLO definition
