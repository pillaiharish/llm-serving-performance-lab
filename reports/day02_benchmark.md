# Day 2 Benchmark Report

## Summary

Date: 2026-06-13  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  
Server: vLLM OpenAI-compatible API  
Input length: 128 tokens  
Output length: 64 tokens  
Concurrency: 1, 2, 4  
Prompts per run: 16  

## Goal

Run the first small vLLM benchmark loop and save raw benchmark evidence.

## Server command

```bash
source configs/vllm_default.env

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```