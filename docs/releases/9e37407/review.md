# Candidate evidence review

Result: offline summary integrity and candidate binding verified.
This report does not authorize a tag or release or establish production readiness.

- Repository: `w4llisz/small-chain-platform`
- Version: `v0.1.0`
- Commit: [9e3740752ad5d05bd46662262776fea5078cb4bf](https://github.com/w4llisz/small-chain-platform/commit/9e3740752ad5d05bd46662262776fea5078cb4bf)
- Workflow: [run 43584342944](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944)
- Summary ZIP SHA-256: `ae890c76570069f9cda9ca1774af7f239a4315ff5de3f39c793ca3a773027e03`
- Canonical summary JSON SHA-256: `4f758e80b5653a6f2d7f9d893987d2f4efb800bd199bde5b49ffad0890171aea`
- Release archive SHA-256: `f15c4784527e8ce8872395cae0c2984d0fff58c55054fdeabd0ee4164b344846`

## Indexed input artifacts

ZIP digests identify GitHub downloads; payload digests identify their extracted contents.

### candidate-9e3740752ad5d05bd46662262776fea5078cb4bf

- Source: [artifact 14110065746](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944/artifacts/14110065746)
- Recorded ZIP size: 507 bytes
- GitHub ZIP digest: `sha256:a77f2c0b935dee37d0e10e2782eb16434b8e15b51217b7cf716d11074e0f64e5`
- Payload SHA-256: `36f64d60dabcec8651e352c891b1f925890ad628a72bbe801a8614c2c00d6fa6`

### release-bundle-9e3740752ad5d05bd46662262776fea5078cb4bf

- Source: [artifact 14109648911](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944/artifacts/14109648911)
- Recorded ZIP size: 40310867 bytes
- GitHub ZIP digest: `sha256:0c8ab00f2395c9d24334351a84cb93ed3e15d350d5239d85d3f3286f24f9e4dd`
- Payload SHA-256: `f15c4784527e8ce8872395cae0c2984d0fff58c55054fdeabd0ee4164b344846`

### packaged-smoke-9e3740752ad5d05bd46662262776fea5078cb4bf

- Source: [artifact 14110021041](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944/artifacts/14110021041)
- Recorded ZIP size: 511 bytes
- GitHub ZIP digest: `sha256:73a484c30d614794e3ee9760bde08e33ac1d60317f0ee894b18ff47990ac3e33`
- Payload SHA-256: `f8730b870752d9efa629ca2039fe22007b25cd2e5d61f9fcc3b6780149b4ff1d`

### packaged-postgres-smoke-9e3740752ad5d05bd46662262776fea5078cb4bf

- Source: [artifact 14109781832](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944/artifacts/14109781832)
- Recorded ZIP size: 656 bytes
- GitHub ZIP digest: `sha256:a3a5bde575b432e5743b9decc364ff5f6ded262621b5c7bfd1ba2d468b301749`
- Payload SHA-256: `ae6225128b270a09edd3260acfd57f9520026b404b8eac0ddee418b42bba65a3`

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
