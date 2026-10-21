#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  echo "TEST_DATABASE_URL is required" >&2
  exit 1
fi

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_dir="$(mktemp -d)"
children=()

cleanup() {
  status=$?
  trap - EXIT
  for pid in "${children[@]}"; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in "${children[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
  if [[ $status -ne 0 ]]; then
    for log in "$run_dir"/*.log; do
      [[ -f "$log" ]] && { echo "--- ${log##*/} ---" >&2; tail -n 100 "$log" >&2; }
    done
  fi
  rm -rf "$run_dir"
  exit "$status"
}
trap cleanup EXIT INT TERM

wait_for_url() {
  local url=$1
  python3 - "$url" <<'PY'
import sys, time, urllib.request

url = sys.argv[1]
deadline = time.monotonic() + 20
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(url, timeout=1) as response:
            if response.status == 200:
                raise SystemExit(0)
    except Exception:
        time.sleep(0.2)
raise SystemExit(f"timed out waiting for {url}")
PY
}

export DATABASE_URL="$TEST_DATABASE_URL"
"$project_dir/bin/migrate-e2e" >"$run_dir/migrate.log" 2>&1
"$project_dir/bin/small-chain-api-e2e" -storage=postgres -retention-interval=0 \
  >"$run_dir/api.log" 2>&1 &
children+=("$!")
wait_for_url "http://127.0.0.1:8080/readyz"

"$project_dir/bin/small-chain-worker-e2e" -owner=e2e-worker -concurrency=1 \
  -poll-interval=20ms -lease-duration=3s -heartbeat-interval=1s \
  >"$run_dir/worker.log" 2>&1 &
children+=("$!")

npm --prefix "$project_dir/web" run dev -- --host 127.0.0.1 --port 4173 \
  >"$run_dir/vite.log" 2>&1 &
children+=("$!")
wait_for_url "http://127.0.0.1:4173/"

npm --prefix "$project_dir/web" run e2e
