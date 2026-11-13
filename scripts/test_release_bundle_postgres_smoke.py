import hashlib
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.release_bundle_postgres_smoke import (
    SmokeError,
    isolated_environment,
    submit_job,
    wait_for_job,
)


ROOT = Path(__file__).resolve().parents[1]


class ReleaseBundlePostgresSmokeTest(unittest.TestCase):
    def test_isolated_environment_keeps_only_database_connection(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            environment = isolated_environment(root, "postgres://db/test")
            self.assertEqual("postgres://db/test", environment["DATABASE_URL"])
            self.assertEqual(root / ".postgres-runtime" / "empty-path", Path(environment["PATH"]))
            self.assertEqual([], list(Path(environment["PATH"]).iterdir()))
            self.assertNotIn("GOPATH", environment)

    @patch("scripts.release_bundle_postgres_smoke.http_json")
    def test_wait_for_job_accepts_expected_terminal_attempt(self, request):
        request.side_effect = [
            (200, {"state": "running"}, ""),
            (200, {"id": "job-1", "state": "succeeded", "attempts": 2}, ""),
        ]
        result = wait_for_job("http://api", "/v1/jobs/job-1", 2)
        self.assertEqual("job-1", result["id"])

    @patch("scripts.release_bundle_postgres_smoke.http_json")
    def test_wait_for_job_rejects_terminal_attempt_mismatch(self, request):
        request.return_value = (
            200,
            {"id": "job-1", "state": "succeeded", "attempts": 1},
            "",
        )
        with self.assertRaisesRegex(SmokeError, "unexpected attempt count"):
            wait_for_job("http://api", "/v1/jobs/job-1", 2)

    @patch("scripts.release_bundle_postgres_smoke.wait_for_job")
    @patch("scripts.release_bundle_postgres_smoke.http_json")
    def test_submit_job_requires_expected_checksum(self, request, wait):
        payload = "durable"
        request.return_value = (202, {"id": "job-1"}, "/v1/jobs/job-1")
        wait.return_value = {
            "id": "job-1",
            "state": "succeeded",
            "attempts": 1,
            "result": hashlib.sha256(payload.encode()).hexdigest(),
        }
        result = submit_job("http://api", "key", payload, 0)
        self.assertEqual("job-1", result["id"])

    def test_workflow_runs_durable_package_gate_without_setup_go(self):
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )
        packaged = workflow.split("  packaged-postgres-smoke:\n", 1)[1]
        self.assertIn("    needs: [packaged-smoke]\n", packaged)
        self.assertIn("      postgres:\n", packaged)
        self.assertIn('gh run download "$GITHUB_RUN_ID"', packaged)
        self.assertIn(
            'make release-bundle-postgres-smoke RELEASE_SHA="$GITHUB_SHA"',
            packaged,
        )
        self.assertNotIn("actions/setup-go", packaged)


if __name__ == "__main__":
    unittest.main()
