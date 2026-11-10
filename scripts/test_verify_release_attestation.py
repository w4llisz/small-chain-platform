import hashlib
import json
import stat
import tempfile
import unittest
import zipfile
from pathlib import Path

from scripts.verify_release_attestation import (
    MAX_ARCHIVE_BYTES,
    ReviewError,
    read_artifact,
    verify_binding,
)


ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "w4llisz/small-chain-platform"
SHA = "a" * 40
RUN_ID = 12345


def valid_payload() -> dict[str, object]:
    return {
        "schema_version": 1,
        "candidate": {
            "commit_url": f"https://github.com/{REPOSITORY}/commit/{SHA}",
            "ref": "refs/heads/main",
            "repository": REPOSITORY,
            "sha": SHA,
        },
        "source": {
            "clean_checkout": True,
            "forbidden_tracked_paths": [],
            "tracked_file_count": 149,
        },
        "workflow": {
            "name": "CI",
            "required_jobs": [
                "e2e",
                "incident",
                "measurement",
                "postgres",
                "verify",
                "web",
            ],
            "required_jobs_conclusion": "success",
            "run_id": RUN_ID,
            "run_url": f"https://github.com/{REPOSITORY}/actions/runs/{RUN_ID}",
        },
    }


class VerifyReleaseAttestationTest(unittest.TestCase):
    def make_archive(
        self,
        payload: dict[str, object] | None = None,
        *,
        extra_entry: bool = False,
        symlink: bool = False,
    ) -> tuple[Path, str, tempfile.TemporaryDirectory]:
        temporary = tempfile.TemporaryDirectory()
        archive = Path(temporary.name) / "candidate.zip"
        raw = json.dumps(payload or valid_payload(), indent=2, sort_keys=True) + "\n"
        with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
            if symlink:
                entry = zipfile.ZipInfo("attestation.json")
                entry.create_system = 3
                entry.external_attr = (stat.S_IFLNK | 0o777) << 16
                bundle.writestr(entry, "target")
            else:
                bundle.writestr("attestation.json", raw)
            if extra_entry:
                bundle.writestr("unexpected.txt", "unexpected")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        return archive, f"sha256:{digest}", temporary

    def test_valid_artifact_and_binding_pass(self):
        archive, digest, temporary = self.make_archive()
        self.addCleanup(temporary.cleanup)
        data, observed = read_artifact(archive, digest)
        verify_binding(
            data,
            expected_repository=REPOSITORY,
            expected_sha=SHA,
            expected_run_id=RUN_ID,
        )
        self.assertEqual(f"sha256:{observed}", digest)

    def test_digest_substitution_is_rejected(self):
        archive, _digest, temporary = self.make_archive()
        self.addCleanup(temporary.cleanup)
        with self.assertRaisesRegex(ReviewError, "digest mismatch"):
            read_artifact(archive, "sha256:" + "0" * 64)

    def test_extra_archive_entry_is_rejected(self):
        archive, digest, temporary = self.make_archive(extra_entry=True)
        self.addCleanup(temporary.cleanup)
        with self.assertRaisesRegex(ReviewError, "exactly attestation.json"):
            read_artifact(archive, digest)

    def test_oversized_archive_is_rejected_before_extraction(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        archive = Path(temporary.name) / "oversized.zip"
        archive.write_bytes(b"x" * (MAX_ARCHIVE_BYTES + 1))
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        with self.assertRaisesRegex(ReviewError, "64 KiB"):
            read_artifact(archive, f"sha256:{digest}")

    def test_symbolic_link_entry_is_rejected(self):
        archive, digest, temporary = self.make_archive(symlink=True)
        self.addCleanup(temporary.cleanup)
        with self.assertRaisesRegex(ReviewError, "symbolic link"):
            read_artifact(archive, digest)

    def test_wrong_commit_binding_is_rejected(self):
        archive, digest, temporary = self.make_archive()
        self.addCleanup(temporary.cleanup)
        data, _observed = read_artifact(archive, digest)
        with self.assertRaisesRegex(ReviewError, "candidate does not match"):
            verify_binding(
                data,
                expected_repository=REPOSITORY,
                expected_sha="b" * 40,
                expected_run_id=RUN_ID,
            )

    def test_preserved_first_review_matches_its_commit_and_run(self):
        path = ROOT / "docs" / "releases" / "a401fe1" / "attestation.json"
        data = json.loads(path.read_text(encoding="utf-8"))
        verify_binding(
            data,
            expected_repository="w4llisz/small-chain-platform",
            expected_sha="a401fe147a7cfbdc7b8e263144a3b315e860eec0",
            expected_run_id=42605311933,
        )


if __name__ == "__main__":
    unittest.main()
