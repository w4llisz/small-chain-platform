#!/usr/bin/env python3
"""Build a strict commit/run-bound index of release-candidate evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path

try:
    from scripts.release_attestation import REPOSITORY_PATTERN, SHA_PATTERN
    from scripts.release_bundle import VERSION_PATTERN, canonical_json
    from scripts.verify_release_attestation import ReviewError, verify_binding
except ModuleNotFoundError:  # Direct execution adds scripts/, not its parent.
    from release_attestation import REPOSITORY_PATTERN, SHA_PATTERN  # type: ignore[no-redef]
    from release_bundle import VERSION_PATTERN, canonical_json  # type: ignore[no-redef]
    from verify_release_attestation import ReviewError, verify_binding  # type: ignore[no-redef]


DIGEST_PATTERN = re.compile(r"sha256:[0-9a-f]{64}")
HEX_32_PATTERN = re.compile(r"[0-9a-f]{32}")
HEX_64_PATTERN = re.compile(r"[0-9a-f]{64}")
MAX_JSON_BYTES = 256 * 1024
REQUIRED_JOBS = (
    "candidate-attestation",
    "e2e",
    "incident",
    "measurement",
    "packaged-postgres-smoke",
    "packaged-smoke",
    "postgres",
    "release-bundle",
    "verify",
    "web",
)
ARTIFACT_KEYS = (
    "candidate",
    "release_bundle",
    "packaged_smoke",
    "packaged_postgres_smoke",
)


class SummaryError(ValueError):
    """The supplied artifacts cannot support a candidate evidence summary."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise SummaryError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def read_json(path: Path, *, canonical: bool = False) -> dict[str, object]:
    raw = path.read_bytes()
    if len(raw) > MAX_JSON_BYTES:
        raise SummaryError(f"{path.name} exceeds the 256 KiB limit")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SummaryError(f"{path.name} must be valid UTF-8 JSON") from exc
    if not isinstance(value, dict):
        raise SummaryError(f"{path.name} root must be an object")
    if canonical and raw != canonical_json(value):
        raise SummaryError(f"{path.name} must use canonical JSON")
    return value


def require_keys(value: object, expected: set[str], context: str) -> dict[str, object]:
    if not isinstance(value, dict):
        raise SummaryError(f"{context} must be an object")
    observed = set(value)
    if observed != expected:
        raise SummaryError(
            f"{context} keys differ: missing={sorted(expected - observed)}, "
            f"unexpected={sorted(observed - expected)}"
        )
    return value


def validate_inputs(repository: str, sha: str, run_id: int, version: str) -> None:
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise SummaryError("repository must use owner/name form")
    if not SHA_PATTERN.fullmatch(sha):
        raise SummaryError("SHA must be 40 lowercase hexadecimal characters")
    if run_id <= 0:
        raise SummaryError("run ID must be positive")
    if not VERSION_PATTERN.fullmatch(version):
        raise SummaryError("version must use vMAJOR.MINOR.PATCH form")


def validate_jobs(document: dict[str, object], run_id: int) -> None:
    jobs = document.get("jobs")
    if not isinstance(jobs, list):
        raise SummaryError("jobs metadata must contain a jobs array")
    observed: dict[str, dict[str, object]] = {}
    for value in jobs:
        if not isinstance(value, dict) or not isinstance(value.get("name"), str):
            raise SummaryError("jobs metadata contains an invalid job")
        name = str(value["name"])
        if name in observed:
            raise SummaryError(f"jobs metadata repeats {name}")
        observed[name] = value
    for name in REQUIRED_JOBS:
        job = observed.get(name)
        if job is None:
            raise SummaryError(f"required job is missing: {name}")
        if job.get("run_id") != run_id or job.get("status") != "completed" or job.get("conclusion") != "success":
            raise SummaryError(f"required job did not complete successfully: {name}")


def expected_artifact_names(sha: str) -> dict[str, str]:
    return {
        "candidate": f"candidate-{sha}",
        "release_bundle": f"release-bundle-{sha}",
        "packaged_smoke": f"packaged-smoke-{sha}",
        "packaged_postgres_smoke": f"packaged-postgres-smoke-{sha}",
    }


