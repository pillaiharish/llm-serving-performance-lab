#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: scripts/gpu_commission.sh OPTIONS

Required:
  --evidence-root PATH              New commissioning evidence root
  --slentore-repo PATH              Clean Slentore source checkout
  --expected-slentore-sha SHA       Exact expected git revision
  --slentore-bin PATH               Existing executable Slentore binary
  --base-url URL                    OpenAI base URL ending in /v1
  --model MODEL                     Expected served model
  --api-key-env NAME                Environment variable containing the API key
  --server-config FILE              Operator-captured server configuration JSON
  --expected-vllm-version VERSION
  --expected-dtype DTYPE
  --expected-tensor-parallel-size N
  --expected-kv-cache-dtype DTYPE
  --expected-max-model-len N
  --expected-max-num-seqs N
  --expected-gpu-memory-utilization FLOAT
  --expected-generation-config POLICY
  --expected-thinking true|false|not-applicable
  --expected-prefix-caching true|false
  --input-tokens N                  Exact rendered input-token target
  --max-output-tokens N             Requested output maximum

Optional:
  --tokenizer-url URL               Defaults to <server-root>/tokenize
  --metrics-url URL                 Defaults to <server-root>/metrics
  --requests N                      Measured smoke requests (default: 3)
  --warmup-requests N               Discarded benchmark requests (default: 1)
  --concurrency N                   Closed-loop concurrency (default: 1)
  --expected-actual-output-tokens N Require this server-reported output count per request
  --token-timing vllm|disabled      Default: vllm
  --provider-label TEXT             Optional metadata only
  --telemetry-interval SECONDS      Default: 1
EOF
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
helper="$script_dir/gpu_commission_validate.py"
evidence_root="" slentore_repo="" expected_sha="" slentore_bin="" base_url="" model="" api_key_env=""
server_config="" expected_vllm="" expected_dtype="" expected_tp="" expected_kv_dtype="" expected_max_len=""
expected_max_seqs="" expected_gpu_util="" expected_generation_config="" expected_thinking="" expected_prefix=""
input_tokens="" max_output="" expected_actual_output="" tokenizer_url="" metrics_url="" provider_label=""
requests=3 warmup=1 concurrency=1 token_timing=vllm telemetry_interval=1

need_value() { [[ $# -ge 2 && -n $2 ]] || { echo "error: $1 requires a value" >&2; exit 2; }; }
while [[ $# -gt 0 ]]; do
  case $1 in
    --evidence-root) need_value "$@"; evidence_root=$2; shift 2 ;;
    --slentore-repo) need_value "$@"; slentore_repo=$2; shift 2 ;;
    --expected-slentore-sha) need_value "$@"; expected_sha=$2; shift 2 ;;
    --slentore-bin) need_value "$@"; slentore_bin=$2; shift 2 ;;
    --base-url) need_value "$@"; base_url=${2%/}; shift 2 ;;
    --model) need_value "$@"; model=$2; shift 2 ;;
    --api-key-env) need_value "$@"; api_key_env=$2; shift 2 ;;
    --server-config) need_value "$@"; server_config=$2; shift 2 ;;
    --expected-vllm-version) need_value "$@"; expected_vllm=$2; shift 2 ;;
    --expected-dtype) need_value "$@"; expected_dtype=$2; shift 2 ;;
    --expected-tensor-parallel-size) need_value "$@"; expected_tp=$2; shift 2 ;;
    --expected-kv-cache-dtype) need_value "$@"; expected_kv_dtype=$2; shift 2 ;;
    --expected-max-model-len) need_value "$@"; expected_max_len=$2; shift 2 ;;
    --expected-max-num-seqs) need_value "$@"; expected_max_seqs=$2; shift 2 ;;
    --expected-gpu-memory-utilization) need_value "$@"; expected_gpu_util=$2; shift 2 ;;
    --expected-generation-config) need_value "$@"; expected_generation_config=$2; shift 2 ;;
    --expected-thinking) need_value "$@"; expected_thinking=$2; shift 2 ;;
    --expected-prefix-caching) need_value "$@"; expected_prefix=$2; shift 2 ;;
    --input-tokens) need_value "$@"; input_tokens=$2; shift 2 ;;
    --max-output-tokens) need_value "$@"; max_output=$2; shift 2 ;;
    --expected-actual-output-tokens) need_value "$@"; expected_actual_output=$2; shift 2 ;;
    --tokenizer-url) need_value "$@"; tokenizer_url=$2; shift 2 ;;
    --metrics-url) need_value "$@"; metrics_url=$2; shift 2 ;;
    --requests) need_value "$@"; requests=$2; shift 2 ;;
    --warmup-requests) need_value "$@"; warmup=$2; shift 2 ;;
    --concurrency) need_value "$@"; concurrency=$2; shift 2 ;;
    --token-timing) need_value "$@"; token_timing=$2; shift 2 ;;
    --provider-label) need_value "$@"; provider_label=$2; shift 2 ;;
    --telemetry-interval) need_value "$@"; telemetry_interval=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

