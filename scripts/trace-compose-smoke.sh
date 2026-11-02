#!/usr/bin/env bash
set -euo pipefail

project_name="small-chain-trace-smoke-$$"
trace_dir=$(mktemp -d "${TMPDIR:-/tmp}/small-chain-traces.XXXXXX")
chmod 0777 "$trace_dir"
export SMALL_CHAIN_TRACE_DIR="$trace_dir"
export SMALL_CHAIN_TRACE_ENDPOINT="http://otel-collector:4318/v1/traces"
export SMALL_CHAIN_TRACE_SAMPLE_RATIO="1"

cleanup() {
  status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker compose -p "$project_name" --profile tracing ps || true
    docker compose -p "$project_name" --profile tracing logs --no-color || true
  fi
  docker compose -p "$project_name" --profile tracing down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$trace_dir"
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
  return 1
}

if env -u SMALL_CHAIN_TRACE_DIR -u SMALL_CHAIN_TRACE_ENDPOINT -u SMALL_CHAIN_TRACE_SAMPLE_RATIO \
  docker compose -p "$project_name" config --services | grep -qx 'otel-collector'; then
  echo "default Compose service set unexpectedly includes otel-collector" >&2
  exit 1
fi
docker compose -p "$project_name" --profile tracing config --quiet
docker compose -p "$project_name" --profile tracing config --services | grep -qx 'otel-collector'

docker compose -p "$project_name" --profile tracing up --build --detach
wait_for_url http://127.0.0.1:13133/
wait_for_url http://127.0.0.1:8080/readyz
wait_for_url http://127.0.0.1:9091/metrics

idempotency_key="compose-trace-smoke-001"
payload="compose trace smoke secret"
response=$(curl --fail --silent --show-error --max-time 5 \
  -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $idempotency_key" \
  -d "{\"kind\":\"demo.checksum\",\"payload\":\"$payload\"}")
job_id=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

for _ in $(seq 1 30); do
  state=$(curl --fail --silent --show-error --max-time 2 \
    "http://127.0.0.1:8080/v1/jobs/$job_id" | \
    python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])')
  if [[ "$state" == "succeeded" ]]; then
    break
  fi
  sleep 1
done
if [[ "${state:-}" != "succeeded" ]]; then
  echo "job $job_id did not succeed; last state: ${state:-unknown}" >&2
  exit 1
fi

trace_file="$trace_dir/traces.jsonl"
for _ in $(seq 1 30); do
  if python3 scripts/assert_traces.py "$trace_file" "$job_id" \
    --forbid "$payload" --forbid "$idempotency_key" --forbid "compose-worker" \
    >/dev/null 2>&1; then
    echo "PASS: optional Collector received correlated admission and worker spans for $job_id"
    exit 0
  fi
  sleep 1
done
python3 scripts/assert_traces.py "$trace_file" "$job_id" \
  --forbid "$payload" --forbid "$idempotency_key" --forbid "compose-worker"