def validate_artifacts(
    document: dict[str, object], *, repository: str, sha: str, run_id: int
) -> dict[str, dict[str, object]]:
    artifacts = document.get("artifacts")
    if not isinstance(artifacts, list):
        raise SummaryError("artifact metadata must contain an artifacts array")
    names = expected_artifact_names(sha)
    selected: dict[str, dict[str, object]] = {}
    for key, expected_name in names.items():
        matches = [value for value in artifacts if isinstance(value, dict) and value.get("name") == expected_name]
        if len(matches) != 1:
            raise SummaryError(f"expected exactly one artifact named {expected_name}")
        artifact = matches[0]
        workflow = artifact.get("workflow_run")
        artifact_id = artifact.get("id")
        size = artifact.get("size_in_bytes")
        digest = artifact.get("digest")
        if (
            isinstance(artifact_id, bool)
            or not isinstance(artifact_id, int)
            or artifact_id <= 0
            or isinstance(size, bool)
            or not isinstance(size, int)
            or size <= 0
            or not isinstance(digest, str)
            or not DIGEST_PATTERN.fullmatch(digest)
            or artifact.get("expired") is not False
            or not isinstance(workflow, dict)
            or workflow.get("id") != run_id
            or workflow.get("head_sha") != sha
        ):
            raise SummaryError(f"artifact metadata is not bound to the candidate: {expected_name}")
        selected[key] = {
            "github_digest": digest,
            "id": artifact_id,
            "name": expected_name,
            "size_bytes": size,
            "url": f"https://github.com/{repository}/actions/runs/{run_id}/artifacts/{artifact_id}",
        }
    return selected


def validate_common_evidence(
    value: dict[str, object], *, repository: str, sha: str, version: str,
    context: str, result_key: str, expected_checks: set[str],
) -> None:
    require_keys(
        value,
        {
            "archive_sha256",
            "checks",
            "commit",
            result_key,
            "repository",
            "schema_version",
            "version",
        },
        context,
    )
    if value.get("schema_version") != 1 or value.get("repository") != repository or value.get("commit") != sha or value.get("version") != version:
        raise SummaryError(f"{context} is not bound to the candidate")
    checks = value.get("checks")
    if (
        not isinstance(checks, dict)
        or set(checks) != expected_checks
        or any(item is not True for item in checks.values())
    ):
        raise SummaryError(f"{context} does not report all checks passing")
    digest = value.get("archive_sha256")
    if not isinstance(digest, str) or not HEX_64_PATTERN.fullmatch(digest):
        raise SummaryError(f"{context} has an invalid release archive digest")


def validate_memory_evidence(value: dict[str, object]) -> None:
    job = require_keys(value.get("job"), {"attempts", "job_id", "result", "service_exit_code"}, "memory smoke job")
    if (
        job["attempts"] != 2
        or job["service_exit_code"] != 0
        or not isinstance(job["job_id"], str)
        or not HEX_32_PATTERN.fullmatch(job["job_id"])
        or not isinstance(job["result"], str)
        or not HEX_64_PATTERN.fullmatch(job["result"])
    ):
        raise SummaryError("memory smoke job result is invalid")


def validate_postgres_evidence(value: dict[str, object]) -> None:
    result = require_keys(value.get("result"), {"after_restart", "before_restart", "graceful_exit_codes", "migration_runs"}, "PostgreSQL smoke result")
    if result["migration_runs"] != 2 or result["graceful_exit_codes"] != [0, 0, 0, 0]:
        raise SummaryError("PostgreSQL migration or graceful shutdown result is invalid")
    for key, attempts in (("before_restart", 2), ("after_restart", 1)):
        job = require_keys(result[key], {"attempts", "job_id", "result"}, f"PostgreSQL {key} job")
        if (
            job["attempts"] != attempts
            or not isinstance(job["job_id"], str)
            or not HEX_32_PATTERN.fullmatch(job["job_id"])
            or not isinstance(job["result"], str)
            or not HEX_64_PATTERN.fullmatch(job["result"])
        ):
            raise SummaryError(f"PostgreSQL {key} job result is invalid")
    before = result["before_restart"]
    after = result["after_restart"]
    if isinstance(before, dict) and isinstance(after, dict) and before["job_id"] == after["job_id"]:
        raise SummaryError("PostgreSQL restart jobs must be distinct")


