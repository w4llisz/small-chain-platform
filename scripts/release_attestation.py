#!/usr/bin/env python3
"""Generate a commit-bound release-candidate attestation in a clean checkout."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path, PurePosixPath


SHA_PATTERN = re.compile(r"[0-9a-f]{40}")
REPOSITORY_PATTERN = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
REQUIRED_JOBS = ("e2e", "incident", "measurement", "postgres", "verify", "web")
FORBIDDEN_EXACT_PATHS = {".env", "coverage.out"}
FORBIDDEN_PREFIXES = (
    "artifacts/",
    "bin/",
    "web/dist/",
    "web/node_modules/",
    "web/test-results/",
)
FORBIDDEN_SUFFIXES = (".key", ".p12", ".pem", ".pfx")
FORBIDDEN_BASENAMES = {"credentials", "id_ed25519", "id_rsa"}


class AttestationError(ValueError):
    """The checkout cannot be attested as an immutable candidate."""


def run_git(root: Path, *args: str) -> str:
    result = subprocess.run(
        ("git", *args),
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        raise AttestationError(f"git {' '.join(args)} failed: {detail}")
    return result.stdout


def forbidden_tracked_paths(paths: list[str]) -> list[str]:
    forbidden = []
    for path in paths:
        pure = PurePosixPath(path)
        if (
            path in FORBIDDEN_EXACT_PATHS
            or path.startswith(FORBIDDEN_PREFIXES)
            or pure.name in FORBIDDEN_BASENAMES
            or pure.suffix.lower() in FORBIDDEN_SUFFIXES
        ):
            forbidden.append(path)
    return sorted(forbidden)


def build_attestation(
    root: Path,
    *,
    sha: str,
    run_id: int,
    repository: str,
    ref: str,
) -> dict[str, object]:
    if not SHA_PATTERN.fullmatch(sha):
        raise AttestationError("candidate SHA must be 40 lowercase hexadecimal characters")
    if run_id <= 0:
        raise AttestationError("workflow run ID must be a positive integer")
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise AttestationError("repository must use owner/name form")
    if ref != "refs/heads/main":
        raise AttestationError("release candidates must come from refs/heads/main")

    observed_sha = run_git(root, "rev-parse", "HEAD").strip()
    if observed_sha != sha:
        raise AttestationError(
            f"checkout HEAD {observed_sha} does not match candidate SHA {sha}"
        )

    status = run_git(root, "status", "--porcelain=v1", "--untracked-files=all")
    if status:
        entries = ", ".join(line[3:] for line in status.splitlines())
        raise AttestationError(f"checkout is not clean: {entries}")

    tracked_paths = [
        path for path in run_git(root, "ls-files", "-z").split("\0") if path
    ]
    forbidden = forbidden_tracked_paths(tracked_paths)
    if forbidden:
        raise AttestationError(
            "forbidden release files are tracked: " + ", ".join(forbidden)
        )

    return {
        "schema_version": 1,
        "candidate": {
            "repository": repository,
            "ref": ref,
            "sha": sha,
            "commit_url": f"https://github.com/{repository}/commit/{sha}",
        },
        "source": {
            "clean_checkout": True,
            "forbidden_tracked_paths": [],
            "tracked_file_count": len(tracked_paths),
        },
        "workflow": {
            "name": "CI",
            "run_id": run_id,
            "run_url": f"https://github.com/{repository}/actions/runs/{run_id}",
            "required_jobs": list(REQUIRED_JOBS),
            "required_jobs_conclusion": "success",
        },
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--sha", required=True)
    parser.add_argument("--run-id", required=True, type=int)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--ref", default="refs/heads/main")
    parser.add_argument("--output", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        attestation = build_attestation(
            args.root.resolve(),
            sha=args.sha,
            run_id=args.run_id,
            repository=args.repository,
            ref=args.ref,
        )
    except AttestationError as exc:
        raise SystemExit(f"release attestation failed: {exc}") from exc

    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(attestation, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(f"wrote release-candidate attestation to {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
