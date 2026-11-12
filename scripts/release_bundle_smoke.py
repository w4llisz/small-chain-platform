#!/usr/bin/env python3
"""Exercise a verified release archive without source or a Go toolchain."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
import signal
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path, PurePosixPath

try:
    from scripts.release_bundle import (
        BINARY_PACKAGES,
        BundleError,
        EXPECTED_FILES,
        MANIFEST_PATH,
        MAX_ARCHIVE_BYTES,
        MAX_EXPANDED_BYTES,
        METADATA_PATH,
        canonical_json,
        expected_root,
        metadata,
        parse_manifest,
        required_toolchain,
        sha256_bytes,
        unique_object,
        validate_inputs,
    )
except ModuleNotFoundError:  # Direct execution adds scripts/, not its parent, to sys.path.
    from release_bundle import (  # type: ignore[no-redef]
        BINARY_PACKAGES,
        BundleError,
        EXPECTED_FILES,
        MANIFEST_PATH,
        MAX_ARCHIVE_BYTES,
        MAX_EXPANDED_BYTES,
        METADATA_PATH,
        canonical_json,
        expected_root,
        metadata,
        parse_manifest,
        required_toolchain,
        sha256_bytes,
        unique_object,
        validate_inputs,
    )


class SmokeError(ValueError):
    """The packaged runtime did not satisfy the smoke-test contract."""


def read_archive(
    archive: Path, repository: str, sha: str, version: str
) -> tuple[str, dict[str, bytes]]:
    """Validate the safe extraction boundary without invoking the Go tool."""
    validate_inputs(repository, sha, version)
    if not archive.is_file():
        raise SmokeError("release archive does not exist")
    if archive.stat().st_size > MAX_ARCHIVE_BYTES:
        raise SmokeError("release archive exceeds the 64 MiB limit")
    raw = archive.read_bytes()
    if len(raw) < 10 or int.from_bytes(raw[4:8], "little") != 0:
        raise SmokeError("gzip header must use the deterministic zero timestamp")

    root = expected_root(version)
    expected_names = [f"{root}/{path}" for path in EXPECTED_FILES]
    content: dict[str, bytes] = {}
    try:
        with tarfile.open(archive, "r:gz") as bundle:
            members = bundle.getmembers()
            if [member.name for member in members] != expected_names:
                raise SmokeError("archive entries differ from the fixed release layout")
            total = 0
            for member in members:
                relative = member.name.removeprefix(f"{root}/")
                if (
                    not member.isfile()
                    or member.name.startswith("/")
                    or ".." in PurePosixPath(member.name).parts
                ):
                    raise SmokeError("archive entries must be bounded regular files")
                expected_mode = 0o755 if relative in BINARY_PACKAGES else 0o644
                if (
                    member.uid != 0
                    or member.gid != 0
                    or member.mtime != 0
                    or member.mode != expected_mode
                ):
                    raise SmokeError(f"noncanonical tar metadata for {relative}")
                total += member.size
                if total > MAX_EXPANDED_BYTES:
                    raise SmokeError("expanded release archive exceeds the 128 MiB limit")
                stream = bundle.extractfile(member)
                if stream is None:
                    raise SmokeError(f"cannot read {relative}")
                content[relative] = stream.read()
    except (tarfile.TarError, gzip.BadGzipFile, EOFError) as exc:
        raise SmokeError("release archive is not valid tar.gz") from exc

    try:
        document = json.loads(
            content[METADATA_PATH].decode("utf-8"), object_pairs_hook=unique_object
        )
    except (UnicodeDecodeError, json.JSONDecodeError, BundleError) as exc:
        raise SmokeError("BUILD-METADATA.json is invalid") from exc
    expected_metadata = metadata(
        repository, sha, version, required_toolchain()
    )
    if content[METADATA_PATH] != canonical_json(document) or document != expected_metadata:
        raise SmokeError("BUILD-METADATA.json does not match the expected candidate")

    try:
        checksums = parse_manifest(
            content[MANIFEST_PATH], set(EXPECTED_FILES) - {MANIFEST_PATH}
        )
    except BundleError as exc:
        raise SmokeError(str(exc)) from exc
    for path, digest in checksums.items():
        if sha256_bytes(content[path]) != digest:
            raise SmokeError(f"SHA-256 mismatch for {path}")
    return sha256_bytes(raw), content


def extract_archive(content: dict[str, bytes], destination: Path, version: str) -> Path:
    """Write the already validated fixed layout into a new empty directory."""
    if destination.exists():
        raise SmokeError("runtime directory must not already exist")
    root = destination / expected_root(version)
    (root / "bin").mkdir(parents=True)
    for relative in EXPECTED_FILES:
        target = root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content[relative])
        target.chmod(0o755 if relative in BINARY_PACKAGES else 0o644)
    return root


def read_events(log_path: Path) -> list[dict[str, object]]:
    events: list[dict[str, object]] = []
    if not log_path.exists():
        return events
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            events.append(value)
    return events


def http_json(
    method: str, url: str, body: dict[str, object] | None = None, key: str = ""
) -> tuple[int, dict[str, object], str]:
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode() if body is not None else None,
        headers=headers,
        method=method,
    )
    with urllib.request.urlopen(request, timeout=2) as response:
        document = json.load(response)
        if not isinstance(document, dict):
            raise SmokeError(f"{method} {url} returned a non-object JSON response")
        return response.status, document, response.headers.get("Location", "")


def run_packaged_runtime(root: Path) -> dict[str, object]:
    if list(root.rglob("*.go")):
        raise SmokeError("runtime directory unexpectedly contains Go source")

    isolated = root / ".runtime"
    empty_path = isolated / "empty-path"
    home = isolated / "home"
    temporary = isolated / "tmp"
    for directory in (empty_path, home, temporary):
        directory.mkdir(parents=True)
    environment = {
        "HOME": str(home),
        "LANG": "C.UTF-8",
        "PATH": str(empty_path),
        "TMPDIR": str(temporary),
        "GOCACHE": str(isolated / "no-go-cache"),
        "GOMODCACHE": str(isolated / "no-go-mod-cache"),
        "GOROOT": str(isolated / "no-go-root"),
        "GOTOOLCHAIN": "local",
    }
    log_path = isolated / "service.jsonl"
    api = root / "bin" / "small-chain"
    healthcheck = root / "bin" / "healthcheck"
    with log_path.open("wb") as log:
        process = subprocess.Popen(
            [
                str(api),
                "-addr=127.0.0.1:0",
                "-storage=memory",
                "-workers=1",
                "-queue=4",
                "-shutdown-grace=3s",
            ],
            cwd=root,
            env=environment,
            stdout=log,
            stderr=subprocess.STDOUT,
        )
    try:
        deadline = time.monotonic() + 5
        address = ""
        while not address:
            for event in read_events(log_path):
                if event.get("msg") == "service.started":
                    address = str(event.get("address", ""))
            if process.poll() is not None:
                raise SmokeError(f"packaged API exited early with {process.returncode}")
            if time.monotonic() >= deadline:
                raise SmokeError("packaged API did not start within 5 seconds")
            time.sleep(0.02)
        base = f"http://{address}"

        probe = subprocess.run(
            [str(healthcheck), f"-url={base}/readyz", "-timeout=2s"],
            cwd=root,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=3,
            check=False,
        )
        if probe.returncode != 0:
            raise SmokeError(f"packaged healthcheck failed: {probe.stderr.strip()}")

        payload = "release-bundle-smoke"
        spec: dict[str, object] = {
            "kind": "demo.checksum",
            "payload": payload,
            "fail_first_attempts": 1,
        }
        status, submitted, location = http_json(
            "POST", base + "/v1/jobs", spec, "release-bundle-smoke"
        )
        if status != 202 or not location.startswith("/v1/jobs/"):
            raise SmokeError(f"job submission failed: status={status} body={submitted}")
        deadline = time.monotonic() + 5
        completed: dict[str, object] = {}
        while time.monotonic() < deadline:
            _status, current, _location = http_json("GET", base + location)
            if current.get("state") == "succeeded":
                completed = current
                break
            if current.get("state") in {"failed", "canceled"}:
                raise SmokeError(f"checksum job reached {current.get('state')}")
            time.sleep(0.02)
        if not completed:
            raise SmokeError("checksum job did not complete within 5 seconds")
        expected_result = hashlib.sha256(payload.encode()).hexdigest()
        if completed.get("attempts") != 2 or completed.get("result") != expected_result:
            raise SmokeError(f"unexpected checksum result: {completed}")

        process.send_signal(signal.SIGTERM)
        try:
            exit_code = process.wait(timeout=5)
        except subprocess.TimeoutExpired as exc:
            raise SmokeError("packaged API did not stop within 5 seconds") from exc
        events = read_events(log_path)
        messages = [event.get("msg") for event in events]
        if exit_code != 0 or "service.draining" not in messages or "service.stopped" not in messages:
            raise SmokeError(
                f"graceful SIGTERM failed: exit={exit_code} messages={messages}"
            )
        return {
            "attempts": completed["attempts"],
            "job_id": completed["id"],
            "result": completed["result"],
            "service_exit_code": exit_code,
        }
    except (OSError, subprocess.SubprocessError, urllib.error.URLError) as exc:
        raise SmokeError(str(exc)) from exc
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)


def smoke(
    archive: Path,
    repository: str,
    sha: str,
    version: str,
    runtime_directory: Path,
) -> dict[str, object]:
    digest, content = read_archive(archive, repository, sha, version)
    root = extract_archive(content, runtime_directory, version)
    result = run_packaged_runtime(root)
    return {
        "archive_sha256": digest,
        "checks": {
            "checksum_job": True,
            "graceful_sigterm": True,
            "packaged_healthcheck": True,
            "source_free_runtime": True,
            "toolchain_free_runtime_path": True,
        },
        "commit": sha,
        "job": result,
        "repository": repository,
        "schema_version": 1,
        "version": version,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--runtime-directory", type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    temporary: tempfile.TemporaryDirectory[str] | None = None
    runtime_directory = args.runtime_directory
    if runtime_directory is None:
        temporary = tempfile.TemporaryDirectory(prefix="small-chain-package-smoke-")
        runtime_directory = Path(temporary.name) / "runtime"
    try:
        evidence = smoke(
            args.archive,
            args.repository,
            args.sha,
            args.version,
            runtime_directory,
        )
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_bytes(canonical_json(evidence))
    except (BundleError, SmokeError, OSError) as exc:
        raise SystemExit(f"release bundle smoke failed: {exc}") from exc
    finally:
        if temporary is not None:
            temporary.cleanup()
    print(
        "PASS: packaged API, healthcheck, retrying checksum job and graceful SIGTERM "
        "completed without Go source or toolchain PATH"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
