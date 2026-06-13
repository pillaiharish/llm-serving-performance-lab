#!/usr/bin/env bash
set -euo pipefail

source .venv/bin/activate
source configs/vllm_default.env

mkdir -p results/day02/raw results/day02/logs

for c in 1 2 4; do
  echo "Running Day 2 benchmark: concurrency=$c"

  vllm bench serve \
    --backend openai-chat \
    --base-url http://127.0.0.1:8000 \
    --endpoint /v1/chat/completions \
    --model "$MODEL_NAME" \
    --dataset-name random \
    --random-input-len 128 \
    --random-output-len 64 \
    --num-prompts 16 \
    --request-rate inf \
    --max-concurrency "$c" \
    --percentile-metrics ttft,tpot,itl,e2el \
    --metric-percentiles 50,90,95,99 \
    --save-result \
    --save-detailed \
    --result-dir results/day02/raw \
    --result-filename "day02_c${c}_i128_o64.json" \
    2>&1 | tee "results/day02/logs/bench_c${c}_i128_o64.txt"
done
