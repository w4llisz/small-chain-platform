# Reviewed candidate attestation: a401fe1

This directory preserves the first independently reviewed release-candidate
attestation. The review was performed on 2026-11-10 while `a401fe1` was the
current remote `main` commit and no pull request, tag or GitHub Release was
open.

## Source

- Candidate: [`a401fe147a7cfbdc7b8e263144a3b315e860eec0`](https://github.com/w4llisz/small-chain-platform/commit/a401fe147a7cfbdc7b8e263144a3b315e860eec0)
- Workflow: [CI run #65](https://github.com/w4llisz/small-chain-platform/actions/runs/42605311933)
- Artifact ID: `13667377873`
- Artifact name: `candidate-a401fe147a7cfbdc7b8e263144a3b315e860eec0`
- GitHub ZIP digest: `sha256:2f50ab2ddcaabbee1e73fafb6529280aef313e91b43d3a2de91641792d37a162`
- Artifact expiry recorded by GitHub: 2026-12-09 20:03 JST
- Preserved JSON SHA-256: `ca16230e55644bf07d5422a7a9ea48bbff52450a1b5b84379a67c0197f7595ae`

The downloaded ZIP digest matched GitHub's metadata. The archive contained
exactly one regular, unencrypted, non-symlink entry named `attestation.json`.
The preserved [`attestation.json`](attestation.json) is byte-identical to that
entry and remains available after the temporary Actions artifact expires.

## Review result

CI run #65 completed `verify`, `postgres`, `web`, `e2e`, `measurement`, and
`incident` successfully before `candidate-attestation` ran successfully. The
canonical JSON binds the expected repository, `refs/heads/main`, candidate SHA,
workflow run ID and URLs. It records a clean checkout, no forbidden tracked
paths and 149 tracked files.

At the review snapshot, `a401fe1` satisfied the four immutable-candidate gates:
it was clean remote `main` with no release-blocking PR, fresh-clone checks
passed, all required CI jobs and the dependent attestation passed, and the
tracked-path gate reported no generated or credential-like release files.

This review commit advances `main`, so `a401fe1` must not later be tagged under
the repository rule that a release candidate remain current `origin/main`.
Every later head produces and requires its own artifact. This review creates no
tag, GitHub Release or deployment.

## Reproduce the artifact check

After downloading the original artifact ZIP without extracting it:

```sh
python3 scripts/verify_release_attestation.py \
  --archive candidate-a401fe1.zip \
  --digest sha256:2f50ab2ddcaabbee1e73fafb6529280aef313e91b43d3a2de91641792d37a162 \
  --repository w4llisz/small-chain-platform \
  --sha a401fe147a7cfbdc7b8e263144a3b315e860eec0 \
  --run-id 42605311933
```

The verifier checks the GitHub-supplied digest, exact archive shape, canonical
JSON schema, clean-source claims and commit/run binding. It does not verify a
cryptographic signature, search arbitrary file contents for secrets or turn
the synthetic runtime evidence into a production-readiness claim.
