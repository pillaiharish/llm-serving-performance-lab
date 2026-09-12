# H100 single-GPU baseline runbook

Use this checklist for a **new experiment**. Do not add these runs as rep2 or
rep3 of the incomplete 2026-09-06 experiment. Run all commands on the GPU host
in Bash unless a section says to run them locally.

All three repetitions must use one continuously running vLLM process. If vLLM
is restarted or its configuration changes, stop: the repetitions no longer
belong to the same experiment.

## PRE-FLIGHT

Set values for the new rental. Keep the experiment ID unique and do not reuse a
historical directory. Keep one Bash session for the server and all three reps,
and do not enable shell tracing (`set -x`) while a secret is in the environment.

```bash
umask 077
export MODEL='Qwen/Qwen3.8-27B'
export VLLM_API_KEY='replace-with-the-rental-secret'
export VLLM_ROOT_URL='http://127.0.0.1:8000'
export VLLM_API_ROOT="${VLLM_ROOT_URL}/v1"
export TOKENIZER_URL="${VLLM_ROOT_URL}/tokenize"
export SLENTORE_REPO='/workspace/llm-serving-performance-lab'
export SLENTORE_BIN="${SLENTORE_REPO}/build/slentore"
export EXPERIMENT_ID="h100-qwen38-single-gpu-c1-$(date -u +%Y%m%dT%H%M%SZ)"
export EXPERIMENT_ROOT="/workspace/experiments/${EXPERIMENT_ID}"
export ARCHIVE_DIR='/workspace/archives'

test -n "${VLLM_API_KEY:-}" || { echo 'VLLM_API_KEY is unset or empty' >&2; exit 1; }
echo 'VLLM_API_KEY is configured (value not printed)'

mkdir -p "$EXPERIMENT_ROOT"/{environment,server,prometheus,telemetry,logs,slentore}
mkdir -p "$ARCHIVE_DIR"
```

Capture and inspect the GPU identity. It must report one H100 SXM with about
80 GB of memory; stop if it does not.

```bash
nvidia-smi --query-gpu=index,name,uuid,memory.total,driver_version \
  --format=csv | tee "$EXPERIMENT_ROOT/environment/gpu-identity.csv"
nvidia-smi | tee "$EXPERIMENT_ROOT/environment/nvidia-smi.txt"
nvidia-smi -q | tee "$EXPERIMENT_ROOT/environment/nvidia-smi-query.txt"
test "$(nvidia-smi --query-gpu=index --format=csv,noheader | wc -l)" -eq 1 || exit 1
```

Capture software and Slentore identity before starting the benchmark. A dirty
Slentore checkout must be explained or replaced with a clean, identified build.

```bash
{
  date -u +'%Y-%m-%dT%H:%M:%SZ'
  uname -a
  python3 --version
  python3 -c 'import torch; print("torch", torch.__version__, "cuda", torch.version.cuda)'
  python3 -c 'import vllm; print("vllm", vllm.__version__)'
  command -v vllm
  command -v nvidia-smi
} > "$EXPERIMENT_ROOT/environment/software-versions.txt" 2>&1
python3 -m pip freeze > "$EXPERIMENT_ROOT/environment/python-packages.txt"

git -C "$SLENTORE_REPO" rev-parse HEAD \
  > "$EXPERIMENT_ROOT/environment/slentore-git-sha.txt"
git -C "$SLENTORE_REPO" status --short \
  > "$EXPERIMENT_ROOT/environment/slentore-git-status.txt"
"$SLENTORE_BIN" version \
  > "$EXPERIMENT_ROOT/environment/slentore-version.txt"
sha256sum "$SLENTORE_BIN" \
  > "$EXPERIMENT_ROOT/environment/slentore-binary.sha256"
df -h "$EXPERIMENT_ROOT" | tee "$EXPERIMENT_ROOT/environment/disk-space.txt"
```

Confirm there is enough free space for request artifacts, telemetry, logs, the
uncompressed experiment, and a second archived copy. Stop if space is marginal.

## SERVER EVIDENCE

Record the serving configuration without recording the API key. This is the
configuration contract for every repetition.

