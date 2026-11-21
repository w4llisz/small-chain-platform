# Candidate evidence review

Result: offline summary integrity and candidate binding verified.
This report does not authorize a tag or release or establish production readiness.

- Repository: `w4llisz/small-chain-platform`
- Version: `v0.1.0`
- Commit: [6e4962b7229253f2545a40beb88019c3bfc01506](https://github.com/w4llisz/small-chain-platform/commit/6e4962b7229253f2545a40beb88019c3bfc01506)
- Workflow: [run 44718814246](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246)
- Summary ZIP SHA-256: `d100fb49cc9cf99af47945e001dac507b32bfd9b7b67d7ca9c0748eb11a6704b`
- Canonical summary JSON SHA-256: `8bb810bb2b0540bf531c5dad461b50ad5410ebea06e6e206b69b81abebf3e5d4`
- Release archive SHA-256: `68b2d7db6415d53cc9328e8e3efa2780bc03e8465122349a397f873fe19ca2f8`

## Indexed input artifacts

ZIP digests identify GitHub downloads; payload digests identify their extracted contents.

### candidate-6e4962b7229253f2545a40beb88019c3bfc01506

- Source: [artifact 14621421358](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246/artifacts/14621421358)
- Recorded ZIP size: 507 bytes
- GitHub ZIP digest: `sha256:832f8862ed51ce9307a321a2f4710e97b12b9d7be9098dd45ef517ab3043f027`
- Payload SHA-256: `8e3e99030c62197e24914c8284b111869e806fc0aac127cd797819dd73098771`

### release-bundle-6e4962b7229253f2545a40beb88019c3bfc01506

- Source: [artifact 14620473464](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246/artifacts/14620473464)
- Recorded ZIP size: 40328672 bytes
- GitHub ZIP digest: `sha256:f10626d0ba4ab2d73eb1f059bb939f7021c39a3cf5e8b444cebdced00b044015`
- Payload SHA-256: `68b2d7db6415d53cc9328e8e3efa2780bc03e8465122349a397f873fe19ca2f8`

### packaged-smoke-6e4962b7229253f2545a40beb88019c3bfc01506

- Source: [artifact 14620767519](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246/artifacts/14620767519)
- Recorded ZIP size: 511 bytes
- GitHub ZIP digest: `sha256:f29a5157c4026a81ed86fd98c64bc3d4b8959b04c9f3632ae43617fc6c7ef51e`
- Payload SHA-256: `a73afde5abb2fbfa89ea812179f63b9bdf162d2ddb45e46f823192018174f24f`

### packaged-postgres-smoke-6e4962b7229253f2545a40beb88019c3bfc01506

- Source: [artifact 14620358747](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246/artifacts/14620358747)
- Recorded ZIP size: 658 bytes
- GitHub ZIP digest: `sha256:ebeadf1b49a701ae7a25d4c10c38ba906a67bdcbe29b94859513155a385d623e`
- Payload SHA-256: `2878da27abf3adc2c0a2b460dfebc7739647c8a99fe8f0b3f9ab00f01835fbd2`

## Checks recorded by the summary

- `archive_digest_consensus`: passed
- `artifact_identity_binding`: passed
- `attestation_binding`: passed
- `packaged_postgres_restart_evidence`: passed
- `packaged_runtime_evidence`: passed
- `required_jobs_successful`: passed

## Upstream jobs recorded as successful

- `candidate-attestation`
- `e2e`
- `incident`
- `measurement`
- `packaged-postgres-smoke`
- `packaged-smoke`
- `postgres`
- `release-bundle`
- `verify`
- `web`

## Review boundary

The ZIP digest and repository/SHA/run/version expectations must come from trusted
metadata supplied by the reviewer. This offline view checks the summary; it does
not download or independently re-run the four indexed payloads or their tests.
It provides no cryptographic signature, current-main check or current CI status.
Artifact links may expire. Historical success cannot be applied to a newer commit.
Tagging and publishing require a separate current-head review and authorization.
The synthetic checksum evidence does not prove production readiness, an SLO,
PostgreSQL failover or exactly-once external side effects.
