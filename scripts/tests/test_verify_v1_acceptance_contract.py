import csv
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "verify_v1_acceptance.py"
SPEC = importlib.util.spec_from_file_location("verify_v1_acceptance_contract", SCRIPT)
verify = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(verify)


class AcceptanceContractTests(unittest.TestCase):
    def test_positive_typed_fixture_proves_configured_child_counts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            make_fixture(root)
            report = verify.Audit(root, "").run()
            self.assertTrue(report["artifacts_valid"], report["errors"])
            self.assertTrue(report["acceptance_passed"], report["benchmark_failures"])
            self.assertEqual(report["scenarios"]["basic"]["verified_children"], 1)
            self.assertEqual(report["scenarios"]["closed_loop"]["verified_children"], 3)
            self.assertEqual(report["scenarios"]["open_loop"]["verified_children"], 3)
            self.assertEqual(report["scenarios"]["token_length"]["verified_children"], 4)

    def test_artifact_contract_corruptions_fail(self):
        cases = {
            "missing closed point": remove_closed_point,
            "wrong concurrency axis": wrong_closed_axis,
            "wrong open rate": wrong_open_rate,
            "wrong duration": wrong_open_duration,
            "wrong max in flight": wrong_open_max_in_flight,
            "missing token point": remove_token_point,
            "wrong token output": wrong_token_output,
            "reordered token matrix": reorder_token_points,
            "wrong basic concurrency": wrong_basic_concurrency,
            "scenario path mismatch": wrong_scenario_path,
        }
        for name, mutate in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                make_fixture(root)
                mutate(root)
                report = verify.Audit(root, "").run()
                self.assertFalse(report["artifacts_valid"])
                self.assertFalse(report["acceptance_passed"])
                self.assertTrue(report["errors"])

    def test_nonzero_cli_exit_retains_valid_artifacts_as_benchmark_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            make_fixture(root)
            update_json(root / "acceptance.json", lambda value: value["scenarios"]["open_loop"].update(cli_exit=1))
            report = verify.Audit(root, "").run()
            self.assertTrue(report["artifacts_valid"], report["errors"])
            self.assertFalse(report["acceptance_passed"])
            self.assertTrue(any("CLI exited 1" in value for value in report["benchmark_failures"]))

    def test_incomplete_experiment_is_benchmark_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            make_fixture(root)
            update_json(closed_manifest(root), lambda value: value.update(complete=False))
            report = verify.Audit(root, "").run()
            self.assertTrue(report["artifacts_valid"], report["errors"])
            self.assertFalse(report["acceptance_passed"])
            self.assertTrue(any("complete=False" in value for value in report["benchmark_failures"]))

    def test_failed_child_is_retained_as_benchmark_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            make_fixture(root)
            manifest_path = closed_manifest(root)
            manifest = read_json(manifest_path)
            point = manifest["points"][0]
            point["run_status"] = "failed"
            write_json(manifest_path, manifest)
            child = manifest_path.parent / point["run_path"]
            update_json(child / "run.json", lambda value: value.update(run_status="failed"))
            update_json(child / "summary.json", lambda value: value.update(run_status="failed"))
            report = verify.Audit(root, "").run()
            self.assertTrue(report["artifacts_valid"], report["errors"])
            self.assertFalse(report["acceptance_passed"])
            self.assertTrue(any("status='failed'" in value for value in report["benchmark_failures"]))


def make_fixture(root: Path) -> None:
    acceptance = {
        "acceptance_schema_version": 1,
        "scenarios": {
            "basic": {"path": "basic", "cli_exit": 0, "settings": {"concurrency": 1, "requests": 5, "warmup_requests": 2, "max_output_tokens": 32}},
            "closed_loop": {"path": "closed-loop", "cli_exit": 0, "settings": {"concurrency_values": [1, 4, 8], "requests": 16, "warmup_requests": 2}},
            "open_loop": {"path": "open-loop", "cli_exit": 0, "settings": {"request_rate_values": [2.0, 5.0, 10.0], "duration": "2s", "max_in_flight": 16, "warmup_requests": 2}},
            "token_length": {"path": "token-length", "cli_exit": 0, "settings": {"input_token_values": [128, 512], "output_token_values": [32, 64], "concurrency": 1, "requests": 2, "warmup_requests": 1}},
        },
    }
    write_json(root / "acceptance.json", acceptance)
    write_run(root / "basic" / "run-basic", mode="closed_loop", concurrency=1, requests=5, warmup=2, output=32, basic=True)
    write_experiment(root, "closed-loop", "exp-closed", "closed_loop", "prompt", [1, 4, 8], [], [], [], requests=16, warmup=2)
    write_experiment(root, "open-loop", "exp-open", "open_loop", "prompt", [], [2.0, 5.0, 10.0], [], [], duration="2s", max_in_flight=16, warmup=2)
    write_experiment(root, "token-length", "exp-token", "closed_loop", "token_length", [], [], [128, 512], [32, 64], concurrency=1, requests=2, warmup=1)


