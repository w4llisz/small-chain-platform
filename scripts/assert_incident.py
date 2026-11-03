#!/usr/bin/env python3
"""Validate and summarize the bounded worker-crash incident evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import Path


SHA_PATTERN = re.compile(r"^[0-9a-f]{40}$")
JOB_ID_PATTERN = re.compile(r"^[0-9a-f]{32}$")
ATTEMPT_COUNT_PATTERN = re.compile(
    r"^small_chain_worker_attempt_duration_seconds_count ([0-9]+(?:\.[0-9]+)?)$",
    re.MULTILINE,
)


@dataclass(frozen=True)
class Incident:
    source_commit: str
    job_id: str
    killed_owner: str
    recovery_owner: str
    attempt_count: int


def _read_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"read {path.name}: {error}") from error


def validate_incident(directory: Path, expected_commit: str) -> Incident:
    context = _read_json(directory / "context.json")
    if context.get("schema_version") != 1 or context.get("scenario") != "worker-sigkill-recovery":
        raise ValueError("unsupported incident context")
    source_commit = context.get("source_commit", "")
    if not SHA_PATTERN.fullmatch(source_commit) or source_commit != expected_commit:
        raise ValueError("incident evidence does not match expected source commit")
    job_id = context.get("job_id", "")
    if not JOB_ID_PATTERN.fullmatch(job_id):
        raise ValueError("invalid incident job ID")
    killed_owner = context.get("killed_owner")
    recovery_owner = context.get("recovery_owner")
    if (killed_owner, recovery_owner) != ("incident-worker-a", "incident-worker-b"):
        raise ValueError("unexpected incident worker identities")

    job = _read_json(directory / "job.json")
    if job.get("id") != job_id or job.get("state") != "succeeded" or job.get("attempts") != 2:
        raise ValueError("recovered job did not succeed on attempt two")
    spec = job.get("spec", {})
    payload = spec.get("payload", "")
    payload_hash = hashlib.sha256(payload.encode()).hexdigest()
    if payload_hash != context.get("payload_sha256") or job.get("result") != payload_hash:
        raise ValueError("terminal checksum does not match the controlled payload")
    if spec.get("max_attempts") != 2:
        raise ValueError("incident job must have exactly two permitted attempts")

    event_page = _read_json(directory / "events.json")
    if event_page.get("next_cursor"):
        raise ValueError("incident event evidence is unexpectedly paginated")
    events = event_page.get("events")
    if not isinstance(events, list) or len(events) != 5:
        raise ValueError("incident must retain exactly five lifecycle events")
    expected = [
        ("submitted", "queued", 0, "", 0),
        ("claimed", "running", 1, killed_owner, 1),
        ("lease_recovered", "retrying", 1, killed_owner, 2),
        ("claimed", "running", 2, recovery_owner, 3),
        ("succeeded", "succeeded", 2, recovery_owner, 3),
    ]
    event_ids: list[int] = []
    for event, want in zip(events, expected, strict=True):
        observed = (
            event.get("kind"), event.get("state"), event.get("attempt"),
            event.get("lease_owner", ""), event.get("lease_version", 0),
        )
        if observed != want:
            raise ValueError(f"unexpected lifecycle event: {observed}, want {want}")
        try:
            event_ids.append(int(event["id"]))
        except (KeyError, TypeError, ValueError) as error:
            raise ValueError("invalid lifecycle event ID") from error
    if event_ids != sorted(event_ids) or len(set(event_ids)) != len(event_ids):
        raise ValueError("lifecycle event IDs are not strictly increasing")
    if events[2].get("message") != "worker lease expired before completion":
        raise ValueError("lease recovery reason is missing")

    inspection = _read_json(directory / "worker-a-inspect.json")
    if not isinstance(inspection, list) or len(inspection) != 1:
        raise ValueError("worker inspection must contain one container")
    state = inspection[0].get("State", {})
    if state.get("ExitCode") != 137 or state.get("OOMKilled") is not False:
        raise ValueError("worker A was not deliberately SIGKILLed outside OOM handling")

    try:
        metrics = (directory / "worker-b-metrics.prom").read_text(encoding="utf-8")
    except OSError as error:
        raise ValueError(f"read worker-b-metrics.prom: {error}") from error
    match = ATTEMPT_COUNT_PATTERN.search(metrics)
    if match is None or float(match.group(1)) != 1:
        raise ValueError("recovery worker must observe exactly one completed attempt")

    return Incident(source_commit, job_id, killed_owner, recovery_owner, 1)


def write_report(path: Path, incident: Incident) -> None:
    path.write_text(
        "\n".join([
            "# Worker crash incident evidence",
            "",
            "| Field | Observed evidence |",
            "| --- | --- |",
            f"| Source commit | `{incident.source_commit}` |",
            f"| Job ID | `{incident.job_id}` |",
            f"| Injected failure | `{incident.killed_owner}` exited 137 after SIGKILL; not OOM-killed |",
            "| Recovery fence | lease version 1 claim → version 2 recovery → version 3 reclaim |",
            f"| Recovery owner | `{incident.recovery_owner}` |",
            "| Terminal result | `succeeded` on attempt 2 |",
            f"| Recovery-worker attempts | {incident.attempt_count} |",
            "",
            "## Operator diagnosis",
            "",
            "Worker A disappeared while holding a running lease. After the two-second lease expired, "
            "worker B recorded `lease_recovered`, advanced the fencing version, reclaimed the job, "
            "and persisted success on attempt two. A stale writer holding version 1 can no longer "
            "match the row after the recovery fence advanced it to version 2.",
            "",
            "This drill demonstrates bounded recovery for the trusted checksum executor. It does not "
            "prove exactly-once external side effects: an executor integrating with another system "
            "must make its own side effects idempotent.",
            "",
        ]),
        encoding="utf-8",
    )


def write_manifest(directory: Path, path: Path) -> None:
    lines = []
    for candidate in sorted(directory.iterdir(), key=lambda item: item.name):
        if not candidate.is_file() or candidate == path:
            continue
        digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
        lines.append(f"{digest}  {candidate.name}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--manifest", type=Path)
    arguments = parser.parse_args()
    incident = validate_incident(arguments.directory, arguments.expected_commit)
    if arguments.report:
        write_report(arguments.report, incident)
    if arguments.manifest:
        write_manifest(arguments.directory, arguments.manifest)
    print(f"validated worker crash recovery for job {incident.job_id}")


if __name__ == "__main__":
    main()
