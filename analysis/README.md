# H100 Qwen3.8-27B single-GPU baseline analysis

The analyzer reads the source archive directly and never extracts or modifies raw evidence. Every invocation is published atomically under `generated/<archive-sha256>/<analysis-run-id>/`, so failed validation cannot be confused with outputs from another run.

## Reproduce

From the repository root, verify the trusted checksum first:

```bash
shasum -a 256 -c analysis/checksums.sha256
python3 -m venv analysis/.venv
analysis/.venv/bin/python -m pip install -r analysis/requirements.txt
analysis/.venv/bin/python analysis/scripts/test_analyze.py
analysis/.venv/bin/python analysis/scripts/analyze.py \
  h100-qwen38-single-gpu-baseline-c1-20260906.tar.gz \
  --checksum-file analysis/checksums.sha256 --mode strict
```

Strict mode requires exactly rep1, rep2, and rep3. The supplied archive contains only valid rep1, so the expected exit status is `2`; that invocation publishes only `validation.json`, `validation.md`, and `manifest.json`.

To generate explicitly labeled rep1-only exploratory evidence:

```bash
analysis/.venv/bin/python analysis/scripts/analyze.py \
  h100-qwen38-single-gpu-baseline-c1-20260906.tar.gz \
  --checksum-file analysis/checksums.sha256 --mode exploratory
```

Every exploratory plot, table, report, validation record, and manifest identifies the result as:

> EXPLORATORY — ONE AVAILABLE REPETITION — NOT A REPEATABILITY RESULT

The package requires the exact persisted experiment identity: model `Qwen/Qwen3.8-27B`, schema `7` in `run.json`, `summary.json`, and `summary.csv`, and Slentore version `v0.0.0-20260830170145-4a573b104970`. Cross-repetition comparison also requires those fields to match exactly. The report reads its model, workload, load, schema, and Slentore version from validated run fields.

The package validates request-level TTFT, TPOT, TTLT, E2E, exact token usage, and client-observed token-ID-presence timing evidence. Persisted TTLT percentiles are checked against request-level values reconstructed from the last token-bearing receive timestamp. ITL gaps are recomputed from token-bearing client receive timestamps; actual token ID values are not persisted. NVIDIA telemetry timestamps have no timezone, so wall-clock alignment is explicitly unresolved and never used to claim request-level hardware causality.

Request evidence is accepted only at canonical immediate-child paths. Measurement duration is independently derived from `run.json` `measurement.elapsed_ns`, then cross-checked against lifecycle timestamps, `summary.json`, and `summary.csv` with an absolute tolerance of 1 microsecond. Request and token throughput are recomputed from that duration and request-observation totals.

Each manifest records the exact analyzer SHA-256 plus Git tracking/dirty state. The analyzer hash is the authoritative byte-level identity; Git provenance is marked complete only when the analyzer is tracked and all critical analysis files are clean.

PNG and SVG values, axes, and labels are numerically reproducible with the pinned plotting version. Deterministic SVG IDs and metadata are used, but files are not promised byte-identical across Python, fonts, operating systems, or rendering backends.

`analysis/generated/` contains local invocation artifacts and is intentionally ignored by Git. Version-control source, tests, and documentation explicitly; do not stage the raw archive, `.venv`, caches, or generated invocation directories, and do not use a blanket `git add analysis/`.