required=(evidence_root slentore_repo expected_sha slentore_bin base_url model api_key_env server_config expected_vllm expected_dtype expected_tp expected_kv_dtype expected_max_len expected_max_seqs expected_gpu_util expected_generation_config expected_thinking expected_prefix input_tokens max_output)
for name in "${required[@]}"; do
  [[ -n ${!name} ]] || { echo "error: --${name//_/-} is required" >&2; exit 2; }
done
[[ $base_url == */v1 ]] || { echo "error: --base-url must end in /v1" >&2; exit 2; }
[[ $expected_prefix == true || $expected_prefix == false ]] || { echo "error: --expected-prefix-caching must be true or false" >&2; exit 2; }
[[ $expected_thinking == true || $expected_thinking == false || $expected_thinking == not-applicable ]] || { echo "error: invalid --expected-thinking" >&2; exit 2; }
[[ $token_timing == vllm || $token_timing == disabled ]] || { echo "error: --token-timing must be vllm or disabled" >&2; exit 2; }
for value_name in expected_tp expected_max_len expected_max_seqs input_tokens max_output requests concurrency; do
  [[ ${!value_name} =~ ^[1-9][0-9]*$ ]] || { echo "error: $value_name must be a positive integer" >&2; exit 2; }
done
[[ $warmup =~ ^[0-9]+$ ]] || { echo "error: warmup must be a nonnegative integer" >&2; exit 2; }
if [[ -n $expected_actual_output ]]; then [[ $expected_actual_output =~ ^[0-9]+$ ]] || { echo "error: expected actual output must be a nonnegative integer" >&2; exit 2; }; fi
[[ -x $helper ]] || { echo "error: validation helper is not executable: $helper" >&2; exit 2; }
[[ ! -e $evidence_root ]] || { echo "error: evidence root already exists: $evidence_root" >&2; exit 2; }
[[ -n ${!api_key_env:-} ]] || { echo "error: API-key environment variable $api_key_env is unset or empty" >&2; exit 2; }

server_root=${base_url%/v1}
tokenizer_url=${tokenizer_url:-$server_root/tokenize}
metrics_url=${metrics_url:-$server_root/metrics}
attempt_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
attempt="$evidence_root/attempts/$attempt_id"
mkdir -p "$attempt"/{environment,server,smoke,telemetry}
current_gate=initialization
telemetry_pid="" private_response="$attempt/.private-response.json"

