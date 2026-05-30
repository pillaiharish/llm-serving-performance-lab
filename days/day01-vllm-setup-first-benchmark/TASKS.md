# Day 1 — vLLM Setup + First LLM Benchmark (Both Models)

**Date:** Wed Mar 19, 2026
**Phase:** 1 — LLM Inference Baseline ("I can measure it")
**Time:** 2.5h build + 0.5h tweet = 3h total

---

## TARGET

Establish your unoptimized baseline numbers for both Qwen2.5-7B and Qwen3.5-4B. Every future optimization will be compared against these numbers.

## SKILLS TARGETED

- vLLM server deployment
- TTFT (Time to First Token) measurement
- ITL (Inter-Token Latency) measurement
- Streaming response parsing
- Benchmarking methodology

---

## WHAT TO BUILD

1. Deploy Qwen2.5-7B-Instruct on vLLM on your 5070 Ti
2. Deploy Qwen3.5-4B on vLLM (swap after first benchmark — they can't run simultaneously today)
3. Write a benchmark script that hits the vLLM OpenAI-compatible endpoint
4. Measure for BOTH models: TTFT, ITL, tokens/sec, end-to-end latency
5. Run with 3 prompt lengths: short (50 tokens), medium (200 tokens), long (500 tokens)
6. Save results to JSON — one file per model

---

## COMMANDS

```bash
# FIRST: Benchmark Qwen2.5-7B
python -m vllm.entrypoints.openai.api_server \
  --model Qwen/Qwen2.5-7B-Instruct \
  --max-model-len 4096 \
  --gpu-memory-utilization 0.85 \
  --port 8000
# Run benchmark script → save to results/qwen25-7b-baseline.json
# Stop server (Ctrl+C)

# THEN: Benchmark Qwen3.5-4B
python -m vllm.entrypoints.openai.api_server \
  --model Qwen/Qwen3.5-4B \
  --max-model-len 4096 \
  --gpu-memory-utilization 0.85 \
  --port 8000
# Run benchmark script → save to results/qwen35-4b-baseline.json
```

## DAY 1 SAFE SERVING PROFILE

If `vllm` fails to start on RTX 5070 Ti 16 GB because of startup memory pressure, use this lower-risk profile first and benchmark from there:

```bash
export VLLM_USE_FLASHINFER_SAMPLER=0
export VLLM_ATTENTION_BACKEND=FLASH_ATTN
export PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True

vllm serve /data/llm-inference-prod/models/Qwen/Qwen3.5-4B \
  --served-model-name Qwen/Qwen3.5-4B \
  --host 0.0.0.0 \
  --port 9001 \
  --api-key local-dev \
  --generation-config vllm \
  --max-model-len 2048 \
  --gpu-memory-utilization 0.80 \
  --max-num-seqs 1 \
  --max-num-batched-tokens 2048 \
  --enforce-eager
```

Use the same pattern for `Qwen2.5-7B-Instruct` if needed.

## TROUBLESHOOTING

### Why the stable command works

The stable command works mainly because it is a low-memory Day 1 configuration:

- `--served-model-name` keeps the OpenAI-compatible API model id predictable
- `--gpu-memory-utilization 0.80` gives `vllm` a workable planning budget
- `--max-num-seqs 1` reduces concurrency pressure
- `--max-num-batched-tokens 2048` reduces per-step token pressure
- `--enforce-eager` avoids CUDA graph capture overhead during startup

### Why a looser command can fail

A command with lower planned memory, default scheduler limits, and default CUDA graph behavior can still fail with OOM even if the card looks mostly idle. On this machine, the failure pattern was:

```text
model loads most of the way, then startup fails on an additional ~1 GiB CUDA allocation
```

### If `curl -s` returns nothing

Do not assume the request succeeded.

Use:

```bash
curl -v http://127.0.0.1:9001/v1/models \
  -H "Authorization: Bearer local-dev"
```

Then verify the socket exists:

```bash
ss -ltnp | rg ':9001'
```

If the socket is missing, the API server is not actually listening yet even if `nvidia-smi` shows VRAM in use.

### If the GPU is already partly busy before `vllm` starts

Check for:

- `llama.cpp` services under user `systemd`
- `model-orchestrator.service`
- Docker containers such as ComfyUI or Ollama

Freeing those background consumers may be required before Day 1 benchmarking is reproducible.

---

## CODE PATTERN — Benchmark Script

```python
import time, requests, json

def benchmark_streaming(prompt, model="Qwen/Qwen2.5-7B-Instruct"):
    start = time.perf_counter()
    first_token_time = None
    token_times = []
    prev_time = start

    response = requests.post(
        "http://localhost:8000/v1/chat/completions",
        json={"model": model, "messages": [{"role": "user", "content": prompt}],
              "stream": True, "max_tokens": 256},
        stream=True
    )

    for line in response.iter_lines():
        if line and line.startswith(b"data: "):
            data = line.decode("utf-8")[6:]
            if data.strip() == "[DONE]":
                break
            chunk = json.loads(data)
            if chunk["choices"][0]["delta"].get("content"):
                now = time.perf_counter()
                if first_token_time is None:
                    first_token_time = now - start  # TTFT
                else:
                    token_times.append(now - prev_time)  # ITL
                prev_time = now

    end = time.perf_counter()
    total_tokens = len(token_times) + 1
    return {
        "ttft_ms": first_token_time * 1000 if first_token_time else 0,
        "avg_itl_ms": (sum(token_times) / len(token_times)) * 1000 if token_times else 0,
        "p95_itl_ms": sorted(token_times)[int(len(token_times) * 0.95)] * 1000 if token_times else 0,
        "total_tokens": total_tokens,
        "tokens_per_sec": total_tokens / (end - start),
        "total_time_ms": (end - start) * 1000
    }
```

---

## DELIVERABLES

- [ ] `benchmark.py` — reusable benchmark script
- [ ] `results/qwen25-7b-baseline.json` — Qwen2.5-7B results
- [ ] `results/qwen35-4b-baseline.json` — Qwen3.5-4B results
- [ ] Screenshot of comparison table
- [ ] Tweet posted

---

## TWEET TEMPLATE

```
Day 1/30: LLM Inference Optimization Challenge

First head-to-head — RTX 5070 Ti via vLLM:

Qwen2.5-7B (older, bigger):
  TTFT: __ms | __ tok/s | VRAM: __GB

Qwen3.5-4B (newer, smaller):
  TTFT: __ms | __ tok/s | VRAM: __GB

Can a 4B model from a newer generation beat a 7B?

#BuildInPublic #LLMOps #AI
[attach: comparison table screenshot]
```

---

## WHAT YOU LEARN

- How vLLM serves LLMs (OpenAI-compatible API, model loading, GPU memory allocation)
- What TTFT and ITL actually mean in practice
- How model size vs architecture generation affects performance
- Streaming response parsing for accurate token-level timing
