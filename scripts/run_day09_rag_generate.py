from __future__ import annotations
import time
import sys
import os
from pathlib import Path
from dotenv import load_dotenv
from openai import OpenAI

REPO_ROOT = Path(__file__).resolve().parents[1]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from rag.simple_retriever import SimpleRetriever

load_dotenv("vllm_default.env")
model_name = os.getenv("MODEL_NAME")
host = os.getenv("HOST")
port = os.getenv("PORT")

QUERIES = [
    "What is TTFT?",
    "How does concurrency affect throughput?",
    "Why are these benchmarks not production capacity claims?",
]

def streaming(query, context):
    context_text = "\n".join([r['text'] for r in context])
    print(f"\n Context here:{context_text}")
    print(f"\n {model_name}, {host}, {port}")
    prompt = f"Context:\n{context_text}\n\nQuestion: {query}"
    client = OpenAI(
        base_url=f"http://{host}:{port}/v1",
        api_key="not-needed"
    )

    # Use the model name from the file
    stream = client.chat.completions.create(
        model=model_name,
        messages=[
            {"role": "user", "content": f"{prompt}"}
        ],
        stream=True
    )

    for chunk in stream:
        if chunk.choices[0].delta.content:
            print(chunk.choices[0].delta.content, end="", flush=True)
            

def main() -> None:
    repo_root = REPO_ROOT
    corpus_path = repo_root / "rag" / "sample_corpus.md"
    output_path = repo_root / "results" / "day09" / "rag_generation_output.txt"

    output_path.parent.mkdir(parents=True, exist_ok=True)

    generation_start = time.perf_counter()
    retriever = SimpleRetriever(corpus_path)

    lines: list[str] = []
    lines.append("# Day 9 RAG Generation Output")
    lines.append("")
    lines.append(f"Corpus: {corpus_path}")
    lines.append(f"Chunks loaded: {len(retriever.chunks)}")
    lines.append("")

    i = 0

    for query in QUERIES:
        retrieval_start = time.perf_counter()
        results = retriever.retrieve(query, top_k=3)
        retrieval_ms = (time.perf_counter() - retrieval_start) * 1000.0

        lines.append("=" * 80)
        lines.append(f"Query: {query}")
        lines.append(f"Retrieval latency ms: {retrieval_ms:.3f}")
        lines.append(f"Top-k results: {len(results)}")
        lines.append("")

        lines.append("LLM Response:")
        # Pass the 'results' into the streaming function
        resp_start_time = time.perf_counter()
        response_text = streaming(query, results) 
        resp_end_time = (time.perf_counter() - resp_start_time) * 1000.0
        lines.append(str(response_text))
        i+=1
        lines.append(f"Question {i} generation completed in ms: {resp_end_time:.3f}")
        lines.append("")

        for rank, result in enumerate(results, start=1):
            lines.append(f"Rank: {rank}")
            lines.append(f"Chunk ID: {result['chunk_id']}")
            lines.append(f"Title: {result['title']}")
            lines.append(f"Score: {result['score']}")
            lines.append(f"Matched terms: {', '.join(result['matched_terms'])}")
            lines.append("Text:")
            lines.append(str(result["text"]))
            lines.append("")

    total_ms = (time.perf_counter() - generation_start) * 1000.0
    lines.append("=" * 80)
    lines.append(f"Total generation runtime ms: {total_ms:.3f}")
    lines.append("")


    output = "\n".join(lines)
    output_path.write_text(output, encoding="utf-8")

    print(output)


if __name__ == "__main__":
    main()