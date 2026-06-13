# Day 3 Plot Report

## Summary

Date: 2026-06-14  
Source data: Day 2 vLLM benchmark JSON  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  

## Goal

Parse Day 2 benchmark JSON into CSV and generate the first latency/throughput plots.

## Input files

```text
results/day02/raw/day02_c1_i128_o64.json
results/day02/raw/day02_c2_i128_o64.json
results/day02/raw/day02_c4_i128_o64.json
```

---

## Output files

```text
results/day03/day02_summary.csv
plots/day03/day02_concurrency_vs_e2e_p95.png
plots/day03/day02_concurrency_vs_output_tps.png
```

---

## Result Summary

| Concurrency | E2E p95 | Output tok/s | Notes    |
| ----------: | ------: | -----------: | -------- |
|           1 |    148.7 |         438.3 | baseline |
|           2 |    164.9 |         782.6 | Almost double Output tokens     |
|           4 |    170.1 |         1526.0 | Minor change in latency but doubled Output tokens     |

---

## Next actions

- Add more concurrency levels.
- Add repeated runs.
- Add prompt/output length experiments.