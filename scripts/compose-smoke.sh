#!/usr/bin/env bash
set -euo pipefail

project_name="small-chain-smoke-$$"

cleanup() {
  status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker compose -p "$project_name" ps || true
    docker compose -p "$project_name" logs --no-color || true
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
  return 1
}

docker compose -p "$project_name" config --quiet
docker compose -p "$project_name" up --build --detach
wait_for_url http://127.0.0.1:8080/readyz
wait_for_url http://127.0.0.1:9091/metrics

response=$(curl --fail --silent --show-error --max-time 5 \
  -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: compose-smoke-001' \
  -d '{"kind":"demo.checksum","payload":"compose smoke"}')
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

curl --fail --silent --show-error --max-time 2 http://127.0.0.1:8080/metrics | \
  grep -q 'small_chain_job_state_metrics_up 1'
curl --fail --silent --show-error --max-time 2 http://127.0.0.1:9091/metrics | \
  grep -Eq 'small_chain_worker_attempt_duration_seconds_count [1-9][0-9]*'

echo "PASS: Compose migration, API readiness, worker execution and metrics"
