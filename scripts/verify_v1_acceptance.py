#!/usr/bin/env python3
"""Offline V1 acceptance artifact audit using only the Python standard library."""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import math
import os
from pathlib import Path
import re
import sys
from typing import Any

RUN_SCHEMA = 7
EXPERIMENT_SCHEMA = 1
VERIFICATION_SCHEMA = 1
ITL_SOURCE = "vllm_return_token_ids_client_receive"
PROMPT_SENTINEL = "Slentore V1 acceptance sentinel."
TOKEN_SENTINELS = ("987654300", "987654301", "987654321", "987654322", "987654323", "987654324")
RAW_KEYS = {"messages", "choices", "delta", "prompt_token_ids", "token_ids"}
SAFE_PATH = re.compile(r"^[A-Za-z0-9._/-]+$")
SCENARIO_PATHS = {
    "basic": "basic",
    "closed_loop": "closed-loop",
    "open_loop": "open-loop",
    "token_length": "token-length",
}
SLO_THRESHOLDS = {"ttft": 10000.0, "tpot": 1000.0, "e2e": 60000.0}


class Audit:
    def __init__(self, root: Path, api_key_env: str) -> None:
        self.root = root
        self.api_key_env = api_key_env
        self.errors: list[str] = []
        self.benchmark_failures: list[str] = []
        self.scenarios: dict[str, dict[str, Any]] = {}

    def error(self, message: str) -> None:
        self.errors.append(message)

    def benchmark_failure(self, message: str) -> None:
        self.benchmark_failures.append(message)

    def run(self) -> dict[str, Any]:
        acceptance = load_json(self.root / "acceptance.json", self.errors)
        if acceptance.get("acceptance_schema_version") != 1:
            self.error("acceptance.json has unsupported schema")
        scenarios = acceptance.get("scenarios")
        if not isinstance(scenarios, dict):
            self.error("acceptance.json scenarios must be an object")
            scenarios = {}
        if set(scenarios) != set(SCENARIO_PATHS):
            self.error(f"acceptance.json scenarios are {sorted(scenarios)}, want {sorted(SCENARIO_PATHS)}")

        settings: dict[str, dict[str, Any]] = {}
        for name, expected_path in SCENARIO_PATHS.items():
            scenario = scenarios.get(name)
            if not isinstance(scenario, dict):
                self.error(f"acceptance scenario {name} is missing or invalid")
                scenario = {}
            if scenario.get("path") != expected_path:
                self.error(f"acceptance scenario {name} path is {scenario.get('path')!r}, want {expected_path!r}")
            cli_exit = scenario.get("cli_exit")
            if not is_integer(cli_exit):
                self.error(f"acceptance scenario {name} cli_exit is not an integer")
            elif cli_exit != 0:
                self.benchmark_failure(f"acceptance scenario {name} CLI exited {cli_exit}")
            value = scenario.get("settings")
            if not isinstance(value, dict):
                self.error(f"acceptance scenario {name} settings must be a typed object")
                value = {}
            settings[name] = value

        self.validate_settings(settings)
        self.scenarios["basic"] = self.verify_basic(self.root / SCENARIO_PATHS["basic"], settings["basic"])
        self.scenarios["closed_loop"] = self.verify_closed_loop(self.root / SCENARIO_PATHS["closed_loop"], settings["closed_loop"])
        self.scenarios["open_loop"] = self.verify_open_loop(self.root / SCENARIO_PATHS["open_loop"], settings["open_loop"])
        self.scenarios["token_length"] = self.verify_token_length(self.root / SCENARIO_PATHS["token_length"], settings["token_length"])

        self.scan_redaction()
        artifacts_valid = not self.errors
        acceptance_passed = artifacts_valid and not self.benchmark_failures
        report = {
            "verification_schema_version": VERIFICATION_SCHEMA,
            "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "artifacts_valid": artifacts_valid,
            "acceptance_passed": acceptance_passed,
            "errors": self.errors,
            "benchmark_failures": self.benchmark_failures,
            "scenarios": self.scenarios,
        }
        write_json_atomic(self.root / "verification.json", report)
        return report

    def validate_settings(self, settings: dict[str, dict[str, Any]]) -> None:
        specifications = {
            "basic": {"concurrency", "requests", "warmup_requests", "max_output_tokens"},
            "closed_loop": {"concurrency_values", "requests", "warmup_requests"},
            "open_loop": {"request_rate_values", "duration", "max_in_flight", "warmup_requests"},
            "token_length": {"input_token_values", "output_token_values", "concurrency", "requests", "warmup_requests"},
        }
        for name, keys in specifications.items():
            if set(settings[name]) != keys:
                self.error(f"acceptance scenario {name} setting keys are {sorted(settings[name])}, want {sorted(keys)}")

        basic = settings["basic"]
        self.require_positive_integer(basic, "concurrency", "basic")
        if basic.get("concurrency") != 1:
            self.error("basic acceptance concurrency must be 1")
        self.require_positive_integer(basic, "requests", "basic")
        self.require_nonnegative_integer(basic, "warmup_requests", "basic")
        self.require_positive_integer(basic, "max_output_tokens", "basic")

        closed = settings["closed_loop"]
        self.require_integer_list(closed, "concurrency_values", "closed_loop")
        self.require_positive_integer(closed, "requests", "closed_loop")
        self.require_nonnegative_integer(closed, "warmup_requests", "closed_loop")

        opened = settings["open_loop"]
        self.require_number_list(opened, "request_rate_values", "open_loop")
        if not isinstance(opened.get("duration"), str) or not opened.get("duration"):
            self.error("acceptance scenario open_loop duration must be a nonempty string")
        self.require_positive_integer(opened, "max_in_flight", "open_loop")
        self.require_nonnegative_integer(opened, "warmup_requests", "open_loop")

        token = settings["token_length"]
        self.require_integer_list(token, "input_token_values", "token_length")
        self.require_integer_list(token, "output_token_values", "token_length")
        self.require_positive_integer(token, "concurrency", "token_length")
        if token.get("concurrency") != 1:
            self.error("token_length acceptance concurrency must be 1")
        self.require_positive_integer(token, "requests", "token_length")
        self.require_nonnegative_integer(token, "warmup_requests", "token_length")

    def require_positive_integer(self, value: dict[str, Any], key: str, scenario: str) -> None:
        if not is_integer(value.get(key)) or value[key] <= 0:
            self.error(f"acceptance scenario {scenario} {key} must be a positive integer")

    def require_nonnegative_integer(self, value: dict[str, Any], key: str, scenario: str) -> None:
        if not is_integer(value.get(key)) or value[key] < 0:
            self.error(f"acceptance scenario {scenario} {key} must be a nonnegative integer")

    def require_integer_list(self, value: dict[str, Any], key: str, scenario: str) -> None:
        axis = value.get(key)
        if not isinstance(axis, list) or not axis or any(not is_integer(item) or item <= 0 for item in axis) or len(set(axis)) != len(axis):
            self.error(f"acceptance scenario {scenario} {key} must be a nonempty unique positive-integer array")

    def require_number_list(self, value: dict[str, Any], key: str, scenario: str) -> None:
        axis = value.get(key)
        if not isinstance(axis, list) or not axis or any(not is_number(item) or item <= 0 for item in axis) or len(set(axis)) != len(axis):
            self.error(f"acceptance scenario {scenario} {key} must be a nonempty unique positive-number array")

    def verify_basic(self, scenario_root: Path, settings: dict[str, Any]) -> dict[str, Any]:
        run_dirs = sorted(path for path in scenario_root.iterdir() if path.is_dir()) if scenario_root.is_dir() else []
        if len(run_dirs) != 1:
            self.error(f"basic scenario has {len(run_dirs)} run directories, want 1")
            return {"path": "basic", "expected_children": 1, "verified_children": 0}
        run, summary, report = self.verify_run(run_dirs[0], require_timing=True, require_slo=True)
        self.verify_closed_child(
            run_dirs[0], run, summary,
            concurrency=settings.get("concurrency"), requests=settings.get("requests"),
            warmup=settings.get("warmup_requests"), output=settings.get("max_output_tokens"), workload_mode="prompt",
        )
        report.update({"expected_children": 1, "verified_children": 1})
        return report

    def verify_closed_loop(self, scenario_root: Path, settings: dict[str, Any]) -> dict[str, Any]:
        values = settings.get("concurrency_values") if isinstance(settings.get("concurrency_values"), list) else []
        experiment_dir, manifest, points, report = self.load_experiment(scenario_root, "closed_loop", len(values))
        if experiment_dir is None:
            return report
        self.verify_experiment_contract(manifest, experiment_dir, load_mode="closed_loop", workload_mode="prompt")
        axes = manifest.get("axes") or {}
        if axes.get("concurrency_values") != values or axes.get("request_rate_values") != [] or axes.get("input_token_values") != [] or axes.get("output_token_values") != []:
            self.error(f"{experiment_dir}: closed-loop axes do not exactly match acceptance settings")
        referenced: set[str] = set()
        verified = 0
        for offset, expected in enumerate(values, 1):
            if offset > len(points):
                break
            point = points[offset - 1]
            self.verify_point_identity(experiment_dir, point, offset)
            parameters = point.get("parameters") or {}
            if parameters.get("concurrency") != expected or parameters.get("request_rate") is not None or parameters.get("input_tokens") is not None:
                self.error(f"{experiment_dir}: closed-loop point {offset} does not match configured concurrency {expected}")
            child = self.verify_point_child(experiment_dir, point, offset)
            if child is None:
                continue
            run_dir, run, summary = child
            referenced.add(run_dir.name)
            self.verify_closed_child(run_dir, run, summary, expected, settings.get("requests"), settings.get("warmup_requests"), parameters.get("requested_output_tokens"), "prompt")
            verified += 1
        self.verify_child_directory_set(experiment_dir, referenced)
        report["verified_children"] = verified
        return report

    def verify_open_loop(self, scenario_root: Path, settings: dict[str, Any]) -> dict[str, Any]:
        values = settings.get("request_rate_values") if isinstance(settings.get("request_rate_values"), list) else []
        experiment_dir, manifest, points, report = self.load_experiment(scenario_root, "open_loop", len(values))
        if experiment_dir is None:
            return report
        self.verify_experiment_contract(manifest, experiment_dir, load_mode="open_loop", workload_mode="prompt")
        axes = manifest.get("axes") or {}
        if axes.get("request_rate_values") != values or axes.get("concurrency_values") != [] or axes.get("input_token_values") != [] or axes.get("output_token_values") != []:
            self.error(f"{experiment_dir}: open-loop axes do not exactly match acceptance settings")
        referenced: set[str] = set()
        verified = 0
        for offset, expected in enumerate(values, 1):
            if offset > len(points):
                break
            point = points[offset - 1]
            self.verify_point_identity(experiment_dir, point, offset)
            parameters = point.get("parameters") or {}
            if parameters.get("request_rate") != expected or parameters.get("concurrency") is not None or parameters.get("input_tokens") is not None:
                self.error(f"{experiment_dir}: open-loop point {offset} does not match configured request rate {expected}")
            child = self.verify_point_child(experiment_dir, point, offset)
            if child is None:
                continue
            run_dir, run, summary = child
            referenced.add(run_dir.name)
            self.verify_open_child(run_dir, run, summary, expected, settings.get("duration"), settings.get("max_in_flight"), settings.get("warmup_requests"), parameters.get("requested_output_tokens"))
            verified += 1
        self.verify_child_directory_set(experiment_dir, referenced)
        report["verified_children"] = verified
        return report

    def verify_token_length(self, scenario_root: Path, settings: dict[str, Any]) -> dict[str, Any]:
        inputs = settings.get("input_token_values") if isinstance(settings.get("input_token_values"), list) else []
        outputs = settings.get("output_token_values") if isinstance(settings.get("output_token_values"), list) else []
        expected_points = [(input_tokens, output_tokens) for input_tokens in inputs for output_tokens in outputs]
        experiment_dir, manifest, points, report = self.load_experiment(scenario_root, "token_length", len(expected_points))
        if experiment_dir is None:
            return report
        self.verify_experiment_contract(manifest, experiment_dir, load_mode="closed_loop", workload_mode="token_length")
        axes = manifest.get("axes") or {}
        if axes.get("input_token_values") != inputs or axes.get("output_token_values") != outputs or axes.get("concurrency_values") != [] or axes.get("request_rate_values") != []:
            self.error(f"{experiment_dir}: token-length axes do not exactly match acceptance settings")
        referenced: set[str] = set()
        verified = 0
        for offset, (expected_input, expected_output) in enumerate(expected_points, 1):
            if offset > len(points):
                break
            point = points[offset - 1]
            self.verify_point_identity(experiment_dir, point, offset)
            parameters = point.get("parameters") or {}
            if parameters.get("input_tokens") != expected_input or parameters.get("requested_output_tokens") != expected_output or parameters.get("concurrency") != settings.get("concurrency") or parameters.get("request_rate") is not None:
                self.error(f"{experiment_dir}: token-length point {offset} does not match ordered input/output/concurrency contract")
            child = self.verify_point_child(experiment_dir, point, offset, token_target=expected_input)
            if child is None:
                continue
            run_dir, run, summary = child
            referenced.add(run_dir.name)
            self.verify_closed_child(run_dir, run, summary, settings.get("concurrency"), settings.get("requests"), settings.get("warmup_requests"), expected_output, "token_length", token_target=expected_input)
            verified += 1
        self.verify_child_directory_set(experiment_dir, referenced)
        report["verified_children"] = verified
        return report

    def load_experiment(self, scenario_root: Path, name: str, expected_points: int) -> tuple[Path | None, dict[str, Any], list[dict[str, Any]], dict[str, Any]]:
        experiment_dirs = sorted((scenario_root / "experiments").glob("*")) if (scenario_root / "experiments").is_dir() else []
        experiment_dirs = [path for path in experiment_dirs if path.is_dir()]
        report: dict[str, Any] = {"path": SCENARIO_PATHS[name], "expected_children": expected_points, "verified_children": 0}
        if len(experiment_dirs) != 1:
            self.error(f"{name} scenario has {len(experiment_dirs)} experiment directories, want 1")
            return None, {}, [], report
        experiment_dir = experiment_dirs[0]
        manifest = load_json(experiment_dir / "experiment.json", self.errors)
        points_value = manifest.get("points")
        points = points_value if isinstance(points_value, list) and all(isinstance(point, dict) for point in points_value) else []
        if points_value is not None and isinstance(points_value, list) and len(points) != len(points_value):
            self.error(f"{experiment_dir}: points must be objects")
        experiment_id = manifest.get("experiment_id")
        if manifest.get("experiment_schema_version") != EXPERIMENT_SCHEMA:
            self.error(f"{experiment_dir}: unsupported experiment schema")
        if not safe_identity(experiment_id) or experiment_id != experiment_dir.name:
            self.error(f"{experiment_dir}: incoherent experiment identity")
        planned = manifest.get("planned_points")
        if planned != expected_points or len(points) != expected_points:
            self.error(f"{experiment_dir}: has planned_points={planned} and {len(points)} points, want {expected_points}")
        check_csv_rows(experiment_dir / "summary.csv", expected_points + 1, self.errors)
        executed = sum(1 for point in points if point.get("point_status") == "executed")
        if manifest.get("executed_points") != executed:
            self.error(f"{experiment_dir}: executed_points does not match point records")
        status = manifest.get("status")
        complete = manifest.get("complete")
        if status not in {"completed", "failed", "cancelled"} or not isinstance(complete, bool):
            self.error(f"{experiment_dir}: invalid experiment status/completeness evidence")
        elif status != "completed" or not complete:
            self.benchmark_failure(f"{experiment_dir}: experiment status={status!r} complete={complete!r}")
        report.update({"experiment_id": experiment_id, "status": status, "complete": complete, "planned_points": planned, "path": str(experiment_dir.relative_to(self.root))})
        return experiment_dir, manifest, points, report

    def verify_experiment_contract(self, manifest: dict[str, Any], experiment_dir: Path, *, load_mode: str, workload_mode: str) -> None:
        if manifest.get("load_mode") != load_mode:
            self.error(f"{experiment_dir}: load mode does not match {load_mode} acceptance scenario")
        if manifest.get("workload_mode") != workload_mode:
            self.error(f"{experiment_dir}: workload mode does not match {workload_mode} acceptance scenario")
        if not isinstance(manifest.get("axes"), dict):
            self.error(f"{experiment_dir}: experiment axes are missing")

    def verify_point_identity(self, experiment_dir: Path, point: dict[str, Any], offset: int) -> None:
        if point.get("point_index") != offset or point.get("point_id") != f"point-{offset:06d}":
            self.error(f"{experiment_dir}: point {offset} identity is inconsistent")
        if not isinstance(point.get("parameters"), dict):
            self.error(f"{experiment_dir}: point {offset} parameters are missing")

    def verify_point_child(self, experiment_dir: Path, point: dict[str, Any], offset: int, token_target: int | None = None) -> tuple[Path, dict[str, Any], dict[str, Any]] | None:
        if point.get("point_status") != "executed":
            self.benchmark_failure(f"{experiment_dir}: point {offset} was not executed ({point.get('point_status')})")
            return None
        run_id = point.get("run_id")
        run_path = point.get("run_path")
        if not safe_child_path(run_path, run_id):
            self.error(f"{experiment_dir}: point {offset} has unsafe child identity/path")
            return None
        run_dir = experiment_dir / run_path
        run, summary, _ = self.verify_run(run_dir, token_target=token_target, expected_status=point.get("run_status"))
        return run_dir, run, summary

    def verify_run(self, run_dir: Path, *, require_timing: bool = False, require_slo: bool = False, token_target: int | None = None, expected_status: Any = None) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
        run = load_json(run_dir / "run.json", self.errors)
        summary = load_json(run_dir / "summary.json", self.errors)
        check_csv_rows(run_dir / "summary.csv", 2, self.errors)
        run_id = run.get("run_id")
        if run.get("schema_version") != RUN_SCHEMA or summary.get("schema_version") != RUN_SCHEMA:
            self.error(f"{run_dir}: child schema is not {RUN_SCHEMA}")
        if not safe_identity(run_id) or run_id != run_dir.name or summary.get("run_id") != run_id:
            self.error(f"{run_dir}: incoherent run identity")
        if run.get("run_status") != summary.get("run_status"):
            self.error(f"{run_dir}: run and summary statuses differ")
        if expected_status is not None and run.get("run_status") != expected_status:
            self.error(f"{run_dir}: child status does not match experiment point")
        status = run.get("run_status")
        complete = summary.get("complete")
        if status not in {"completed", "failed", "cancelled"} or not isinstance(complete, bool):
            self.error(f"{run_dir}: invalid child status/completeness evidence")
        elif status != "completed" or not complete:
            self.benchmark_failure(f"{run_dir}: benchmark run status={status!r} complete={complete!r}")

        if token_target is not None:
            run_input = ((run.get("workload") or {}).get("input") or {})
            summary_workload = summary.get("workload") or {}
            values = (run_input.get("target_tokens"), run_input.get("resolved_tokens"), summary_workload.get("input_target_tokens"), summary_workload.get("input_resolved_tokens"))
            if any(value != token_target for value in values):
                self.error(f"{run_dir}: token target/resolved evidence {values} does not equal point target {token_target}")

        if require_timing:
            self.verify_itl(run_dir, run, summary)
        if require_slo:
            self.verify_slo(run_dir, run, summary)
        report = {"run_id": run_id, "run_status": status, "path": str(run_dir.relative_to(self.root))}
        return run, summary, report

    def verify_closed_child(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any], concurrency: Any, requests: Any, warmup: Any, output: Any, workload_mode: str, token_target: int | None = None) -> None:
        load = run.get("load") or {}
        closed = load.get("closed_loop") or {}
        summary_load = summary.get("load") or {}
        summary_closed = summary_load.get("closed_loop") or {}
        if load.get("mode") != "closed_loop" or load.get("open_loop") is not None or summary_load.get("mode") != "closed_loop" or summary_load.get("open_loop") is not None:
            self.error(f"{run_dir}: child load mode is not closed_loop")
        if closed.get("requested_concurrency") != concurrency or summary_closed.get("requested_concurrency") != concurrency:
            self.error(f"{run_dir}: requested concurrency does not match acceptance point")
        if closed.get("requested_requests") != requests or (run.get("measurement") or {}).get("requested") != requests or (summary.get("counts") or {}).get("requested_or_planned") != requests:
            self.error(f"{run_dir}: requested measured count does not match acceptance settings")
        self.verify_warmup(run_dir, run, warmup)
        self.verify_workload(run_dir, run, summary, workload_mode, output, token_target)

    def verify_open_child(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any], rate: Any, duration: Any, max_in_flight: Any, warmup: Any, output: Any) -> None:
        load = run.get("load") or {}
        opened = load.get("open_loop") or {}
        summary_load = summary.get("load") or {}
        summary_open = summary_load.get("open_loop") or {}
        if load.get("mode") != "open_loop" or load.get("closed_loop") is not None or summary_load.get("mode") != "open_loop" or summary_load.get("closed_loop") is not None:
            self.error(f"{run_dir}: child load mode is not open_loop")
        if opened.get("request_rate") != rate or summary_open.get("configured_request_rate") != rate:
            self.error(f"{run_dir}: configured request rate does not match acceptance point")
        if opened.get("duration") != duration:
            self.error(f"{run_dir}: configured duration does not match acceptance settings")
        if opened.get("max_in_flight") != max_in_flight or summary_open.get("max_in_flight") != max_in_flight:
            self.error(f"{run_dir}: configured max_in_flight does not match acceptance settings")
        self.verify_warmup(run_dir, run, warmup)
        self.verify_workload(run_dir, run, summary, "prompt", output)
        self.verify_open_delivery(run_dir, summary)

    def verify_warmup(self, run_dir: Path, run: dict[str, Any], expected: Any) -> None:
        warmup = run.get("warmup") or {}
        if warmup.get("requested") != expected:
            self.error(f"{run_dir}: configured warmup count does not match acceptance settings")
        elif warmup.get("attempted") != expected:
            self.benchmark_failure(f"{run_dir}: warmup attempted {warmup.get('attempted')}, want {expected}")

    def verify_workload(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any], mode: str, output: Any, token_target: int | None = None) -> None:
        workload = run.get("workload") or {}
        summary_workload = summary.get("workload") or {}
        if workload.get("mode") != mode or summary_workload.get("mode") != mode:
            self.error(f"{run_dir}: workload mode does not match {mode}")
        if (workload.get("output") or {}).get("requested_max_tokens") != output or summary_workload.get("requested_output_max_tokens") != output:
            self.error(f"{run_dir}: requested output maximum does not match acceptance point")
        if mode == "token_length" and token_target is not None:
            input_metadata = workload.get("input") or {}
            values = (input_metadata.get("target_tokens"), input_metadata.get("resolved_tokens"), summary_workload.get("input_target_tokens"), summary_workload.get("input_resolved_tokens"))
            if any(value != token_target for value in values):
                self.error(f"{run_dir}: token target/resolved evidence {values} does not equal {token_target}")
        elif mode == "prompt" and (workload.get("input") is not None or summary_workload.get("input_target_tokens") is not None or summary_workload.get("input_resolved_tokens") is not None):
            self.error(f"{run_dir}: prompt workload contains token-length input evidence")

    def verify_open_delivery(self, run_dir: Path, summary: dict[str, Any]) -> None:
        opened = ((summary.get("load") or {}).get("open_loop") or {})
        counts = summary.get("counts") or {}
        integer_fields = ("planned_arrivals", "processed_arrivals", "started_arrivals", "client_limited", "scheduler_limited", "unprocessed_due_to_cancellation")
        if any(not is_integer(opened.get(field)) or opened[field] < 0 for field in integer_fields):
            self.error(f"{run_dir}: open-loop delivery counts are invalid")
            return
        planned = opened["planned_arrivals"]
        processed = opened["processed_arrivals"]
        started = opened["started_arrivals"]
        if counts.get("requested_or_planned") != planned or processed + opened["unprocessed_due_to_cancellation"] != planned or started + opened["client_limited"] + opened["scheduler_limited"] != processed:
            self.error(f"{run_dir}: open-loop delivery counts are incoherent")
        ratio = opened.get("delivery_ratio") or {}
        if ratio.get("numerator") != started or ratio.get("denominator") != planned:
            self.error(f"{run_dir}: open-loop delivery ratio counts are incoherent")
        elif planned > 0 and (not ratio.get("available") or ratio.get("value") != started / planned):
            self.error(f"{run_dir}: open-loop delivery ratio value is incoherent")
        lag = summary.get("scheduler_lag_ms")
        if not isinstance(lag, dict) or not isinstance(lag.get("available"), bool) or not is_integer(lag.get("sample_count")):
            self.error(f"{run_dir}: scheduler-lag evidence is missing or malformed")

    def verify_child_directory_set(self, experiment_dir: Path, referenced: set[str]) -> None:
        runs_root = experiment_dir / "runs"
        actual = {path.name for path in runs_root.iterdir() if path.is_dir()} if runs_root.is_dir() else set()
        if actual != referenced:
            self.error(f"{experiment_dir}: child run directories {sorted(actual)} do not match executed point references {sorted(referenced)}")

    def verify_itl(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any]) -> None:
        timing = run.get("token_timing") or {}
        if timing.get("mode") != "vllm" or timing.get("source") != ITL_SOURCE:
            self.error(f"{run_dir}: vLLM token timing metadata is missing")
        available_counts = 0
        available_requests = 0
        unavailable_requests = 0
        successful_requests = 0
        request_dirs = sorted((run_dir / "measured" / "requests").glob("*"))
        if not request_dirs:
            self.error(f"{run_dir}: timing run has no measured request evidence")
        for request_dir in request_dirs:
            observation = load_json(request_dir / "observation.json", self.errors)
            metrics = load_json(request_dir / "metrics.json", self.errors)
            if observation.get("error"):
                continue
            successful_requests += 1
            reason = validate_itl(metrics.get("itl") or {}, metrics.get("token_usage") or {})
            if reason:
                self.error(f"{request_dir / 'metrics.json'}: {reason}")
            elif (metrics.get("itl") or {}).get("available"):
                available_requests += 1
                available_counts += int((metrics.get("itl") or {}).get("count", 0))
            else:
                unavailable_requests += 1
        aggregate_itl = summary.get("itl") or {}
        if aggregate_itl.get("eligible_successful_requests") != successful_requests or aggregate_itl.get("available_requests") != available_requests or aggregate_itl.get("unavailable_requests") != unavailable_requests:
            self.error(f"{run_dir}: aggregate ITL request counts are inconsistent")
        intervals = aggregate_itl.get("intervals_ms") or {}
        if available_requests:
            if aggregate_itl.get("available_sources") != [ITL_SOURCE]:
                self.error(f"{run_dir}: aggregate ITL source is invalid")
            if intervals.get("sample_count") != available_counts:
                self.error(f"{run_dir}: aggregate ITL sample count is inconsistent")
        elif successful_requests and (aggregate_itl.get("available_sources") not in ([], None) or not intervals.get("reason")):
            self.error(f"{run_dir}: aggregate unavailable ITL evidence is incomplete")

    def verify_slo(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any]) -> None:
        configured = run.get("slo") or {}
        expected_config = {f"{name}_ms": threshold for name, threshold in SLO_THRESHOLDS.items()}
        if configured != expected_config:
            self.error(f"{run_dir}: configured acceptance SLO thresholds do not match {expected_config}")
        slo = summary.get("slo") or {}
        if not slo.get("configured") or not all(slo.get(name) for name in SLO_THRESHOLDS):
            self.error(f"{run_dir}: SLO/goodput path was not configured")
            return
        for name, expected in SLO_THRESHOLDS.items():
            if (slo.get(name) or {}).get("threshold_ms") != expected:
                self.error(f"{run_dir}: {name} acceptance threshold is not {expected} ms")
        successful = slo.get("successful_requests", 0)
        evaluable = slo.get("evaluable_requests", 0)
        good = slo.get("good_requests", 0)
        bad = slo.get("bad_requests", 0)
        unevaluable = slo.get("unevaluable_requests", 0)
        if evaluable != good + bad or successful != evaluable + unevaluable:
            self.error(f"{run_dir}: SLO counts are inconsistent")
        if evaluable and bad:
            self.benchmark_failure(f"{run_dir}: permissive acceptance SLO had {bad} bad requests")

    def scan_redaction(self) -> None:
        forbidden_values = [PROMPT_SENTINEL, *TOKEN_SENTINELS]
        if self.api_key_env:
            secret = os.environ.get(self.api_key_env, "")
            if secret:
                forbidden_values.append(secret)
        for path in self.root.rglob("*"):
            if not path.is_file() or path.name == "verification.json" or path.suffix.lower() not in {".json", ".jsonl", ".csv", ".log", ".txt"}:
                continue
            try:
                text = path.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                continue
            for value in forbidden_values:
                if value and value in text:
                    self.error(f"{path}: contains forbidden redaction sentinel")
            if re.search(r"(?i)(authorization[\"']?\s*:|authorization\s+header|bearer\s+)", text):
                self.error(f"{path}: contains Authorization material")
            if path.suffix.lower() in {".json", ".jsonl"}:
                for line_number, value in enumerate_json_values(text, path):
                    bad_keys = find_raw_keys(value)
                    if bad_keys:
                        self.error(f"{path}:{line_number}: contains raw payload keys {sorted(bad_keys)}")


