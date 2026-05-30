# Day 1 — vLLM Setup + First LLM Benchmark (Both Models)

**Phase:** 1 — LLM Inference Baseline

## What goes in this folder
- `benchmark.py` — benchmark script
- `results/qwen25-7b-baseline.json`
- `results/qwen35-4b-baseline.json`

## Day 1 in order

### 1. Environment setup succeeded

The Python and `vllm` environment is installed on this machine.

Recorded environment logs:

- `/data/llm-inference-prod/results/day-one/logs/install-check.txt`
- `/data/llm-inference-prod/results/day-one/logs/env-check.txt`

### 2. Local model files were validated

The `Qwen3.5-4B` local folder is valid. The problem was not missing files.

Observed local model folder:

```text
models/Qwen/Qwen3.5-4B
model.safetensors-00001-of-00002.safetensors
model.safetensors-00002-of-00002.safetensors
config.json
tokenizer.json
tokenizer_config.json
```

Base-model paths found on this machine:

```text
Qwen2.5-7B-Instruct
/data/models/Qwen/Qwen2.5-7B-Instruct
/data/ai/models/huggingface/Qwen2.5-7B-Instruct

Qwen3.5-4B
/data/models/Qwen/Qwen3.5-4B
/data/ai/models/huggingface/Qwen3.5-4B
```

### 3. First startup attempts failed because of VRAM pressure

The failure mode was:

```text
CUDA out of memory.
Tried to allocate 1.03 GiB.
GPU total: 15.46 GiB
Free: ~100 MiB
```

That means `vllm` could see and load the model, but it ran out of VRAM during initialization, profiling, KV-cache planning, or runtime buffer setup.

Important mental model:

```text
VRAM needed = model weights
            + KV cache
            + CUDA graphs / eager buffers
            + attention and sampler temporary memory
            + profiling warmup memory
            + fragmentation and headroom
```

So:

```text
model folder valid
startup failure due to VRAM pressure
```

### 4. Background GPU consumers made the margin worse

This machine had hidden GPU consumers from:

- user `systemd` `llama.cpp` services
- a system `model-orchestrator.service`
- a Docker `comfyui-ltx2` container with `restart: unless-stopped`

Those needed to be stopped or disabled before reliable `vllm` testing.

### 5. The stable Day 1 command is a low-memory profile

The working local `Qwen3.5-4B` command succeeds mainly because of the memory-control flags, not because the absolute path itself is magical.

Use this as the Day 1 stable profile for RTX 5070 Ti 16 GB:

```bash
source .venv/bin/activate

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

Treat this as:

```text
Day 1 stable local serving profile for RTX 5070 Ti 16 GB.
```

### 6. Why the stable command worked

The stable command differs from the failing one in a few important ways:

| Area | Stable setup | Less stable setup | Why it matters |
| --- | --- | --- | --- |
| Model path | `/data/llm-inference-prod/models/Qwen/Qwen3.5-4B` | `models/Qwen/Qwen3.5-4B` | Absolute path is more reproducible. Relative path only works if the current working directory is exactly `/data/llm-inference-prod`. |
| API model name | `--served-model-name Qwen/Qwen3.5-4B` | not set | Without this, the OpenAI-compatible API model id may resolve to the local filesystem path instead of `Qwen/Qwen3.5-4B`. |
| GPU memory fraction | `0.80` | `0.70` | Lower is not always safer in `vllm`. Too low a planning budget can leave insufficient room for model + KV cache + startup allocations. |
| Scheduler limits | `--max-num-seqs 1 --max-num-batched-tokens 2048` | defaults | The stable command constrains concurrency and per-step token budget, which reduces startup and serving memory pressure. |
| CUDA graph mode | `--enforce-eager` | default hybrid mode | Disabling CUDA graph execution removes extra memory pressure during graph capture and warmup. |

Practical takeaway:

```text
The stable command works because it is a low-memory Day 1-safe configuration.
The failing command lets vLLM use heavier default batching, profiling, and CUDA-graph behavior.
```

### 7. Why the key flags helped

`--enforce-eager`

```text
Disables CUDA graph capture and reduces startup memory overhead.
```

`--max-num-seqs 1`

```text
Avoids concurrency memory pressure during startup and early testing.
```

`--max-num-batched-tokens 2048`

```text
Keeps prefill and scheduler token budget small.
```

`--max-model-len 2048`

```text
Reduces KV-cache pressure. Day 1 does not need 4096 or 32768 context.
```

`VLLM_USE_FLASHINFER_SAMPLER=0`

```text
Avoids the earlier FlashInfer sampler instability seen on this GPU.
```

### 8. Cleanup before retrying

Before restarting `vllm`, clear old GPU consumers:

```bash
pkill -f "vllm serve" || true
pkill -f "VLLM::EngineCore" || true
pkill -f "llama-server" || true

nvidia-smi
```

Make sure there is no old `VLLM::EngineCore` and no extra `llama-server` still reserving VRAM.

### 9. How to verify the server is actually up

One earlier trap on this machine was:

```text
GPU memory allocated, process exists, but API port is not listening yet
```

Do not use silent `curl` first.

Use:

```bash
curl -v http://127.0.0.1:9001/v1/models \
  -H "Authorization: Bearer local-dev"