def build_summary(
    *, repository: str, sha: str, run_id: int, version: str,
    artifact_metadata: Path, jobs_metadata: Path, attestation: Path,
    release_bundle: Path, packaged_smoke: Path, packaged_postgres_smoke: Path,
) -> dict[str, object]:
    validate_inputs(repository, sha, run_id, version)
    artifacts = validate_artifacts(read_json(artifact_metadata), repository=repository, sha=sha, run_id=run_id)
    validate_jobs(read_json(jobs_metadata), run_id)

    attestation_data = read_json(attestation, canonical=True)
    try:
        verify_binding(attestation_data, expected_repository=repository, expected_sha=sha, expected_run_id=run_id)
    except ReviewError as exc:
        raise SummaryError(f"candidate attestation binding failed: {exc}") from exc

    memory = read_json(packaged_smoke, canonical=True)
    durable = read_json(packaged_postgres_smoke, canonical=True)
    validate_common_evidence(
        memory,
        repository=repository,
        sha=sha,
        version=version,
        context="memory smoke evidence",
        result_key="job",
        expected_checks={
            "checksum_job",
            "graceful_sigterm",
            "packaged_healthcheck",
            "source_free_runtime",
            "toolchain_free_runtime_path",
        },
    )
    validate_common_evidence(
        durable,
        repository=repository,
        sha=sha,
        version=version,
        context="PostgreSQL smoke evidence",
        result_key="result",
        expected_checks={
            "all_packaged_binaries_exercised",
            "durable_result_survived_restart",
            "graceful_process_shutdown",
            "idempotent_migration",
            "source_free_runtime",
            "toolchain_free_runtime_path",
        },
    )
    validate_memory_evidence(memory)
    validate_postgres_evidence(durable)

    archive_digest = sha256_file(release_bundle)
    if memory["archive_sha256"] != archive_digest or durable["archive_sha256"] != archive_digest:
        raise SummaryError("release archive digest differs from packaged smoke evidence")

    files = {
        "candidate": attestation,
        "release_bundle": release_bundle,
        "packaged_smoke": packaged_smoke,
        "packaged_postgres_smoke": packaged_postgres_smoke,
    }
    for key in ARTIFACT_KEYS:
        artifacts[key]["payload_sha256"] = sha256_file(files[key])

    return {
        "artifacts": [artifacts[key] for key in ARTIFACT_KEYS],
        "candidate": {
            "commit_url": f"https://github.com/{repository}/commit/{sha}",
            "repository": repository,
            "run_id": run_id,
            "run_url": f"https://github.com/{repository}/actions/runs/{run_id}",
            "sha": sha,
            "version": version,
        },
        "checks": {
            "archive_digest_consensus": True,
            "artifact_identity_binding": True,
            "attestation_binding": True,
            "packaged_postgres_restart_evidence": True,
            "packaged_runtime_evidence": True,
            "required_jobs_successful": True,
        },
        "release_archive_sha256": archive_digest,
        "required_jobs": list(REQUIRED_JOBS),
        "schema_version": 1,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--run-id", required=True, type=int)
    parser.add_argument("--version", required=True)
    parser.add_argument("--artifact-metadata", required=True, type=Path)
    parser.add_argument("--jobs-metadata", required=True, type=Path)
    parser.add_argument("--attestation", required=True, type=Path)
    parser.add_argument("--release-bundle", required=True, type=Path)
    parser.add_argument("--packaged-smoke", required=True, type=Path)
    parser.add_argument("--packaged-postgres-smoke", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        summary = build_summary(
            repository=args.repository, sha=args.sha, run_id=args.run_id,
            version=args.version, artifact_metadata=args.artifact_metadata,
            jobs_metadata=args.jobs_metadata, attestation=args.attestation,
            release_bundle=args.release_bundle, packaged_smoke=args.packaged_smoke,
            packaged_postgres_smoke=args.packaged_postgres_smoke,
        )
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_bytes(canonical_json(summary))
    except (OSError, SummaryError) as exc:
        raise SystemExit(f"candidate evidence summary failed: {exc}") from exc
    print(f"PASS: commit/run-bound candidate evidence summary written to {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
