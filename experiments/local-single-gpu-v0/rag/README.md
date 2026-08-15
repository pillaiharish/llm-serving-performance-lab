# RAG Skeleton

This directory contains a minimal local retrieval scaffold for future RAG serving performance experiments.

This is not a production RAG system.

## Current scope

Day 8 only includes:

- a small markdown corpus
- a standard-library keyword retriever
- top-k retrieval
- retrieval latency measurement
- a smoke test script

## Not included yet

This skeleton does not include:

- embeddings
- vector databases
- rerankers
- chunk ingestion pipelines
- hosted LLM calls
- vLLM generation calls
- production APIs
- chat UI

## Why this exists

RAG systems add latency before generation. Before measuring full RAG latency under load, retrieval should be isolated and measured separately.

Future benchmark stages can add:

- generation latency
- retrieval plus generation latency
- concurrent RAG requests
- retrieval quality evaluation
- vector search
- reranking