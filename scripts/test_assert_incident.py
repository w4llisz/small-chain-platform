import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from scripts.assert_incident import (
    Incident,
    validate_incident,
    verify_manifest,
    write_manifest,
    write_report,
)


COMMIT = "1" * 40
JOB_ID = "2" * 32
PAYLOAD = "controlled worker crash"


def event(identifier, kind, state, attempt, owner=None, version=None, message=None):
    value = {"id": str(identifier), "kind": kind, "state": state, "attempt": attempt,
             "created_at": "2026-11-03T10:00:00Z"}
    if owner:
        value["lease_owner"] = owner
    if version:
        value["lease_version"] = version
    if message:
        value["message"] = message
    return value


def fixture():
    digest = hashlib.sha256(PAYLOAD.encode()).hexdigest()
    return {
        "context.json": {"schema_version": 1, "scenario": "worker-sigkill-recovery",
                         "source_commit": COMMIT, "job_id": JOB_ID,
                         "killed_owner": "incident-worker-a", "recovery_owner": "incident-worker-b",
                         "payload_sha256": digest},
        "job.json": {"id": JOB_ID, "state": "succeeded", "attempts": 2, "result": digest,
                     "spec": {"payload": PAYLOAD, "max_attempts": 2}},
        "events.json": {"events": [
            event(1, "submitted", "queued", 0),
            event(2, "claimed", "running", 1, "incident-worker-a", 1),
            event(3, "lease_recovered", "retrying", 1, "incident-worker-a", 2,
                  "worker lease expired before completion"),
            event(4, "claimed", "running", 2, "incident-worker-b", 3),
            event(5, "succeeded", "succeeded", 2, "incident-worker-b", 3),
        ]},
        "worker-a-inspect.json": [{"State": {"ExitCode": 137, "OOMKilled": False}}],
        "worker-b-metrics.prom": "small_chain_worker_attempt_duration_seconds_count 1\n",
    }


class IncidentEvidenceTest(unittest.TestCase):
    def validate(self, mutate=None):
        files = fixture()
        if mutate:
            mutate(files)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in files.items():
                if isinstance(value, str):
                    (root / name).write_text(value, encoding="utf-8")
                else:
                    (root / name).write_text(json.dumps(value), encoding="utf-8")
            return validate_incident(root, COMMIT)

    def test_accepts_fenced_recovery_sequence(self):
        incident = self.validate()
        self.assertEqual(incident.job_id, JOB_ID)

    def test_rejects_wrong_recovery_fence(self):
        with self.assertRaisesRegex(ValueError, "unexpected lifecycle event"):
            self.validate(lambda files: files["events.json"]["events"][2].update(lease_version=1))

    def test_rejects_non_sigkill_exit(self):
        with self.assertRaisesRegex(ValueError, "not deliberately SIGKILLed"):
            self.validate(lambda files: files["worker-a-inspect.json"][0]["State"].update(ExitCode=1))

    def test_rejects_missing_recovery_attempt_metric(self):
        with self.assertRaisesRegex(ValueError, "exactly one"):
            self.validate(lambda files: files.update({"worker-b-metrics.prom":
                                                       "small_chain_worker_attempt_duration_seconds_count 0\n"}))

    def test_writes_deterministic_report_and_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "events.json").write_text("{}\n", encoding="utf-8")
            report = root / "README.md"
            manifest = root / "manifest.sha256"
            write_report(report, Incident(COMMIT, JOB_ID, "incident-worker-a", "incident-worker-b", 1))
            write_manifest(root, manifest)
            self.assertIn("lease version 1 claim → version 2 recovery", report.read_text(encoding="utf-8"))
            manifest_text = manifest.read_text(encoding="utf-8")
            self.assertIn("README.md", manifest_text)
            self.assertIn("events.json", manifest_text)
            self.assertNotIn("manifest.sha256", manifest_text)
            self.assertEqual(verify_manifest(root, manifest), 2)

    def test_manifest_rejects_changed_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / "events.json"
            evidence.write_text("{}\n", encoding="utf-8")
            manifest = root / "manifest.sha256"
            write_manifest(root, manifest)
            evidence.write_text('{"events": []}\n', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "digest mismatch: events.json"):
                verify_manifest(root, manifest)

    def test_manifest_requires_exact_file_coverage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "events.json").write_text("{}\n", encoding="utf-8")
            manifest = root / "manifest.sha256"
            write_manifest(root, manifest)
            (root / "unreviewed.log").write_text("new evidence\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "missing=\\['unreviewed.log'\\]"):
                verify_manifest(root, manifest)

    def test_manifest_rejects_nested_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "events.json").write_text("{}\n", encoding="utf-8")
            manifest = root / "manifest.sha256"
            write_manifest(root, manifest)
            (root / "nested").mkdir()
            with self.assertRaisesRegex(ValueError, "unsupported.*nested"):
                verify_manifest(root, manifest)


if __name__ == "__main__":
    unittest.main()