```bash
cat > "$EXPERIMENT_ROOT/server/serving-config.txt" <<EOF
model=$MODEL
tensor_parallel_size=1
world_size=1
dtype=bfloat16
kv_cache_dtype=bfloat16
max_model_len=32768
gpu_memory_utilization=0.90
max_num_seqs=16
prefix_caching=false
api_authentication=enabled
host=127.0.0.1
port=8000
EOF

cat > "$EXPERIMENT_ROOT/server/vllm-command-redacted.txt" <<EOF
VLLM_API_KEY=<SET_AND_REDACTED> vllm serve $MODEL --host 127.0.0.1 --port 8000 --tensor-parallel-size 1 --dtype bfloat16 --kv-cache-dtype bfloat16 --max-model-len 32768 --gpu-memory-utilization 0.90 --max-num-seqs 16 --no-enable-prefix-caching
EOF

nohup vllm serve "$MODEL" \
  --host 127.0.0.1 \
  --port 8000 \
  --tensor-parallel-size 1 \
  --dtype bfloat16 \
  --kv-cache-dtype bfloat16 \
  --max-model-len 32768 \
  --gpu-memory-utilization 0.90 \
  --max-num-seqs 16 \
  --no-enable-prefix-caching \
  > "$EXPERIMENT_ROOT/server/vllm-startup.log" 2>&1 &
VLLM_PID=$!
printf '%s\n' "$VLLM_PID" > "$EXPERIMENT_ROOT/server/vllm.pid"
```

Wait for startup to finish, inspect the complete startup log, and verify the
authenticated endpoint. These commands fail without printing the key.

```bash
UNAUTH_STATUS=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  "$VLLM_API_ROOT/models")
printf '%s\n' "$UNAUTH_STATUS" > "$EXPERIMENT_ROOT/server/unauthenticated-models-status.txt"
case "$UNAUTH_STATUS" in 401|403) ;; *) echo 'API accepted an unauthenticated request' >&2; exit 1;; esac

curl --fail --silent --show-error \
  -H "Authorization: Bearer ${VLLM_API_KEY}" \
  "$VLLM_ROOT_URL/health" \
  -o "$EXPERIMENT_ROOT/server/health.txt"

curl --fail --silent --show-error \
  -H "Authorization: Bearer ${VLLM_API_KEY}" \
  "$VLLM_API_ROOT/models" \
  -o "$EXPERIMENT_ROOT/server/models.json"

kill -0 "$VLLM_PID" || { echo 'vLLM is not running' >&2; exit 1; }
tail -n 100 "$EXPERIMENT_ROOT/server/vllm-startup.log"

python3 - "$EXPERIMENT_ROOT/server/models.json" <<'PY'
import json, os, sys
models = json.load(open(sys.argv[1]))["data"]
assert [item["id"] for item in models] == [os.environ["MODEL"]]
PY
```

Check that `/v1/models` exposes exactly the intended model and that the startup
log agrees with every value in `serving-config.txt`. Do not continue on a
mismatch.

## PER-REPETITION PROCEDURE

Run this procedure with `REP=1`, then `REP=2`, then `REP=3`. Do not restart
vLLM, alter its flags, change `MODEL`, change Slentore, or change the workload
between repetitions.

### 1. Capture telemetry and pre-run metrics

```bash
export REP=1                         # then 2, then 3
export REP_ROOT="$EXPERIMENT_ROOT/slentore/c1-512-128-rep${REP}"
mkdir -p "$REP_ROOT"
kill -0 "$VLLM_PID" || { echo 'vLLM stopped or restarted' >&2; exit 1; }

nvidia-smi \
  --query-gpu=timestamp,utilization.gpu,memory.used,power.draw,clocks.current.sm \
  --format=csv -l 1 \
  > "$EXPERIMENT_ROOT/telemetry/c1-512-128-rep${REP}-gpu.csv" \
  2> "$EXPERIMENT_ROOT/telemetry/c1-512-128-rep${REP}-gpu.stderr" &
TELEMETRY_PID=$!

curl --fail --silent --show-error \
  -H "Authorization: Bearer ${VLLM_API_KEY}" \
  "$VLLM_ROOT_URL/metrics" \
  -o "$EXPERIMENT_ROOT/prometheus/c1-512-128-rep${REP}-before.prom"
PRE_METRICS_EXIT=$?

if (( PRE_METRICS_EXIT != 0 )); then
  kill "$TELEMETRY_PID"
  wait "$TELEMETRY_PID" 2>/dev/null
  echo "rep${REP}: pre-run metrics failed; stop" >&2
  exit 1
fi
```

Do not run Slentore without the pre-run snapshot.

### 2. Run the exact workload and capture its exit code

