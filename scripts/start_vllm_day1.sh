#!/usr/bin/env bash
set -euo pipefail

MODEL="${MODEL:-Qwen/Qwen2.5-7B-Instruct}"
HOST="${HOST:-0.0.0.0}"
PORT="${PORT:-9001}"
API_KEY="${API_KEY:-local-dev}"

echo "Starting vLLM OpenAI-compatible server"
echo "MODEL=${MODEL}"
echo "HOST=${HOST}"
echo "PORT=${PORT}"

vllm serve "${MODEL}" \
  --host "${HOST}" \
  --port "${PORT}" \
  --api-key "${API_KEY}" \
  --generation-config vllm
