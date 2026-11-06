# v0.1.0 release-candidate checklist

This checklist turns the current portfolio scope into a reviewable release
decision. It does not declare v0.1.0 released: every unchecked gate must be
completed against the exact commit that will be tagged.

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

## Candidate gates

- [ ] The candidate is the current `origin/main` commit and the tracked tree is clean.
- [ ] The fresh-clone commands above pass with the Go version in `.go-version`.
- [ ] The main-branch CI run for that exact commit passes `verify`, `postgres`, `web`, `e2e`, `measurement`, and `incident`.
- [ ] Open dependency PRs are either merged after their own checks or explicitly deferred; none is silently folded into the release.
- [ ] [`docs/evidence.md`](evidence.md) and [`docs/verification.md`](verification.md) identify the strongest evidence and its limits.
- [ ] Release notes describe the memory default, opt-in PostgreSQL mode, local-only Compose credentials, and unauthenticated demo boundary.
- [ ] No credentials, generated artifacts, binaries, coverage output, or dirty measurement bundles are tracked.

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
