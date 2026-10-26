# Reproducible measurement protocol

Small Chain separates measurement tooling from performance claims. The
`small-chain-load` command is implemented; a controlled benchmark report is the
next milestone. Until that report exists, the repository does not claim a
throughput, percentile latency, worker-scaling ratio or SLO.

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
