#!/usr/bin/env python3
"""Exercise the real binary, SIGTERM drain, and a forced shutdown deadline."""
import hashlib
import json
import os
import signal
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def check_cli_arguments():
    # No inherited database credentials; every invocation must finish before
    # startup. subprocess.run kills and reaps a timed-out child.
    env = dict(os.environ, DATABASE_URL="")
    for binary, prefix, started in (
        ("./bin/small-chain", ["-addr=127.0.0.1:0"], "service.started"),
        ("./bin/small-chain-worker", ["-owner=cli-smoke"], "worker.started"),
    ):
        cases = [
            (["unexpected"], "unexpected positional arguments"),
            (["unexpected", "-storage=postgres"], "unexpected positional arguments"),
            (["--", "unexpected"], "unexpected positional arguments"),
            (["-unknown"], "invalid command-line flags"),
        ]
        if binary.endswith("/small-chain"):
            cases.append((["-storage=postgres", "unexpected"],
                          "unexpected positional arguments"))
        for args, expected in cases:
            result = subprocess.run([binary, *prefix, *args], env=env,
                                    capture_output=True, text=True, timeout=3)
            output = result.stdout + result.stderr
            assert result.returncode != 0, (binary, args, output)
            assert expected in output, (binary, args, output)
            assert started not in output, (binary, args, output)
        help_result = subprocess.run([binary, "-h"], env=env,
                                     capture_output=True, text=True, timeout=3)
        assert help_result.returncode == 0, help_result


def check_startup_positional_privacy():
    marker = "synthetic-private-value-DO-NOT-USE"
    for binary, event in (
        ("./bin/small-chain", "service.exit"),
        ("./bin/small-chain-worker", "worker.exit"),
    ):
        for args in (
            ["postgres://demo:" + marker + "@localhost/demo"],
            ["--", "token=" + marker],
            ["unexpected", "-trace-endpoint=http://localhost/" + marker],
        ):
            result = subprocess.run([binary, *args],
                                    env=dict(os.environ, DATABASE_URL=""),
                                    capture_output=True, text=True, timeout=3)
            assert result.returncode == 1, (binary, result.returncode)
            output = result.stdout + result.stderr
            assert marker not in output, binary + " echoed positional input"
            records = [json.loads(line) for line in result.stdout.splitlines()]
            assert len(records) == 1 and records[0]["msg"] == event, records
            assert records[0]["error"] == (
                "unexpected positional arguments: use named flags only"), records


def check_flag_privacy():
    marker = "synthetic-private-value-DO-NOT-USE"
    for binary, event, integer in (
        ("./bin/small-chain", "service.exit", "workers"),
        ("./bin/small-chain-worker", "worker.exit", "concurrency"),
    ):
        for args in (
            ["-" + integer + "=" + marker],
            ["-trace-sample-ratio=" + marker],
            ["-unknown-" + marker],
            ["-trace-endpoint=" + marker, "-" + integer],
        ):
            result = subprocess.run([binary, *args],
                                    env=dict(os.environ, DATABASE_URL=""),
                                    capture_output=True, text=True, timeout=3)
            assert marker not in result.stdout + result.stderr, binary + " echoed flag input"
            assert result.returncode == 1, (binary, result.returncode)
            records = [json.loads(line) for line in result.stdout.splitlines()]
            assert len(records) == 1 and records[0]["msg"] == event, records
            assert records[0]["error"] == "invalid command-line flags: use -h for usage", records
        for help_flag in ("-h", "-help"):
            result = subprocess.run([binary, "-trace-endpoint=" + marker, help_flag],
                                    capture_output=True, text=True, timeout=3)
            assert result.returncode == 0, binary
            assert marker not in result.stdout + result.stderr, binary
            assert "Usage of " in result.stderr and "-trace-endpoint" in result.stderr, binary


def check_retention_configuration():
    # Invalid cleanup settings must win over both absent and malformed DSNs.
    for dsn in ("", "postgres://%invalid"):
        for args, expected in (
            (["-retention-interval=-1s"], "retention-interval must not be negative"),
            (["-retention-batch=0"], "retention batch must be between 1 and 1000"),
            (["-retention-batch=1001"], "retention batch must be between 1 and 1000"),
        ):
            result = subprocess.run(
                ["./bin/small-chain", "-storage=postgres", *args],
                env=dict(os.environ, DATABASE_URL=dsn),
                capture_output=True, text=True, timeout=3)
            assert result.returncode == 1, result
            assert expected in result.stdout, result.stdout
            assert "service.started" not in result.stdout, result.stdout
    # Valid boundaries and disabled cleanup must reach the missing-DSN check.
    for args in (
        ["-retention-batch=1"], ["-retention-batch=1000"],
        ["-retention-interval=0", "-retention-batch=0"],
        ["-retention-interval=0", "-retention-batch=1001"],
    ):
        result = subprocess.run(
            ["./bin/small-chain", "-storage=postgres", *args],
            env=dict(os.environ, DATABASE_URL=""),
            capture_output=True, text=True, timeout=3)
        assert result.returncode == 1, result
        assert "DATABASE_URL is required" in result.stdout, result.stdout


def run(grace, delay_ms, expected_exit):
    with tempfile.NamedTemporaryFile(mode="w+") as logs:
        def read_events():
            # Separate file description: reading must not move the child's
            # stdout offset. Ignore an incomplete final line while it runs.
            with open(logs.name, encoding="utf-8") as reader:
                return [json.loads(line) for line in reader if line.endswith("\n")]

        process = subprocess.Popen(
            ["./bin/small-chain", "-addr=127.0.0.1:0", "-workers=1",
             "-shutdown-grace=" + grace,
             "-retention-interval=-1s", "-retention-batch=0"], stdout=logs, stderr=logs)
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
    check_flag_privacy()
    check_cli_arguments()
    check_startup_positional_privacy()
    check_retention_configuration()
    run("3s", 100, 0)
    run("20ms", 5000, 1)
    print("PASS: retention config, CLI argument rejection/help, retry, checksum, replay/conflict, memory-only 501 contract, "
          "cancel, timeout, SIGTERM drain and forced cancellation")
