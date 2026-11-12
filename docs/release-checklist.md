# v0.1.0 release-candidate checklist

This checklist turns the current portfolio scope into a reviewable release
decision. It does not declare v0.1.0 released: every unchecked gate must be
completed against the exact commit that will be tagged.

## Preparation gates

- [x] The pgx v5.12.0 security-floor decision and compatibility review are recorded below.
- [x] [`docs/evidence.md`](evidence.md) and [`docs/verification.md`](verification.md) identify the strongest evidence and its limits.
- [x] [Candidate release notes](releases/v0.1.0.md) describe memory and PostgreSQL modes, compatibility, local-only credentials and the unauthenticated demo boundary.

## Frozen scope

The candidate contains the bounded memory engine, PostgreSQL scheduling and
recovery, the React operations console, optional observability, and the
reviewed measurement and worker-crash evidence listed in the [portfolio
evidence index](evidence.md). Release work may correct verified defects,
dependencies, documentation, or reproducibility. It must not add another
scheduler, service, cloud resource, blockchain feature, or production claim.

The release does not claim authenticated multi-tenancy, TLS termination,
exactly-once external effects, multi-region failover, production availability,
an SLO, or performance outside the retained synthetic experiments.

## Reviewed dependency floor

The release candidate requires pgx v5.12.0 or later. Version 5.12.0 fixes the
upstream [simple-protocol batch injection advisory](https://github.com/jackc/pgx/security/advisories/GHSA-j38q-h3ph-jjpq)
and [authentication/channel-binding advisory](https://github.com/jackc/pgx/security/advisories/GHSA-95fj-3fxw-g62g).
Small Chain does not call `SendBatch`, select simple-protocol batches, implement
`BatchResults`, or configure `require_auth`/required channel binding. The floor
still prevents a later release edit from silently restoring a dependency with
known high-impact defects. Future pgx upgrades must repeat the PostgreSQL,
process-recovery and main-branch CI gates rather than relying on this review.

## Fresh-clone candidate check

Run from a new directory so ignored build output and cached repository state
cannot satisfy a gate accidentally:

```sh
git clone https://github.com/w4llisz/small-chain-platform.git
cd small-chain-platform
test "$(cat .go-version)" = "1.27.3"
make verify
go mod verify
go test -tags=integration -run '^$' ./internal/postgres ./internal/dbworker ./internal/httpapi
go vet -tags=integration ./internal/postgres ./internal/dbworker ./internal/httpapi
git status --short
```

`make verify` covers formatting, vet, race-enabled unit tests, repository
contract tests, six static binaries, and a real API process smoke test. The
tagged integration command compiles database-only test paths without claiming
to execute PostgreSQL. A clean final status proves the checks did not edit the
candidate.

On a `main` push, `candidate-attestation` runs only after `verify`, `postgres`,
`web`, `e2e`, `measurement`, and `incident` succeed. It checks the exact
`GITHUB_SHA`, a clean checkout, and tracked-path deny rules before uploading
`candidate-<sha>` with the commit and workflow run URLs. These path rules catch
the repository's generated output plus common private-key and credential-file
names; they are not a general secret scanner.

Before selecting a tag, independently download the unextracted
`candidate-<sha>` ZIP and run `scripts/verify_release_attestation.py` with the
digest GitHub records for that artifact, the intended SHA and its workflow run
ID. The verifier requires one canonical `attestation.json`, rejects archive
substitution, extra entries and symlinks, and checks the exact commit/run
binding. The first reviewed example is preserved under
[`docs/releases/a401fe1/`](releases/a401fe1/).

After `candidate-attestation` succeeds, the dependent `release-bundle` job
builds `small-chain-platform-v0.1.0-linux-amd64.tar.gz` from the same clean
checkout. The archive has a fixed root and contains only the API, worker,
migration and healthcheck binaries, `LICENSE`, candidate release notes,
canonical build metadata and `SHA256SUMS`. Its verifier rejects unexpected
entries, links, unsafe paths, wrong modes, incomplete hashes, a mismatched
commit and binaries whose embedded Go package/module/target metadata differs.
The bundle is a dry-run artifact retained for 30 days; it does not create a tag
or GitHub Release.

The dependent `packaged-smoke` job downloads that retained archive rather than
rebuilding it. Without a Go setup step, it safely expands the fixed layout into
a new directory, runs the packaged memory API with an empty `PATH`, probes it
with the packaged healthcheck, completes a retrying checksum job and requires a
zero exit after SIGTERM. Its commit-bound `packaged-smoke-<sha>` JSON evidence
is retained for 30 days. PostgreSQL package execution is outside this gate.

## Immutable candidate gates

- [ ] The candidate is the current `origin/main` commit, has no unresolved release-blocking PR and the tracked tree is clean.
- [ ] The fresh-clone commands above pass with the Go version in `.go-version`.
- [ ] The main-branch CI run for that exact commit passes `verify`, `postgres`, `web`, `e2e`, `measurement`, and `incident`, then uploads its successful `candidate-<sha>` attestation.
- [ ] No credentials, generated artifacts, binaries, coverage output, or dirty measurement bundles are tracked.
- [ ] The dependent release-bundle job verifies and uploads the exact candidate's bounded Linux amd64 archive.
- [ ] The packaged-smoke job downloads that exact archive and passes healthcheck, checksum/retry and graceful SIGTERM checks without a Go toolchain runtime path.

## Tag and publish

Only after every gate is checked on one immutable commit:

```sh
git fetch origin main --tags
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git tag -a v0.1.0 -m "small-chain-platform v0.1.0"
git push origin v0.1.0
```

Create the GitHub release from that annotated tag and link its successful CI
run. If a required job fails or `main` advances, do not reuse the candidate
result: fix or rebase, then rerun the gates on the new commit. The release
process creates no deployment and provisions no cloud resources.
