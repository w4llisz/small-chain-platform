import json
import tempfile
import unittest
from pathlib import Path

from scripts.assert_traces import validate_trace_file


JOB_ID = "0123456789abcdef0123456789abcdef"


def attribute(key, kind, value):
    return {"key": key, "value": {kind: value}}


def batch(worker_outcome="succeeded", extra_attributes=None):
    extra_attributes = extra_attributes or []
    return {
        "resourceSpans": [
            {
                "resource": {"attributes": [attribute("service.name", "stringValue", "small-chain-api")]},
                "scopeSpans": [{"spans": [{
                    "name": "small_chain.http.admission",
                    "attributes": [
                        attribute("small_chain.job.id", "stringValue", JOB_ID),
                        attribute("small_chain.admission.outcome", "stringValue", "accepted"),
                    ],
                }]}],
            },
            {
                "resource": {"attributes": [attribute("service.name", "stringValue", "small-chain-worker")]},
                "scopeSpans": [{"spans": [{
                    "name": "small_chain.worker.attempt",
                    "attributes": [
                        attribute("small_chain.job.id", "stringValue", JOB_ID),
                        attribute("small_chain.attempt.outcome", "stringValue", worker_outcome),
                        *extra_attributes,
                    ],
                }]}],
            },
        ]
    }


class TraceEvidenceTest(unittest.TestCase):
    def validate(self, payload, forbidden=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "traces.jsonl"
            path.write_text(json.dumps(payload) + "\n", encoding="utf-8")
            validate_trace_file(path, JOB_ID, forbidden or [])

    def test_accepts_correlated_admission_and_attempt(self):
        self.validate(batch())

    def test_rejects_missing_successful_attempt(self):
        with self.assertRaisesRegex(ValueError, "missing correlated"):
            self.validate(batch(worker_outcome="failed"))

    def test_rejects_sensitive_attribute_key(self):
        with self.assertRaisesRegex(ValueError, "forbidden trace attribute"):
            self.validate(batch(extra_attributes=[attribute("task.payload", "stringValue", "secret")]))

    def test_rejects_forbidden_attribute_value(self):
        with self.assertRaisesRegex(ValueError, "forbidden value"):
            self.validate(batch(extra_attributes=[attribute("unexpected", "stringValue", "secret-key")]), ["secret-key"])


if __name__ == "__main__":
    unittest.main()
