import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from scripts.candidate_evidence_summary import (
    REQUIRED_JOBS,
    SummaryError,
    build_summary,
    expected_artifact_names,
)
from scripts.release_attestation import REQUIRED_JOBS as ATTESTATION_JOBS
from scripts.release_bundle import canonical_json


ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "w4llisz/small-chain-platform"
SHA = "a" * 40
RUN_ID = 12345
VERSION = "v0.1.0"


class CandidateEvidenceSummaryTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.archive = self.root / "release.tar.gz"
        self.archive.write_bytes(b"release archive")
        self.archive_digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()

        self.attestation = self.root / "attestation.json"
        self.write_json(
            self.attestation,
            {
                "candidate": {
                    "commit_url": f"https://github.com/{REPOSITORY}/commit/{SHA}",
                    "ref": "refs/heads/main",
                    "repository": REPOSITORY,
                    "sha": SHA,
                },
                "schema_version": 1,
                "source": {
                    "clean_checkout": True,
                    "forbidden_tracked_paths": [],
                    "tracked_file_count": 100,
                },
                "workflow": {
                    "name": "CI",
                    "required_jobs": list(ATTESTATION_JOBS),
                    "required_jobs_conclusion": "success",
                    "run_id": RUN_ID,
                    "run_url": f"https://github.com/{REPOSITORY}/actions/runs/{RUN_ID}",
                },
            },
        )

        self.memory = self.root / "memory.json"
        self.write_json(
            self.memory,
            {
                "archive_sha256": self.archive_digest,
                "checks": {
                    "checksum_job": True,
                    "graceful_sigterm": True,
                    "packaged_healthcheck": True,
                    "source_free_runtime": True,
                    "toolchain_free_runtime_path": True,
                },
                "commit": SHA,
                "job": {
                    "attempts": 2,
                    "job_id": "b" * 32,
                    "result": "c" * 64,
                    "service_exit_code": 0,
                },
                "repository": REPOSITORY,
                "schema_version": 1,
                "version": VERSION,
            },
        )

        self.postgres = self.root / "postgres.json"
        self.write_json(
            self.postgres,
            {
                "archive_sha256": self.archive_digest,
                "checks": {
                    "all_packaged_binaries_exercised": True,
                    "durable_result_survived_restart": True,
                    "graceful_process_shutdown": True,
                    "idempotent_migration": True,
                    "source_free_runtime": True,
                    "toolchain_free_runtime_path": True,
                },
                "commit": SHA,
                "repository": REPOSITORY,
                "result": {
                    "after_restart": {
                        "attempts": 1,
                        "job_id": "d" * 32,
                        "result": "e" * 64,
                    },
                    "before_restart": {
                        "attempts": 2,
                        "job_id": "f" * 32,
                        "result": "1" * 64,
                    },
                    "graceful_exit_codes": [0, 0, 0, 0],
                    "migration_runs": 2,
                },
                "schema_version": 1,
                "version": VERSION,
            },
        )

        self.jobs = self.root / "jobs.json"
        self.write_json(
            self.jobs,
            {
                "jobs": [
                    {
                        "conclusion": "success",
                        "name": name,
                        "run_id": RUN_ID,
                        "status": "completed",
                    }
                    for name in REQUIRED_JOBS
                ]
            },
            canonical=False,
        )

        self.artifacts = self.root / "artifacts.json"
        names = expected_artifact_names(SHA)
        self.write_json(
            self.artifacts,
            {
                "artifacts": [
                    {
                        "digest": "sha256:" + str(index) * 64,
                        "expired": False,
                        "id": index,
                        "name": name,
                        "size_in_bytes": 100 + index,
                        "workflow_run": {"head_sha": SHA, "id": RUN_ID},
                    }
                    for index, name in enumerate(names.values(), start=1)
                ]
            },
            canonical=False,
        )

    def tearDown(self):
        self.temporary.cleanup()

    def write_json(self, path, value, canonical=True):
        if canonical:
            path.write_bytes(canonical_json(value))
        else:
            path.write_text(json.dumps(value), encoding="utf-8")

    def build(self):
        return build_summary(
            repository=REPOSITORY,
            sha=SHA,
            run_id=RUN_ID,
            version=VERSION,
            artifact_metadata=self.artifacts,
            jobs_metadata=self.jobs,
            attestation=self.attestation,
            release_bundle=self.archive,
            packaged_smoke=self.memory,
            packaged_postgres_smoke=self.postgres,
        )

    def test_builds_strict_commit_run_bound_summary(self):
        summary = self.build()
        self.assertEqual(SHA, summary["candidate"]["sha"])
        self.assertEqual(RUN_ID, summary["candidate"]["run_id"])
        self.assertEqual(self.archive_digest, summary["release_archive_sha256"])
        self.assertEqual(list(REQUIRED_JOBS), summary["required_jobs"])
        self.assertEqual(4, len(summary["artifacts"]))
        self.assertTrue(all(summary["checks"].values()))

    def test_rejects_archive_digest_divergence(self):
        value = json.loads(self.memory.read_text(encoding="utf-8"))
        value["archive_sha256"] = "0" * 64
        self.write_json(self.memory, value)
        with self.assertRaisesRegex(SummaryError, "archive digest differs"):
            self.build()

    def test_rejects_artifact_from_another_commit(self):
        value = json.loads(self.artifacts.read_text(encoding="utf-8"))
        value["artifacts"][0]["workflow_run"]["head_sha"] = "0" * 40
        self.write_json(self.artifacts, value, canonical=False)
        with self.assertRaisesRegex(SummaryError, "not bound to the candidate"):
            self.build()

    def test_rejects_failed_required_job(self):
        value = json.loads(self.jobs.read_text(encoding="utf-8"))
        value["jobs"][0]["conclusion"] = "failure"
        self.write_json(self.jobs, value, canonical=False)
        with self.assertRaisesRegex(SummaryError, "did not complete successfully"):
            self.build()

    def test_rejects_noncanonical_smoke_evidence(self):
        value = json.loads(self.memory.read_text(encoding="utf-8"))
        self.memory.write_text(json.dumps(value), encoding="utf-8")
        with self.assertRaisesRegex(SummaryError, "canonical JSON"):
            self.build()

    def test_workflow_collects_evidence_after_both_package_gates(self):
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        summary = workflow.split("  candidate-evidence-summary:\n", 1)[1]
        self.assertIn("    needs: [packaged-postgres-smoke]\n", summary)
        self.assertIn('make candidate-evidence-summary RELEASE_SHA="$GITHUB_SHA" RELEASE_RUN_ID="$GITHUB_RUN_ID"', summary)
        self.assertIn('name: candidate-evidence-summary-${{ github.sha }}', summary)
        self.assertNotIn("actions/setup-go", summary)


if __name__ == "__main__":
    unittest.main()
