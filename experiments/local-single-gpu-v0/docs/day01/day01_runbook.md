# Day 1 Runbook — First LLM Serving Baseline

## Goal

Start a local OpenAI-compatible LLM server, send one smoke request, collect environment details, and document the first baseline.

## Hardware

- GPU: NVIDIA GeForce RTX 5070 Ti
- VRAM: 16 GB
- OS: Linux
- Python: 3.12.3
- NVIDIA driver: 580.126.09
- CUDA reported by nvidia-smi: 13.0
- PyTorch CUDA: 13.0
- GPU capability: sm_120

## Environment check

```bash
source .venv/bin/activate
python scripts/check_env.py | tee reports/day01_env_check.txt
```

## Load config

```bash
source configs/vllm_default.env
env | grep -E 'MODEL_NAME|VLLM_USE_FLASHINFER'
```

## Start vLLM server

```bash
vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

vLLM provides an OpenAI-compatible server and starts on `http://localhost:8000` by default unless host/port are changed. It supports endpoints such as `/v1/models`, `/v1/completions`, and `/v1/chat/completions`.

## Smoke test

In another terminal:

```bash
source .venv/bin/activate
source configs/vllm_default.env

curl http://localhost:8000/v1/models

python scripts/smoke_chat.py | tee reports/day01_smoke_output.txt
```

## Known issue from first attempt

The first attempt failed during vLLM engine initialization after selecting the FlashInfer sampler path.

Important lines:

```text
Failed to get device capability: SM 12.x requires CUDA >= 12.9.
Using FlashInfer for top-p & top-k sampling.
RuntimeError: FlashInfer requires GPUs with sm75 or higher
```

Initial workaround:

```bash
export VLLM_USE_FLASHINFER_SAMPLER=0
```

## Pass condition

Day 1 passes if:

- environment check output is saved
- vLLM server is attempted
- smoke request is attempted
- success or failure is documented honestly
- no model weights or wheel bundles are committed