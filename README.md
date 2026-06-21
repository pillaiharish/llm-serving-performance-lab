# LLM Serving Performance Lab

## What this repo is

This repo is a hands-on lab for LLM inference serving, benchmarking, and performance interpretation.

It is intentionally not a chatbot demo. The goal is to show how an LLM server can be started, smoke tested, benchmarked, measured, and explained with raw evidence.

The current stack uses:

- vLLM OpenAI-compatible serving
- `models/Qwen/Qwen2.5-0.5B-Instruct`
- NVIDIA GeForce RTX 5070 Ti, 16 GB
- synthetic random benchmark workloads
- raw JSON, logs, CSV summaries, plots, reports, and methodology docs

## Current status

Week 1 complete: Days 1-7

Week 1 established the serving and benchmark loop, then used it to measure concurrency and prompt/output length effects. The results are useful as local lab evidence, but they are not production capacity claims.

## Why this exists

Most GenAI prototypes prove that a model can respond. That is not enough for serving work.

A serving lab should also answer:

- How long does the first token take?
- How does end-to-end latency change with concurrency?
- How does output throughput scale?
- What happens when prompts and outputs get longer?
- What evidence supports the result?
- What claims are not supported yet?

This repo is built around one engineering question:

> Can this model/configuration be served reliably, measurably, and eventually economically on real GPU infrastructure?

## How to read this repo

Start with the weekly report and benchmark findings:

- `reports/day07_week1_report.md`
- `docs/week01_benchmark_findings.md`

Then read the day-wise reports in order:

1. `reports/day01_baseline.md`
2. `reports/day02_benchmark.md`
3. `reports/day03_plots.md`
4. `reports/day04_methodology.md`
5. `reports/day05_length_sweep.md`
6. `reports/day06_concurrency_sweep.md`

Use `docs/benchmark_methodology.md` and `docs/results_interpretation.md` to understand how the benchmark numbers should and should not be interpreted.

## Week 1 learning path

| Day | Focus | What I learned | Evidence |
|---:|---|---|---|
| 1 | vLLM baseline and smoke test | Serving must be validated before benchmarking | `reports/day01_env_check.txt`, `reports/day01_smoke_output.txt` |
| 2 | First benchmark loop | Concurrency changes throughput and latency | `results/day02/raw/` |
| 3 | Parse results and plots | Raw JSON needs CSV summaries and plots to become useful | `results/day03/day02_summary.csv`, `plots/day03/` |
| 4 | Methodology docs | Benchmark numbers need supported and unsupported claims | `docs/benchmark_methodology.md`, `docs/results_interpretation.md` |
| 5 | Prompt/output length sweep | Larger workloads increased E2E latency; TTFT was not monotonic in one run | `reports/day05_length_sweep.md`, `results/day05/raw/` |
| 6 | Wide concurrency sweep | Throughput scaled to concurrency 16 with latency tradeoff | `reports/day06_concurrency_sweep.md`, `results/day06/day06_summary.csv` |
| 7 | Week 1 report | Consolidated the benchmark story, findings, and caveats | `reports/day07_week1_report.md`, `docs/week01_benchmark_findings.md` |

## Week 1 key results

Week 1 measured two main serving dimensions:

- concurrency at fixed request shape
- prompt/output length at fixed concurrency

The common pattern was a throughput-latency tradeoff. Higher concurrency improved throughput in the tested range, but TTFT p95 and E2E p95 also increased. Larger prompt/output workloads increased E2E latency.

## Day 2/3 concurrency baseline

The first benchmark used 128 configured input tokens, 64 output tokens, and 16 requests per run.

| Concurrency | Completed | Failed | E2E p95 | Output tok/s |
|---:|---:|---:|---:|---:|
| 1 | 16 | 0 | 148.70 ms | 438.30 |
| 2 | 16 | 0 | 164.94 ms | 782.64 |
| 4 | 16 | 0 | 170.16 ms | 1526.10 |

This showed output throughput increasing from concurrency 1 to 4, while E2E p95 also increased.

