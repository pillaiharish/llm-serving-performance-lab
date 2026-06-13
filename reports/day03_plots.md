# Day 3 Plot Report

## Summary

Date: 2026-06-14  
Source data: Day 2 vLLM benchmark JSON  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  

## Goal

Parse Day 2 benchmark JSON into CSV and generate the first latency/throughput plots.

Day 3 converts raw benchmark data into a human-readable summary and visual evidence.

## Input files

```text
results/day02/raw/day02_c1_i128_o64.json
results/day02/raw/day02_c2_i128_o64.json
results/day02/raw/day02_c4_i128_o64.json
```

## Output files

```text
results/day03/day02_summary.csv
plots/day03/day02_concurrency_vs_e2e_p95.png
plots/day03/day02_concurrency_vs_output_tps.png
```

## Reproduction commands

```bash
python scripts/parse_day02_results.py
python scripts/plot_day02_results.py
```

## Result Summary

| Max concurrency | Completed | Failed |   E2E p95 | Output tok/s | Notes                                        |
| --------------: | --------: | -----: | --------: | -----------: | -------------------------------------------- |
|               1 |        16 |      0 | 148.70 ms |       438.30 | baseline                                     |
|               2 |        16 |      0 | 164.94 ms |       782.64 | higher throughput, moderate latency increase |
|               4 |        16 |      0 | 170.16 ms |      1526.10 | best tested throughput-latency tradeoff      |

## Chart 1: concurrency vs E2E p95

The E2E p95 latency increased as max concurrency increased:

```text
c1 -> c2: 148.70 ms to 164.94 ms (+16.24 ms)
c2 -> c4: 164.94 ms to 170.16 ms (+5.22 ms)
c1 -> c4: 148.70 ms to 170.16 ms (+21.45 ms)
```

Most of the E2E p95 increase happened when moving from concurrency 1 to 2. The increase from 2 to 4 was smaller in this tiny benchmark.

## Chart 2: concurrency vs output throughput

Output token throughput increased strongly as concurrency increased:

```text
c1: 438.30 tok/s
c2: 782.64 tok/s
c4: 1526.10 tok/s
```

From concurrency 1 to 4, output throughput improved by about 3.5x.

Total generated tokens stayed constant at 1024 per run, so the improvement came from completing the same amount of output work in less time.

## Interpretation

For this small benchmark, max concurrency 4 gave the best throughput-latency tradeoff among the tested values.

Compared with concurrency 1:

- output throughput increased from 438.30 tok/s to 1526.10 tok/s
- E2E p95 increased from 148.70 ms to 170.16 ms
- failed requests stayed at 0

This suggests the server was not saturated at max concurrency 4 for this specific model and workload.

However, this does not prove that concurrency 4 is globally optimal. The benchmark only tested 16 requests per run, one model, one GPU, and one prompt/output length. Higher concurrency levels such as 8 or 16 may show sharper latency growth.

## Caveats

This is a small Day 3 visualization, not a production capacity study.

Limitations:

- only 3 concurrency points: 1, 2, 4
- only 16 requests per run
- no repeated runs
- no variance/error bars
- one model only
- one input/output length only
- no GPU utilization plot yet
- no concurrency 8 or 16 yet

## What worked

- Raw Day 2 JSON was parsed into CSV.
- The summary CSV was committed.
- Two PNG charts were generated.
- The plots make the throughput-latency tradeoff easier to see.

## What failed / what is weak

- The first chart set has no error bars because there are no repeated runs.
- The test is too small to claim production capacity.
- The phrase “optimal concurrency” is premature.
- Higher concurrency levels are still untested.

## What I learned

- Increasing concurrency can improve throughput while also increasing latency.
- E2E p95 increased from concurrency 1 to 4, but the increase was modest for this small workload.
- Output token throughput improved by about 3.5x from concurrency 1 to 4.
- The best tested point is not necessarily the true optimal point.
- To find saturation, I need to test higher concurrency values such as 8 and 16.


## Next actions

- Clean benchmark methodology docs on Day 4.
- Add repeated runs later to reduce noise.
- Extend concurrency sweep to 8 and 16 on Day 6.
- Add prompt/output length experiments on Day 5.