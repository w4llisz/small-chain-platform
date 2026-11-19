#!/usr/bin/env python3
"""Exercise the real binary, SIGTERM drain, and a forced shutdown deadline."""
import hashlib
import json
import signal
import subprocess
import tempfile
import time
import urllib.error
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
                request = urllib.request.Request(
                    base + path, data=data, headers=headers, method=method)
                try:
                    response = urllib.request.urlopen(request, timeout=2)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    return response.status, json.load(response)

            assert call("GET", "/readyz")[0] == 200
            with urllib.request.urlopen(base + "/metrics", timeout=2) as response:
                assert response.status == 200
                assert 'small_chain_backend_info{storage="memory"} 1' in response.read().decode()
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
            status, conflict = call("POST", "/v1/jobs", dict(spec, payload="changed"), "smoke")
            assert status == 409 and conflict["error"]["code"] == "conflict", conflict
            for path, code in (
                ("/v1/jobs?state=failed&limit=50", "job_list_unavailable"),
                ("/v1/jobs/" + job["id"] + "/events?limit=50", "event_history_unavailable"),
            ):
                status, error = call("GET", path)
                assert status == 501 and error["error"]["code"] == code, error

            status, cancel_job = call("POST", "/v1/jobs", {
                "kind": "demo.checksum", "delay_ms": 5000, "timeout_ms": 10000}, "cancel-001")
            assert status == 202
            cancel_path = "/v1/jobs/" + cancel_job["id"]
            status, canceled = call("POST", cancel_path + "/cancel")
            assert status == 200 and canceled["state"] == "canceled", canceled
            status, canceled = call("GET", cancel_path)
            assert status == 200 and canceled["state"] == "canceled", canceled

            status, timeout_job = call("POST", "/v1/jobs", {
                "kind": "demo.checksum", "delay_ms": 500, "timeout_ms": 20}, "timeout-001")
            assert status == 202
            deadline = time.monotonic() + 5
            while True:
                status, timed_out = call("GET", "/v1/jobs/" + timeout_job["id"])
                assert status == 200
                if timed_out["state"] == "failed":
                    assert timed_out["attempts"] == 1, timed_out
                    assert timed_out["error"] == "context deadline exceeded", timed_out
                    break
                assert time.monotonic() < deadline, timed_out
                time.sleep(0.01)
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
    print("PASS: retry, checksum, replay/conflict, memory-only 501 contract, "
          "cancel, timeout, SIGTERM drain and forced cancellation")
