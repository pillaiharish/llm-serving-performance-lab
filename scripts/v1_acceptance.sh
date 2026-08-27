#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  echo "Usage: scripts/v1_acceptance.sh [--fixture]" >&2
}

fixture=false
if [[ $# -gt 1 ]]; then
  usage
  exit 2
fi
if [[ $# -eq 1 ]]; then
  if [[ $1 != "--fixture" ]]; then
    usage
    exit 2
  fi
  fixture=true
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
build_dir=$(mktemp -d)
fake_pid=""

cleanup() {
  if [[ -n $fake_pid ]]; then
    kill "$fake_pid" 2>/dev/null || true
    wait "$fake_pid" 2>/dev/null || true
  fi
  if [[ -d $build_dir ]]; then
    find "$build_dir" -depth -delete 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

if [[ $fixture == false ]]; then
  for required_name in SLENTORE_BASE_URL SLENTORE_MODEL SLENTORE_TOKENIZER_URL; do
    if [[ -z ${!required_name:-} ]]; then
      echo "error: $required_name is required" >&2
      exit 2
    fi
  done
fi

if [[ -n ${SLENTORE_ACCEPTANCE_DIR:-} ]]; then
  acceptance_dir=$SLENTORE_ACCEPTANCE_DIR
  if [[ -e $acceptance_dir ]]; then
    echo "error: SLENTORE_ACCEPTANCE_DIR already exists: $acceptance_dir" >&2
    exit 2
  fi
  mkdir -p "$(dirname "$acceptance_dir")"
  mkdir "$acceptance_dir"
else
  mkdir -p "$repo_root/runs/v1-acceptance"
  short_sha=$(git -C "$repo_root" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  acceptance_dir=$(mktemp -d "$repo_root/runs/v1-acceptance/${timestamp}-${short_sha}-XXXXXX")
fi
mkdir -p "$acceptance_dir/logs"

toolchain=${SLENTORE_GO_TOOLCHAIN:-go1.25.5}
if [[ -n ${SLENTORE_BIN:-} ]]; then
  slentore_bin=$SLENTORE_BIN
  if [[ ! -x $slentore_bin ]]; then
    echo "error: SLENTORE_BIN is not executable: $slentore_bin" >&2
    exit 2
  fi
else
  slentore_bin="$build_dir/slentore"
  (cd "$repo_root" && GOTOOLCHAIN="$toolchain" go build -trimpath -o "$slentore_bin" ./cmd/slentore)
fi

api_key_env=${SLENTORE_API_KEY_ENV:-}
api_key_args=(--api-key-env "$api_key_env")
sentinel_prompt="Slentore V1 acceptance sentinel."

if [[ $fixture == true ]]; then
  fake_bin="$build_dir/slentore-fake-server"
  (cd "$repo_root" && GOTOOLCHAIN="$toolchain" go build -trimpath -o "$fake_bin" ./cmd/slentore-fake-server)
  "$fake_bin" \
    --listen 127.0.0.1:0 \
    --header-delay 0 \
    --first-content-delay 0 \
    --chunk-interval 0 \
    --usage-delay 0 \
    --done-delay 0 \
    --token-evidence singleton \
    --tokenizer-fixture \
    >"$acceptance_dir/logs/fake-server.log" 2>&1 &
  fake_pid=$!
  fake_address=""
  for _ in $(seq 1 200); do
    fake_address=$(awk '/^listen:/{print $2; exit}' "$acceptance_dir/logs/fake-server.log" 2>/dev/null || true)
    if [[ -n $fake_address ]]; then
      if python3 - "$fake_address" <<'PY'
import socket, sys
host, port = sys.argv[1].rsplit(":", 1)
with socket.create_connection((host, int(port)), timeout=0.2):
    pass
PY
      then
        break
      fi
    fi
    sleep 0.025
  done
  if [[ -z $fake_address ]]; then
    echo "error: fixture fake server did not become ready" >&2
    exit 1
  fi
  SLENTORE_BASE_URL="http://$fake_address/v1"
  SLENTORE_MODEL="fixture-model"
  SLENTORE_TOKENIZER_URL="http://$fake_address/tokenize"
  api_key_env=""
  api_key_args=(--api-key-env "")
fi

basic_requests=${SLENTORE_BASIC_REQUESTS:-5}
basic_warmup=${SLENTORE_BASIC_WARMUP:-2}
basic_output=${SLENTORE_BASIC_MAX_OUTPUT_TOKENS:-32}
closed_values=${SLENTORE_CLOSED_CONCURRENCY_VALUES:-1,4,8}
closed_requests=${SLENTORE_CLOSED_REQUESTS:-16}
closed_warmup=${SLENTORE_CLOSED_WARMUP:-2}
open_values=${SLENTORE_OPEN_RATE_VALUES:-2,5,10}
open_duration=${SLENTORE_OPEN_DURATION:-2s}
open_in_flight=${SLENTORE_OPEN_MAX_IN_FLIGHT:-16}
open_warmup=${SLENTORE_OPEN_WARMUP:-2}
token_inputs=${SLENTORE_TOKEN_INPUT_VALUES:-128,512}
token_outputs=${SLENTORE_TOKEN_OUTPUT_VALUES:-32,64}
token_requests=${SLENTORE_TOKEN_REQUESTS:-2}
token_warmup=${SLENTORE_TOKEN_WARMUP:-1}

run_scenario() {
  local name=$1
  shift
  set +e
  "$@" >"$acceptance_dir/logs/${name}.stdout.log" 2>"$acceptance_dir/logs/${name}.stderr.log"
  scenario_exit=$?
  set -e
}

run_scenario basic "$slentore_bin" bench \
  --base-url "$SLENTORE_BASE_URL" --model "$SLENTORE_MODEL" "${api_key_args[@]}" \
  --prompt "$sentinel_prompt" --max-output-tokens "$basic_output" --mode closed-loop --concurrency 1 \
  --requests "$basic_requests" --warmup-requests "$basic_warmup" --token-timing vllm \
  --slo-ttft 10s --slo-tpot 1s --slo-e2e 60s --output-dir "$acceptance_dir/basic"
basic_exit=$scenario_exit

run_scenario closed-loop "$slentore_bin" sweep \
  --base-url "$SLENTORE_BASE_URL" --model "$SLENTORE_MODEL" "${api_key_args[@]}" \
  --prompt "$sentinel_prompt" --max-output-tokens 32 --mode closed-loop \
  --concurrency-values "$closed_values" --requests "$closed_requests" --warmup-requests "$closed_warmup" \
  --output-dir "$acceptance_dir/closed-loop"
closed_exit=$scenario_exit

run_scenario open-loop "$slentore_bin" sweep \
  --base-url "$SLENTORE_BASE_URL" --model "$SLENTORE_MODEL" "${api_key_args[@]}" \
  --prompt "$sentinel_prompt" --max-output-tokens 32 --mode open-loop \
  --request-rate-values "$open_values" --duration "$open_duration" --max-in-flight "$open_in_flight" \
  --warmup-requests "$open_warmup" --output-dir "$acceptance_dir/open-loop"
open_exit=$scenario_exit

run_scenario token-length "$slentore_bin" sweep \
  --base-url "$SLENTORE_BASE_URL" --model "$SLENTORE_MODEL" "${api_key_args[@]}" \
  --workload-mode token-length --tokenizer-adapter vllm --tokenizer-url "$SLENTORE_TOKENIZER_URL" \
  --input-token-values "$token_inputs" --output-token-values "$token_outputs" --mode closed-loop --concurrency 1 \
  --requests "$token_requests" --warmup-requests "$token_warmup" --output-dir "$acceptance_dir/token-length"
token_exit=$scenario_exit

slentore_version=$($slentore_bin version)
git_sha=$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo unknown)

ACCEPTANCE_DIR="$acceptance_dir" \
ACCEPTANCE_FIXTURE="$fixture" \
ACCEPTANCE_GIT_SHA="$git_sha" \
ACCEPTANCE_VERSION="$slentore_version" \
ACCEPTANCE_BASE_URL="$SLENTORE_BASE_URL" \
ACCEPTANCE_MODEL="$SLENTORE_MODEL" \
ACCEPTANCE_TOKENIZER_URL="$SLENTORE_TOKENIZER_URL" \
ACCEPTANCE_API_KEY_ENV="$api_key_env" \
ACCEPTANCE_BASIC_EXIT="$basic_exit" \
ACCEPTANCE_CLOSED_EXIT="$closed_exit" \
ACCEPTANCE_OPEN_EXIT="$open_exit" \
ACCEPTANCE_TOKEN_EXIT="$token_exit" \
ACCEPTANCE_BASIC_SETTINGS="$basic_requests,$basic_warmup,$basic_output" \
ACCEPTANCE_CLOSED_SETTINGS="$closed_values|$closed_requests|$closed_warmup" \
ACCEPTANCE_OPEN_SETTINGS="$open_values|$open_duration|$open_in_flight|$open_warmup" \
ACCEPTANCE_TOKEN_SETTINGS="$token_inputs|$token_outputs|$token_requests|$token_warmup" \
python3 - <<'PY'
import datetime, json, os, pathlib
root = pathlib.Path(os.environ["ACCEPTANCE_DIR"])
payload = {
    "acceptance_schema_version": 1,
    "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "fixture": os.environ["ACCEPTANCE_FIXTURE"] == "true",
    "slentore_git_sha": os.environ["ACCEPTANCE_GIT_SHA"],
    "slentore_version_output": os.environ["ACCEPTANCE_VERSION"].splitlines(),
    "base_url": os.environ["ACCEPTANCE_BASE_URL"],
    "model": os.environ["ACCEPTANCE_MODEL"],
    "tokenizer_url": os.environ["ACCEPTANCE_TOKENIZER_URL"],
    "api_key_env": os.environ["ACCEPTANCE_API_KEY_ENV"],
    "scenarios": {
        "basic": {"path": "basic", "cli_exit": int(os.environ["ACCEPTANCE_BASIC_EXIT"]), "settings": os.environ["ACCEPTANCE_BASIC_SETTINGS"]},
        "closed_loop": {"path": "closed-loop", "cli_exit": int(os.environ["ACCEPTANCE_CLOSED_EXIT"]), "settings": os.environ["ACCEPTANCE_CLOSED_SETTINGS"]},
        "open_loop": {"path": "open-loop", "cli_exit": int(os.environ["ACCEPTANCE_OPEN_EXIT"]), "settings": os.environ["ACCEPTANCE_OPEN_SETTINGS"]},
        "token_length": {"path": "token-length", "cli_exit": int(os.environ["ACCEPTANCE_TOKEN_EXIT"]), "settings": os.environ["ACCEPTANCE_TOKEN_SETTINGS"]},
    },
}
temporary = root / ".acceptance.json.tmp"
temporary.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
temporary.replace(root / "acceptance.json")
PY

set +e
python3 "$script_dir/verify_v1_acceptance.py" --acceptance-dir "$acceptance_dir" --api-key-env "$api_key_env"
verify_exit=$?
set -e

echo "V1 acceptance artifacts: $acceptance_dir"
if [[ $verify_exit -ne 0 || $basic_exit -ne 0 || $closed_exit -ne 0 || $open_exit -ne 0 || $token_exit -ne 0 ]]; then
  exit 1
fi
exit 0