def write_experiment(root: Path, scenario: str, experiment_id: str, load_mode: str, workload_mode: str, concurrency_values: list[int], rate_values: list[float], input_values: list[int], output_values: list[int], *, concurrency: int | None = None, requests: int = 0, duration: str = "", max_in_flight: int = 0, warmup: int = 0) -> None:
    experiment_root = root / scenario / "experiments" / experiment_id
    combinations: list[tuple[int | None, float | None, int | None, int]] = []
    if workload_mode == "token_length":
        for input_tokens in input_values:
            for output_tokens in output_values:
                combinations.append((concurrency, None, input_tokens, output_tokens))
    elif load_mode == "closed_loop":
        for value in concurrency_values:
            combinations.append((value, None, None, 32))
    else:
        for value in rate_values:
            combinations.append((None, value, None, 32))
    points = []
    for index, (point_concurrency, rate, input_tokens, output) in enumerate(combinations, 1):
        run_id = f"run-{scenario}-{index}"
        points.append({
            "point_index": index,
            "point_id": f"point-{index:06d}",
            "parameters": {"concurrency": point_concurrency, "request_rate": rate, "input_tokens": input_tokens, "requested_output_tokens": output},
            "point_status": "executed",
            "run_id": run_id,
            "run_path": f"runs/{run_id}",
            "run_status": "completed",
        })
        write_run(
            experiment_root / "runs" / run_id,
            mode=load_mode,
            concurrency=point_concurrency,
            rate=rate,
            requests=requests,
            duration=duration,
            max_in_flight=max_in_flight,
            warmup=warmup,
            workload_mode=workload_mode,
            input_tokens=input_tokens,
            output=output,
        )
    manifest = {
        "experiment_schema_version": 1,
        "experiment_id": experiment_id,
        "status": "completed",
        "complete": True,
        "load_mode": load_mode,
        "workload_mode": workload_mode,
        "axes": {
            "concurrency_values": concurrency_values,
            "request_rate_values": rate_values,
            "input_token_values": input_values,
            "output_token_values": output_values,
        },
        "planned_points": len(points),
        "executed_points": len(points),
        "points": points,
    }
    write_json(experiment_root / "experiment.json", manifest)
    write_csv(experiment_root / "summary.csv", len(points) + 1)


def write_run(run_root: Path, *, mode: str, concurrency: int | None = None, rate: float | None = None, requests: int = 0, duration: str = "", max_in_flight: int = 0, warmup: int = 0, workload_mode: str = "prompt", input_tokens: int | None = None, output: int = 32, basic: bool = False) -> None:
    run_id = run_root.name
    workload = {"mode": workload_mode, "output": {"requested_max_tokens": output}}
    summary_workload = {"mode": workload_mode, "requested_output_max_tokens": output}
    if input_tokens is not None:
        workload["input"] = {"target_tokens": input_tokens, "resolved_tokens": input_tokens}
        summary_workload.update(input_target_tokens=input_tokens, input_resolved_tokens=input_tokens)
    run = {
        "schema_version": 7,
        "run_id": run_id,
        "run_status": "completed",
        "workload": workload,
        "warmup": {"requested": warmup, "attempted": warmup},
        "measurement": {"requested": requests},
    }
    summary = {
        "schema_version": 7,
        "run_id": run_id,
        "run_status": "completed",
        "complete": True,
        "workload": summary_workload,
    }
    if mode == "closed_loop":
        run["load"] = {"mode": "closed_loop", "closed_loop": {"requested_concurrency": concurrency, "requested_requests": requests}}
        summary["load"] = {"mode": "closed_loop", "closed_loop": {"requested_concurrency": concurrency}}
        summary["counts"] = {"requested_or_planned": requests}
    else:
        planned = max(1, int(float(rate or 1) * 2))
        run["load"] = {"mode": "open_loop", "open_loop": {"request_rate": rate, "duration": duration, "max_in_flight": max_in_flight, "planned_arrivals": planned}}
        summary["load"] = {"mode": "open_loop", "open_loop": {
            "configured_request_rate": rate,
            "max_in_flight": max_in_flight,
            "planned_arrivals": planned,
            "processed_arrivals": planned,
            "started_arrivals": planned,
            "client_limited": 0,
            "scheduler_limited": 0,
            "unprocessed_due_to_cancellation": 0,
            "delivery_ratio": {"available": True, "value": 1.0, "numerator": planned, "denominator": planned},
        }}
        summary["counts"] = {"requested_or_planned": planned}
        summary["scheduler_lag_ms"] = {"available": True, "sample_count": planned}
    if basic:
        run["token_timing"] = {"mode": "vllm", "source": verify.ITL_SOURCE}
        run["slo"] = {"ttft_ms": 10000.0, "tpot_ms": 1000.0, "e2e_ms": 60000.0}
        summary["itl"] = {
            "eligible_successful_requests": 1,
            "available_requests": 1,
            "unavailable_requests": 0,
            "available_sources": [verify.ITL_SOURCE],
            "intervals_ms": {"sample_count": 1},
        }
        summary["slo"] = {
            "configured": True,
            "ttft": {"threshold_ms": 10000.0},
            "tpot": {"threshold_ms": 1000.0},
            "e2e": {"threshold_ms": 60000.0},
            "successful_requests": 1,
            "evaluable_requests": 1,
            "good_requests": 1,
            "bad_requests": 0,
            "unevaluable_requests": 0,
        }
        request_root = run_root / "measured" / "requests" / "req-000001"
        write_json(request_root / "observation.json", {"run_id": run_id, "request_id": "req-000001"})
        write_json(request_root / "metrics.json", {
            "itl": {"available": True, "source": verify.ITL_SOURCE, "count": 1, "values_ms": [1.0]},
            "token_usage": {"available": True, "output_tokens": 2},
        })
    write_json(run_root / "run.json", run)
    write_json(run_root / "summary.json", summary)
    write_csv(run_root / "summary.csv", 2)


