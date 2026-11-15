#!/usr/bin/env python3
"""Verify a downloaded candidate-evidence summary artifact offline."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import stat
import sys
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from scripts.candidate_evidence_summary import (  # noqa: E402
    REQUIRED_JOBS,
    expected_artifact_names,
)
from scripts.release_attestation import REPOSITORY_PATTERN, SHA_PATTERN  # noqa: E402
from scripts.release_bundle import VERSION_PATTERN, canonical_json  # noqa: E402


DIGEST_PATTERN = re.compile(r"sha256:([0-9a-f]{64})")
HEX_64_PATTERN = re.compile(r"[0-9a-f]{64}")
MAX_ARCHIVE_BYTES = 64 * 1024
MAX_SUMMARY_BYTES = 32 * 1024
CHECKS = {
    "archive_digest_consensus",
    "artifact_identity_binding",
    "attestation_binding",
    "packaged_postgres_restart_evidence",
    "packaged_runtime_evidence",
    "required_jobs_successful",
}
ARTIFACT_ORDER = (
    "candidate",
    "release_bundle",
    "packaged_smoke",
    "packaged_postgres_smoke",
)


class SummaryReviewError(ValueError):
    """The artifact cannot support the requested offline review."""


def require_keys(value: object, expected: set[str], context: str) -> dict[str, object]:
    if not isinstance(value, dict):
        raise SummaryReviewError(f"{context} must be an object")
    observed = set(value)
    if observed != expected:
        raise SummaryReviewError(
            f"{context} keys differ: missing={sorted(expected - observed)}, "
            f"unexpected={sorted(observed - expected)}"
        )
    return value


def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise SummaryReviewError(f"duplicate JSON key: {key}")
        value[key] = item
    return value


def read_artifact(archive: Path, expected_digest: str) -> tuple[dict[str, object], str]:
    match = DIGEST_PATTERN.fullmatch(expected_digest)
    if not match:
        raise SummaryReviewError("artifact digest must use sha256:<64 lowercase hex> form")
    if archive.stat().st_size > MAX_ARCHIVE_BYTES:
        raise SummaryReviewError("artifact exceeds the 64 KiB review limit")

    observed_digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if observed_digest != match.group(1):
        raise SummaryReviewError(
            f"artifact digest mismatch: observed={observed_digest}, expected={match.group(1)}"
        )

    try:
        with zipfile.ZipFile(archive) as bundle:
            entries = bundle.infolist()
            if [entry.filename for entry in entries] != ["summary.json"]:
                raise SummaryReviewError("artifact must contain exactly summary.json")
            entry = entries[0]
            mode = entry.external_attr >> 16
            file_type = stat.S_IFMT(mode)
            if (
                entry.is_dir()
                or entry.flag_bits & 0x1
                or stat.S_ISLNK(mode)
                or file_type not in (0, stat.S_IFREG)
            ):
                raise SummaryReviewError("summary.json must be a regular unencrypted file")
            if entry.file_size > MAX_SUMMARY_BYTES:
                raise SummaryReviewError("summary.json exceeds the 32 KiB review limit")
            raw = bundle.read(entry)
    except zipfile.BadZipFile as exc:
        raise SummaryReviewError("artifact is not a valid ZIP file") from exc

    try:
        data = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    except UnicodeDecodeError as exc:
        raise SummaryReviewError("summary.json must be UTF-8") from exc
    except json.JSONDecodeError as exc:
        raise SummaryReviewError(f"summary.json is invalid JSON: {exc.msg}") from exc
    if not isinstance(data, dict):
        raise SummaryReviewError("summary root must be an object")
    if raw != canonical_json(data):
        raise SummaryReviewError("summary.json must use canonical JSON")
    return data, observed_digest


def validate_expected_inputs(repository: str, sha: str, run_id: int, version: str) -> None:
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise SummaryReviewError("expected repository must use owner/name form")
    if not SHA_PATTERN.fullmatch(sha):
        raise SummaryReviewError("expected SHA must be 40 lowercase hexadecimal characters")
    if run_id <= 0:
        raise SummaryReviewError("expected run ID must be positive")
    if not VERSION_PATTERN.fullmatch(version):
        raise SummaryReviewError("expected version must use vMAJOR.MINOR.PATCH form")


def verify_binding(
    data: dict[str, object],
    *,
    expected_repository: str,
    expected_sha: str,
    expected_run_id: int,
    expected_version: str,
) -> None:
    validate_expected_inputs(
        expected_repository, expected_sha, expected_run_id, expected_version
    )
    root = require_keys(
        data,
        {
            "artifacts",
            "candidate",
            "checks",
            "release_archive_sha256",
            "required_jobs",
            "schema_version",
        },
        "root",
    )
    if type(root["schema_version"]) is not int or root["schema_version"] != 1:
        raise SummaryReviewError("unsupported summary schema version")

    candidate = require_keys(
        root["candidate"],
        {"commit_url", "repository", "run_id", "run_url", "sha", "version"},
        "candidate",
    )
    expected_candidate = {
        "commit_url": f"https://github.com/{expected_repository}/commit/{expected_sha}",
        "repository": expected_repository,
        "run_id": expected_run_id,
        "run_url": f"https://github.com/{expected_repository}/actions/runs/{expected_run_id}",
        "sha": expected_sha,
        "version": expected_version,
    }
    if candidate != expected_candidate:
        raise SummaryReviewError("candidate does not match the expected repository, SHA, run and version")

    if root["required_jobs"] != list(REQUIRED_JOBS):
        raise SummaryReviewError("required jobs differ from the supported candidate contract")
    checks = require_keys(root["checks"], CHECKS, "checks")
    if any(value is not True for value in checks.values()):
        raise SummaryReviewError("summary does not report every required check passing")

    release_digest = root["release_archive_sha256"]
    if not isinstance(release_digest, str) or not HEX_64_PATTERN.fullmatch(release_digest):
        raise SummaryReviewError("release archive SHA-256 is invalid")

    artifacts = root["artifacts"]
    if not isinstance(artifacts, list) or len(artifacts) != len(ARTIFACT_ORDER):
        raise SummaryReviewError("artifacts must contain exactly four records")
    names = expected_artifact_names(expected_sha)
    observed_ids: set[int] = set()
    observed_github_digests: set[str] = set()
    for index, key in enumerate(ARTIFACT_ORDER):
        artifact = require_keys(
            artifacts[index],
            {"github_digest", "id", "name", "payload_sha256", "size_bytes", "url"},
            f"artifacts[{index}]",
        )
        artifact_id = artifact["id"]
        size_bytes = artifact["size_bytes"]
        github_digest = artifact["github_digest"]
        payload_digest = artifact["payload_sha256"]
        expected_name = names[key]
        if (
            isinstance(artifact_id, bool)
            or not isinstance(artifact_id, int)
            or artifact_id <= 0
            or isinstance(size_bytes, bool)
            or not isinstance(size_bytes, int)
            or size_bytes <= 0
            or not isinstance(github_digest, str)
            or not DIGEST_PATTERN.fullmatch(github_digest)
            or not isinstance(payload_digest, str)
            or not HEX_64_PATTERN.fullmatch(payload_digest)
            or artifact["name"] != expected_name
            or artifact["url"]
            != (
                f"https://github.com/{expected_repository}/actions/runs/"
                f"{expected_run_id}/artifacts/{artifact_id}"
            )
        ):
            raise SummaryReviewError(f"artifact is not bound to the candidate: {expected_name}")
        if artifact_id in observed_ids:
            raise SummaryReviewError("artifact IDs must be unique")
        if github_digest in observed_github_digests:
            raise SummaryReviewError("GitHub artifact digests must be unique")
        observed_ids.add(artifact_id)
        observed_github_digests.add(github_digest)

    release_bundle = artifacts[1]
    if not isinstance(release_bundle, dict) or release_bundle.get("payload_sha256") != release_digest:
        raise SummaryReviewError("release archive digest differs from the release-bundle payload")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--digest", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--run-id", required=True, type=int)
    parser.add_argument("--version", required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        data, digest = read_artifact(args.archive, args.digest)
        verify_binding(
            data,
            expected_repository=args.repository,
            expected_sha=args.sha,
            expected_run_id=args.run_id,
            expected_version=args.version,
        )
    except (OSError, SummaryReviewError) as exc:
        raise SystemExit(f"candidate evidence review failed: {exc}") from exc
    print(
        "PASS: summary artifact digest, archive shape, canonical schema and "
        f"candidate binding verified (sha256:{digest})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