## Day 5 prompt/output length sweep

Day 5 kept concurrency fixed at 4 and tested three configured prompt/output lengths.

| Case | Configured input | Configured output | TTFT p95 | E2E p95 | Output tok/s |
|---|---:|---:|---:|---:|---:|
| small | 128 | 64 | 84.80 ms | 215.67 ms | 1566.47 |
| medium | 512 | 128 | 31.50 ms | 296.37 ms | 1742.67 |
| large | 2048 | 256 | 92.10 ms | 664.01 ms | 1568.42 |

Larger workloads increased E2E latency.

TTFT did not increase monotonically in this single run, so the result should not be used to claim a globally best prompt/output length.

## Day 6 wide concurrency sweep

Day 6 kept the request shape fixed at 128 configured input tokens, 64 output tokens, and 64 requests per run.

| Concurrency | Completed | Failed | TTFT p95 | E2E p95 | Req/s | Output tok/s |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 64 | 0 | 8.61 ms | 134.51 ms | 7.45 | 477.07 |
| 2 | 64 | 0 | 10.25 ms | 140.30 ms | 14.37 | 919.96 |
| 4 | 64 | 0 | 12.42 ms | 143.84 ms | 28.02 | 1792.99 |
| 8 | 64 | 0 | 16.12 ms | 148.93 ms | 54.92 | 3515.04 |
| 16 | 64 | 0 | 22.08 ms | 160.22 ms | 100.45 | 6428.86 |

Throughput increased strongly with concurrency. Latency also increased.

Concurrency 16 was the highest-throughput tested point, but this is not a production capacity claim.

## What Week 1 suggests

- The local vLLM serving and benchmark loop works.
- Higher concurrency improved throughput in the tested range.
- Higher concurrency also increased TTFT p95 and E2E p95.
- Larger prompt/output workloads increased E2E latency.
- Benchmark interpretation needs raw evidence, summaries, plots, and caveats.

## What Week 1 does not prove

- It does not prove production readiness.
- It does not prove concurrency 16 is production-safe capacity.
- It does not identify the breaking point.
- It does not include repeated-run variance.
- It does not include GPU utilization tracking yet.
- It does not test real traffic or RAG workloads yet.

## How to reproduce the benchmark loop

Set up the environment:

```bash
source .venv/bin/activate
source configs/vllm_default.env
```

Start the vLLM server:

```bash
vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

In another terminal, run the smoke test:

```bash
source .venv/bin/activate
source configs/vllm_default.env

python scripts/smoke_chat.py
```

Run benchmark scripts:

```bash
./scripts/run_day02_bench.sh
./scripts/run_day05_length_sweep.sh
./scripts/run_day06_concurrency_sweep.sh
```

Parse and plot saved benchmark results:

```bash
python scripts/parse_day02_results.py
python scripts/plot_day02_results.py

python scripts/parse_day06_results.py
python scripts/plot_day06_results.py
```

The benchmark workflow is:

```text
vLLM benchmark JSON -> CSV summary -> plots -> report
```

## Repo map

| Path | Purpose |
|---|---|
| `configs/` | vLLM runtime configuration |
| `scripts/` | smoke tests, benchmark scripts, parsers, and plotters |
| `results/day*/raw/` | raw vLLM benchmark JSON |
| `results/day*/logs/` | benchmark and smoke-test logs |
| `plots/day*/` | generated benchmark plots |
| `reports/` | day-wise and weekly reports |
| `docs/` | methodology, metric glossary, and benchmark findings |

## Key docs and reports

- `reports/day07_week1_report.md`
- `docs/week01_benchmark_findings.md`
- `docs/benchmark_methodology.md`
- `docs/results_interpretation.md`
- `docs/day01/metrics_glossary.md`

## Next work

- concurrency 32, 64, and 128 breaking-point tests
- repeated runs and variance
- GPU utilization tracking
- explicit latency SLO
- RAG-under-load benchmark skeleton

## License

Apache-2.0