```bash
"$SLENTORE_BIN" bench \
  --base-url "$VLLM_API_ROOT" \
  --model "$MODEL" \
  --api-key-env VLLM_API_KEY \
  --workload-mode token-length \
  --input-tokens 512 \
  --tokenizer-adapter vllm \
  --tokenizer-url "$TOKENIZER_URL" \
  --max-output-tokens 128 \
  --temperature 0 \
  --token-timing vllm \
  --mode closed-loop \
  --concurrency 1 \
  --warmup-requests 10 \
  --requests 100 \
  --timeout 120s \
  --drain-timeout 120s \
  --output-dir "$REP_ROOT" \
  2>&1 | tee "$EXPERIMENT_ROOT/logs/c1-512-128-rep${REP}-slentore.log"
SLENTORE_EXIT=${PIPESTATUS[0]}
printf '%s\n' "$SLENTORE_EXIT" \
  > "$EXPERIMENT_ROOT/logs/c1-512-128-rep${REP}-slentore.exit-code"
```

### 3. Capture post-run evidence and stop telemetry

Always do this even if Slentore failed, so the failure has surrounding
evidence.

```bash
curl --fail --silent --show-error \
  -H "Authorization: Bearer ${VLLM_API_KEY}" \
  "$VLLM_ROOT_URL/metrics" \
  -o "$EXPERIMENT_ROOT/prometheus/c1-512-128-rep${REP}-after.prom"
POST_METRICS_EXIT=$?

kill "$TELEMETRY_PID"
wait "$TELEMETRY_PID" 2>/dev/null
```

### 4. Validate the repetition

Resolve the one Slentore-created run directory and validate the persisted
summary and token-timing evidence. The script prints the artifact path only
after every assertion passes.

```bash
python3 - "$REP_ROOT" "$REP" <<'PY'
import json, pathlib, sys

rep_root, rep = pathlib.Path(sys.argv[1]), sys.argv[2]
runs = [p.parent for p in rep_root.glob("*/run.json")]
assert len(runs) == 1, f"rep{rep}: expected one run directory, found {len(runs)}"
root = runs[0]
run = json.loads((root / "run.json").read_text())
summary = json.loads((root / "summary.json").read_text())

assert run["run_status"] == summary["run_status"] == "completed"
assert summary["complete"] is True
assert all(run["warmup"][k] == 10 for k in ("requested", "attempted", "completed", "successful"))
assert run["warmup"]["failed"] == 0
assert all(run["measurement"][k] == 100 for k in ("requested", "attempted", "completed", "successful"))
assert run["measurement"]["failed"] == 0
assert run["workload"]["input"]["target_tokens"] == 512
assert run["workload"]["input"]["resolved_tokens"] == 512
assert run["workload"]["output"]["requested_max_tokens"] == 128
assert run["load"]["mode"] == "closed_loop"
assert run["load"]["closed_loop"]["requested_concurrency"] == 1
assert run["token_timing"]["mode"] == "vllm"

for phase, expected in (("warmup", 10), ("measured", 100)):
    metrics = sorted((root / phase / "requests").glob("*/metrics.json"))
    assert len(metrics) == expected, f"rep{rep}: {phase} metric count={len(metrics)}"
    for path in metrics:
        metric = json.loads(path.read_text())
        usage, itl = metric["token_usage"], metric["itl"]
        assert usage["available"] is True
        assert (usage["input_tokens"], usage["output_tokens"], usage["total_tokens"]) == (512, 128, 640)
        assert itl["available"] is True
        assert itl["source"] == "vllm_return_token_ids_client_receive"

print(root)
PY
VALIDATION_EXIT=$?

mapfile -t RUN_DIRS < <(find "$REP_ROOT" -mindepth 1 -maxdepth 1 -type d -print)
if (( ${#RUN_DIRS[@]} == 1 )); then
  printf '%s\n' "${RUN_DIRS[0]}" \
    > "$EXPERIMENT_ROOT/logs/c1-512-128-rep${REP}-artifact-path.txt"
fi

if (( SLENTORE_EXIT != 0 || POST_METRICS_EXIT != 0 || VALIDATION_EXIT != 0 )); then
  echo "rep${REP} failed; stop and preserve all evidence" >&2
  exit 1
fi
kill -0 "$VLLM_PID" || { echo 'vLLM stopped or restarted' >&2; exit 1; }
```

Do not proceed silently after a failed repetition. Diagnose and preserve it.
If remediation requires a vLLM restart, serving-configuration change, or
Slentore/workload change, start a new experiment ID and rerun all three reps.

## POST-RUN VALIDATION

After rep3, confirm the complete evidence set before stopping vLLM:

