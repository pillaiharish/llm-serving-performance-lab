import csv
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "verify_v1_acceptance.py"
SPEC = importlib.util.spec_from_file_location("verify_v1_acceptance", SCRIPT)
verify = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(verify)


class VerifierUnitTests(unittest.TestCase):
    def test_available_itl_contract(self):
        usage = {"available": True, "output_tokens": 4}
        valid = {"available": True, "source": verify.ITL_SOURCE, "count": 3, "values_ms": [1.0, 2.0, 3.0]}
        self.assertIsNone(verify.validate_itl(valid, usage))
        invalid = dict(valid, count=2)
        self.assertIn("output_tokens", verify.validate_itl(invalid, usage))
        invalid = dict(valid, source="interpolated")
        self.assertIn("source", verify.validate_itl(invalid, usage))
        invalid = dict(valid, values_ms=[1.0, float("inf"), 3.0])
        self.assertIn("values", verify.validate_itl(invalid, usage))

    def test_unavailable_itl_requires_reason(self):
        self.assertIsNone(verify.validate_itl({"available": False, "reason": "multi-token SSE event"}, {}))
        self.assertIn("reason", verify.validate_itl({"available": False, "reason": ""}, {}))

    def test_safe_child_path_rejects_traversal_and_identity_mismatch(self):
        self.assertTrue(verify.safe_child_path("runs/run-1", "run-1"))
        self.assertFalse(verify.safe_child_path("../runs/run-1", "run-1"))
        self.assertFalse(verify.safe_child_path("runs/run-2", "run-1"))
        self.assertFalse(verify.safe_child_path("/runs/run-1", "run-1"))

    def test_raw_payload_key_scan_allows_safe_derived_fields(self):
        value = {"stream_events": [{"token_ids_present": True, "content_bytes": 1}], "nested": {"messages": []}}
        self.assertEqual(verify.find_raw_keys(value), {"messages"})

    def test_run_audit_catches_schema_target_and_missing_csv(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            run_dir = root / "run-1"
            run_dir.mkdir()
            (run_dir / "run.json").write_text(json.dumps({
                "schema_version": 6,
                "run_id": "run-1",
                "run_status": "completed",
                "workload": {"input": {"target_tokens": 128, "resolved_tokens": 127}},
            }), encoding="utf-8")
            (run_dir / "summary.json").write_text(json.dumps({
                "schema_version": 7,
                "run_id": "run-1",
                "run_status": "completed",
                "complete": True,
                "workload": {"input_target_tokens": 128, "input_resolved_tokens": 127},
            }), encoding="utf-8")
            audit = verify.Audit(root, "")
            audit.verify_run(run_dir, token_target=128)
            joined = "\n".join(audit.errors)
            self.assertIn("schema", joined)
            self.assertIn("target/resolved", joined)
            self.assertIn("cannot read CSV", joined)

    def test_csv_row_count(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "summary.csv"
            with path.open("w", newline="", encoding="utf-8") as handle:
                csv.writer(handle).writerow(["header"])
            errors = []
            verify.check_csv_rows(path, 2, errors)
            self.assertTrue(errors)

    def test_redaction_scan_detects_secret_prompt_and_raw_keys(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "artifact.json").write_text(json.dumps({
                "prompt": verify.PROMPT_SENTINEL,
                "messages": [{"content": "private"}],
            }), encoding="utf-8")
            audit = verify.Audit(root, "")
            audit.scan_redaction()
            joined = "\n".join(audit.errors)
            self.assertIn("redaction sentinel", joined)
            self.assertIn("raw payload keys", joined)


if __name__ == "__main__":
    unittest.main()
