# Reviewed incident evidence

This directory preserves selected synthetic failure drills after their ephemeral
GitHub Actions artifacts have been independently reviewed. The extracted files
remain byte-for-byte inputs from CI; review metadata lives here so it does not
change the artifact's internal manifest.

## Worker SIGKILL recovery — CI run #57

| Field | Reviewed value |
| --- | --- |
| Source commit | [`899cce9edf522f6ffba1591b54cdd8bda68d0e6e`](https://github.com/w4llisz/small-chain-platform/commit/899cce9edf522f6ffba1591b54cdd8bda68d0e6e) |
| Workflow run | [CI #57](https://github.com/w4llisz/small-chain-platform/actions/runs/41617447374) — all six jobs passed |
| Artifact | [`worker-crash-incident-899cce9edf522f6ffba1591b54cdd8bda68d0e6e`](https://github.com/w4llisz/small-chain-platform/actions/runs/41617447374/artifacts/13220904619), ID `13220904619` |
| ZIP SHA-256 | `1ff017a6573fc4a6fb5e785c5d71b42f8c1e37d45e0997ed895065f8d44dc7b8` |
| Preserved evidence | [`899cce9/`](899cce9/) |
| Review date | 2026-11-04 |

The downloaded ZIP digest matched the digest reported by GitHub. The checked-in
`manifest.sha256` then matched all eight other extracted files with exact file
coverage. `scripts/assert_incident.py` accepted the source commit, terminal
checksum, SIGKILL inspection, recovery-worker metric and exact five-event
sequence. Regenerating the operator report produced a byte-identical `README.md`.

The observed job `0757362e18fe3c6732e36ee9d02d7c8e` moved through claim version 1,
recovery fence version 2, reclaim version 3 and success version 3. Worker A exited
137 with `OOMKilled=false`; worker B completed attempt two and recorded one
completed attempt. These observations show bounded lease recovery and stale-row
write fencing for this trusted checksum task. They do not prove exactly-once
execution, external side-effect idempotency, production availability or an SLO.

Recheck the preserved artifact without Docker:

```sh
python3 scripts/assert_incident.py docs/incidents/899cce9 \
  --expected-commit 899cce9edf522f6ffba1591b54cdd8bda68d0e6e \
  --verify-manifest
```
