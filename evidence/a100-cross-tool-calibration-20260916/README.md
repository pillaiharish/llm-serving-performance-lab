# Curated A100 cross-tool calibration evidence

These files are compact derivatives of the local immutable archive named in `archive-checksums.txt`. The archive remains outside Git and is the source of truth.

- `environment.md` and `tool-versions.md` record verified identities.
- `metric-semantics.md` defines the permitted comparisons.
- `repetition-summary.csv` contains 15 rows: three repetitions across four tools, plus separate GuideLLM raw-100 and native-finalized-98 representations.
- `tool-summary.csv` contains four mean-of-repetition rows for directly comparable cohorts only. Its values are means of repetition summaries, not pooled request percentiles.
- `evidence-audit.md` records integrity, cohort, and disclosure checks.

GuideLLM's 100 raw request records and 98-request native aggregate are intentionally not merged. Missing Slentore true-ITL requests are not imputed. Blank cells denote unavailable or deliberately non-comparable values.

No prompt text, generated response, credential, raw request body, server log, archive, or large telemetry file is published here.
