# Day 8 RAG Under Load Skeleton

## Summary

Day 8 adds a minimal local RAG-under-load skeleton to the benchmark lab.

This is not a production RAG system. It is a small scaffold for future experiments that measure retrieval latency plus generation latency under load.

## Goal

The goal is to prepare the repository for RAG serving performance experiments without adding heavy dependencies or external services.

## What was added

- `rag/README.md`
- `rag/sample_corpus.md`
- `rag/simple_retriever.py`
- `scripts/run_day08_rag_smoke.py`
- `results/day08/rag_smoke_output.txt`
- `reports/day08_rag_skeleton.md`

## What this skeleton does

The skeleton:

- reads a small local markdown corpus
- splits the corpus into heading-based chunks
- tokenizes text using Python standard library
- scores chunks using simple token overlap with IDF-style weighting
- returns top-k chunks
- measures retrieval latency in milliseconds
- writes smoke-test output to `results/day08/rag_smoke_output.txt`

## What this skeleton does not do yet

The skeleton does not include:

- embeddings
- vector databases
- rerankers
- LLM generation
- hosted API calls
- vLLM calls
- production RAG APIs
- UI
- heavy load testing

## Smoke test

Validation commands:

```bash
python -m py_compile rag/simple_retriever.py scripts/run_day08_rag_smoke.py
python scripts/run_day08_rag_smoke.py | tee results/day08/rag_smoke_output.txt
```

The smoke test runs three fixed queries:

```text
What is TTFT?
How does concurrency affect throughput?
Why are these benchmarks not production capacity claims?
```

The output includes:

- retrieved chunks
- retrieval latency in milliseconds
- total smoke-test runtime

## Why this matters for future benchmarking

RAG serving performance is not only model generation speed.

A RAG request may include:
request
→ retrieval
→ prompt construction
→ generation
→ response
