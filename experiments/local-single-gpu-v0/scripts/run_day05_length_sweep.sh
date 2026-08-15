#!/usr/bin/env bash
set -euo pipefail

source .venv/bin/activate
source configs/vllm_default.env

mkdir -p results/day05/raw results/day05/logs

# format: label input_len output_len
cases=(
  "small 128 64"
  "medium 512 128"
  "large 2048 256"
)

for case in "${cases[@]}"; do
  read -r label input_len output_len <<< "$case"

  echo "Running Day 5 length sweep: label=$label input=$input_len output=$output_len concurrency=4"

  vllm bench serve \
    --backend openai-chat \
    --base-url http://127.0.0.1:8000 \
    --endpoint /v1/chat/completions \
    --model "$MODEL_NAME" \
    --dataset-name random \
    --random-input-len "$input_len" \
    --random-output-len "$output_len" \
    --num-prompts 16 \
    --request-rate inf \
    --max-concurrency 4 \
    --temperature 0 \
    --percentile-metrics ttft,tpot,itl,e2el \
    --metric-percentiles 50,90,95,99 \
    --save-result \
    --save-detailed \
    --result-dir results/day05/raw \
    --result-filename "day05_${label}_i${input_len}_o${output_len}_c4.json" \
    2>&1 | tee "results/day05/logs/bench_${label}_i${input_len}_o${output_len}_c4.txt"
done