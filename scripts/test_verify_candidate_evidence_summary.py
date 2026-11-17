import copy
import hashlib
import json
import stat
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

from scripts.candidate_evidence_summary import REQUIRED_JOBS, expected_artifact_names
from scripts.release_bundle import canonical_json
from scripts.verify_candidate_evidence_summary import (
    SummaryReviewError,
    MAX_REPORT_BYTES,
    read_artifact,
    render_report,
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

    def run_report(self, digest, output, *, sha=SHA):
        return subprocess.run(
            [sys.executable, str(Path(__file__).with_name("verify_candidate_evidence_summary.py")),
             "--archive", str(self.archive), "--digest", digest,
             "--repository", REPOSITORY, "--sha", sha, "--run-id", str(RUN_ID),
             "--version", VERSION, "--report", str(output)],
            capture_output=True, text=True, timeout=10,
        )

    def test_report_cli_is_deterministic_and_cites_every_input(self):
        digest = self.write_archive()
        first = self.archive.with_name("first.md")
        second = self.archive.with_name("second.md")
        for output in (first, second):
            result = self.run_report(digest, output)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("PASS:", result.stdout)
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertLessEqual(len(first.read_bytes()), MAX_REPORT_BYTES)
        text = first.read_text()
        data = valid_summary()
        for artifact in data["artifacts"]:
            for field in ("url", "name", "github_digest", "payload_sha256"):
                self.assertIn(artifact[field], text)
        for field in ("commit_url", "run_url", "version", "sha"):
            self.assertIn(data["candidate"][field], text)
        self.assertIn(digest.removeprefix("sha256:"), text)
        self.assertIn(hashlib.sha256(canonical_json(data)).hexdigest(), text)
        for job in REQUIRED_JOBS:
            self.assertIn(f"`{job}`", text)
        self.assertIn("does not authorize a tag or release", text)
        self.assertIn("does not prove production readiness", text)
        self.assertIn("not download or independently re-run", text)
        self.assertNotIn(self.tempdir.name, text)

    def test_report_does_not_depend_on_dictionary_insertion_order(self):
        data = valid_summary()
        reversed_data = dict(reversed(list(data.items())))
        reversed_data["checks"] = dict(reversed(list(data["checks"].items())))
        self.assertEqual(render_report(data, "b" * 64), render_report(reversed_data, "b" * 64))

    def test_invalid_evidence_never_creates_report(self):
        output = self.archive.with_name("review.md")
        cases = [(valid_summary(), "f" * 40, None),
                 (valid_summary(), SHA, "sha256:" + "f" * 64)]
        invalid = valid_summary()
        invalid["checks"]["required_jobs_successful"] = False
        cases.append((invalid, SHA, None))
        for data, sha, override_digest in cases:
            with self.subTest(sha=sha, override_digest=override_digest):
                digest = self.write_archive(data)
                result = self.run_report(override_digest or digest, output, sha=sha)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("PASS:", result.stdout)
                self.assertFalse(output.exists())

    def test_report_refuses_existing_file_and_source_aliases(self):
        digest = self.write_archive()
        previous = self.archive.with_name("previous.md")
        previous.write_text("previous review\n")
        symlink = self.archive.with_name("source-link.md")
        symlink.symlink_to(self.archive)
        hardlink = self.archive.with_name("source-hardlink.md")
        hardlink.hardlink_to(self.archive)
        original = self.archive.read_bytes()
        for output in (previous, self.archive, symlink, hardlink):
            with self.subTest(output=output.name):
                result = self.run_report(digest, output)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("PASS:", result.stdout)
                self.assertEqual(self.archive.read_bytes(), original)
                self.assertEqual(previous.read_text(), "previous review\n")

    def test_report_size_limit_fails_before_writing(self):
        data = valid_summary()
        data["candidate"]["version"] = "v" + "1" * MAX_REPORT_BYTES + ".0.0"
        with self.assertRaisesRegex(SummaryReviewError, "16 KiB"):
            render_report(data, "b" * 64)

    def test_preserved_historical_report_matches_verified_summary(self):
        directory = Path(__file__).resolve().parents[1] / "docs/releases/9e37407"
        data = json.loads((directory / "summary.json").read_bytes())
        verify_binding(
            data, expected_repository=REPOSITORY,
            expected_sha="9e3740752ad5d05bd46662262776fea5078cb4bf",
            expected_run_id=43584342944, expected_version=VERSION,
        )
        # Historical ZIP digest from the independent review's GitHub metadata.
        digest = "ae890c76570069f9cda9ca1774af7f239a4315ff5de3f39c793ca3a773027e03"
        self.assertEqual((directory / "review.md").read_bytes(), render_report(data, digest))


if __name__ == "__main__":
    unittest.main()
