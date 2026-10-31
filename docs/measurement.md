# Reproducible measurement protocol

Small Chain separates measurement tooling from performance claims. The
`small-chain-load` command and the first reviewed [controlled CI
measurement](measurements/8444b4f/README.md) are implemented. That single
shared-runner result does not establish production throughput, percentile
latency, a worker-scaling ratio or an SLO.

## Generator contract

The command sends one finite HTTP admission workload to `POST /v1/jobs`:

- `-requests` is limited to 1–100,000 and every request has a deterministic,
  distinct idempotency key.
- `-concurrency` is limited to 1–1,000 and cannot exceed the request count.
- every request has its own deadline of 10 milliseconds to 60 seconds;
  SIGINT/SIGTERM cancels all in-flight calls.
- there are no automatic retries. A queue rejection or ambiguous transport
  failure remains visible instead of silently changing offered load.
- every response body is read through a 4 KiB limit and connection reuse is
  bounded by the configured concurrency.
- the shared task controls are recorded in the report. The payload itself is
  omitted; its byte count and SHA-256 are included.

The JSON schema is versioned. Counts classify every offered request exactly
once as accepted, replayed, queue-full, record-capacity, draining, another 4xx,
5xx, another status, or transport failure. Admission latency measures the HTTP
round trip through closing the bounded response body. It reports min/mean/max
and fixed cumulative buckets, not a percentile.

The generator stops at admission. Worker completion, queue wait, retries,
database time and resource usage require separate evidence from the API,
Prometheus and the host. `duration_ms` is wall time for the finite client run;
it is not service throughput by itself.

## One local run

Start from a clean durable stack when new admission is the intended workload:

```sh
make compose-clean
make compose-up
go run ./cmd/small-chain-load \
  -url=http://127.0.0.1:8080 \
  -requests=200 \
  -concurrency=4 \
  -request-timeout=5s \
  -key-prefix=m4-c4-run1 \
  -payload=small-chain-load \
  -delay-ms=25 \
  -fail-first-attempts=0 \
  -max-attempts=3 \
  -timeout-ms=1000 > load-result.json
```

Use a new key prefix for every new-admission run. Reusing the prefix deliberately
measures idempotent replay and must be labeled as such. Preserve the raw JSON;
do not copy only a favorable subset of its counts.

## CI evidence bundle

`make measure-compose` owns an isolated Compose project and new volumes, then
runs three 60-request repetitions at concurrency 1, 4 and 16 in that fixed
order. It requires every admission to be accepted, waits for all 540 jobs to
reach `succeeded`, requires no queued/running/retrying jobs to remain, and
checks that the worker histogram observed at least 540 attempts. Every wait is
bounded. The exit trap always removes the project's containers and volumes.

The command writes `artifacts/measurement/` with the nine raw generator
reports, a hashed scenario index, commit/runner/toolchain context, raw
Prometheus query responses, final Compose state, a final Docker resource
snapshot and a SHA-256 manifest. On a `main` push, CI uploads this directory as
`measurement-<commit>` for 14 days, including failure diagnostics when
collection stops early. The output directory must be empty before collection,
which prevents evidence from separate runs being mixed.

Before a bundle is used in a report, run:

```sh
go run ./cmd/small-chain-evidence \
  -dir artifacts/measurement \
  -expected-commit="$(git rev-parse HEAD)"
```

The verifier requires the bundle to name that exact commit and, by default,
rejects a dirty tracked tree. It
recomputes every manifest and scenario-index hash, rejects missing or extra JSON
files, checks the declared workload matrix and admission accounting, and
requires the Prometheus snapshots to show healthy targets, 540 successes, zero
active jobs and the expected worker observations. It emits only a small
validated context summary; it does not calculate latency percentiles or
throughput. This makes report input reviewable without rerunning Docker.

After validation, the same command can render a deterministic Markdown view:

```sh
go run ./cmd/small-chain-evidence \
  -dir artifacts/measurement \
  -expected-commit="$(git rev-parse HEAD)" \
  -format=markdown > artifacts/measurement/report.md
```

The collector creates this derived file automatically. It contains recorded
runner/toolchain context, the exact workload, one row per scenario, and the
terminal Prometheus snapshots. Scenario task controls must match the context;
mixed workloads are rejected even when their hashes are internally valid. The
renderer sorts scenario rows and rounds copied millisecond values to three
decimal places. It does not calculate a percentile, request rate, scaling
ratio, alert threshold or SLO. The hashed raw JSON remains the authority.

This bundle and its generated view are collection evidence, not a reviewed
benchmark report. GitHub-hosted
runners are shared, execution order is fixed without a warm-up, all scenarios
reuse one process/database, and Docker stats is sampled only at the end. A
reviewed report must preserve those limitations and show repeatability before
deriving percentiles or thresholds.

## Required report context

A committed result must include all of the following alongside raw output:

1. commit SHA, dirty-tree state, Go version, Docker/Compose versions;
2. OS, CPU model/count, memory limit and whether the host was shared;
3. database/API/worker configuration and whether volumes were cold or reused;
4. the exact generator command, key reset policy, warm-up, run count and run
   duration;
5. admission classifications plus terminal-state convergence from the API or
   Prometheus;
6. worker histogram boundaries, process restarts and any missing samples;
7. limitations and every failed/aborted run, not just the selected result.

Only after a fixed workload matrix has repeatable raw evidence should the
project calculate percentiles from histogram buckets or propose alert/SLO
thresholds.

## Reviewed evidence

The first preserved bundle is indexed in [`docs/measurements/`](measurements/).
Its directory contains the exact generated report, manifest, scenario outputs,
Prometheus responses, Compose state, Docker resource snapshot and review notes.
Run the validator against that directory to reproduce the integrity and
convergence decision without Docker. More independent runs are still required
before deriving a threshold or portable performance conclusion.
