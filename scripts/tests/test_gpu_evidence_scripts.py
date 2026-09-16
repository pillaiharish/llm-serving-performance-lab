import contextlib
import csv
import datetime as dt
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import threading
import unittest


SCRIPTS = Path(__file__).resolve().parents[1]
SCRIPT = SCRIPTS / "gpu_commission.sh"
HELPER = SCRIPTS / "gpu_commission_validate.py"
SPEC = importlib.util.spec_from_file_location("gpu_commission_validate", HELPER)
verify = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(verify)
SECRET = "test-secret-sentinel-DO-NOT-WRITE"
MODEL = "fixture-model"


def write_json(path: Path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value) + "\n", encoding="utf-8")


class Handler(BaseHTTPRequestHandler):
    prefix = True
    models_enabled = True
    version = "0.26.0"

    def log_message(self, *_args):
        pass

    def send_value(self, status, value, content_type="application/json"):
        payload = value if isinstance(value, bytes) else json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def authenticated(self):
        return self.headers.get("Authorization") == f"Bearer {SECRET}"

    def do_GET(self):
        if self.path == "/health":
            self.send_value(200, b"ok", "text/plain")
        elif self.path == "/metrics":
            flag = "True" if self.prefix else "False"
            self.send_value(200, f'vllm:cache_config_info{{enable_prefix_caching="{flag}"}} 1\n'.encode(), "text/plain")
        elif self.path == "/v1/models" and self.authenticated() and self.models_enabled:
            self.send_value(200, {"data": [{"id": MODEL, "max_model_len": 4096}]})
        elif self.path == "/v1/models":
            self.send_value(401 if not self.authenticated() else 404, {"error": "unavailable"})
        elif self.path == "/version" and self.authenticated():
            self.send_value(200, {"version": self.version} if self.version is not None else {"unexpected": True})
        else:
            self.send_value(404, {"error": "not found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        if not self.authenticated():
            self.send_value(401, {"error": "unauthorized"})
        elif self.path == "/tokenize":
            self.send_value(200, {"count": 3, "tokens": [1, 2, 3], "max_model_len": 4096})
        elif self.path == "/v1/chat/completions":
            self.send_value(200, {"model": MODEL, "choices": [{"message": {"role": "assistant", "content": "OK"}}], "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}})
        else:
            self.send_value(404, {"error": "not found"})


@contextlib.contextmanager
def fake_server(*, prefix=True, models=True, version="0.26.0"):
    handler = type("ConfiguredHandler", (Handler,), {"prefix": prefix, "models_enabled": models, "version": version})
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        thread.join()
        server.server_close()


class HelperTests(unittest.TestCase):
    def test_model_and_server_configuration_contracts(self):
        self.assertEqual(verify.validate_models({"data": [{"id": MODEL, "max_model_len": 4096}]}, MODEL, 4096)["model"], MODEL)
        with self.assertRaises(verify.ValidationError):
            verify.validate_models({"data": [{"id": "wrong"}]}, MODEL, None)
        config = {"model": MODEL, "vllm_version": "0.26.0", "dtype": "bfloat16", "tensor_parallel_size": 1, "world_size": 1,
                  "kv_cache_dtype": "auto", "max_model_len": 4096, "max_num_seqs": 16, "gpu_memory_utilization": 0.9,
                  "generation_config": "vllm", "thinking": False, "prefix_caching": True}
        self.assertEqual(verify.validate_server_config(config, config)["world_size"], 1)
        divergent = dict(config, world_size=2)
        self.assertEqual(verify.validate_server_config(divergent, divergent)["world_size"], 2)
        with self.assertRaises(verify.ValidationError):
            verify.validate_server_config(dict(config, api_key=SECRET), config)

    def test_live_version_and_gpu_contracts(self):
        self.assertEqual(verify.validate_version({"version": "0.26.0"}, "0.26.0")["observed_vllm_version"], "0.26.0")
        with self.assertRaises(verify.ValidationError):
            verify.validate_version({"version": "0.25.0"}, "0.26.0")
        with self.assertRaises(verify.ValidationError):
            verify.validate_version({"unexpected": True}, "0.26.0")
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "gpus.csv"
            path.write_text("gpu_index,gpu_name,memory_total_mib,gpu_uuid,driver_version,power_limit_w\n0,Fixture GPU,1000,GPU-fixture,999.0,300\n", encoding="utf-8")
            result = verify.validate_gpu(path, 1, "Fixture GPU", 1000)
            self.assertTrue(result["identity_asserted"])
            self.assertFalse(verify.validate_gpu(path, None, None, None)["identity_asserted"])
            for expected in ((2, "Fixture GPU", 1000), (1, "Wrong GPU", 1000), (1, "Fixture GPU", 2000)):
                with self.assertRaises(verify.ValidationError):
                    verify.validate_gpu(path, *expected)

    def test_token_and_generation_counts_are_separate(self):
        self.assertEqual(verify.validate_tokenize({"count": 3, "tokens": [1, 2, 3]}, 3)["probe_resolved_tokens"], 3)
        with self.assertRaises(verify.ValidationError):
            verify.validate_tokenize({"count": 2, "tokens": [1, 2]}, 3)
        result = verify.validate_generation({"model": MODEL, "choices": [{}], "usage": {"prompt_tokens": 5, "completion_tokens": 2}}, MODEL, 5, 2)
        self.assertEqual(result["actual_output_tokens"], 2)

    def test_prefix_cache_both_mismatch_directions_fail(self):
        enabled = 'vllm:cache_config_info{enable_prefix_caching="True"} 1\n'
        disabled = 'vllm:cache_config_info{enable_prefix_caching="False"} 1\n'
        self.assertTrue(verify.validate_metrics(enabled, True)["observed_prefix_caching"])
        self.assertFalse(verify.validate_metrics(disabled, False)["observed_prefix_caching"])
        with self.assertRaises(verify.ValidationError):
            verify.validate_metrics(enabled, False)
        with self.assertRaises(verify.ValidationError):
            verify.validate_metrics(disabled, True)

    def test_telemetry_header_only_malformed_and_valid(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "telemetry.csv"
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n", encoding="utf-8")
            now = dt.datetime.now(dt.timezone.utc).timestamp()
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now, now)
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\nmalformed\n", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now, now)
            stamp = dt.datetime.now().strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            row = [stamp, "0", "GPU-fixture", "Fixture GPU", "100", "1000", "50", "200", "300", "40", "1200"]
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n" + ",".join(row) + "\n", encoding="utf-8")
            result = verify.validate_telemetry(path, now - 1, now + 1)
            self.assertEqual(result["sample_count"], 1)
            self.assertEqual(result["samples_in_benchmark_window"], 1)
            before = dt.datetime.fromtimestamp(now - 10).strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n" + ",".join([before, *row[1:]]) + "\n", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now - 1, now + 1)
            stale = dt.datetime.fromtimestamp(now - 1000).strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n" + ",".join([stale, *row[1:]]) + "\n", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now - 1, now + 1)
            first = dt.datetime.fromtimestamp(now + 0.5).strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            second = dt.datetime.fromtimestamp(now).strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n" + ",".join([first, *row[1:]]) + "\n" + ",".join([second, *row[1:]]) + "\n", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now - 1, now + 1)
            after = dt.datetime.fromtimestamp(now + 10).strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
            path.write_text(",".join(verify.TELEMETRY_HEADER) + "\n" + ",".join([after, *row[1:]]) + "\n", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.validate_telemetry(path, now - 1, now + 1)

    def test_hash_sidecar_round_trip(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); source = root / "archive.tar.gz"; sidecar = root / "archive.tar.gz.sha256"
            source.write_bytes(b"portable hash fixture")
            result = verify.hash_file(source, sidecar)
            self.assertEqual(result["sha256"], hashlib.sha256(source.read_bytes()).hexdigest())
            self.assertTrue(verify.verify_hash(source, sidecar)["verified"])

    def test_redaction_rejects_exact_and_shaped_secrets(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            os.environ["TEST_KEY"] = SECRET
            (root / "safe.txt").write_text("safe", encoding="utf-8")
            self.assertEqual(verify.scan_secrets(root, "TEST_KEY")["secret_values_found"], 0)
            (root / "bad.txt").write_text(f"Authorization: Bearer {SECRET}", encoding="utf-8")
            with self.assertRaises(verify.ValidationError):
                verify.scan_secrets(root, "TEST_KEY")

    def test_missing_summary_and_token_mismatch_fail(self):
        with tempfile.TemporaryDirectory() as temporary:
            smoke = Path(temporary)
            (smoke / "run-1").mkdir()
            with self.assertRaises(verify.ValidationError):
                verify.validate_smoke(smoke, MODEL, 1, 0, 1, 8, 4, None, True)
            make_run(smoke, input_tokens=9)
            shutil.rmtree(smoke / "run-1")
            with self.assertRaises(verify.ValidationError):
                verify.validate_smoke(smoke, MODEL, 1, 0, 1, 8, 4, None, True)


def make_run(smoke: Path, *, requests=1, warmup=0, input_tokens=8, max_output=4, actual_output=2, summary=True):
    root = smoke / "run-fixture"
    warmup_status = "completed" if warmup else "skipped"
    run = {"schema_version": 7, "run_id": "run-fixture", "run_status": "completed", "model": MODEL, "temperature": 0,
           "warmup": {"status": warmup_status, "requested": warmup, "attempted": warmup, "completed": warmup, "successful": warmup, "failed": 0},
           "measurement": {"requested": requests, "attempted": requests, "completed": requests, "successful": requests, "failed": 0},
           "token_timing": {"mode": "vllm", "source": verify.ITL_SOURCE}}
    result = {"schema_version": 7, "run_id": "run-fixture", "run_status": "completed", "complete": True,
              "workload": {"input_target_tokens": 8, "input_resolved_tokens": 8, "requested_output_max_tokens": max_output},
              "load": {"mode": "closed_loop", "closed_loop": {"requested_concurrency": 1}},
              "counts": {"requested_or_planned": requests, "started": requests, "completed": requests, "successful": requests, "failed": 0}}
    write_json(root / "run.json", run)
    if summary:
        write_json(root / "summary.json", result)
        (root / "summary.csv").write_text("header\nrow\n", encoding="utf-8")
    for index in range(1, requests + 1):
        write_json(root / "measured" / "requests" / f"req-{index:06d}" / "metrics.json", {
            "token_usage": {"available": True, "input_tokens": input_tokens, "output_tokens": actual_output, "total_tokens": input_tokens + actual_output},
            "itl": {"available": True, "source": verify.ITL_SOURCE, "count": max(0, actual_output - 1), "values_ms": [1.0] * max(0, actual_output - 1)},
        })


class ScriptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        subprocess.run(["git", "init", "-q"], cwd=self.repo, check=True)
        subprocess.run(["git", "config", "user.email", "fixture@example.invalid"], cwd=self.repo, check=True)
        subprocess.run(["git", "config", "user.name", "Fixture"], cwd=self.repo, check=True)
        (self.repo / "source.txt").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["git", "add", "source.txt"], cwd=self.repo, check=True)
        subprocess.run(["git", "commit", "-qm", "fixture"], cwd=self.repo, check=True)
        self.sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.repo, text=True).strip()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.slentore = self.bin_dir / "slentore"
        self.nvidia_smi = self.bin_dir / "nvidia-smi"
        self.write_fake_slentore()
        self.write_fake_nvidia_smi()
        self.config = self.root / "server-config.json"
        write_json(self.config, {"model": MODEL, "vllm_version": "0.26.0", "dtype": "bfloat16", "tensor_parallel_size": 1, "world_size": 1,
                                 "kv_cache_dtype": "auto", "max_model_len": 4096, "max_num_seqs": 16, "gpu_memory_utilization": 0.9,
                                 "generation_config": "vllm", "thinking": False, "prefix_caching": True})

    def tearDown(self):
        self.temporary.cleanup()

    def write_fake_slentore(self):
        self.slentore.write_text(f'''#!/usr/bin/env python3
import json, os, pathlib, sys
if sys.argv[1:] == ["version"]:
    print("version: fixture\\nrevision: {self.sha}\\ngo: fixture")
    raise SystemExit(0)
args = sys.argv[1:]
def value(name): return args[args.index(name) + 1]
out = pathlib.Path(value("--output-dir")); out.mkdir(parents=True, exist_ok=True)
mode = os.environ.get("FAKE_SLENTORE_MODE", "success")
if mode == "exit1":
    (out / "partial.txt").write_text("preserved\\n")
    raise SystemExit(1)
requests = int(value("--requests")); warmup = int(value("--warmup-requests")); target = int(value("--input-tokens")); maximum = int(value("--max-output-tokens"))
import time; time.sleep(0.12)
root = out / "run-fixture"; root.mkdir()
warmup_status = "completed" if warmup else "skipped"
run = {{"schema_version":7,"run_id":"run-fixture","run_status":"completed","model":"{MODEL}","temperature":0,"warmup":{{"status":warmup_status,"requested":warmup,"attempted":warmup,"completed":warmup,"successful":warmup,"failed":0}},"measurement":{{"requested":requests,"attempted":requests,"completed":requests,"successful":requests,"failed":0}},"token_timing":{{"mode":"vllm","source":"{verify.ITL_SOURCE}"}}}}
summary = {{"schema_version":7,"run_id":"run-fixture","run_status":"completed","complete":True,"workload":{{"input_target_tokens":target,"input_resolved_tokens":target,"requested_output_max_tokens":maximum}},"load":{{"mode":"closed_loop","closed_loop":{{"requested_concurrency":int(value("--concurrency"))}}}},"counts":{{"requested_or_planned":requests,"started":requests,"completed":requests,"successful":requests,"failed":0}}}}
(root / "run.json").write_text(json.dumps(run))
if mode != "missing-summary":
    (root / "summary.json").write_text(json.dumps(summary)); (root / "summary.csv").write_text("header\\nrow\\n")
for number in range(1, requests + 1):
    path = root / "measured" / "requests" / f"req-{{number:06d}}"; path.mkdir(parents=True)
    observed_input = target + 1 if mode == "token-mismatch" else target
    metric = {{"token_usage":{{"available":True,"input_tokens":observed_input,"output_tokens":2,"total_tokens":observed_input+2}},"itl":{{"available":True,"source":"{verify.ITL_SOURCE}","count":1,"values_ms":[1.0]}}}}
    (path / "metrics.json").write_text(json.dumps(metric))
''', encoding="utf-8")
        self.slentore.chmod(0o755)

    def write_fake_nvidia_smi(self):
        self.nvidia_smi.write_text('''#!/usr/bin/env python3
import datetime, sys
args = " ".join(sys.argv[1:])
if "timestamp,index,uuid" in args:
    stamp = datetime.datetime.now().strftime("%Y/%m/%d %H:%M:%S.%f")[:-3]
    print(f"{stamp}, 0, GPU-fixture, Fixture GPU, 100, 1000, 50, 200, 300, 40, 1200")
elif "index,name,memory.total" in args:
    print("0, Fixture GPU, 1000, GPU-fixture, 999.0, 300")
else:
    print("Fixture NVIDIA SMI")
''', encoding="utf-8")
        self.nvidia_smi.chmod(0o755)

    def command(self, evidence: Path, base: str, *, model=MODEL, sha=None):
        return [str(SCRIPT), "--evidence-root", str(evidence), "--slentore-repo", str(self.repo),
                "--expected-slentore-sha", sha or self.sha, "--slentore-bin", str(self.slentore),
                "--base-url", base + "/v1", "--model", model, "--api-key-env", "TEST_API_KEY",
                "--server-config", str(self.config), "--expected-vllm-version", "0.26.0", "--expected-dtype", "bfloat16",
                "--expected-tensor-parallel-size", "1", "--expected-world-size", "1", "--expected-kv-cache-dtype", "auto", "--expected-max-model-len", "4096",
                "--expected-max-num-seqs", "16", "--expected-gpu-memory-utilization", "0.9", "--expected-generation-config", "vllm",
                "--expected-thinking", "false", "--expected-prefix-caching", "true", "--input-tokens", "8", "--max-output-tokens", "4",
                "--expected-actual-output-tokens", "2", "--expected-gpu-count", "1", "--expected-gpu-name", "Fixture GPU",
                "--expected-gpu-memory-mib", "1000", "--requests", "1", "--warmup-requests", "0", "--telemetry-interval", "0.05"]

    def run_command(self, command, **extra_env):
        env = os.environ.copy()
        env.update({"PATH": str(self.bin_dir) + os.pathsep + env["PATH"], "TEST_API_KEY": SECRET}, **extra_env)
        return subprocess.run(command, text=True, capture_output=True, env=env)

    def test_complete_authenticated_flow_archive_checksum_and_clean_source(self):
        evidence = self.root / "evidence"
        with fake_server() as base:
            result = self.run_command(self.command(evidence, base))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((evidence / "commissioning" / "validation.json").is_file())
        archive = Path(str(evidence) + ".tar.gz")
        expected = Path(str(archive) + ".sha256").read_text().split()[0]
        self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), expected)
        with tarfile.open(archive) as handle:
            names = handle.getnames()
            self.assertTrue(any(name.endswith("/commissioning/validation.json") for name in names))
            self.assertFalse(any("/attempts/" in name for name in names))
        canonical = json.loads((evidence / "commissioning" / "server" / "config.json").read_text())
        self.assertEqual(set(canonical), {"model", "vllm_version", "dtype", "tensor_parallel_size", "world_size", "kv_cache_dtype", "max_model_len", "max_num_seqs", "gpu_memory_utilization", "generation_config", "thinking", "prefix_caching"})
        self.assertEqual(subprocess.check_output(["git", "status", "--porcelain"], cwd=self.repo, text=True), "")
        for path in evidence.rglob("*"):
            if path.is_file():
                self.assertNotIn(SECRET.encode(), path.read_bytes(), path)

    def test_wrong_sha_dirty_checkout_and_missing_binary_fail(self):
        with fake_server() as base:
            wrong = self.run_command(self.command(self.root / "wrong", base, sha="0" * 40))
            self.assertNotEqual(wrong.returncode, 0)
            (self.repo / "dirty.txt").write_text("dirty", encoding="utf-8")
            dirty = self.run_command(self.command(self.root / "dirty", base))
            self.assertNotEqual(dirty.returncode, 0)
            (self.repo / "dirty.txt").unlink()
            self.slentore.chmod(0o644)
            missing = self.run_command(self.command(self.root / "missing", base))
            self.assertNotEqual(missing.returncode, 0)

    def test_health_only_is_insufficient_and_wrong_key_does_not_leak(self):
        with fake_server(models=False) as base:
            health_only = self.run_command(self.command(self.root / "health-only", base))
        self.assertNotEqual(health_only.returncode, 0)
        with fake_server() as base:
            wrong_key = self.run_command(self.command(self.root / "wrong-key", base), TEST_API_KEY="wrong-secret-sentinel")
        self.assertNotEqual(wrong_key.returncode, 0)
        combined = wrong_key.stdout + wrong_key.stderr
        self.assertNotIn("wrong-secret-sentinel", combined)

    def test_wrong_model_and_missing_key_fail(self):
        wrong_config = json.loads(self.config.read_text())
        wrong_config["model"] = "other-model"
        write_json(self.config, wrong_config)
        with fake_server() as base:
            wrong_model = self.run_command(self.command(self.root / "wrong-model", base, model="other-model"))
            env = os.environ.copy(); env["PATH"] = str(self.bin_dir) + os.pathsep + env["PATH"]; env.pop("TEST_API_KEY", None)
            missing_key = subprocess.run(self.command(self.root / "missing-key", base), text=True, capture_output=True, env=env)
        self.assertNotEqual(wrong_model.returncode, 0)
        self.assertNotEqual(missing_key.returncode, 0)
        self.assertNotIn(SECRET, missing_key.stdout + missing_key.stderr)

    def test_benchmark_failure_and_missing_summary_are_preserved(self):
        with fake_server() as base:
            failed = self.run_command(self.command(self.root / "benchmark-failed", base), FAKE_SLENTORE_MODE="exit1")
            missing = self.run_command(self.command(self.root / "summary-missing", base), FAKE_SLENTORE_MODE="missing-summary")
        self.assertNotEqual(failed.returncode, 0)
        self.assertTrue(any((self.root / "benchmark-failed" / "attempts").glob("*/smoke/partial.txt")))
        self.assertNotEqual(missing.returncode, 0)
        self.assertTrue(any((self.root / "summary-missing" / "attempts").glob("*/validation.json")))

    def test_token_mismatch_and_prefix_mismatch_fail(self):
        with fake_server() as base:
            token = self.run_command(self.command(self.root / "token-mismatch", base), FAKE_SLENTORE_MODE="token-mismatch")
        self.assertNotEqual(token.returncode, 0)
        with fake_server(prefix=False) as base:
            prefix = self.run_command(self.command(self.root / "prefix-mismatch", base))
        self.assertNotEqual(prefix.returncode, 0)

    def test_operator_config_secret_is_rejected_before_persistence(self):
        value = json.loads(self.config.read_text()); value["api_key"] = SECRET; write_json(self.config, value)
        evidence = self.root / "unsafe-config"
        with fake_server() as base:
            result = self.run_command(self.command(evidence, base))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((evidence / "commissioning").exists())
        for path in evidence.rglob("*"):
            if path.is_file():
                self.assertNotIn(SECRET.encode(), path.read_bytes(), path)

    def test_live_version_mismatch_and_malformed_fail(self):
        with fake_server(version="0.25.0") as base:
            mismatch = self.run_command(self.command(self.root / "version-mismatch", base))
        with fake_server(version=None) as base:
            malformed = self.run_command(self.command(self.root / "version-malformed", base))
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertNotEqual(malformed.returncode, 0)

    def test_gpu_identity_mismatches_fail(self):
        cases = (("--expected-gpu-count", "2"), ("--expected-gpu-name", "Wrong GPU"), ("--expected-gpu-memory-mib", "2000"))
        with fake_server() as base:
            for index, (flag, value) in enumerate(cases):
                command = self.command(self.root / f"gpu-mismatch-{index}", base)
                command[command.index(flag) + 1] = value
                self.assertNotEqual(self.run_command(command).returncode, 0)

    def test_finalization_failures_never_promote(self):
        with fake_server() as base:
            for gate, expected_gate in (("manifest", "manifest_finalization"), ("archive", "archive_finalization")):
                evidence = self.root / f"fail-{gate}"
                result = self.run_command(self.command(evidence, base), SLENTORE_COMMISSION_TEST_FAIL_AT=gate)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((evidence / "commissioning").exists())
                validations = list((evidence / "attempts").glob("*/validation.json"))
                self.assertEqual(len(validations), 1)
                self.assertEqual(json.loads(validations[0].read_text())["failed_gate"], expected_gate)


if __name__ == "__main__":
    unittest.main()
