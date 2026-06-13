# LLM Serving Performance Lab

Production-style lab for **LLM inference, serving, benchmarking, observability, and GPU/datacenter AI engineering**.

This repo is intentionally not a chatbot demo. It is a hands-on engineering portfolio for proving that an LLM service can be deployed, measured, debugged, optimized, and explained.

---

## Why this exists

Most GenAI prototypes look good in a demo and fail in production because nobody measured the real serving behavior:

- time to first token, inter-token latency, and end-to-end latency
- GPU memory pressure and KV cache behavior
- throughput versus goodput under an SLO
- streaming versus non-streaming responses
- prompt length and output length impact
- observability, dashboards, and incident handling
- RAG/agent quality under concurrent load
- cost per 1M tokens and capacity planning

This lab is built to answer one question:

> Can this model/configuration be served reliably, measurably, and economically on real GPU infrastructure?

---

## Target role signal

Core skills demonstrated here:

```text
vLLM
SGLang
TensorRT-LLM
OpenAI-compatible serving
Kubernetes GPU deployment
Prometheus / Grafana observability
TTFT / ITL / TPOT / E2E latency
throughput and goodput benchmarking
KV cache and prefix caching experiments
RAG under load
agent reliability evaluation
Triton kernel learning
capacity planning
```

---

## Day 1 deliverable

Day 1 creates the first runnable serving + benchmark loop:

```text
1. create Python environment
2. install PyTorch CUDA 12.8+ stack for Blackwell
3. install vLLM and benchmark dependencies
4. start an OpenAI-compatible vLLM server
5. run a smoke chat request
6. run a tiny concurrency sweep
7. save JSON/CSV results
8. plot first latency/throughput charts
9. document failures and environment details
```

Start here:

```bash
cp configs/vllm_default.env .env
make setup
make check-env
make serve
```

In another terminal:

```bash
source .venv/bin/activate
make smoke
make bench-day1
make extract-results
make plot-results
```

If `make serve` does not work because your GPU/CUDA/vLLM stack is not ready yet, do not hide it. Capture the failure in `reports/day01_baseline_template.md`. A documented failure with clear next steps is still an engineering artifact.


---

## Day 1 default model

The default model is intentionally small:

```text
Qwen/Qwen2.5-1.5B-Instruct
```

Reason: Day 1 is about validating the serving and measurement loop. Larger 7B/8B models come after the harness is stable.

---

## Benchmark philosophy

Do not report only tokens/sec.

A useful benchmark includes:

```text
hardware
GPU driver/CUDA/PyTorch/vLLM versions
model name and revision when possible
server configuration
input length
output length
concurrency
request rate
TTFT p50/p95/p99
ITL or TPOT p50/p95/p99
end-to-end latency p50/p95/p99
output tokens/sec
requests/sec
error rate
GPU memory
notes/failures
```

Raw throughput is not enough. The production question is:

> How much good traffic can the system serve while staying inside latency and reliability limits?

---

## Public writing plan

This repo feeds three public surfaces without LinkedIn:

- GitHub: code, benchmark data, reports, dashboards
- Medium: engineering writeups with commands, charts, failures, lessons
- X/Twitter: short technical progress logs and benchmark findings
- harishpillai.com: curated portfolio page linking only the strongest artifacts

---

## License

Apache-2.0