#!/usr/bin/env python3
"""Exercise the real binary, SIGTERM drain, and a forced shutdown deadline."""
import hashlib
import json
import signal
import subprocess
import tempfile
import time
import urllib.request


def run(grace, delay_ms, expected_exit):
    with tempfile.NamedTemporaryFile(mode="w+") as logs:
        def read_events():
            # Separate file description: reading must not move the child's
            # stdout offset. Ignore an incomplete final line while it runs.
            with open(logs.name, encoding="utf-8") as reader:
                return [json.loads(line) for line in reader if line.endswith("\n")]

        process = subprocess.Popen(
            ["./bin/small-chain", "-addr=127.0.0.1:0", "-workers=1",
             "-shutdown-grace=" + grace], stdout=logs, stderr=logs)
        try:
            deadline = time.monotonic() + 5
            address = None
            while address is None:
                for event in read_events():
                    if event.get("msg") == "service.started":
                        address = event["address"]
                if process.poll() is not None or time.monotonic() > deadline:
                    raise AssertionError("service failed to start")
                time.sleep(0.01)
            base = "http://" + address

            def call(method, path, body=None, key=None):
                headers = {"Content-Type": "application/json"}
                if key:
                    headers["Idempotency-Key"] = key
                data = json.dumps(body).encode() if body is not None else None
                with urllib.request.urlopen(urllib.request.Request(
                        base + path, data=data, headers=headers, method=method), timeout=2) as response:
                    return response.status, json.load(response)

            assert call("GET", "/readyz")[0] == 200
            # First prove the full result path independently of shutdown.
            spec = {"kind": "demo.checksum", "payload": "hello", "fail_first_attempts": 2}
            status, job = call("POST", "/v1/jobs", spec, "smoke")
            assert status == 202
            deadline = time.monotonic() + 5
            while True:
                _, current = call("GET", "/v1/jobs/" + job["id"])
                if current["state"] == "succeeded":
                    assert current["attempts"] == 3
                    assert current["result"] == hashlib.sha256(b"hello").hexdigest()
                    break
                assert time.monotonic() < deadline, current
                time.sleep(0.01)
            status, replay = call("POST", "/v1/jobs", spec, "smoke")
            assert status == 200 and replay["id"] == job["id"]
            pending = []
            for i in range(3):
                _, task = call("POST", "/v1/jobs", {
                    "kind": "demo.checksum", "payload": "drain",
                    "delay_ms": delay_ms, "timeout_ms": 10000}, "drain-" + str(i))
                pending.append(task["id"])
            process.send_signal(signal.SIGTERM)
            assert process.wait(timeout=5) == expected_exit
            events = read_events()
            terminal = "succeeded" if expected_exit == 0 else "canceled"
            for job_id in pending:
                assert any(e.get("job_id") == job_id and e.get("state") == terminal
                           for e in events), (job_id, terminal, events)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=5)


if __name__ == "__main__":
    run("3s", 100, 0)
    run("20ms", 5000, 1)
    print("PASS: retry, checksum, idempotency, SIGTERM drain and forced cancellation")
