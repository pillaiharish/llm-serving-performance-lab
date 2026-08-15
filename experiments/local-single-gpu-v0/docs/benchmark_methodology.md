# Benchmark Methodology

## Goal

This document explains how benchmarks are run in the LLM Serving Performance Lab and what evidence is saved.

The goal is not to publish one-off numbers. The goal is to create a reproducible workflow for serving, measuring, parsing, plotting, and interpreting LLM inference behavior.

## Current benchmark stack

- Server: vLLM OpenAI-compatible API
- Model: models/Qwen/Qwen2.5-0.5B-Instruct
- GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB
- Client benchmark tool: `vllm bench serve`
- Dataset: random
- Metrics: TTFT, TPOT, ITL, E2E latency, throughput
- Raw output: JSON and text logs
- Derived output: CSV summaries and PNG plots

## Benchmark lifecycle

```text
1. Start vLLM server
2. Run smoke test
3. Run benchmark
4. Save raw JSON and text logs
5. Parse JSON into CSV
6. Generate plots
7. Write interpretation report
```

## Server startup

The server is started with the shared config file:

```bash
source .venv/bin/activate
source configs/vllm_default.env

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

## Smoke test

Before benchmarking, the OpenAI-compatible endpoint is checked with:

```bash
python scripts/smoke_chat.py
```

The smoke test verifies:

- the server is reachable
- `/v1/models` responds
- chat completion works
- the expected local model is available

A smoke test is not a benchmark. It only proves API liveness.

## Benchmark command pattern

Day 2 used this pattern:

```bash
vllm bench serve \
  --backend openai-chat \
  --base-url http://127.0.0.1:8000 \
  --endpoint /v1/chat/completions \
  --model "$MODEL_NAME" \
  --dataset-name random \
  --random-input-len 128 \
  --random-output-len 64 \
  --num-prompts 16 \
  --request-rate inf \
  --max-concurrency "$c" \
  --temperature 0 \
  --percentile-metrics ttft,tpot,itl,e2el \
  --metric-percentiles 50,90,95,99 \
  --save-result \
  --save-detailed \
  --result-dir results/day02/raw
```

## Why raw JSON is saved

Raw JSON is saved so benchmark results can be re-parsed later without rerunning the benchmark.

This matters because:

- plots may need to be regenerated
- summary fields may change
- new metrics may be extracted later
- reports should be traceable back to raw evidence

## Why logs are saved

Text logs preserve the exact benchmark configuration and terminal output.

They are useful for checking:

- request count
- failed requests
- throughput
- percentile metrics
- warnings
- benchmark duration
- command arguments

## Why CSV is generated

CSV is easier to inspect, diff, plot, and compare across runs.

The current parser converts Day 2 raw JSON into:

```text
results/day03/day02_summary.csv
```

## Why plots are generated

Plots make tradeoffs easier to see.

Current Day 3 plots:

```text
plots/day03/day02_concurrency_vs_e2e_p95.png
plots/day03/day02_concurrency_vs_output_tps.png
```

## Current benchmark limitations

The current benchmark is intentionally small.

Limitations:

- only one model
- only one GPU
- only one input/output length
- only 16 requests per run
- only concurrency 1, 2, and 4
- no repeated runs yet
- no error bars yet
- no long-context workload yet
- no production traffic shape yet
- no cost analysis yet

## Claims allowed from current data

Allowed:

```text
For this small benchmark, max concurrency 4 gave the best throughput-latency tradeoff among tested points.
```

Allowed:

```text
Output throughput increased from about 438 tok/s at concurrency 1 to about 1526 tok/s at concurrency 4.
```

Allowed:

```text
E2E p95 increased from about 149 ms to about 170 ms between concurrency 1 and 4.
```

## Claims not allowed yet

Not allowed:

```text
Concurrency 4 is globally optimal.
```

Not allowed:

```text
This setup is production-ready.
```

Not allowed:

```text
This model can handle all real workloads.
```

Not allowed:

```text
The server is not saturated at higher concurrency.
```

To make those claims, the lab needs larger sweeps, repeated runs, different prompt/output lengths, and higher concurrency levels.