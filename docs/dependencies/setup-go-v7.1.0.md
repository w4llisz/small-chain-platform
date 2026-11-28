# setup-go v7.1.0 review

Reviewed 2026-11-28 for dependency PR #8.

## Scope and provenance

- PR: https://github.com/w4llisz/small-chain-platform/pull/8
- Original commit: 57f4a9a8fc20f7f4d4e191f1f14c4a6b85f5788a
- Previous pin: b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
- New pin: d8cbb8045d7f43706b9177cceb36c1f91a6644d5
- Upstream tag binding checked through
  https://api.github.com/repos/actions/setup-go/git/ref/tags/v7.1.0
- Reviewed action metadata:
  https://github.com/actions/setup-go/blob/d8cbb8045d7f43706b9177cceb36c1f91a6644d5/action.yml

The upstream tag resolves directly to the pinned commit. The PR changes only
five uses lines in ci.yml. Integration also labels those pins v7.1.0 and
preserves the original commit as a merge parent.

## Repository compatibility

The action still accepts go-version-file and cache, and declares node24.
Every one of the five jobs still uses .go-version (1.27.3); no version range,
check-latest override, new download mirror, token input or output consumption
is introduced. Existing cache choices remain four false and one true.
Dockerfile, go.mod, go.sum, application code, job permissions and dependencies
are unchanged. Updating the installer action does not upgrade the Go compiler.

## Evidence and limits

PR CI https://github.com/w4llisz/small-chain-platform/actions/runs/45616726418
passed verify, postgres, web and e2e. The seven main-only jobs were skipped;
they are not counted as passed. Latest main before integration,
957f9f2e267b49d3a001f2a04c84d2273d6ee8ad, passed run 45984482309.

Local checks are recorded in ../verification.md. Existing tests check that all
five setup-go jobs use the canonical version file and that the README,
Dockerfile and go.mod remain compatible. Local Go tests do not execute the
GitHub-hosted action itself; the merged commit requires its own workflow.
This is a scoped dependency compatibility/provenance review, not an audit of
all upstream bundled JavaScript, a vulnerability claim, or release approval.