```bash
for REP in 1 2 3; do
  REP_ROOT="$EXPERIMENT_ROOT/slentore/c1-512-128-rep${REP}"
  test -d "$REP_ROOT" || exit 1
  test "$(find "$REP_ROOT" -mindepth 2 -maxdepth 2 -name run.json | wc -l)" -eq 1 || exit 1
  test "$(find "$REP_ROOT" -mindepth 2 -maxdepth 2 -name summary.json | wc -l)" -eq 1 || exit 1
  test "$(find "$REP_ROOT" -mindepth 2 -maxdepth 2 -name summary.csv | wc -l)" -eq 1 || exit 1
  test -s "$EXPERIMENT_ROOT/telemetry/c1-512-128-rep${REP}-gpu.csv" || exit 1
  test -s "$EXPERIMENT_ROOT/prometheus/c1-512-128-rep${REP}-before.prom" || exit 1
  test -s "$EXPERIMENT_ROOT/prometheus/c1-512-128-rep${REP}-after.prom" || exit 1
done

test -s "$EXPERIMENT_ROOT/environment/gpu-identity.csv" || exit 1
test -s "$EXPERIMENT_ROOT/environment/nvidia-smi.txt" || exit 1
test -s "$EXPERIMENT_ROOT/environment/software-versions.txt" || exit 1
test -s "$EXPERIMENT_ROOT/environment/python-packages.txt" || exit 1
test -s "$EXPERIMENT_ROOT/environment/slentore-git-sha.txt" || exit 1
test -s "$EXPERIMENT_ROOT/server/serving-config.txt" || exit 1
test -s "$EXPERIMENT_ROOT/server/vllm-startup.log" || exit 1
kill -0 "$VLLM_PID" || exit 1
echo 'post-run evidence check passed'
```

Once all three runs and post-run snapshots are complete, stop vLLM cleanly so
the startup/runtime log is closed before archiving:

```bash
kill -INT "$VLLM_PID"
wait "$VLLM_PID"
printf '%s\n' "$?" > "$EXPERIMENT_ROOT/server/vllm.exit-code"
```

## ARCHIVE

Create the archive outside the experiment tree, hash it by basename, and save
its complete member listing.

```bash
ARCHIVE="$ARCHIVE_DIR/${EXPERIMENT_ID}.tar.gz"
tar -czf "$ARCHIVE" \
  -C "$(dirname "$EXPERIMENT_ROOT")" "$(basename "$EXPERIMENT_ROOT")"

(
  cd "$ARCHIVE_DIR"
  sha256sum "$(basename "$ARCHIVE")" > "$(basename "$ARCHIVE").sha256"
  sha256sum -c "$(basename "$ARCHIVE").sha256"
  tar -tzf "$(basename "$ARCHIVE")" | tee "$(basename "$ARCHIVE").contents.txt"
)

for REP in 1 2 3; do
  tar -tzf "$ARCHIVE" | grep -q "/slentore/c1-512-128-rep${REP}/" || exit 1
done
```

## TRANSFER

Run these commands on the local machine. Use the rental's SSH destination and
remote archive path; no provider-specific address is assumed.

```bash
export REMOTE_HOST='user@host'
export REMOTE_ARCHIVE='/remote/path/to/h100-new-experiment.tar.gz'
export LOCAL_DEST='/local/path/to/llm-serving-performance-lab/incoming'
mkdir -p "$LOCAL_DEST"

scp "$REMOTE_HOST:$REMOTE_ARCHIVE" "$LOCAL_DEST/"
scp "$REMOTE_HOST:$REMOTE_ARCHIVE.sha256" "$LOCAL_DEST/"
cd "$LOCAL_DEST"
sha256sum -c "$(basename "$REMOTE_ARCHIVE").sha256"
tar -tzf "$(basename "$REMOTE_ARCHIVE")" > "$(basename "$REMOTE_ARCHIVE").local-contents.txt"

for REP in 1 2 3; do
  grep -q "/slentore/c1-512-128-rep${REP}/" \
    "$(basename "$REMOTE_ARCHIVE").local-contents.txt" || exit 1
done
```

On macOS, use `shasum -a 256 -c` instead of `sha256sum -c`. A successful local
checksum check proves the local archive equals the remotely hashed archive.

## CRITICAL TEARDOWN GATE

> # DO NOT DESTROY THE GPU UNTIL ALL ARE TRUE
>
> - [ ] rep1 captured
> - [ ] rep2 captured
> - [ ] rep3 captured
> - [ ] environment captured
> - [ ] startup log captured
> - [ ] GPU telemetry captured for all reps
> - [ ] vLLM metrics captured for all reps
> - [ ] archive created
> - [ ] archive SHA-256 created
> - [ ] archive copied locally
> - [ ] local SHA-256 verified against remote
> - [ ] archive contents checked locally

Only after every box is checked may the GPU instance be destroyed. The remote
instance is the only recoverable source until the local checksum and local
archive-content checks both pass; an SCP exit status alone is not evidence that
the archive arrived intact or contains all three repetitions.
