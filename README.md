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
models/Qwen/Qwen2.5-0.5B-Instruct
```

Reason: Day 1 is about validating the serving and measurement loop. Larger 7B/8B models come after the harness is stable.



---

## License

Apache-2.0