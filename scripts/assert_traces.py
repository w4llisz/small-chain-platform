#!/usr/bin/env python3
"""Validate the bounded Compose trace evidence without third-party modules."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def _attributes(values: list[dict]) -> dict[str, str]:
    result: dict[str, str] = {}
    for attribute in values:
        key = attribute.get("key")
        value = attribute.get("value", {})
        if not isinstance(key, str) or not isinstance(value, dict):
            continue
        for kind in ("stringValue", "intValue", "boolValue", "doubleValue"):
            if kind in value:
                result[key] = str(value[kind]).lower() if kind == "boolValue" else str(value[kind])
                break
    return result


def _read_spans(path: Path) -> list[tuple[str, dict, dict[str, str]]]:
    spans: list[tuple[str, dict, dict[str, str]]] = []
    with path.open(encoding="utf-8") as evidence:
        for line_number, line in enumerate(evidence, 1):
            if not line.strip():
                continue
            try:
                batch = json.loads(line)
            except json.JSONDecodeError as error:
                raise ValueError(f"invalid OTLP JSON at line {line_number}: {error}") from error
            for resource_spans in batch.get("resourceSpans", []):
                resource = _attributes(resource_spans.get("resource", {}).get("attributes", []))
                service = resource.get("service.name", "")
                for scope_spans in resource_spans.get("scopeSpans", []):
                    for span in scope_spans.get("spans", []):
                        spans.append((service, span, _attributes(span.get("attributes", []))))
    return spans


def validate_trace_file(path: Path, job_id: str, forbidden_values: list[str]) -> None:
    if len(job_id) != 32 or any(character not in "0123456789abcdef" for character in job_id):
        raise ValueError("job ID must be 32 lowercase hexadecimal characters")
    raw_evidence = path.read_text(encoding="utf-8")
    for forbidden in forbidden_values:
        if forbidden and forbidden in raw_evidence:
            raise ValueError("forbidden value appears in trace evidence")
    spans = _read_spans(path)
    required = {
        ("small-chain-api", "small_chain.http.admission", "accepted"),
        ("small-chain-worker", "small_chain.worker.attempt", "succeeded"),
    }
    found: set[tuple[str, str, str]] = set()
    forbidden_keys = ("payload", "idempotency_key", "idempotency-key", "worker.owner", "lease.owner", "baggage")
    for service, span, attributes in spans:
        for key, value in attributes.items():
            if any(fragment in key.lower() for fragment in forbidden_keys):
                raise ValueError(f"forbidden trace attribute: {key}")
            if any(forbidden and forbidden in value for forbidden in forbidden_values):
                raise ValueError(f"forbidden value exported by attribute {key}")
        if attributes.get("small_chain.job.id") != job_id:
            continue
        name = span.get("name", "")
        if service == "small-chain-api" and name == "small_chain.http.admission":
            found.add((service, name, attributes.get("small_chain.admission.outcome", "")))
        if service == "small-chain-worker" and name == "small_chain.worker.attempt":
            found.add((service, name, attributes.get("small_chain.attempt.outcome", "")))
    missing = required - found
    if missing:
        raise ValueError(f"missing correlated trace evidence: {sorted(missing)}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    parser.add_argument("job_id")
    parser.add_argument("--forbid", action="append", default=[])
    arguments = parser.parse_args()
    validate_trace_file(arguments.path, arguments.job_id, arguments.forbid)


if __name__ == "__main__":
    main()