finish() {
  status=$?
  trap - EXIT INT TERM
  if [[ -n $telemetry_pid ]] && kill -0 "$telemetry_pid" 2>/dev/null; then
    kill "$telemetry_pid" 2>/dev/null || true
    wait "$telemetry_pid" 2>/dev/null || true
  fi
  rm -f "$private_response"
  if [[ $status -ne 0 && -d $attempt ]]; then
    VALIDATION_PATH="$attempt/validation.json" VALIDATION_GATE="$current_gate" VALIDATION_EXIT="$status" python3 - <<'PY'
import datetime, json, os, pathlib
path = pathlib.Path(os.environ["VALIDATION_PATH"])
path.write_text(json.dumps({
    "commissioning_schema_version": 1,
    "status": "failed",
    "failed_gate": os.environ["VALIDATION_GATE"],
    "exit_code": int(os.environ["VALIDATION_EXIT"]),
    "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
    echo "Commissioning failed at gate '$current_gate'; evidence preserved: $attempt" >&2
  fi
  exit "$status"
}
trap finish EXIT INT TERM

auth_curl() {
  output=$1; shift
  printf 'header = "Authorization: Bearer %s"\n' "${!api_key_env}" |
    curl --silent --show-error --fail-with-body --config - --output "$output" "$@"
}

current_gate=contract
CONTRACT_PATH="$attempt/contract.json" CONTRACT_CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
CONTRACT_PROVIDER="$provider_label" CONTRACT_SHA="$expected_sha" CONTRACT_BASE="$base_url" CONTRACT_MODEL="$model" \
CONTRACT_API_ENV="$api_key_env" CONTRACT_PREFIX="$expected_prefix" CONTRACT_INPUT="$input_tokens" CONTRACT_MAX_OUTPUT="$max_output" \
CONTRACT_ACTUAL_OUTPUT="$expected_actual_output" CONTRACT_REQUESTS="$requests" CONTRACT_WARMUP="$warmup" CONTRACT_CONCURRENCY="$concurrency" \
CONTRACT_TIMING="$token_timing" python3 - <<'PY'
import json, os, pathlib
integer_or_none = lambda value: int(value) if value else None
value = {
    "commissioning_schema_version": 1, "created_at": os.environ["CONTRACT_CREATED"],
    "provider_label": os.environ["CONTRACT_PROVIDER"] or None,
    "slentore_expected_sha": os.environ["CONTRACT_SHA"], "base_url": os.environ["CONTRACT_BASE"],
    "model": os.environ["CONTRACT_MODEL"], "api_key_env": os.environ["CONTRACT_API_ENV"],
    "expected_prefix_caching": os.environ["CONTRACT_PREFIX"] == "true",
    "token_contract": {"target_input_tokens": int(os.environ["CONTRACT_INPUT"]), "requested_max_output_tokens": int(os.environ["CONTRACT_MAX_OUTPUT"]), "expected_actual_output_tokens": integer_or_none(os.environ["CONTRACT_ACTUAL_OUTPUT"])},
    "smoke": {"mode": "closed_loop", "concurrency": int(os.environ["CONTRACT_CONCURRENCY"]), "warmup_requests": int(os.environ["CONTRACT_WARMUP"]), "measured_requests": int(os.environ["CONTRACT_REQUESTS"]), "token_timing": os.environ["CONTRACT_TIMING"]},
}
path = pathlib.Path(os.environ["CONTRACT_PATH"]); path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

current_gate=slentore_identity
[[ -d $slentore_repo/.git || -f $slentore_repo/.git ]] || { echo "error: Slentore checkout is not a git worktree" >&2; false; }
observed_sha=$(git -C "$slentore_repo" rev-parse HEAD)
[[ $observed_sha == "$expected_sha" ]] || { echo "error: Slentore HEAD is $observed_sha, expected $expected_sha" >&2; false; }
[[ -z $(git -C "$slentore_repo" status --porcelain) ]] || { echo "error: Slentore checkout is dirty" >&2; false; }
[[ -x $slentore_bin ]] || { echo "error: Slentore binary is missing or not executable: $slentore_bin" >&2; false; }
"$slentore_bin" version >"$attempt/environment/slentore-version.txt"
grep -Fq "$expected_sha" "$attempt/environment/slentore-version.txt" || { echo "error: Slentore version output does not identify expected SHA" >&2; false; }
shasum -a 256 "$slentore_bin" >"$attempt/environment/slentore-binary.sha256"

current_gate=environment
date -u +%Y-%m-%dT%H:%M:%SZ >"$attempt/environment/utc.txt"
uname -a >"$attempt/environment/kernel.txt"
{ [[ -r /etc/os-release ]] && cat /etc/os-release || sw_vers 2>/dev/null || true; } >"$attempt/environment/os.txt"
{ lscpu 2>/dev/null || sysctl -n machdep.cpu.brand_string 2>/dev/null || true; } >"$attempt/environment/cpu.txt"
df -Pk "$evidence_root" >"$attempt/environment/disk.txt"
python3 --version >"$attempt/environment/python.txt" 2>&1
python3 - <<'PY' >"$attempt/environment/python-packages.json"
import importlib.metadata, json
names = ["torch", "vllm", "transformers", "tokenizers"]
versions = {}
for name in names:
    try: versions[name] = importlib.metadata.version(name)
    except importlib.metadata.PackageNotFoundError: versions[name] = None
try:
    import torch
    versions["torch_cuda_build"] = torch.version.cuda
except Exception:
    versions["torch_cuda_build"] = None
print(json.dumps(versions, indent=2, sort_keys=True))
PY
command -v nvidia-smi >/dev/null || { echo "error: nvidia-smi is required" >&2; false; }
nvidia-smi >"$attempt/environment/nvidia-smi.txt"
nvidia-smi --query-gpu=index,name,memory.total,uuid,driver_version,power.limit --format=csv >"$attempt/environment/gpus.csv"

current_gate=server_configuration
cp "$server_config" "$attempt/server/config.json"
"$helper" server-config --file "$attempt/server/config.json" --model "$model" --vllm-version "$expected_vllm" \
  --dtype "$expected_dtype" --tensor-parallel-size "$expected_tp" --kv-cache-dtype "$expected_kv_dtype" \
  --max-model-len "$expected_max_len" --max-num-seqs "$expected_max_seqs" --gpu-memory-utilization "$expected_gpu_util" \
  --generation-config "$expected_generation_config" --thinking "$expected_thinking" --prefix-caching "$expected_prefix" \
  >"$attempt/server/config-validation.json"

current_gate=health
curl --silent --show-error --fail-with-body --output "$attempt/server/health.txt" "$server_root/health"

current_gate=authenticated_models
auth_curl "$attempt/server/models.json" "$base_url/models"
"$helper" models --file "$attempt/server/models.json" --model "$model" --max-model-len "$expected_max_len" >"$attempt/server/models-validation.json"

current_gate=tokenizer
auth_curl "$private_response" -H 'Content-Type: application/json' --data '{"prompt":"Slentore commissioning tokenizer probe.","add_special_tokens":false}' "$tokenizer_url"
"$helper" tokenize --file "$private_response" >"$attempt/server/tokenizer-validation.json"
rm -f "$private_response"

current_gate=authenticated_generation
GENERATION_MODEL="$model" python3 - <<'PY' >"$attempt/server/generation-request.json"
import json, os
print(json.dumps({"model": os.environ["GENERATION_MODEL"], "messages": [{"role": "user", "content": "Reply with OK."}], "max_tokens": 1, "temperature": 0, "stream": False}))
PY
auth_curl "$private_response" -H 'Content-Type: application/json' --data-binary @"$attempt/server/generation-request.json" "$base_url/chat/completions"
"$helper" generation --file "$private_response" --model "$model" >"$attempt/server/generation-validation.json"
rm -f "$private_response" "$attempt/server/generation-request.json"

current_gate=unauthenticated_rejection
unauth_status=$(curl --silent --output /dev/null --write-out '%{http_code}' -H 'Content-Type: application/json' \
  --data "{\"model\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$model"),\"messages\":[{\"role\":\"user\",\"content\":\"auth probe\"}],\"max_tokens\":1}" \
  "$base_url/chat/completions")
[[ $unauth_status == 401 || $unauth_status == 403 ]] || { echo "error: unauthenticated inference returned HTTP $unauth_status, expected 401 or 403" >&2; false; }
printf '{"unauthenticated_inference_http_status":%s,"rejected":true}\n' "$unauth_status" >"$attempt/server/auth-validation.json"

current_gate=runtime_cache_pre
curl --silent --show-error --fail-with-body --output "$attempt/server/metrics-before.prom" "$metrics_url"
"$helper" metrics --file "$attempt/server/metrics-before.prom" --expected-prefix "$expected_prefix" >"$attempt/server/cache-before-validation.json"

current_gate=telemetry_start
telemetry="$attempt/telemetry/nvidia-smi.csv"
printf '%s\n' 'timestamp,gpu_index,gpu_uuid,gpu_name,memory_used_mib,memory_total_mib,utilization_gpu_percent,power_draw_w,power_limit_w,temperature_c,sm_clock_mhz' >"$telemetry"
(
  while nvidia-smi --query-gpu=timestamp,index,uuid,name,memory.used,memory.total,utilization.gpu,power.draw,power.limit,temperature.gpu,clocks.sm --format=csv,noheader,nounits >>"$telemetry"; do
    sleep "$telemetry_interval"
  done
) >"$attempt/telemetry/collector.stdout.log" 2>"$attempt/telemetry/collector.stderr.log" &
telemetry_pid=$!
sleep 0.1
kill -0 "$telemetry_pid" 2>/dev/null || { echo "error: NVIDIA telemetry collector exited during startup" >&2; false; }
benchmark_started=$(python3 -c 'import time; print(time.time())')

current_gate=smoke_benchmark
slentore_args=(bench --base-url "$base_url" --model "$model" --api-key-env "$api_key_env" \
  --workload-mode token-length --input-tokens "$input_tokens" --tokenizer-adapter vllm --tokenizer-url "$tokenizer_url" \
  --max-output-tokens "$max_output" --temperature 0 --mode closed-loop --concurrency "$concurrency" \
  --requests "$requests" --warmup-requests "$warmup" --token-timing "$token_timing" --output-dir "$attempt/smoke")
set +e
"$slentore_bin" "${slentore_args[@]}" >"$attempt/smoke/slentore.stdout.log" 2>"$attempt/smoke/slentore.stderr.log"
benchmark_exit=$?
set -e
printf '%s\n' "$benchmark_exit" >"$attempt/smoke/exit-code.txt"
[[ $benchmark_exit -eq 0 ]] || { echo "error: Slentore smoke benchmark exited $benchmark_exit" >&2; false; }
benchmark_ended=$(python3 -c 'import time; print(time.time())')

current_gate=telemetry_stop
kill -0 "$telemetry_pid" 2>/dev/null || { echo "error: NVIDIA telemetry collector exited before clean termination" >&2; false; }
kill "$telemetry_pid" 2>/dev/null || true
wait "$telemetry_pid" 2>/dev/null || true
telemetry_pid=""
"$helper" telemetry --file "$telemetry" --started "$benchmark_started" --ended "$benchmark_ended" >"$attempt/telemetry/validation.json"

current_gate=smoke_validation
smoke_validate=("$helper" smoke --root "$attempt/smoke" --requests "$requests" --warmup "$warmup" --concurrency "$concurrency" \
  --input-tokens "$input_tokens" --max-output "$max_output" --token-timing "$([[ $token_timing == vllm ]] && echo true || echo false)")
if [[ -n $expected_actual_output ]]; then smoke_validate+=(--expected-output "$expected_actual_output"); fi
"${smoke_validate[@]}" >"$attempt/smoke/validation.json"

current_gate=runtime_cache_post
curl --silent --show-error --fail-with-body --output "$attempt/server/metrics-after.prom" "$metrics_url"
"$helper" metrics --file "$attempt/server/metrics-after.prom" --expected-prefix "$expected_prefix" >"$attempt/server/cache-after-validation.json"

current_gate=source_cleanliness
[[ $(git -C "$slentore_repo" rev-parse HEAD) == "$expected_sha" ]] || { echo "error: Slentore HEAD changed during commissioning" >&2; false; }
[[ -z $(git -C "$slentore_repo" status --porcelain) ]] || { echo "error: Slentore checkout became dirty during commissioning" >&2; false; }

current_gate=redaction
"$helper" redact --root "$attempt" --api-key-env "$api_key_env" >"$attempt/redaction.json"

current_gate=finalization
VALIDATION_PATH="$attempt/validation.json" python3 - <<'PY'
import datetime, json, os, pathlib
path = pathlib.Path(os.environ["VALIDATION_PATH"])
path.write_text(json.dumps({"commissioning_schema_version": 1, "status": "passed", "completed_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
"$helper" manifest --root "$attempt" --output "$attempt/manifest.json" >/dev/null
mv "$attempt" "$evidence_root/commissioning"
archive="${evidence_root}.tar.gz"
tar -czf "$archive" -C "$(dirname "$evidence_root")" "$(basename "$evidence_root")"
(cd "$(dirname "$archive")" && shasum -a 256 "$(basename "$archive")") >"${archive}.sha256"
"$helper" redact --root "$evidence_root/commissioning" --api-key-env "$api_key_env" >/dev/null

trap - EXIT INT TERM
echo "Commissioning passed: $evidence_root/commissioning"
echo "Archive: $archive"
echo "Checksum: ${archive}.sha256"
