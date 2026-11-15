import copy
import hashlib
import stat
import tempfile
import unittest
import zipfile
from pathlib import Path

from scripts.candidate_evidence_summary import REQUIRED_JOBS, expected_artifact_names
from scripts.release_bundle import canonical_json
from scripts.verify_candidate_evidence_summary import (
    SummaryReviewError,
    read_artifact,
    verify_binding,
)


REPOSITORY = "w4llisz/small-chain-platform"
SHA = "a" * 40
RUN_ID = 123456
VERSION = "v0.1.0"


def valid_summary() -> dict[str, object]:
    names = expected_artifact_names(SHA)
    keys = (
        "candidate",
        "release_bundle",
        "packaged_smoke",
        "packaged_postgres_smoke",
    )
    artifacts = []
    for index, key in enumerate(keys, start=1):
        artifact_id = 1000 + index
        artifacts.append(
            {
                "github_digest": f"sha256:{index:064x}",
                "id": artifact_id,
                "name": names[key],
                "payload_sha256": f"{index + 10:064x}",
                "size_bytes": index * 100,
                "url": (
                    f"https://github.com/{REPOSITORY}/actions/runs/"
                    f"{RUN_ID}/artifacts/{artifact_id}"
                ),
            }
        )
    release_digest = artifacts[1]["payload_sha256"]
    return {
        "artifacts": artifacts,
        "candidate": {
            "commit_url": f"https://github.com/{REPOSITORY}/commit/{SHA}",
            "repository": REPOSITORY,
            "run_id": RUN_ID,
            "run_url": f"https://github.com/{REPOSITORY}/actions/runs/{RUN_ID}",
            "sha": SHA,
            "version": VERSION,
        },
        "checks": {
            "archive_digest_consensus": True,
            "artifact_identity_binding": True,
            "attestation_binding": True,
            "packaged_postgres_restart_evidence": True,
            "packaged_runtime_evidence": True,
            "required_jobs_successful": True,
        },
        "release_archive_sha256": release_digest,
        "required_jobs": list(REQUIRED_JOBS),
        "schema_version": 1,
    }


class CandidateEvidenceSummaryReviewTest(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.archive = Path(self.tempdir.name) / "summary.zip"

    def tearDown(self):
        self.tempdir.cleanup()

    def write_archive(
        self,
        document: dict[str, object] | None = None,
        *,
        raw: bytes | None = None,
        extra: bool = False,
        symlink: bool = False,
    ) -> str:
        content = raw if raw is not None else canonical_json(document or valid_summary())
        with zipfile.ZipFile(self.archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
            if symlink:
                info = zipfile.ZipInfo("summary.json")
                info.create_system = 3
                info.external_attr = (stat.S_IFLNK | 0o777) << 16
                bundle.writestr(info, content)
            else:
                bundle.writestr("summary.json", content)
            if extra:
                bundle.writestr("extra.json", b"{}")
        return f"sha256:{hashlib.sha256(self.archive.read_bytes()).hexdigest()}"

    def verify(self, document: dict[str, object]) -> None:
        verify_binding(
            document,
            expected_repository=REPOSITORY,
            expected_sha=SHA,
            expected_run_id=RUN_ID,
            expected_version=VERSION,
        )

    def test_valid_artifact_and_binding(self):
        expected_digest = self.write_archive()
        document, observed = read_artifact(self.archive, expected_digest)
        self.verify(document)
        self.assertEqual(f"sha256:{observed}", expected_digest)

    def test_rejects_archive_digest_substitution(self):
        self.write_archive()
        with self.assertRaisesRegex(SummaryReviewError, "artifact digest mismatch"):
            read_artifact(self.archive, f"sha256:{'f' * 64}")

    def test_rejects_extra_archive_entry(self):
        digest = self.write_archive(extra=True)
        with self.assertRaisesRegex(SummaryReviewError, "exactly summary.json"):
            read_artifact(self.archive, digest)

    def test_rejects_symlink_entry(self):
        digest = self.write_archive(symlink=True)
        with self.assertRaisesRegex(SummaryReviewError, "regular unencrypted"):
            read_artifact(self.archive, digest)

    def test_rejects_noncanonical_json(self):
        digest = self.write_archive(raw=b'{"schema_version": 1}\n')
        with self.assertRaisesRegex(SummaryReviewError, "canonical JSON"):
            read_artifact(self.archive, digest)

    def test_rejects_wrong_candidate_binding(self):
        document = valid_summary()
        document["candidate"]["run_id"] = RUN_ID + 1
        with self.assertRaisesRegex(SummaryReviewError, "candidate does not match"):
            self.verify(document)

    def test_rejects_artifact_order_or_name_substitution(self):
        document = valid_summary()
        document["artifacts"][0], document["artifacts"][1] = (
            document["artifacts"][1],
            document["artifacts"][0],
        )
        with self.assertRaisesRegex(SummaryReviewError, "not bound to the candidate"):
            self.verify(document)

    def test_rejects_repeated_artifact_identity(self):
        document = valid_summary()
        document["artifacts"][1]["id"] = document["artifacts"][0]["id"]
        document["artifacts"][1]["url"] = document["artifacts"][0]["url"]
        with self.assertRaisesRegex(SummaryReviewError, "artifact IDs must be unique"):
            self.verify(document)

    def test_rejects_release_archive_digest_disagreement(self):
        document = valid_summary()
        document["release_archive_sha256"] = "f" * 64
        with self.assertRaisesRegex(SummaryReviewError, "differs from the release-bundle"):
            self.verify(document)

    def test_rejects_false_required_check(self):
        document = valid_summary()
        document["checks"]["required_jobs_successful"] = False
        with self.assertRaisesRegex(SummaryReviewError, "every required check passing"):
            self.verify(document)

    def test_rejects_unexpected_schema_field(self):
        document = copy.deepcopy(valid_summary())
        document["candidate"]["tag"] = "v0.1.0"
        with self.assertRaisesRegex(SummaryReviewError, "candidate keys differ"):
            self.verify(document)


if __name__ == "__main__":
    unittest.main()
