# Small Chain measurement evidence

> Deterministic rendering of a validated evidence bundle. These observations describe this runner and workload; they are not a production benchmark, percentile, throughput claim, alert threshold, or SLO.

## Provenance

| Field | Observed value |
| --- | --- |
| Commit | 8444b4f0c4bc18b63ecf873504e36521acba03fb |
| Captured at | 2026-10-30T10:54:01.761081+00:00 |
| Tracked tree | clean |
| Runner | GitHub Actions shared/virtualized runner; Linux; X64 |
| Host | Linux-7.0.0-1017-azure-x86\_64-with-glibc2.39; x86\_64; AMD EPYC 7763 64-Core Processor; 4 CPUs; 16367524 KiB memory |
| Go | go version go1.27.1 linux/amd64 |
| Docker server | 28.0.4 |
| Docker Compose | 2.38.2 |

## Workload

| Field | Configured value |
| --- | --- |
| Database | postgres:16 |
| Worker processes | 1 |
| Worker concurrency | 4 |
| Volume policy | new isolated volumes deleted after collection |
| Admission concurrency | 1, 4, 16 |
| Repetitions per concurrency | 3 |
| Requests per repetition | 60 |
| Admission target | http://127.0.0.1:8080/v1/jobs |
| Payload bytes | 23 |
| Payload SHA-256 | 56a6e7dc07167fbab96d6db2e00a282f8a50d386bb0d5d7eaa37c96de284da48 |
| Task delay | 25 ms |
| Injected failures before success | 0 |
| Task attempt budget | 3 |
| Task timeout | 1000 ms |
| Request timeout | 10s |

## Admission observations

All offered requests were accepted. Millisecond values are copied from the validated generator output and rounded to three decimal places; no percentile or rate is derived.

| Concurrency | Repetition | Offered | Accepted | Client run duration (ms) | Admission min (ms) | Admission mean (ms) | Admission max (ms) |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 1 | 60 | 60 | 113.027 | 1.203 | 1.872 | 7.684 |
| 1 | 2 | 60 | 60 | 114.995 | 1.248 | 1.905 | 5.732 |
| 1 | 3 | 60 | 60 | 90.480 | 1.176 | 1.492 | 3.188 |
| 4 | 1 | 60 | 60 | 68.360 | 1.493 | 4.451 | 19.427 |
| 4 | 2 | 60 | 60 | 56.385 | 1.788 | 3.657 | 7.100 |
| 4 | 3 | 60 | 60 | 59.688 | 1.910 | 3.854 | 8.642 |
| 16 | 1 | 60 | 60 | 58.815 | 6.288 | 14.138 | 19.629 |
| 16 | 2 | 60 | 60 | 56.836 | 3.824 | 13.379 | 19.296 |
| 16 | 3 | 60 | 60 | 55.547 | 4.228 | 12.998 | 18.830 |

## Completion evidence

| Check | Observed value |
| --- | ---: |
| Durable succeeded jobs | 540 |
| Active queued/running/retrying jobs | 0 |
| Worker attempt count snapshot | 540 |
| Worker executor-duration sum snapshot (seconds) | 13.67233131 |

## Limitations

- GitHub-hosted runner performance is shared and variable.
- Client latency ends at admission response body close, not terminal completion.
- Docker stats is an end-of-run snapshot, not a peak-resource measurement.
- Scenarios run in fixed 1, 4, 16 concurrency order without a warm-up.
- All scenarios share one API process and database, so cache and order effects remain.
