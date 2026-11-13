#!/usr/bin/env python3
"""Exercise every packaged binary against an ephemeral PostgreSQL database."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import signal
import subprocess
import tempfile
import time
import urllib.error
from pathlib import Path

try:
    from scripts.release_bundle import BundleError, canonical_json
    from scripts.release_bundle_smoke import (
        SmokeError,
        extract_archive,
        http_json,
        read_archive,
        read_events,
    )
except ModuleNotFoundError:  # Direct execution adds scripts/, not its parent, to sys.path.
    from release_bundle import BundleError, canonical_json  # type: ignore[no-redef]
    from release_bundle_smoke import (  # type: ignore[no-redef]
        SmokeError,
        extract_archive,
        http_json,
        read_archive,
        read_events,
    )


def isolated_environment(root: Path, database_url: str) -> dict[str, str]:
    runtime = root / ".postgres-runtime"
    environment = {
        "DATABASE_URL": database_url,
        "HOME": str(runtime / "home"),
        "LANG": "C.UTF-8",
        "PATH": str(runtime / "empty-path"),
        "TMPDIR": str(runtime / "tmp"),
        "GOCACHE": str(runtime / "no-go-cache"),
        "GOMODCACHE": str(runtime / "no-go-mod-cache"),
        "GOROOT": str(runtime / "no-go-root"),
        "GOTOOLCHAIN": "local",
    }
    for key in ("HOME", "PATH", "TMPDIR"):
        Path(environment[key]).mkdir(parents=True, exist_ok=True)
    return environment


def run_migrations(root: Path, environment: dict[str, str]) -> None:
    for run_number in (1, 2):
        result = subprocess.run(
            [str(root / "bin" / "migrate")],
            cwd=root,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=10,
            check=False,
        )
        if result.returncode != 0 or "PostgreSQL migrations applied" not in result.stdout:
            detail = result.stderr.strip() or result.stdout.strip()
            raise SmokeError(f"packaged migration run {run_number} failed: {detail}")


def start_process(
    command: list[str], root: Path, environment: dict[str, str], log_path: Path
) -> subprocess.Popen[bytes]:
    with log_path.open("wb") as log:
        return subprocess.Popen(
            command,
            cwd=root,
            env=environment,
            stdout=log,
            stderr=subprocess.STDOUT,
        )


def wait_for_event(
    process: subprocess.Popen[bytes], log_path: Path, message: str, timeout: float = 5
) -> dict[str, object]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for event in read_events(log_path):
            if event.get("msg") == message:
                return event
        if process.poll() is not None:
            raise SmokeError(
                f"process exited with {process.returncode} before {message}: "
                f"{log_path.read_text(encoding='utf-8', errors='replace')[-4000:]}"
            )
        time.sleep(0.02)
    raise SmokeError(f"timed out waiting for {message}")


def stop_process(
    process: subprocess.Popen[bytes], log_path: Path, stopped_message: str
) -> int:
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
    try:
        exit_code = process.wait(timeout=5)
    except subprocess.TimeoutExpired as exc:
        process.kill()
        process.wait(timeout=5)
        raise SmokeError(f"process did not stop within 5 seconds: {log_path}") from exc
    messages = [event.get("msg") for event in read_events(log_path)]
    if exit_code != 0 or stopped_message not in messages:
        raise SmokeError(
            f"graceful stop failed: exit={exit_code} expected={stopped_message} "
            f"messages={messages}"
        )
    return exit_code


def wait_for_job(base: str, location: str, attempts: int) -> dict[str, object]:
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        _status, current, _location = http_json("GET", base + location)
        if current.get("state") == "succeeded":
            if current.get("attempts") != attempts:
                raise SmokeError(f"unexpected attempt count: {current}")
            return current
        if current.get("state") in {"failed", "canceled"}:
            raise SmokeError(f"durable job reached {current.get('state')}")
        time.sleep(0.02)
    raise SmokeError("durable job did not complete within 8 seconds")


def submit_job(
    base: str, key: str, payload: str, fail_first_attempts: int
) -> dict[str, object]:
    spec: dict[str, object] = {"kind": "demo.checksum", "payload": payload}
    if fail_first_attempts:
        spec["fail_first_attempts"] = fail_first_attempts
    status, submitted, location = http_json("POST", base + "/v1/jobs", spec, key)
    if status != 202 or not location.startswith("/v1/jobs/"):
        raise SmokeError(f"durable submission failed: status={status} body={submitted}")
    completed = wait_for_job(base, location, fail_first_attempts + 1)
    expected = hashlib.sha256(payload.encode()).hexdigest()
    if completed.get("result") != expected:
        raise SmokeError(f"unexpected durable checksum result: {completed}")
    return completed


def start_stack(
    root: Path,
    environment: dict[str, str],
    phase: str,
) -> tuple[subprocess.Popen[bytes], subprocess.Popen[bytes], str, Path, Path]:
    runtime = root / ".postgres-runtime"
    api_log = runtime / f"api-{phase}.jsonl"
    worker_log = runtime / f"worker-{phase}.jsonl"
    api = start_process(
        [
            str(root / "bin" / "small-chain"),
            "-addr=127.0.0.1:0",
            "-storage=postgres",
            "-retention-interval=0",
            "-shutdown-grace=3s",
        ],
        root,
        environment,
        api_log,
    )
    worker: subprocess.Popen[bytes] | None = None
    try:
        started = wait_for_event(api, api_log, "service.started")
        address = str(started.get("address", ""))
        base = f"http://{address}"
        probe = subprocess.run(
            [
                str(root / "bin" / "healthcheck"),
                f"-url={base}/readyz",
                "-timeout=2s",
            ],
            cwd=root,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=3,
            check=False,
        )
        if probe.returncode != 0:
            raise SmokeError(f"packaged PostgreSQL healthcheck failed: {probe.stderr.strip()}")
        worker = start_process(
            [
                str(root / "bin" / "small-chain-worker"),
                f"-owner=packaged-{phase}",
                "-concurrency=1",
                "-poll-interval=20ms",
                "-lease-duration=3s",
                "-heartbeat-interval=1s",
                "-recovery-interval=200ms",
            ],
            root,
            environment,
            worker_log,
        )
        wait_for_event(worker, worker_log, "worker.started")
        return api, worker, base, api_log, worker_log
    except Exception:
        for process in (worker, api):
            if process is not None and process.poll() is None:
                process.kill()
                process.wait(timeout=5)
        raise


def stop_stack(
    worker: subprocess.Popen[bytes],
    worker_log: Path,
    api: subprocess.Popen[bytes],
    api_log: Path,
) -> tuple[int, int]:
    exits: list[int] = []
    errors: list[str] = []
    for process, log_path, message in (
        (worker, worker_log, "worker.stopped"),
        (api, api_log, "service.stopped"),
    ):
        try:
            exits.append(stop_process(process, log_path, message))
        except SmokeError as exc:
            errors.append(str(exc))
    if errors:
        raise SmokeError("; ".join(errors))
    return exits[0], exits[1]


def run_durable_runtime(
    root: Path, database_url: str
) -> dict[str, object]:
    if list(root.rglob("*.go")):
        raise SmokeError("runtime directory unexpectedly contains Go source")
    environment = isolated_environment(root, database_url)
    run_migrations(root, environment)

    phase1 = start_stack(root, environment, "before-restart")
    api1, worker1, base1, api1_log, worker1_log = phase1
    try:
        first = submit_job(
            base1,
            "packaged-postgres-before-restart",
            "durable-before-restart",
            1,
        )
    finally:
        worker1_exit, api1_exit = stop_stack(
            worker1, worker1_log, api1, api1_log
        )

    phase2 = start_stack(root, environment, "after-restart")
    api2, worker2, base2, api2_log, worker2_log = phase2
    try:
        _status, persisted, _location = http_json(
            "GET", base2 + "/v1/jobs/" + str(first["id"])
        )
        if (
            persisted.get("state") != "succeeded"
            or persisted.get("result") != first.get("result")
            or persisted.get("attempts") != first.get("attempts")
        ):
            raise SmokeError(f"persisted job changed after restart: {persisted}")
        second = submit_job(
            base2,
            "packaged-postgres-after-restart",
            "durable-after-restart",
            0,
        )
    finally:
        worker2_exit, api2_exit = stop_stack(
            worker2, worker2_log, api2, api2_log
        )

    return {
        "after_restart": {
            "attempts": second["attempts"],
            "job_id": second["id"],
            "result": second["result"],
        },
        "before_restart": {
            "attempts": first["attempts"],
            "job_id": first["id"],
            "result": first["result"],
        },
        "graceful_exit_codes": [
            worker1_exit,
            api1_exit,
            worker2_exit,
            api2_exit,
        ],
        "migration_runs": 2,
    }


def smoke(
    archive: Path,
    repository: str,
    sha: str,
    version: str,
    database_url: str,
    runtime_directory: Path,
) -> dict[str, object]:
    digest, content = read_archive(archive, repository, sha, version)
    root = extract_archive(content, runtime_directory, version)
    result = run_durable_runtime(root, database_url)
    return {
        "archive_sha256": digest,
        "checks": {
            "all_packaged_binaries_exercised": True,
            "durable_result_survived_restart": True,
            "graceful_process_shutdown": True,
            "idempotent_migration": True,
            "source_free_runtime": True,
            "toolchain_free_runtime_path": True,
        },
        "commit": sha,
        "repository": repository,
        "result": result,
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
    database_url = os.environ.get("DATABASE_URL", "")
    if not database_url:
        raise SystemExit("release bundle PostgreSQL smoke failed: DATABASE_URL is required")
    temporary: tempfile.TemporaryDirectory[str] | None = None
    runtime_directory = args.runtime_directory
    if runtime_directory is None:
        temporary = tempfile.TemporaryDirectory(prefix="small-chain-package-postgres-")
        runtime_directory = Path(temporary.name) / "runtime"
    try:
        evidence = smoke(
            args.archive,
            args.repository,
            args.sha,
            args.version,
            database_url,
            runtime_directory,
        )
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_bytes(canonical_json(evidence))
    except (BundleError, SmokeError, OSError, subprocess.SubprocessError, urllib.error.URLError) as exc:
        raise SystemExit(f"release bundle PostgreSQL smoke failed: {exc}") from exc
    finally:
        if temporary is not None:
            temporary.cleanup()
    print(
        "PASS: packaged migration, API, worker, healthcheck, durable restart and "
        "graceful shutdown completed without a Go toolchain PATH"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
