#!/usr/bin/env python3
"""Verify a GitHub release-candidate artifact and its commit/run binding."""

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

from scripts.release_attestation import REQUIRED_JOBS, REPOSITORY_PATTERN, SHA_PATTERN


DIGEST_PATTERN = re.compile(r"sha256:([0-9a-f]{64})")
MAX_ARCHIVE_BYTES = 64 * 1024
MAX_ATTESTATION_BYTES = 16 * 1024


class ReviewError(ValueError):
    """The candidate artifact cannot support the requested review."""


def require_keys(value: object, expected: set[str], context: str) -> dict[str, object]:
    if not isinstance(value, dict):
        raise ReviewError(f"{context} must be an object")
    observed = set(value)
    if observed != expected:
        raise ReviewError(
            f"{context} keys differ: missing={sorted(expected - observed)}, "
            f"unexpected={sorted(observed - expected)}"
        )
    return value


def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ReviewError(f"duplicate JSON key: {key}")
        value[key] = item
    return value


def read_artifact(archive: Path, expected_digest: str) -> tuple[dict[str, object], str]:
    match = DIGEST_PATTERN.fullmatch(expected_digest)
    if not match:
        raise ReviewError("artifact digest must use sha256:<64 lowercase hex> form")

    if archive.stat().st_size > MAX_ARCHIVE_BYTES:
        raise ReviewError("artifact exceeds the 64 KiB review limit")
    observed_digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if observed_digest != match.group(1):
        raise ReviewError(
            f"artifact digest mismatch: observed={observed_digest}, expected={match.group(1)}"
        )

    try:
        with zipfile.ZipFile(archive) as bundle:
            entries = bundle.infolist()
            if [entry.filename for entry in entries] != ["attestation.json"]:
                raise ReviewError("artifact must contain exactly attestation.json")
            entry = entries[0]
            if entry.is_dir() or entry.flag_bits & 0x1:
                raise ReviewError("attestation.json must be a regular unencrypted file")
            if stat.S_ISLNK(entry.external_attr >> 16):
                raise ReviewError("attestation.json must not be a symbolic link")
            if entry.file_size > MAX_ATTESTATION_BYTES:
                raise ReviewError("attestation.json exceeds the 16 KiB review limit")
            raw = bundle.read(entry)
    except zipfile.BadZipFile as exc:
        raise ReviewError("artifact is not a valid ZIP file") from exc

    try:
        text = raw.decode("utf-8")
        data = json.loads(text, object_pairs_hook=unique_object)
    except UnicodeDecodeError as exc:
        raise ReviewError("attestation.json must be UTF-8") from exc
    except json.JSONDecodeError as exc:
        raise ReviewError(f"attestation.json is invalid JSON: {exc.msg}") from exc

    canonical = json.dumps(data, indent=2, sort_keys=True) + "\n"
    if text != canonical:
        raise ReviewError("attestation.json is not in deterministic canonical form")
    if not isinstance(data, dict):
        raise ReviewError("attestation root must be an object")
    return data, observed_digest


def verify_binding(
    data: dict[str, object],
    *,
    expected_repository: str,
    expected_sha: str,
    expected_run_id: int,
) -> None:
    if not REPOSITORY_PATTERN.fullmatch(expected_repository):
        raise ReviewError("expected repository must use owner/name form")
    if not SHA_PATTERN.fullmatch(expected_sha):
        raise ReviewError("expected SHA must be 40 lowercase hexadecimal characters")
    if expected_run_id <= 0:
        raise ReviewError("expected run ID must be positive")

    root = require_keys(data, {"schema_version", "candidate", "source", "workflow"}, "root")
    if type(root["schema_version"]) is not int or root["schema_version"] != 1:
        raise ReviewError("unsupported attestation schema version")

    candidate = require_keys(
        root["candidate"],
        {"commit_url", "ref", "repository", "sha"},
        "candidate",
    )
    expected_commit_url = f"https://github.com/{expected_repository}/commit/{expected_sha}"
    if candidate != {
        "commit_url": expected_commit_url,
        "ref": "refs/heads/main",
        "repository": expected_repository,
        "sha": expected_sha,
    }:
        raise ReviewError("candidate does not match the expected repository, main ref and SHA")

    source = require_keys(
        root["source"],
        {"clean_checkout", "forbidden_tracked_paths", "tracked_file_count"},
        "source",
    )
    tracked_count = source["tracked_file_count"]
    if (
        source["clean_checkout"] is not True
        or source["forbidden_tracked_paths"] != []
        or isinstance(tracked_count, bool)
        or not isinstance(tracked_count, int)
        or tracked_count <= 0
    ):
        raise ReviewError("source does not prove a clean checkout and tracked-path gate")

    workflow = require_keys(
        root["workflow"],
        {"name", "required_jobs", "required_jobs_conclusion", "run_id", "run_url"},
        "workflow",
    )
    expected_run_url = (
        f"https://github.com/{expected_repository}/actions/runs/{expected_run_id}"
    )
    if workflow != {
        "name": "CI",
        "required_jobs": list(REQUIRED_JOBS),
        "required_jobs_conclusion": "success",
        "run_id": expected_run_id,
        "run_url": expected_run_url,
    }:
        raise ReviewError("workflow does not match the expected run and required jobs")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--digest", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--run-id", required=True, type=int)
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
        )
    except (OSError, ReviewError) as exc:
        raise SystemExit(f"candidate review failed: {exc}") from exc

    print(
        "PASS: candidate artifact digest, archive shape and commit/run binding "
        f"verified (sha256:{digest})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
