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

Day 1 establishes the first serving baseline:

```text
1. create Python environment
2. verify GPU / CUDA / PyTorch / vLLM environment
3. attempt local vLLM server startup
4. document startup failure
5. apply FlashInfer sampler workaround
6. run OpenAI-compatible smoke test
7. save environment and smoke-test evidence
```

Start here:

```bash
source .venv/bin/activate
source configs/vllm_default.env

python scripts/check_env.py | tee reports/day01_env_check.txt

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

In another terminal:

```bash
source .venv/bin/activate
source configs/vllm_default.env

python scripts/smoke_chat.py | tee reports/day01_smoke_output.txt
```

---

## Day 1 result

The first server attempt failed during vLLM engine initialization after selecting the FlashInfer sampler path. After exporting VLLM_USE_FLASHINFER_SAMPLER=0 and reducing max model length to 4096, the OpenAI-compatible smoke test succeeded.

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