import json
import subprocess
import tempfile
import unittest
from pathlib import Path

from scripts.release_attestation import AttestationError, build_attestation


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"


class ReleaseAttestationTest(unittest.TestCase):
    def make_repository(self, tracked_path: str = "README.md") -> tuple[Path, str, tempfile.TemporaryDirectory]:
        temporary = tempfile.TemporaryDirectory()
        root = Path(temporary.name)
        path = root / tracked_path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("candidate\n", encoding="utf-8")
        subprocess.run(("git", "init", "-q", "-b", "main"), cwd=root, check=True)
        subprocess.run(("git", "config", "user.name", "test"), cwd=root, check=True)
        subprocess.run(("git", "config", "user.email", "test@example.invalid"), cwd=root, check=True)
        subprocess.run(("git", "add", "."), cwd=root, check=True)
        subprocess.run(("git", "commit", "-q", "-m", "candidate"), cwd=root, check=True)
        sha = subprocess.run(
            ("git", "rev-parse", "HEAD"),
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        return root, sha, temporary

    def test_clean_main_checkout_produces_commit_bound_payload(self):
        root, sha, temporary = self.make_repository()
        self.addCleanup(temporary.cleanup)

        payload = build_attestation(
            root,
            sha=sha,
            run_id=12345,
            repository="w4llisz/small-chain-platform",
            ref="refs/heads/main",
        )

        self.assertEqual(payload["schema_version"], 1)
        self.assertEqual(payload["candidate"]["sha"], sha)
        self.assertEqual(payload["source"]["tracked_file_count"], 1)
        self.assertEqual(payload["source"]["forbidden_tracked_paths"], [])
        self.assertEqual(
            payload["workflow"]["required_jobs"],
            ["e2e", "incident", "measurement", "postgres", "verify", "web"],
        )
        json.dumps(payload, sort_keys=True)

    def test_dirty_checkout_is_rejected(self):
        root, sha, temporary = self.make_repository()
        self.addCleanup(temporary.cleanup)
        (root / "README.md").write_text("changed\n", encoding="utf-8")

        with self.assertRaisesRegex(AttestationError, "checkout is not clean"):
            build_attestation(
                root,
                sha=sha,
                run_id=1,
                repository="w4llisz/small-chain-platform",
                ref="refs/heads/main",
            )

    def test_checkout_sha_mismatch_is_rejected(self):
        root, _sha, temporary = self.make_repository()
        self.addCleanup(temporary.cleanup)

        with self.assertRaisesRegex(AttestationError, "does not match candidate SHA"):
            build_attestation(
                root,
                sha="0" * 40,
                run_id=1,
                repository="w4llisz/small-chain-platform",
                ref="refs/heads/main",
            )

    def test_forbidden_tracked_release_file_is_rejected(self):
        root, sha, temporary = self.make_repository("coverage.out")
        self.addCleanup(temporary.cleanup)

        with self.assertRaisesRegex(AttestationError, "coverage.out"):
            build_attestation(
                root,
                sha=sha,
                run_id=1,
                repository="w4llisz/small-chain-platform",
                ref="refs/heads/main",
            )

    def test_workflow_attestation_depends_on_every_evidence_job(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("candidate-attestation:", workflow)
        self.assertIn(
            "needs: [web, e2e, verify, postgres, measurement, incident]",
            workflow,
        )
        self.assertIn("candidate-${{ github.sha }}", workflow)
        self.assertIn("retention-days: 30", workflow)


if __name__ == "__main__":
    unittest.main()