```

Also verify the socket:

```bash
ss -ltnp | rg ':9001'
```

If the socket is missing, the API server is not ready regardless of what `nvidia-smi` shows.

Expected model id:

```text
Qwen/Qwen3.5-4B
```

Then test chat:

```bash
curl -sS http://localhost:9001/v1/chat/completions \
  -H "Authorization: Bearer local-dev" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3.5-4B",
    "messages": [
      {"role": "user", "content": "Explain vLLM in 3 bullet points."}
    ],
    "temperature": 0,
    "max_tokens": 120
  }'
```

### 10. What the saved success logs prove

This repo now has enough Day 1 evidence, plus the successful terminal capture from the Day 1 session, to separate "the API is reachable" from "the model output is well tuned."

Primary saved artifacts:

- [install-check.txt](../../results/day-one/logs/install-check.txt)
- [env-check.txt](../../results/day-one/logs/env-check.txt)
- [vllm-server.txt](../../results/day-one/logs/vllm-server.txt)
- [curl-chat-completions-clean.json](../../results/day-one/logs/curl-chat-completions-clean.json)
- [curl-chat-stream.txt](../../results/day-one/logs/curl-chat-stream.txt)

What learners should take from them:

`install-check.txt`

```text
torch importable
CUDA visible
vllm importable
```

That proves the Python environment is usable for GPU inference. It does not prove a model can fit in VRAM.

`env-check.txt`

```text
RTX 5070 Ti 16 GB detected
other GPU processes were already present
```

That proves the machine is CUDA-capable, but it also explains why startup margin was tight. The important lesson is that `nvidia-smi` is a dependency check and a capacity check, not a readiness guarantee.

`vllm-server.txt`

```text
Starting vLLM OpenAI-compatible server
Resolved architecture: Qwen2ForCausalLM
Using max model len 32768
Using FlashInfer for top-p & top-k sampling
Using FLASH_ATTN attention backend
```

That proves `vllm serve` launched, selected a backend, and began model initialization. It does not prove the HTTP port is already ready for requests.

Terminal `curl` `/v1/models`

The successful terminal response showed:

```text
"id": "Qwen/Qwen3.5-4B"
"root": "/data/llm-inference-prod/models/Qwen/Qwen3.5-4B"
"max_model_len": 2048
```

That means the API server was bound, reachable, and had registered the served model ID. This is the clearest proof-of-life check for the OpenAI-compatible server.

`curl-chat-completions-clean.json`

Important fields:

- `id`: request identifier for that completion
- `model`: the served API model ID that handled the request
- `system_fingerprint`: the serving stack fingerprint, useful when comparing runs
- `usage`: prompt and completion token counts, useful for later benchmark accounting
- `finish_reason`: why generation stopped

This saved response ended with:

```text
"finish_reason": "length"
```

That means the request was truncated by `max_tokens`. It does not mean the server crashed. The repeated `prompt_tokens` and `completion_tokens` fields show the model served normally even though the answer quality was not yet ideal.

`curl-chat-stream.txt`

The saved stream uses Server-Sent Events:

```text
data: {...}
...
data: [DONE]
```

That proves streaming is working. It also gives learners the exact wire shape they need later for:

- time to first token
- inter-token latency
- streaming UX checks

See the deeper explanation and source links in the [Day 1 learner appendix](../../docs/day-one-vllm-openai-server.md).

### 11. What failed earlier and why

The main Day 1 failures were:

- startup OOM because `vllm` needed more VRAM for KV cache, profiling, and runtime buffers than the free margin allowed
- `curl -s` hiding connection failures when the API never actually bound to the port
- background GPU consumers from `systemd`, Docker, and other inference processes reducing the available headroom

The key lesson is:

```text
missing files and missing port are different failures
OOM and transport failures must be diagnosed separately
```

### 12. Why the output still showed Thinking Process

The successful API calls still returned `Thinking Process` text. Learners should not treat that as proof that `vllm` serving is broken.

What it means:

- transport was fine, because `/v1/models`, `/v1/chat/completions`, and streaming all worked
- the model is reasoning-capable, so answer formatting depends on model template and reasoning settings
- prompt wording alone is not always enough to suppress reasoning-style output

The Day 1 lesson is:

```text
server readiness and model-behavior tuning are separate tasks
```

For Qwen3-style reasoning behavior, see the official `vllm` reasoning docs in the appendix, especially the `enable_thinking` guidance.

### 13. If it still OOMs

Go one level safer:

```bash
vllm serve /data/llm-inference-prod/models/Qwen/Qwen3.5-4B \
  --served-model-name Qwen/Qwen3.5-4B \
  --host 0.0.0.0 \
  --port 9001 \
  --api-key local-dev \
  --generation-config vllm \
  --max-model-len 1024 \
  --gpu-memory-utilization 0.75 \
  --max-num-seqs 1 \
  --max-num-batched-tokens 1024 \
  --enforce-eager
```

If even this fails, drop to a smaller model for proof-of-life serving.

### 14. Current Day 1 status

Environment setup and troubleshooting are documented.

Logs currently available:

- `/data/llm-inference-prod/results/day-one/logs/install-check.txt`
- `/data/llm-inference-prod/results/day-one/logs/vllm-server.txt`
- `/data/llm-inference-prod/results/day-one/logs/curl-models.json`
- `/data/llm-inference-prod/results/day-one/logs/env-check.txt`

Day 1 is still not signed off because the benchmark outputs are still missing:

- `benchmark.py`
- `results/qwen25-7b-baseline.json`
- `results/qwen35-4b-baseline.json`
- comparison screenshot
- tweet posted
