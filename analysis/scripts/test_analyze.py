#!/usr/bin/env python3
import csv, io, json, pathlib, sys, tarfile, tempfile, unittest
from types import SimpleNamespace
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import analyze

RAW = HERE.parents[1] / "h100-qwen38-single-gpu-baseline-c1-20260906.tar.gz"
RUN_ID = "20260906T112816Z-9187a20d"

def rewrite(source, target, change=lambda name, data: (name, data), additions=()):
    with tarfile.open(source, "r:gz") as old, tarfile.open(target, "w:gz") as new:
        for member in old.getmembers():
            if not member.isfile(): continue
            data = old.extractfile(member).read(); result = change(member.name, data)
            if result is None: continue
            name, data = result; info = tarfile.TarInfo(name); info.size = len(data); info.mtime = 0; new.addfile(info, io.BytesIO(data))
        for name, data in additions:
            info = tarfile.TarInfo(name); info.size = len(data); info.mtime = 0; new.addfile(info, io.BytesIO(data))

def valid_three(source, target):
    additions = []
    with tarfile.open(source, "r:gz") as old:
        for member in old.getmembers():
            if not member.isfile() or "rep1" not in member.name: continue
            raw = old.extractfile(member).read()
            for rep in (2, 3):
                data = raw.replace(b"c1-512-128-rep1", f"c1-512-128-rep{rep}".encode()).replace(RUN_ID.encode(), f"synthetic-rep{rep}".encode())
                additions.append((member.name.replace("rep1", f"rep{rep}"), data))
    rewrite(source, target, additions=additions)

def json_change(match, edit):
    def change(name, data):
        if match(name):
            obj = json.loads(data); edit(obj); return name, json.dumps(obj).encode()
        return name, data
    return change

def csv_change(data, updates):
    source = io.StringIO(data.decode()); rows = list(csv.DictReader(source)); output = io.StringIO()
    writer = csv.DictWriter(output, fieldnames=list(rows[0]), lineterminator="\n")
    writer.writeheader()
    for row in rows: row.update(updates); writer.writerow(row)
    return output.getvalue().encode()

class AnalysisTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(); cls.root = pathlib.Path(cls.temp.name)
        cls.valid = cls.root / "valid.tar.gz"; valid_three(RAW, cls.valid)
    @classmethod
    def tearDownClass(cls): cls.temp.cleanup()
    def changed(self, name, source=None, change=None, additions=()):
        path = self.root / name; rewrite(source or RAW, path, change or (lambda n,d:(n,d)), additions); return path
    def assert_invalid(self, archive, text):
        with self.assertRaisesRegex(analyze.ValidationError, text): analyze.audit(archive)
    def artifacts(self, *suffixes):
        with tarfile.open(RAW, "r:gz") as archive:
            return [(name, archive.extractfile(name).read()) for suffix in suffixes for name in archive.getnames() if name.endswith(suffix) and "/measured/" in name and "c1-512-128-rep1/" in name]

    def test_valid_three_repetition_experiment(self):
        result = analyze.audit(self.valid); self.assertTrue(result.complete and result.cross_valid); self.assertEqual(len(result.runs), 3)
        self.assertEqual(result.runs[0].run["model"], analyze.EXPECTED_MODEL)
    def test_valid_one_run_exploratory_generation(self):
        out = self.root / "one-output"; code, path = analyze.execute(RAW, out, "exploratory", analyze.sha256(RAW)); self.assertEqual(code, 0); self.assertTrue((path/"exploratory_report.md").exists()); self.assertIn(analyze.EXPLORE, (path/"tables/percentiles.csv").read_text())
        manifest = json.loads((path/"manifest.json").read_text()); self.assertEqual(manifest["analyzer"]["analyzer_sha256"], analyze.sha256(HERE/"analyze.py")); self.assertEqual(manifest["analyzer"]["authoritative_identity"], "analyzer_sha256")
    def test_missing_repetition(self):
        result = analyze.audit(RAW); self.assertFalse(result.complete); self.assertEqual(result.found, {1})
    def test_duplicate_repetition(self):
        with tarfile.open(RAW,"r:gz") as t:
            name=next(n for n in t.getnames() if "c1-512-128-rep1/" in n and n.endswith("/run.json")); data=t.extractfile(name).read()
        path=self.changed("duplicate.tar.gz", additions=[(name.replace(RUN_ID,"duplicate"),data)]); self.assert_invalid(path,"exactly one run directory")
    def test_malformed_json(self):
        path=self.changed("bad-json.tar.gz",change=lambda n,d:(n,b"{") if n.endswith("/run.json") and "rep1/" in n else (n,d));self.assert_invalid(path,"invalid JSON")
    def test_malformed_csv_telemetry(self):
        path=self.changed("bad-gpu.tar.gz",change=lambda n,d:(n,b"wrong,header\n1,2\n") if n.endswith("rep1-gpu.csv") else (n,d));self.assert_invalid(path,"malformed GPU telemetry header")
    def test_missing_required_artifact(self):
        path=self.changed("missing.tar.gz",change=lambda n,d:None if n.endswith("req-000001/metrics.json") and "/measured/" in n else (n,d));self.assert_invalid(path,"measured artifacts")
    def test_nested_duplicate_request_artifacts(self):
        original = self.artifacts("req-000001/metrics.json", "req-000001/observation.json")
        additions = [(name.replace("/req-000001/", "/some-nested-path/req-000001/"), data) for name, data in original]
        self.assert_invalid(self.changed("nested-both.tar.gz", additions=additions), "non-canonical request artifact")
    def test_nested_duplicate_metric_only(self):
        name, data = self.artifacts("req-000001/metrics.json")[0]
        self.assert_invalid(self.changed("nested-metric.tar.gz", additions=[(name.replace("/req-000001/", "/nested/req-000001/"), data)]), "non-canonical request artifact")
    def test_nested_duplicate_observation_only(self):
        name, data = self.artifacts("req-000001/observation.json")[0]
        self.assert_invalid(self.changed("nested-observation.tar.gz", additions=[(name.replace("/req-000001/", "/nested/req-000001/"), data)]), "non-canonical request artifact")
    def test_malformed_nested_request_artifact(self):
        name, data = self.artifacts("req-000001/metrics.json")[0]
        request_root = name.split("/req-000001/")[0]
        self.assert_invalid(self.changed("malformed-nested.tar.gz", additions=[(f"{request_root}/bad/path/metrics.json", data)]), "non-canonical request artifact")
    def test_canonical_request_sets_succeed(self):
        self.assertEqual(len(analyze.audit(RAW).runs[0].requests), 100)
    def test_workload_mode_mismatch(self):
        path=self.changed("workload.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["workload"].__setitem__("mode","prompt")));self.assert_invalid(path,"workload mode")
    def test_input_token_mismatch(self):
        path=self.changed("input.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["workload"]["input"].__setitem__("resolved_tokens",511)));self.assert_invalid(path,"input target/resolved")
    def test_output_token_mismatch(self):
        path=self.changed("output.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["workload"]["output"].__setitem__("requested_max_tokens",127)));self.assert_invalid(path,"output max")
    def test_concurrency_mismatch(self):
        path=self.changed("concurrency.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["load"]["closed_loop"].__setitem__("requested_concurrency",2)));self.assert_invalid(path,"closed-loop C=1")
    def test_run_id_mismatch(self):
        path=self.changed("runid.tar.gz",change=json_change(lambda n:n.endswith("req-000001/observation.json") and "/measured/" in n,lambda o:o.__setitem__("run_id","wrong")));self.assert_invalid(path,"run-ID mismatch")
    def test_request_id_mismatch(self):
        path=self.changed("request-id.tar.gz",change=json_change(lambda n:n.endswith("req-000001/observation.json") and "/measured/" in n,lambda o:o.__setitem__("request_id","req-999999")));self.assert_invalid(path,"request-ID mismatch")
    def test_request_http_failure(self):
        path=self.changed("http.tar.gz",change=json_change(lambda n:n.endswith("req-000001/observation.json") and "/measured/" in n,lambda o:o.__setitem__("status_code",500)));self.assert_invalid(path,"did not complete normally")
    def test_request_timeout_and_cancellation(self):
        for field in ("timed_out","cancelled"):
            with self.subTest(field=field):
                path=self.changed(f"{field}.tar.gz",change=json_change(lambda n:n.endswith("req-000001/observation.json") and "/measured/" in n,lambda o,f=field:o.__setitem__(f,True)));self.assert_invalid(path,"timeout, cancellation")
    def test_missing_token_timing_declaration(self):
        path=self.changed("timing.tar.gz",change=json_change(lambda n:n.endswith("req-000001/observation.json") and "/measured/" in n,lambda o:o.__setitem__("token_timing",{})));self.assert_invalid(path,"token timing")
    def test_invalid_itl_evidence(self):
        path=self.changed("itl.tar.gz",change=json_change(lambda n:n.endswith("req-000001/metrics.json") and "/measured/" in n and "c1-512-128-rep1/" in n,lambda o:o["itl"]["values_ms"].__setitem__(0,99)));self.assert_invalid(path,"ITL interval")
    def test_unit_mismatch(self):
        path=self.changed("unit.tar.gz",change=json_change(lambda n:n.endswith("req-000001/metrics.json") and "/measured/" in n,lambda o:o["ttft"].__setitem__("unit","s")));self.assert_invalid(path,"TTFT|ttft")
    def test_nan_infinite_and_negative_metrics(self):
        for label,value in (("nan",float("nan")),("infinite",float("inf")),("negative",-1)):
            with self.subTest(label=label):
                path=self.changed(f"{label}.tar.gz",change=json_change(lambda n:n.endswith("req-000001/metrics.json") and "/measured/" in n,lambda o,v=value:o["ttft"].__setitem__("value",v)));self.assert_invalid(path,"invalid JSON|finite and nonnegative")
    def test_inconsistent_summary_vs_raw(self):
        path=self.changed("summary.tar.gz",change=json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o:o["latency"]["ttft"].__setitem__("p50",999)));self.assert_invalid(path,"raw ttft p50")
    def test_ttlt_summary_corruption(self):
        cases = (("p50", 999, "raw ttlt p50"), ("p95", 999, "raw ttlt p95"), ("p99", 999, "raw ttlt p99"), ("sample_count", 99, "summary ttlt units/count"), ("unit", "s", "summary ttlt units/count"))
        for field, value, message in cases:
            with self.subTest(field=field):
                path=self.changed(f"ttlt-{field}.tar.gz",change=json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o,f=field,v=value:o["latency"]["ttlt"].__setitem__(f,v)));self.assert_invalid(path,message)
    def test_inconsistent_throughput(self):
        path=self.changed("throughput.tar.gz",change=json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o:o["request_rates"]["successful_request_throughput"].__setitem__("value",999)));self.assert_invalid(path,"successful_requests_per_second mismatch")
    def test_elapsed_ns_mismatch(self):
        path=self.changed("elapsed-ns.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["measurement"].__setitem__("elapsed_ns",o["measurement"]["elapsed_ns"]+1_000_000_000)));self.assert_invalid(path,"measurement timestamp duration")
    def test_measurement_timestamp_mismatch(self):
        path=self.changed("timestamps.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o["measurement"].__setitem__("completed_at","2026-09-06T11:33:08.584943248Z")));self.assert_invalid(path,"measurement timestamp duration")
    def test_summary_json_duration_mismatch(self):
        path=self.changed("json-duration.tar.gz",change=json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o:o["durations"]["completion_window_seconds"].__setitem__("value",999)));self.assert_invalid(path,"summary.json completion window")
    def test_summary_csv_duration_mismatch(self):
        path=self.changed("csv-duration.tar.gz",change=lambda n,d:(n,csv_change(d,{"completion_window_seconds":"999"})) if n.endswith("/summary.csv") and "rep1/" in n else (n,d));self.assert_invalid(path,"summary.csv completion window")
    def test_co_tampered_duration_and_throughput(self):
        def change(name, data):
            if name.endswith("/summary.json") and "rep1/" in name:
                obj=json.loads(data);obj["durations"]["completion_window_seconds"]["value"]*=2
                for field in (obj["request_rates"]["successful_request_throughput"],*obj["tokens"]["throughput"].values()):field["value"]/=2;field["denominator_seconds"]*=2
                return name,json.dumps(obj).encode()
            if name.endswith("/summary.csv") and "rep1/" in name:
                return name,csv_change(data,{"completion_window_seconds":"528.521771338","successful_requests_per_second":"0.18920696444886475","input_tokens_per_second":"96.87396579781875","output_tokens_per_second":"24.21849144945469","total_tokens_per_second":"121.09245724727345"})
            return name,data
        self.assert_invalid(self.changed("co-tampered.tar.gz",change=change),"summary.json completion window")
    def test_request_token_corruption(self):
        for field in ("input_tokens", "output_tokens"):
            with self.subTest(field=field):
                path=self.changed(f"request-{field}.tar.gz",change=json_change(lambda n:n.endswith("req-000001/metrics.json") and "/measured/" in n,lambda o,f=field:o["token_usage"].__setitem__(f,0)));self.assert_invalid(path,"usage is not exact")
    def test_summary_token_total_corruption(self):
        for field in ("successful_input_tokens", "successful_output_tokens", "successful_total_tokens"):
            with self.subTest(field=field):
                path=self.changed(f"summary-{field}.tar.gz",change=json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o,f=field:o["tokens"].__setitem__(f,0)));self.assert_invalid(path,"token totals agree")
    def test_exact_model_required_in_both_modes(self):
        cases = (
            ("wrong-top-model", lambda o:o.__setitem__("model","Other/Model")),
            ("wrong-tokenizer-model", lambda o:o["workload"]["tokenizer"].__setitem__("model","Other/Model")),
        )
        for name, mutate in cases:
            with self.subTest(case=name):
                path=self.changed(f"{name}.tar.gz",change=json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,mutate))
                self.assert_invalid(path,"persisted model identities agree")
                for mode in ("strict", "exploratory"):
                    code, output=analyze.execute(path,self.root/f"{name}-{mode}",mode,analyze.sha256(path));self.assertEqual(code,2);self.assertFalse((output/"exploratory_report.md").exists())
    def test_schema_validation(self):
        cases = (
            ("run-999", json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o.__setitem__("schema_version",999))),
            ("summary-mismatch", json_change(lambda n:n.endswith("/summary.json") and "rep1/" in n,lambda o:o.__setitem__("schema_version",999))),
            ("run-missing", json_change(lambda n:n.endswith("/run.json") and "rep1/" in n,lambda o:o.pop("schema_version"))),
            ("csv-mismatch", lambda n,d:(n,csv_change(d,{"schema_version":"999"})) if n.endswith("/summary.csv") and "rep1/" in n else (n,d)),
        )
        for name, change in cases:
            with self.subTest(case=name): self.assert_invalid(self.changed(f"schema-{name}.tar.gz",change=change),"supported schema 7")
    def test_configuration_divergence(self):
        path=self.changed("diverge.tar.gz",source=self.valid,change=json_change(lambda n:n.endswith("/run.json") and "rep3/" in n,lambda o:o.__setitem__("model","different")));self.assert_invalid(path,"persisted model identities agree")
    def test_slentore_version_divergence(self):
        path=self.changed("slentore-diverge.tar.gz",source=self.valid,change=json_change(lambda n:n.endswith("/run.json") and "rep3/" in n,lambda o:o.__setitem__("slentore_version","vDIFFERENT")));self.assert_invalid(path,"Slentore version=")
    def test_report_uses_validated_run_configuration(self):
        run=analyze.audit(RAW).runs[0];run.run["model"]="Validated/Model"
        out=self.root/"report-config";out.mkdir();analyze.report([run],out,"digest",True,{"analyzer_sha256":"sha","git_sha":None,"git_tracked":False,"git_dirty":True,"provenance_complete":False})
        text=(out/"exploratory_report.md").read_text();self.assertIn("Validated/Model",text);self.assertNotIn("Qwen/Qwen3.8-27B",text)
    def test_checksum_mismatch(self):
        with self.assertRaisesRegex(analyze.ValidationError,"SHA-256 mismatch"):analyze.verify_sha(RAW,"0"*64)
    def test_stale_output_isolation(self):
        out=self.root/"isolated"; code,strict=analyze.execute(RAW,out,"strict",analyze.sha256(RAW));self.assertEqual(code,2);self.assertEqual({p.name for p in strict.iterdir()},{"validation.json","validation.md","manifest.json"});code,explore=analyze.execute(RAW,out,"exploratory",analyze.sha256(RAW));self.assertEqual(code,0);self.assertNotEqual(strict,explore);self.assertFalse((strict/"plots").exists())
    def test_valid_three_run_plots_and_report(self):
        out=self.root/"three-output";code,path=analyze.execute(self.valid,out,"strict",analyze.sha256(self.valid));self.assertEqual(code,0);self.assertTrue((path/"experiment_report.md").exists());self.assertEqual(len(list((path/"plots").glob("*.png"))),14);self.assertEqual(len(list((path/"plots").glob("*.svg"))),14)

    def test_analyzer_provenance_states(self):
        script = HERE/"analyze.py"
        def run_for(tracked, dirty):
            def fake(command, **kwargs):
                if "rev-parse" in command: return SimpleNamespace(returncode=0, stdout="abc123\n")
                if "ls-files" in command: return SimpleNamespace(returncode=0 if tracked else 1, stdout=f"{script}\n" if tracked else "")
                return SimpleNamespace(returncode=0, stdout=" M analysis/scripts/analyze.py\n" if dirty else "")
            with patch.object(analyze.subprocess, "run", side_effect=fake): return analyze.analyzer_provenance(script)
        untracked, changed, clean = run_for(False, True), run_for(True, True), run_for(True, False)
        self.assertFalse(untracked["git_tracked"] or untracked["provenance_complete"])
        self.assertTrue(changed["git_tracked"] and changed["git_dirty"]); self.assertFalse(changed["provenance_complete"])
        self.assertTrue(clean["git_tracked"] and not clean["git_dirty"] and clean["provenance_complete"])
        self.assertEqual(clean["analyzer_sha256"], analyze.sha256(script)); self.assertEqual(clean["git_sha"], "abc123")

if __name__ == "__main__": unittest.main()