def remove_closed_point(root: Path) -> None:
    path = closed_manifest(root)
    value = read_json(path)
    value["points"].pop()
    value["planned_points"] = 2
    value["executed_points"] = 2
    write_json(path, value)


def wrong_closed_axis(root: Path) -> None:
    update_json(closed_manifest(root), lambda value: value["axes"]["concurrency_values"].__setitem__(1, 5))


def wrong_open_rate(root: Path) -> None:
    update_json(open_manifest(root), lambda value: value["points"][1]["parameters"].update(request_rate=6.0))


def wrong_open_duration(root: Path) -> None:
    update_json(open_child(root, 1) / "run.json", lambda value: value["load"]["open_loop"].update(duration="3s"))


def wrong_open_max_in_flight(root: Path) -> None:
    update_json(open_child(root, 1) / "run.json", lambda value: value["load"]["open_loop"].update(max_in_flight=8))


def remove_token_point(root: Path) -> None:
    path = token_manifest(root)
    value = read_json(path)
    value["points"].pop()
    value["planned_points"] = 3
    value["executed_points"] = 3
    write_json(path, value)


def wrong_token_output(root: Path) -> None:
    update_json(token_manifest(root), lambda value: value["points"][0]["parameters"].update(requested_output_tokens=99))


def reorder_token_points(root: Path) -> None:
    path = token_manifest(root)
    value = read_json(path)
    first = value["points"][0]["parameters"]
    second = value["points"][1]["parameters"]
    value["points"][0]["parameters"], value["points"][1]["parameters"] = second, first
    write_json(path, value)


def wrong_basic_concurrency(root: Path) -> None:
    update_json(root / "basic" / "run-basic" / "run.json", lambda value: value["load"]["closed_loop"].update(requested_concurrency=2))
    update_json(root / "basic" / "run-basic" / "summary.json", lambda value: value["load"]["closed_loop"].update(requested_concurrency=2))


def wrong_scenario_path(root: Path) -> None:
    update_json(root / "acceptance.json", lambda value: value["scenarios"]["basic"].update(path="alternate-basic"))


def closed_manifest(root: Path) -> Path:
    return root / "closed-loop" / "experiments" / "exp-closed" / "experiment.json"


def open_manifest(root: Path) -> Path:
    return root / "open-loop" / "experiments" / "exp-open" / "experiment.json"


def token_manifest(root: Path) -> Path:
    return root / "token-length" / "experiments" / "exp-token" / "experiment.json"


def open_child(root: Path, index: int) -> Path:
    return open_manifest(root).parent / "runs" / f"run-open-loop-{index}"


def read_json(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def update_json(path: Path, update) -> None:
    value = read_json(path)
    update(value)
    write_json(path, value)


def write_json(path: Path, value) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def write_csv(path: Path, rows: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.writer(handle)
        writer.writerow(["schema"])
        for _ in range(rows - 1):
            writer.writerow(["1"])


if __name__ == "__main__":
    unittest.main()
