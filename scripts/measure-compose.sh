#!/usr/bin/env bash
set -euo pipefail

output_dir=${1:-artifacts/measurement}
project_name="small-chain-measure-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-1}"
requests_per_run=60
repetitions=3
concurrencies=(1 4 16)
expected_total=$((requests_per_run * repetitions * ${#concurrencies[@]}))

if [[ -d "$output_dir" ]] && find "$output_dir" -mindepth 1 -print -quit | grep -q .; then
  echo "measurement output directory must be empty: $output_dir" >&2
  exit 1
fi
mkdir -p "$output_dir" bin

cleanup() {
  status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker compose -p "$project_name" ps --all >"$output_dir/compose-ps-failure.txt" 2>&1 || true
    docker compose -p "$project_name" logs --no-color >"$output_dir/compose-failure.log" 2>&1 || true
  fi
  docker compose -p "$project_name" down --volumes --remove-orphans >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

wait_for_url() {
  url=$1
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "endpoint did not become ready: $url" >&2
  return 1
}

prometheus_value() {
  query=$1
  curl --fail --silent --show-error --max-time 2 \
    --get --data-urlencode "query=$query" \
    http://127.0.0.1:9090/api/v1/query | \
    python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; assert len(r)==1; print(r[0]["value"][1])'
}

wait_for_prometheus_minimum() {
  query=$1
  minimum=$2
  for _ in $(seq 1 60); do
    value=$(prometheus_value "$query" 2>/dev/null || true)
    if [[ -n "$value" ]] && python3 - "$value" "$minimum" <<'PY'
import sys
raise SystemExit(0 if float(sys.argv[1]) >= float(sys.argv[2]) else 1)
PY
    then
      return 0
    fi
    sleep 1
  done
  echo "Prometheus query did not reach $minimum: $query" >&2
  return 1
}

snapshot_query() {
  name=$1
  query=$2
  curl --fail --silent --show-error --max-time 5 \
    --get --data-urlencode "query=$query" \
    http://127.0.0.1:9090/api/v1/query >"$output_dir/prometheus-$name.json"
}

docker compose -p "$project_name" config --quiet
docker compose -p "$project_name" up --build --detach
wait_for_url http://127.0.0.1:8080/readyz
wait_for_url http://127.0.0.1:9090/-/ready
wait_for_prometheus_minimum 'sum(up{job=~"small-chain-(api|worker)"})' 2

CGO_ENABLED=0 go build -trimpath -o bin/small-chain-load ./cmd/small-chain-load
CGO_ENABLED=0 go build -trimpath -o bin/small-chain-evidence ./cmd/small-chain-evidence

python3 - "$output_dir/context.json" "$requests_per_run" "$repetitions" "${concurrencies[*]}" <<'PY'
import datetime
import json
import os
import pathlib
import platform
import subprocess
import sys

def command(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()

cpu_model = "unknown"
cpuinfo = pathlib.Path("/proc/cpuinfo")
if cpuinfo.exists():
    for line in cpuinfo.read_text().splitlines():
        if line.startswith("model name"):
            cpu_model = line.split(":", 1)[1].strip()
            break

memory_kib = None
meminfo = pathlib.Path("/proc/meminfo")
if meminfo.exists():
    for line in meminfo.read_text().splitlines():
        if line.startswith("MemTotal:"):
            memory_kib = int(line.split()[1])
            break

payload = {
    "schema_version": 1,
    "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "source": {
        "commit_sha": command("git", "rev-parse", "HEAD"),
        "tracked_tree_dirty": bool(command("git", "status", "--porcelain", "--untracked-files=no")),
    },
    "runner": {
        "github_actions": os.environ.get("GITHUB_ACTIONS") == "true",
        "run_id": os.environ.get("GITHUB_RUN_ID"),
        "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT"),
        "runner_name": os.environ.get("RUNNER_NAME"),
        "runner_os": os.environ.get("RUNNER_OS"),
        "runner_arch": os.environ.get("RUNNER_ARCH"),
        "shared_or_virtualized": True,
    },
    "host": {
        "platform": platform.platform(),
        "machine": platform.machine(),
        "cpu_count": os.cpu_count(),
        "cpu_model": cpu_model,
        "memory_total_kib": memory_kib,
    },
    "toolchain": {
        "go": command("go", "version"),
        "docker_server": command("docker", "version", "--format", "{{.Server.Version}}"),
        "docker_compose": command("docker", "compose", "version", "--short"),
    },
    "stack": {
        "database": "postgres:16",
        "worker_processes": 1,
        "worker_concurrency": 4,
        "volume_policy": "new isolated volumes deleted after collection",
    },
    "matrix": {
        "requests_per_run": int(sys.argv[2]),
        "repetitions": int(sys.argv[3]),
        "concurrency": [int(value) for value in sys.argv[4].split()],
        "delay_ms": 25,
        "fail_first_attempts": 0,
        "max_attempts": 3,
        "timeout_ms": 1000,
        "request_timeout": "10s",
    },
    "limitations": [
        "GitHub-hosted runner performance is shared and variable.",
        "Client latency ends at admission response body close, not terminal completion.",
        "Docker stats is an end-of-run snapshot, not a peak-resource measurement.",
        "Scenarios run in fixed 1, 4, 16 concurrency order without a warm-up.",
        "All scenarios share one API process and database, so cache and order effects remain.",
    ],
}
pathlib.Path(sys.argv[1]).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
PY

for concurrency in "${concurrencies[@]}"; do
  for repetition in $(seq 1 "$repetitions"); do
    report="$output_dir/scenario-c${concurrency}-r${repetition}.json"
    bin/small-chain-load \
      -url=http://127.0.0.1:8080 \
      -requests="$requests_per_run" \
      -concurrency="$concurrency" \
      -request-timeout=10s \
      -key-prefix="m45-c${concurrency}-r${repetition}" \
      -payload=small-chain-measurement \
      -delay-ms=25 \
      -fail-first-attempts=0 \
      -max-attempts=3 \
      -timeout-ms=1000 >"$report"
  done
done

python3 - "$output_dir" "$requests_per_run" "$repetitions" "${concurrencies[*]}" <<'PY'
import hashlib
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
requests = int(sys.argv[2])
repetitions = int(sys.argv[3])
concurrencies = [int(value) for value in sys.argv[4].split()]
scenarios = []
for concurrency in concurrencies:
    for repetition in range(1, repetitions + 1):
        path = root / f"scenario-c{concurrency}-r{repetition}.json"
        report = json.loads(path.read_text())
        counts = report["counts"]
        outcomes = sum(value for key, value in counts.items() if key != "offered")
        assert counts["offered"] == requests == outcomes
        assert counts["accepted"] == requests
        assert all(value == 0 for key, value in counts.items() if key not in {"offered", "accepted"})
        assert report["workload"]["concurrency"] == concurrency
        assert report["admission_latency"]["cumulative_buckets"][-1]["count"] == requests
        scenarios.append({
            "file": path.name,
            "concurrency": concurrency,
            "repetition": repetition,
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        })
(root / "scenario-index.json").write_text(json.dumps({
    "schema_version": 1,
    "scenarios": scenarios,
}, indent=2, sort_keys=True) + "\n")
PY

wait_for_prometheus_minimum 'small_chain_jobs{state="succeeded"}' "$expected_total"
active=$(prometheus_value 'sum(small_chain_jobs{state=~"queued|running|retrying"})')
python3 - "$active" <<'PY'
import sys
assert float(sys.argv[1]) == 0, f"active jobs remain: {sys.argv[1]}"
PY
wait_for_prometheus_minimum 'sum(small_chain_worker_attempt_duration_seconds_count)' "$expected_total"

snapshot_query targets 'up{job=~"small-chain-(api|worker)"}'
snapshot_query state-health 'small_chain_job_state_metrics_up'
snapshot_query states 'small_chain_jobs'
snapshot_query worker-count 'sum(small_chain_worker_attempt_duration_seconds_count)'
snapshot_query worker-sum 'sum(small_chain_worker_attempt_duration_seconds_sum)'
snapshot_query worker-buckets 'sum by (le) (small_chain_worker_attempt_duration_seconds_bucket)'

docker compose -p "$project_name" ps --all --format json | python3 -c \
  'import json,sys; print(json.dumps([json.loads(line) for line in sys.stdin if line.strip()], indent=2, sort_keys=True))' \
  >"$output_dir/compose-ps.json"
container_ids=$(docker compose -p "$project_name" ps -q)
if [[ -n "$container_ids" ]]; then
  docker stats --no-stream --format '{{json .}}' $container_ids | python3 -c \
    'import json,sys; print(json.dumps([json.loads(line) for line in sys.stdin if line.strip()], indent=2, sort_keys=True))' \
    >"$output_dir/docker-stats-final.json"
fi

python3 - "$output_dir" <<'PY'
import hashlib
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
entries = []
for path in sorted(root.glob("*.json")):
    if path.name == "manifest.json":
        continue
    entries.append({
        "file": path.name,
        "bytes": path.stat().st_size,
        "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
    })
(root / "manifest.json").write_text(json.dumps({
    "schema_version": 1,
    "files": entries,
}, indent=2, sort_keys=True) + "\n")
PY

bin/small-chain-evidence \
  -dir="$output_dir" \
  -expected-commit="$(git rev-parse HEAD)" \
  -require-clean=true

echo "PASS: collected $expected_total terminal jobs across ${#concurrencies[@]} concurrency levels"
