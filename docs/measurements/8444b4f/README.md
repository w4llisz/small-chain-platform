# Measurement review: `8444b4f`

This directory preserves the first reviewed controlled-workload evidence from
Small Chain. GitHub Actions [run #53](https://github.com/w4llisz/small-chain-platform/actions/runs/41043908030)
completed all five jobs successfully, including the measurement, PostgreSQL,
real Chromium E2E and Compose jobs.

## Provenance and verification

- source: `8444b4f0c4bc18b63ecf873504e36521acba03fb`
- artifact: `measurement-8444b4f0c4bc18b63ecf873504e36521acba03fb`
- artifact ID: `12971606734`
- downloaded ZIP SHA-256: `26370acb4f50dc199715653abc0b6ddac876d007ec3b2bd89fa0f3f84f43ad1b`
- captured: 2026-10-30 10:54 UTC on a shared GitHub Actions runner
- reviewed: 2026-10-31

The downloaded ZIP digest matched the digest returned by GitHub. The repository
validator recomputed `manifest.json`, checked every covered JSON file and
scenario digest, bound the bundle to the full source commit and clean tree, and
proved workload accounting plus terminal Prometheus convergence. Regenerating
`report.md` from the committed JSON produced an identical file.

Re-run that audit from the repository root:

```sh
go run ./cmd/small-chain-evidence \
  -dir docs/measurements/8444b4f \
  -expected-commit=8444b4f0c4bc18b63ecf873504e36521acba03fb
```

## Reviewed observations

- All 540 offered requests were accepted; no replay, overload, other HTTP or
  transport outcome was recorded.
- All 540 jobs reached durable `succeeded`; queued, running and retrying were
  zero at the terminal snapshot.
- The worker recorded exactly 540 attempts and 13.67233131 seconds of executor
  time for tasks configured with a 25 ms synthetic delay.
- Per-run admission means were 1.492–1.905 ms at concurrency 1,
  3.657–4.451 ms at concurrency 4, and 12.998–14.138 ms at concurrency 16.
  These are descriptive ranges from three repetitions, not percentiles.
- Finite client-run durations were 90.480–114.995 ms, 56.385–68.360 ms and
  55.547–58.815 ms respectively. The experiment does not divide these values
  into a service-throughput claim.

Higher client concurrency shortened this small finite run while increasing the
observed admission mean. The fixed execution order, lack of warm-up, shared
runner, shared process/database and three repetitions prevent a causal scaling
or production-capacity conclusion. See the generated [report](report.md) for
every scenario and the hashed JSON files in this directory for the raw inputs.
