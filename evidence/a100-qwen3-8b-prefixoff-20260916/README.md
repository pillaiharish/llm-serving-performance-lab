# Curated A100 Qwen3-8B prefix-cache-OFF evidence

This directory contains compact, sanitized derivatives of the controlled
2026-09-16 experiment. The immutable local archives listed in
`archive-checksums.txt` remain the source of truth. They are deliberately not
committed because they contain multi-megabyte request-level observations,
prompts, generated output, telemetry, logs, and endpoint details.

No request payloads, generated text, API keys, credentials, raw Prometheus
dumps, or full NVIDIA telemetry are published here.

## Files

- `commissioning-summary.json`: sanitized PR #26 commissioning identity and
  smoke-test result.
- `environment.md`: hardware, server, model, and measurement-code provenance.
- `concurrency-summary.csv`: six closed-loop concurrency points, each averaged
  across three repetitions.
- `shape-summary.csv`: ten exploratory input/output/concurrency combinations,
  each averaged across three repetitions.
- `prefill32-summary.csv`: six controlled fixed-32-output points, each averaged
  across three repetitions.
- `openloop-summary.csv`: eight offered-rate points, each averaged across three
  repetitions.
- `evidence-audit.md`: archive, manifest, path-safety, provenance, and redaction
  audit.

Percentile columns in the CSVs are arithmetic means of the three
per-repetition summary percentiles. They are not percentiles pooled across raw
requests. Rates and other `mean_` columns are likewise arithmetic means of the
three persisted per-repetition summary values.

Measurements used Slentore commit
`4a573b104970d7300ebebd136548e0c679fd4f74`. Later commissioning and
documentation commits did not retroactively change that measurement identity.
