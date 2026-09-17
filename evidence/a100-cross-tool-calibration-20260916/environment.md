# Controlled environment

| Item | Verified value |
|---|---|
| GPU | NVIDIA A100-SXM4-40GB |
| GPU UUID | `GPU-c757cde4-7971-d84f-7ba0-b6f48a1bda1c` |
| GPU memory | 40960 MiB |
| Driver / reported CUDA | 580.105.08 / 13.0 |
| OS / kernel | Ubuntu 22.04.5 LTS / Linux 5.4.0-216-generic |
| Python / PyTorch | 3.12.13 / 2.11.0+cu130 |
| Model | `Qwen/Qwen3-8B` |
| vLLM | 0.26.0 |
| dtype | bfloat16 |
| tensor parallelism | 1 |
| max model length | 32768 |
| max sequences | 16 |
| GPU memory utilization | 0.90 |
| KV-cache dtype | auto |
| generation configuration | vLLM defaults, request temperature 0 |
| thinking | disabled through default chat-template kwargs |
| prefix cache | enabled; repeated prompt was warm before measurement |

The launch evidence records `vllm serve Qwen/Qwen3-8B` with `--dtype bfloat16`, `--tensor-parallel-size 1`, `--gpu-memory-utilization 0.90`, `--max-model-len 32768`, `--max-num-seqs 16`, `--kv-cache-dtype auto`, `--generation-config vllm`, and thinking disabled. The final tools ran against the same local server deployment.

The preserved package does not record an independently pinned Hugging Face model revision. No revision claim is made.
