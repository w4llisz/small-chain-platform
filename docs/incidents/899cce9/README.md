# Worker crash incident evidence

| Field | Observed evidence |
| --- | --- |
| Source commit | `899cce9edf522f6ffba1591b54cdd8bda68d0e6e` |
| Job ID | `0757362e18fe3c6732e36ee9d02d7c8e` |
| Injected failure | `incident-worker-a` exited 137 after SIGKILL; not OOM-killed |
| Recovery fence | lease version 1 claim → version 2 recovery → version 3 reclaim |
| Recovery owner | `incident-worker-b` |
| Terminal result | `succeeded` on attempt 2 |
| Recovery-worker attempts | 1 |

## Operator diagnosis

Worker A disappeared while holding a running lease. After the two-second lease expired, worker B recorded `lease_recovered`, advanced the fencing version, reclaimed the job, and persisted success on attempt two. A stale writer holding version 1 can no longer match the row after the recovery fence advanced it to version 2.

This drill demonstrates bounded recovery for the trusted checksum executor. It does not prove exactly-once external side effects: an executor integrating with another system must make its own side effects idempotent.
