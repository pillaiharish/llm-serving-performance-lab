# Day 1 Baseline Report

## Summary

Date: 2026-06-13  
Repo: pillaiharish/llm-serving-performance-lab  
Commit: 0c25667 
Machine: admin1-MS-7D75  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  

## Objective

Start a local OpenAI-compatible LLM server using vLLM and confirm that a client can send a request successfully.

## Environment

See:

```text
reports/day01_env_check.txt
```

Key environment:

```text
Python: 3.12.3
NVIDIA driver: 580.126.09
CUDA reported by nvidia-smi: 13.0
PyTorch: 2.11.0+cu130
vLLM: 0.22.0
GPU capability: sm_120
```

## Server attempt 1

Command:

```bash
vllm serve models/Qwen/Qwen2.5-0.5B-Instruct
```

Result:

```text
Failed during vLLM engine initialization.
```

Important error:

```text
Failed to get device capability: SM 12.x requires CUDA >= 12.9.
Using FlashInfer for top-p & top-k sampling.
RuntimeError: FlashInfer requires GPUs with sm75 or higher
RuntimeError: Engine core initialization failed.
```

Interpretation:

The model weights loaded successfully, so this does not look like a model-size or out-of-memory issue. The failure happened during the sampler/profile path after vLLM selected FlashInfer.

## Server attempt 2

Command:

```bash
source configs/vllm_default.env

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

Result:

```text
Success. vLLM server started after exporting VLLM_USE_FLASHINFER_SAMPLER=0 and setting max model length to 4096.
```

## Smoke test

Command:

```bash
python scripts/smoke_chat.py | tee reports/day01_smoke_output.txt
```

Result:

```text
Base URL: http://localhost:8000/v1
Model: models/Qwen/Qwen2.5-0.5B-Instruct
Available models:
- models/Qwen/Qwen2.5-0.5B-Instruct
Response: SMOKE_TEST_OK
```

Quality note:

If the model returns an incorrect explanation of TTFT/ITL, the API smoke test may still prove the server responded, but the response should be documented as semantically incorrect.

## What worked

- Repository renamed.
- Day 1 structure created.
- Environment check script runs.
- GPU is detected by PyTorch.
- vLLM loads the small Qwen model weights.
- vLLM server started after disabling the FlashInfer sampler path.
- OpenAI-compatible `/v1/models` and chat completion smoke test succeeded.

## What failed

- First vLLM attempt failed during engine initialization.
- FlashInfer sampler path failed on the RTX 5070 Ti / SM 12.0 stack.
- First smoke output gave an incorrect explanation of TTFT/ITL and should not be used as educational content.

## What I learned

- A model can load successfully but the serving engine can still fail during profiling or sampler initialization.
- Environment variables must be exported if Python or child processes need to read them.
- Day 1 evidence should separate API liveness from answer quality.

## Next actions

- Start Day 2 benchmark loop.
- Run a tiny vLLM benchmark with random input/output lengths.
- Capture TTFT, TPOT, ITL, and E2E latency.
- Save raw benchmark output under `results/day02/`.
- Write `reports/day02_benchmark.md`.