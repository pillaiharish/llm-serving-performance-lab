# Slentore V1 release checklist

PR #22 is a merge gate, not permission to tag. Complete these gates in order.

## A. Merge gate

- [ ] `Quality (Go 1.22.x)` and `Quality (Go 1.25.5)` pass.
- [ ] `Race`, `Client calibration smoke`, and `V1 acceptance fixture` pass.
- [ ] Local Go 1.25.5 tests, vet, builds, race tests, module cleanliness, and
      formatting checks pass.
- [ ] Separate-process clean closed/open calibration and deliberate
      client-limited calibration have been inspected.
- [ ] Calibration and acceptance artifacts contain no credential, prompt,
      generated text, raw request/SSE JSON, or token IDs.
- [ ] Calibration, release, acceptance, and release-note documentation is
      reviewed with no unresolved PR blocker.

The artifact consistency gate is already anchored by focused regression tests:
artifact writer tests recalculate `RequestMetrics` from observations and the
canonical `RunSummary`/JSON/CSV; experiment writer tests validate plan-to-point,
point-to-child metadata/summary, and child-summary-to-experiment-row identity.
Calibration writer and V1 verifier tests extend this chain without duplicating
the complete writers.

The repository currently has no branch protection. After PR #22 is merged,
make the five CI checks above required for `main`. Do not change repository
settings as part of PR #22.

## B. Real-vLLM acceptance gate

Run against an already-running endpoint. This process does not provision or
manage GPU infrastructure.

```bash
export SLENTORE_BASE_URL='http://127.0.0.1:18000/v1'
export SLENTORE_MODEL='<served-model>'
export SLENTORE_TOKENIZER_URL='http://127.0.0.1:18000/tokenize'

# Optional authentication by environment-variable name:
export SLENTORE_API_KEY_ENV=VLLM_API_KEY
export VLLM_API_KEY='...'

scripts/v1_acceptance.sh
```

`SLENTORE_ACCEPTANCE_DIR` selects a new, nonexistent evidence directory.
Conservative defaults can be changed with
`SLENTORE_BASIC_REQUESTS`, `SLENTORE_BASIC_WARMUP`,
`SLENTORE_BASIC_MAX_OUTPUT_TOKENS`,
`SLENTORE_CLOSED_CONCURRENCY_VALUES`, `SLENTORE_CLOSED_REQUESTS`,
`SLENTORE_CLOSED_WARMUP`, `SLENTORE_OPEN_RATE_VALUES`,
`SLENTORE_OPEN_DURATION`, `SLENTORE_OPEN_MAX_IN_FLIGHT`,
`SLENTORE_OPEN_WARMUP`, `SLENTORE_TOKEN_INPUT_VALUES`,
`SLENTORE_TOKEN_OUTPUT_VALUES`, `SLENTORE_TOKEN_REQUESTS`, and
`SLENTORE_TOKEN_WARMUP`. Overrides are explicit tested values, not capacity
recommendations.

The script prints its unique output root. It must pass its basic direct-prompt
run, closed-loop sweep, open-loop sweep, two-input token-length matrix,
evidence-backed vLLM token-timing audit, permissive SLO/goodput audit, schema
and identity checks, and redaction scan. Any failed default child is an
acceptance failure even when its valid schema-7 evidence is retained.

Record alongside the evidence, with secrets redacted:

- exact Slentore git SHA and `slentore version` output;
- vLLM version;
- model identifier and model revision when known;
- tokenizer endpoint;
- GPU model and count;
- driver and CUDA/runtime versions when known;
- complete server launch command and relevant vLLM flags;
- client OS/architecture;
- UTC timestamp.

Remote GPU facts are manual evidence; Slentore does not pretend to discover
them. After adding the environment record, rerun
`scripts/verify_v1_acceptance.py --acceptance-dir <path>` so the redaction
audit also covers that text.

Normal GitHub CI deliberately uses only the deterministic fake server. It
requires no GPU, hosted endpoint, Hugging Face credential, or other secret and
does not replace this gate.

## C. Tag and release gate

- [ ] PR #22 is merged.
- [ ] `main` CI is green at the exact release SHA.
- [ ] Real-vLLM acceptance passed against a binary built from that merged SHA.
- [ ] The artifact verifier passed after the environment record was added.
- [ ] The checkout is clean and the release SHA is recorded.
- [ ] Reproducible binaries were built and checksummed.
- [ ] Create the tag exactly `v1.0.0` only after every prior item passes.
- [ ] A GitHub release may be created afterward; it is not part of PR #22.

## Reproducible build

From the exact clean release checkout, choose the explicit target and inject
the same revision reported by Git:

```bash
revision=$(git rev-parse HEAD)
mkdir -p dist/linux-amd64

CGO_ENABLED=0 \
GOOS=linux \
GOARCH=amd64 \
GOTOOLCHAIN=go1.25.5 \
go build \
  -trimpath \
  -buildvcs=false \
  -ldflags="-s -w -X main.version=v1.0.0 -X main.revision=$revision" \
  -o dist/linux-amd64/slentore \
  ./cmd/slentore

CGO_ENABLED=0 \
GOOS=linux \
GOARCH=amd64 \
GOTOOLCHAIN=go1.25.5 \
go build \
  -trimpath \
  -buildvcs=false \
  -ldflags="-s -w" \
  -o dist/linux-amd64/slentore-fake-server \
  ./cmd/slentore-fake-server

dist/linux-amd64/slentore version
go version -m dist/linux-amd64/slentore
shasum -a 256 dist/linux-amd64/slentore dist/linux-amd64/slentore-fake-server
```

Repeat with explicit `GOOS`/`GOARCH` values for every distributed target. Do
not tag from a dirty checkout or replace the injected revision with a branch
name.
