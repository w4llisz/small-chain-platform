#!/usr/bin/env bash
set -euo pipefail

project_name="small-chain-incident-$$"
output_dir=${INCIDENT_OUTPUT_DIR:-"$PWD/artifacts/incident"}

if [[ -n $(git status --porcelain --untracked-files=no) ]]; then
  echo "incident evidence requires a clean tracked tree" >&2
  exit 1
fi
mkdir -p "$output_dir"
find "$output_dir" -mindepth 1 -delete

cleanup() {
  status=$?
  trap - EXIT
  if (( status != 0 )); then
    docker compose -p "$project_name" --profile incident ps -a >"$output_dir/failure-compose-ps.txt" 2>&1 || true
    docker compose -p "$project_name" --profile incident logs --no-color >"$output_dir/failure-compose-logs.txt" 2>&1 || true
  fi
  docker compose -p "$project_name" --profile incident down --volumes --remove-orphans >/dev/null 2>&1 || true
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
  echo "URL did not become ready: $url" >&2
  return 1
}

docker compose -p "$project_name" --profile incident config --quiet
docker compose -p "$project_name" up --build --detach api
wait_for_url http://127.0.0.1:8080/readyz

payload="controlled worker crash"
response=$(curl --fail --silent --show-error --max-time 5 \
  -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: incident-worker-crash-001' \
  -d "{\"kind\":\"demo.checksum\",\"payload\":\"$payload\",\"delay_ms\":5000,\"max_attempts\":2,\"timeout_ms\":10000}")
job_id=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

docker compose -p "$project_name" --profile incident up --detach incident-worker-a
wait_for_url http://127.0.0.1:9092/metrics

claimed=false
for _ in $(seq 1 60); do
  events=$(curl --fail --silent --show-error --max-time 2 \
    "http://127.0.0.1:8080/v1/jobs/$job_id/events?limit=100" 2>/dev/null || true)
  if [[ -n "$events" ]] && INCIDENT_EVENTS="$events" python3 - <<'PY'
import json
import os

events = json.loads(os.environ["INCIDENT_EVENTS"])["events"]
if not any(event.get("kind") == "claimed" and
           event.get("lease_owner") == "incident-worker-a" and
           event.get("lease_version") == 1 for event in events):
    raise SystemExit(1)
PY
  then
    claimed=true
    break
  fi
  sleep 0.1
done
if [[ "$claimed" != true ]]; then
  echo "worker A did not claim incident job" >&2
  exit 1
fi

worker_a_id=$(docker compose -p "$project_name" --profile incident ps -q incident-worker-a)
if [[ -z "$worker_a_id" ]]; then
  echo "worker A container ID is missing" >&2
  exit 1
fi
docker compose -p "$project_name" --profile incident kill --signal SIGKILL incident-worker-a
docker inspect "$worker_a_id" >"$output_dir/worker-a-inspect.json"

docker compose -p "$project_name" --profile incident up --detach incident-worker-b
wait_for_url http://127.0.0.1:9093/metrics

state=""
for _ in $(seq 1 40); do
  job=$(curl --fail --silent --show-error --max-time 2 \
    "http://127.0.0.1:8080/v1/jobs/$job_id" 2>/dev/null || true)
  if [[ -n "$job" ]]; then
    state=$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])')
    if [[ "$state" == "succeeded" ]]; then
      printf '%s\n' "$job" >"$output_dir/job.json"
      break
    fi
  fi
  sleep 1
done
if [[ "$state" != "succeeded" ]]; then
  echo "recovered job did not succeed; last state: ${state:-unknown}" >&2
  exit 1
fi

curl --fail --silent --show-error --max-time 5 \
  "http://127.0.0.1:8080/v1/jobs/$job_id/events?limit=100" >"$output_dir/events.json"
curl --fail --silent --show-error --max-time 5 \
  http://127.0.0.1:9093/metrics >"$output_dir/worker-b-metrics.prom"
docker compose -p "$project_name" --profile incident ps -a >"$output_dir/compose-ps.txt"
docker compose -p "$project_name" --profile incident logs --no-color \
  incident-worker-a incident-worker-b >"$output_dir/workers.log"

source_commit=$(git rev-parse HEAD)
payload_sha256=$(printf '%s' "$payload" | sha256sum | cut -d ' ' -f 1)
SOURCE_COMMIT="$source_commit" JOB_ID="$job_id" PAYLOAD_SHA256="$payload_sha256" \
  python3 - <<'PY' >"$output_dir/context.json"
import json
import os

json.dump({
    "schema_version": 1,
    "scenario": "worker-sigkill-recovery",
    "source_commit": os.environ["SOURCE_COMMIT"],
    "job_id": os.environ["JOB_ID"],
    "killed_owner": "incident-worker-a",
    "recovery_owner": "incident-worker-b",
    "payload_sha256": os.environ["PAYLOAD_SHA256"],
}, fp=__import__("sys").stdout, sort_keys=True, indent=2)
print()
PY

python3 scripts/assert_incident.py "$output_dir" \
  --expected-commit "$source_commit" \
  --report "$output_dir/README.md" \
  --manifest "$output_dir/manifest.sha256"

echo "PASS: worker SIGKILL recovered with lifecycle and fencing evidence for $job_id"
