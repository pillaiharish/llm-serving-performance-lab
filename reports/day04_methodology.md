# Day 4 Methodology Report

## Summary

Date: 2026-06-14  
Focus: benchmark methodology and documentation polish  

Day 4 focused on making the repo easier for another engineer to understand and reproduce.

No new benchmark was run today.

## Goal

Document how benchmarks are run, how results are interpreted, and what claims are allowed from the current data.

## Files added

```text
docs/benchmark_methodology.md
docs/results_interpretation.md
reports/day04_methodology.md
```

## Files updated

```text
README.md
docs/day01/metrics_glossary.md
.gitignore
```

## Why this matters

Benchmarks without methodology are easy to misread.

A benchmark report should answer:

- what was measured
- how it was measured
- what files preserve the evidence
- what conclusions are supported
- what conclusions are not supported yet

## Current supported conclusion

For the tiny Day 2 benchmark, max concurrency 4 gave the best throughput-latency tradeoff among the tested values.

This is supported by:
```text
Concurrency 1: E2E p95 148.70 ms, output throughput 438.30 tok/s
Concurrency 2: E2E p95 164.94 ms, output throughput 782.64 tok/s
Concurrency 4: E2E p95 170.16 ms, output throughput 1526.10 tok/s
```

## Current unsupported conclusions

The current data does not prove:

- concurrency 4 is globally optimal
- the system is production-ready
- the system will behave the same at concurrency 8 or 16
- the system will behave the same for longer prompts
- the system will behave the same for longer outputs

## What worked
- Added benchmark methodology documentation.
- Added results interpretation guide.
- Clarified allowed and unsupported claims.
- Connected raw JSON, CSV, plots, and reports into one workflow.

## What is still weak
- No repeated benchmark runs yet.
- No prompt/output length experiment yet.
- No higher concurrency sweep yet.
- No GPU utilization tracking yet.
- No cost analysis yet.

## What I learned
- Benchmark numbers need methodology to be useful.
- Throughput numbers can be misleading without latency context.
- “Optimal” is a dangerous word unless the search space is clear.
- A portfolio repo should show both results and the limits of those results.

## Next actions
Day 5: prompt/output length experiment.
Compare 128/64, 512/128, and 2048/256 token settings.
Save raw results, CSV summary, plots, and report.