def is_integer(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def validate_itl(itl: dict[str, Any], usage: dict[str, Any]) -> str | None:
    if itl.get("available"):
        if itl.get("source") != ITL_SOURCE:
            return "available ITL has an invalid source"
        output_tokens = usage.get("output_tokens") if usage.get("available") else None
        count = itl.get("count")
        values = itl.get("values_ms")
        if not is_integer(output_tokens) or output_tokens < 1 or count != output_tokens - 1:
            return "available ITL count does not equal output_tokens - 1"
        if not isinstance(values, list) or len(values) != count or any(not is_number(value) or value < 0 for value in values):
            return "available ITL values are invalid"
    elif not isinstance(itl.get("reason"), str) or not itl.get("reason").strip():
        return "unavailable ITL lacks an explicit reason"
    return None


def safe_identity(value: Any) -> bool:
    return isinstance(value, str) and bool(value) and Path(value).name == value and value not in {".", ".."} and bool(SAFE_PATH.fullmatch(value))


def safe_child_path(run_path: Any, run_id: Any) -> bool:
    if not isinstance(run_path, str) or not safe_identity(run_id) or not SAFE_PATH.fullmatch(run_path):
        return False
    path = Path(run_path)
    return not path.is_absolute() and ".." not in path.parts and path.parts == ("runs", run_id)


def find_raw_keys(value: Any) -> set[str]:
    found: set[str] = set()
    if isinstance(value, dict):
        for key, child in value.items():
            if key in RAW_KEYS:
                found.add(key)
            found.update(find_raw_keys(child))
    elif isinstance(value, list):
        for child in value:
            found.update(find_raw_keys(child))
    return found


def enumerate_json_values(text: str, path: Path):
    if path.suffix.lower() == ".jsonl":
        for number, line in enumerate(text.splitlines(), 1):
            if line.strip():
                try:
                    yield number, json.loads(line)
                except json.JSONDecodeError:
                    yield number, {}
    else:
        try:
            yield 1, json.loads(text)
        except json.JSONDecodeError:
            yield 1, {}


def load_json(path: Path, errors: list[str]) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        errors.append(f"{path}: cannot read valid JSON: {error}")
        return {}
    if not isinstance(value, dict):
        errors.append(f"{path}: top-level JSON value is not an object")
        return {}
    return value


def check_csv_rows(path: Path, expected: int, errors: list[str]) -> None:
    try:
        with path.open(newline="", encoding="utf-8") as handle:
            rows = list(csv.reader(handle))
    except OSError as error:
        errors.append(f"{path}: cannot read CSV: {error}")
        return
    if len(rows) != expected or not rows or not rows[0]:
        errors.append(f"{path}: has {len(rows)} rows, want {expected}")


def write_json_atomic(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--acceptance-dir", required=True, type=Path)
    parser.add_argument("--api-key-env", default="")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    audit = Audit(args.acceptance_dir.resolve(), args.api_key_env)
    report = audit.run()
    if report["artifacts_valid"] and report["acceptance_passed"]:
        print(f"V1 acceptance verification passed: {args.acceptance_dir}")
        return 0
    for error in report["errors"]:
        print(f"artifact error: {error}", file=sys.stderr)
    for failure in report["benchmark_failures"]:
        print(f"benchmark failure: {failure}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
