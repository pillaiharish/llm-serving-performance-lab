# Day 1 Learner Appendix: Reading vLLM Logs, Flags, and Model Concepts

This appendix explains what the Day 1 logs actually prove, why some runs failed while others succeeded, and how to think about `vllm serve` flags without mixing up model architecture, runtime backend, and API behavior.

## Scope

This is a Day 1 teaching note, not a benchmark sign-off.

It explains:

- what the saved logs mean
- why the low-memory serve profile worked
- why earlier runs failed
- how to choose the important `vllm` flags for a small single-GPU setup
- how to separate dense, sparse, hybrid, paged, and MoE concepts

Primary local artifacts:

- [install-check.txt](../results/day-one/logs/install-check.txt)
- [env-check.txt](../results/day-one/logs/env-check.txt)
- [vllm-server.txt](../results/day-one/logs/vllm-server.txt)
- [curl-chat-completions-clean.json](../results/day-one/logs/curl-chat-completions-clean.json)
- [curl-chat-stream.txt](../results/day-one/logs/curl-chat-stream.txt)
- [Qwen3.5-4B config.json](../models/Qwen/Qwen3.5-4B/config.json)
- [Qwen2.5-7B-Instruct config.json](../models/Qwen/Qwen2.5-7B-Instruct/config.json)

Official references used in this appendix:

- [vLLM `serve` CLI](https://docs.vllm.ai/en/stable/cli/serve/)
- [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/)
- [vLLM Reasoning Outputs](https://docs.vllm.ai/en/stable/features/reasoning_outputs/)
- [vLLM Attention Backend Feature Support](https://docs.vllm.ai/en/latest/design/attention_backends/)
- [vLLM Environment Variables](https://docs.vllm.ai/en/latest/configuration/env_vars/)
- [vLLM blog: PagedAttention introduction](https://vllm.ai/blog/2023-06-20-vllm)
- [PagedAttention paper](https://arxiv.org/abs/2309.06180)
- [FlashAttention paper](https://arxiv.org/abs/2205.14135)
- [PyTorch CUDA memory allocator notes](https://docs.pytorch.org/docs/2.9/notes/cuda.html)
- [Red Hat vLLM Office Hours #28](https://www.redhat.com/en/events/virtual/vllm-office-hours-28-vllm-project-update)
- [vLLM Triton Attention Backend Deep Dive](https://blog.vllm.ai/2026/03/04/vllm-triton-backend-deep-dive.html)

## How to read these logs

### `install-check.txt`

Observed:

```text
torch: 2.11.0+cu130
cuda: 13.0
cuda available: True
vllm imported ok
0.22.0
```

What it proves:

- PyTorch is installed with CUDA support
- CUDA is visible to Python
- `vllm` imports successfully
- the environment can at least start GPU-backed Python inference code

What it does not prove:

- that the chosen model fits in VRAM
- that the `vllm` HTTP server will bind successfully
- that the model output will look the way you want

What learners should infer next:

- environment setup is no longer the blocker
- the next risks are GPU memory, backend choice, and request-level behavior

### `env-check.txt`

Observed:

- Ubuntu `x86_64`
- Python `3.12.3`
- RTX 5070 Ti with `16303 MiB`
- multiple existing GPU consumers in `nvidia-smi`

What it proves:

- the machine has the right class of hardware for local `vllm` serving
- free VRAM was already reduced before Day 1 testing began

What it does not prove:

- that `vllm` has enough free headroom for weights, KV cache, runtime buffers, and warmup

What learners should infer next:

- `nvidia-smi` is part of readiness checking, not just hardware verification
- local inference debugging starts with both "can CUDA run?" and "how much memory is already gone?"

### `vllm-server.txt`

Observed:

```text
Starting vLLM OpenAI-compatible server
Resolved architecture: Qwen2ForCausalLM
Using max model len 32768
Asynchronous scheduling is enabled.
Using FlashInfer for top-p & top-k sampling.
Using FLASH_ATTN attention backend
```

What it proves:

- `vllm serve` launched
- the model architecture was resolved
- runtime components such as sampling and attention backend were selected

What it does not prove:

- that the HTTP server is already accepting requests
- that the chosen settings are safe for a 16 GB GPU

What learners should infer next:

- process existence is weaker evidence than a successful `/v1/models` call
- logs about backend selection matter when debugging performance and stability

### Successful `/v1/models` response

Observed in the Day 1 terminal session:

```json
{
  "object": "list",
  "data": [
    {
      "id": "Qwen/Qwen3.5-4B",
      "root": "/data/llm-inference-prod/models/Qwen/Qwen3.5-4B",
      "max_model_len": 2048
    }
  ]
}
```

What it proves:

- the API server is bound and reachable
- authentication header was accepted
- the served model ID is registered exactly as expected

What it does not prove:

- that response quality is good
- that reasoning output has been suppressed

What learners should infer next:

- `/v1/models` is the cleanest proof-of-life check for the OpenAI-compatible server
- `root` and `max_model_len` are useful sanity checks when serving a local folder

### `curl-chat-completions-clean.json`

Observed:

- `object: "chat.completion"`
- `model: "Qwen/Qwen3.5-4B"`
- `system_fingerprint: "vllm-0.22.0-b259fbd1"`
- `usage.prompt_tokens: 46`
- `usage.completion_tokens: 220`
- `finish_reason: "length"`
- assistant output still began with `Thinking Process`

What it proves:

- the OpenAI-compatible Chat Completions flow works end to end
- the model generated a normal JSON response
- token accounting is present for later benchmarking

What it does not prove:

- that the answer content matches the intended style
- that reasoning output is disabled

What learners should infer next:

- `finish_reason: "length"` means truncation at `max_tokens`, not a crash
- API success and answer-format success are separate questions

### `curl-chat-stream.txt`

Observed:

```text
data: {...}
...
data: [DONE]
```

What it proves:

- Server-Sent Events streaming is working
- token deltas are arriving incrementally rather than only as a final block

What it does not prove:

- that TTFT is good
- that inter-token latency is good

What learners should infer next:

- this is the transport shape needed for future TTFT and ITL measurement
- a streaming success can still stream undesirable reasoning text if model behavior is not tuned

## Why this run succeeded

The working Day 1 profile was:

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

### Flag-by-flag explanation

| Setting | Category | What it does | Why it helped here |
| --- | --- | --- | --- |
| `--served-model-name Qwen/Qwen3.5-4B` | API/identity | vLLM docs say this controls the model name used in API responses and requests. | It kept the API model ID clean and predictable. It did not fix VRAM pressure. |
| `--host 0.0.0.0 --port 9001 --api-key local-dev` | API/identity | These expose the OpenAI-compatible HTTP server on a chosen port with simple bearer auth. | They made local validation repeatable. They are not memory knobs. |
| `--generation-config vllm` | API/identity | vLLM docs say this skips loading a model-side generation config and uses vLLM defaults instead. | It reduced surprises from inherited generation defaults. It was a consistency choice, not the main fit fix. |
| `--max-model-len 2048` | memory-impacting | vLLM supports setting a fixed model length instead of auto-picking the largest fit. | It reduced KV-cache pressure and was more realistic for Day 1 than letting a very large context length dominate memory planning. |
| `--gpu-memory-utilization 0.80` | memory-impacting | vLLM docs describe this as the fraction of GPU memory used by the model executor. | On this 16 GB GPU, `0.80` gave the executor enough planned budget while the other conservative flags reduced overhead. |
| `--max-num-seqs 1` | memory-impacting | vLLM docs describe this as the maximum number of sequences processed in one iteration. | It cut concurrency pressure and made startup safer on a tight VRAM budget. |
| `--max-num-batched-tokens 2048` | memory-impacting | vLLM docs describe this as the maximum number of tokens processed in a single iteration. | It constrained prefill and scheduler pressure. |
| `--enforce-eager` | stability/debugging | vLLM docs say this disables CUDA graph execution and always uses eager mode. | It removed CUDA graph capture overhead and helped a borderline-fit model start successfully. |
| `VLLM_USE_FLASHINFER_SAMPLER=0` | stability/debugging | vLLM env var docs show this controls whether the FlashInfer sampler is used. | In this setup it was a stability workaround after earlier sampler trouble. It is not a universal "best" setting. |
| `VLLM_ATTENTION_BACKEND=FLASH_ATTN` | backend selection | vLLM lets users override automatic attention-backend selection. | It forced a backend that behaved predictably in this setup. That is a deliberate override, not a universal recommendation. |
| `PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True` | stability/debugging | PyTorch docs say this helps the allocator grow segments and reduce unusable fragments. | It is a fragmentation mitigation that can help near-limit inference workloads fit more reliably. |

### Why the combination mattered

The important Day 1 lesson is not "absolute paths fix vLLM." The real lesson is:

```text
the successful run used a low-memory, low-concurrency, low-surprise profile
```

The path made the command reproducible. The memory flags made it fit.

## Why earlier runs failed

The earlier failures were not caused by missing model files.

The local `Qwen3.5-4B` folder already had:

- safetensor shards
- `config.json`
- tokenizer files

The real issue was memory pressure during startup. vLLM needs more than just raw model weight memory:

```text
weights
+ KV cache
+ scheduler/runtime buffers
+ warmup and profiling allocations
+ backend-specific temporary memory
+ fragmentation headroom
```

This matches both the Day 1 OOM symptom and the vLLM/PyTorch memory model:

- the OOM happened during initialization and allocation, not file loading
- earlier commands allowed heavier defaults such as CUDA graph capture and broader batching behavior
- other GPU processes had already consumed part of the 16 GB budget

Another earlier failure mode was transport confusion:

- `curl -s` can hide connection errors
- a model process in `nvidia-smi` does not guarantee the API port is listening

Use `curl -sS` or `curl -v` while debugging bring-up.

## Why the output still showed `Thinking Process`

The `Thinking Process` text in both non-streaming and streaming responses is a model-behavior issue, not proof of a broken server.

The official vLLM reasoning docs show that reasoning-capable models can expose reasoning behavior through chat completion responses, and for Qwen3 series the docs explicitly note that disabling thinking may require:

```python
extra_body={"chat_template_kwargs": {"enable_thinking": False}}
```

What learners should take from this:

- successful HTTP transport does not guarantee the desired response style
- prompt instructions such as "do not show reasoning" may not be sufficient by themselves
- reasoning-capable models often need template-aware request settings, not just different prompt wording

For Day 1, the correct interpretation is:

```text
the server worked
the model output policy still needs tuning
```

## Concepts learners must keep separate

### 1. Model architecture vs inference backend

These are not the same layer of the system.

- model architecture answers "what kind of transformer or hybrid model is this?"
- inference backend answers "which kernel/runtime implementation is vLLM using to execute it efficiently on this GPU?"

Changing `VLLM_ATTENTION_BACKEND` does not change the model architecture. It changes which backend vLLM uses to implement supported attention operations.

### 2. Dense attention vs hybrid linear/full attention vs sparse attention

Dense attention:

- each layer uses standard full attention over the allowed context or window
- local `Qwen2.5-7B-Instruct` is the Day 1 dense example in this repo

Hybrid linear/full attention:

- some layers use a cheaper linear-style attention mechanism
- some layers still use full attention periodically
- local `Qwen3.5-4B` is this kind of example in this repo
- its config shows repeated `linear_attention` layers with periodic `full_attention` layers and `full_attention_interval: 4`

Sparse attention:

- the model architecture restricts which token positions can attend to which others
- this is an architectural pattern, not the same thing as vLLM's paged KV cache
- FlashAttention research also discusses block-sparse attention as a different algorithmic pattern from standard dense attention

### 3. PagedAttention vs "this model uses sparse attention"

This distinction matters a lot.

PagedAttention is a vLLM memory-management technique for KV cache. The vLLM blog and paper describe it as storing KV blocks in non-contiguous memory with page-like management for flexible allocation and sharing.

That means:

- PagedAttention is about serving-time memory layout and cache efficiency
- sparse attention is about the model's attention pattern
- a dense model can still be served with PagedAttention

### 4. FlashAttention and FlashInfer are backend/kernel choices

FlashAttention is an IO-aware exact attention algorithm designed to reduce expensive memory traffic.

FlashInfer and FlashAttention in vLLM are backend/kernel choices that can be selected automatically or overridden. They do not turn a dense model into an MoE model, and they do not change a hybrid model into a sparse model.

### 5. MoE is not the same thing as attention type

Mixture-of-Experts changes which feed-forward experts handle each token. vLLM has separate MoE and expert-parallel machinery for that class of models.

Day 1 takeaway:

- MoE is a routing/expert-compute question
- attention backend is an attention-kernel question
- these should not be debugged as if they were the same problem

## What these two local models actually are

### `Qwen3.5-4B`

Based on the local [config.json](../models/Qwen/Qwen3.5-4B/config.json):

- `architectures: ["Qwen3_5ForConditionalGeneration"]`
- `layer_types` alternates `linear_attention` and `full_attention`
- `full_attention_interval: 4`

For Day 1, teach it as:

```text
hybrid linear/full-attention model
not a MoE example in this repo
```

### `Qwen2.5-7B-Instruct`

Based on the local [config.json](../models/Qwen/Qwen2.5-7B-Instruct/config.json):

- `architectures: ["Qwen2ForCausalLM"]`
- no MoE-specific expert fields are present
- no hybrid layer list like the Qwen3.5 config is present

For Day 1, teach it as:

```text
dense decoder-style model
not a MoE example in this repo
```

## How to choose flags by situation

| Situation | What to prefer | Why |
| --- | --- | --- |
| 16 GB single-GPU interactive setup | conservative `--max-model-len`, `--max-num-seqs 1`, small `--max-num-batched-tokens`, and often `--enforce-eager` | You are optimizing for first successful bring-up, not peak throughput. |
| Larger GPU or later throughput testing | revisit `--max-num-seqs`, `--max-num-batched-tokens`, and possibly remove `--enforce-eager` | Once the model fits comfortably, higher batching and CUDA graphs may improve throughput. |
| Unknown GPU/backend stability | start with automatic backend selection, force a backend only when debugging or validating a hypothesis | vLLM already has backend-priority rules by GPU family. Manual overrides are useful, but should be deliberate. |
| Blackwell-class standard attention GPUs | know that current vLLM docs prioritize `FLASHINFER` before `FLASH_ATTN` in auto mode | If you force `FLASH_ATTN`, document that you are overriding the default backend preference for a reason. |
| Ampere/Hopper standard attention GPUs | know that current vLLM docs prioritize `FLASH_ATTN` before `FLASHINFER` in auto mode | Backend expectations are hardware dependent. |
| Reasoning-capable models | validate chat-template and reasoning settings, not only prompt wording | Response-format issues may come from reasoning mode, not transport or memory failures. |

## What learners should remember from Day 1

The five key conclusions are:

1. A successful `/v1/models` call is the cleanest proof that the API server is ready.
2. `finish_reason: "length"` means truncation by `max_tokens`, not a crash.
3. `Thinking Process` output is a model/reasoning behavior issue, not an HTTP transport issue.
4. The earlier OOM was a VRAM-planning problem, not a missing-model-files problem.
5. PagedAttention is a serving-time KV-cache strategy, not the same thing as sparse attention.

## Recommended future learning

For official documentation:

- [vLLM `serve` CLI](https://docs.vllm.ai/en/stable/cli/serve/)
- [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/)
- [vLLM Reasoning Outputs](https://docs.vllm.ai/en/stable/features/reasoning_outputs/)
- [vLLM Attention Backend Feature Support](https://docs.vllm.ai/en/latest/design/attention_backends/)

For core concepts:

- [PagedAttention paper](https://arxiv.org/abs/2309.06180)
- [FlashAttention paper](https://arxiv.org/abs/2205.14135)
- [vLLM PagedAttention introduction blog](https://vllm.ai/blog/2023-06-20-vllm)

For official project deep dives:

- [Red Hat vLLM Office Hours](https://www.redhat.com/en/events/virtual/vllm-office-hours-28-vllm-project-update)
- [vLLM Triton Attention Backend Deep Dive](https://blog.vllm.ai/2026/03/04/vllm-triton-backend-deep-dive.html)
