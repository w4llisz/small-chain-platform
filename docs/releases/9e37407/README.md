# Reviewed candidate evidence summary: 9e37407

This directory preserves the first independently reviewed aggregate evidence
summary. The review was performed on 2026-11-15 while `9e37407` was the current
remote `main` commit and GitHub showed no open pull request.

## Source

- Candidate: [`9e3740752ad5d05bd46662262776fea5078cb4bf`](https://github.com/w4llisz/small-chain-platform/commit/9e3740752ad5d05bd46662262776fea5078cb4bf)
- Workflow: [CI run #70](https://github.com/w4llisz/small-chain-platform/actions/runs/43584342944), successful on attempt 2
- Artifact ID: `14109634266`
- Artifact name: `candidate-evidence-summary-9e3740752ad5d05bd46662262776fea5078cb4bf`
- GitHub ZIP digest: `sha256:ae890c76570069f9cda9ca1774af7f239a4315ff5de3f39c793ca3a773027e03`
- Artifact expiry recorded by GitHub: 2026-12-14 19:23 JST
- Preserved JSON SHA-256: `4f758e80b5653a6f2d7f9d893987d2f4efb800bd199bde5b49ffad0890171aea`

The ZIP was downloaded through the authorized repository account. Its digest
matched GitHub's metadata, and it contained exactly one regular, unencrypted,
non-symlink `summary.json`. The preserved [`summary.json`](summary.json) is
byte-identical to that entry.

## Review result

The offline verifier accepted the canonical schema and exact repository,
candidate SHA, workflow run and version binding. It also required the fixed ten
upstream jobs, six explicit passing checks, four ordered and uniquely identified
input artifacts, stable artifact URLs, and agreement between the indexed
release-bundle payload and release archive SHA-256.

The summary records release archive SHA-256
`f15c4784527e8ce8872395cae0c2984d0fff58c55054fdeabd0ee4164b344846`
and the four input artifact IDs `14110065746`, `14109648911`, `14110021041`
and `14109781832`. CI run #70 completed all 11 jobs successfully. Attempt 1
encountered a transient Docker Hub PostgreSQL blob error; the failed jobs were
retried and attempt 2 completed successfully.

This is a historical review snapshot. The commit adding this review advances
`main`, so `9e37407` is no longer eligible to be selected under the repository's
current-head release rule. The review does not independently re-run the four
indexed payloads, provide a cryptographic signature, authorize a tag or GitHub
Release, or establish production readiness.

## Reproduce the artifact check

After downloading the original ZIP without extracting it:

```sh
python3 scripts/verify_candidate_evidence_summary.py \
  --archive candidate-evidence-summary-9e37407.zip \
  --digest sha256:ae890c76570069f9cda9ca1774af7f239a4315ff5de3f39c793ca3a773027e03 \
  --repository w4llisz/small-chain-platform \
  --sha 9e3740752ad5d05bd46662262776fea5078cb4bf \
  --run-id 43584342944 \
  --version v0.1.0
```

The digest must come from GitHub's artifact metadata rather than from the
downloaded ZIP itself. The verifier works offline after those inputs and the
ZIP have been obtained.

## Deterministic Markdown view

[`review.md`](review.md) was generated on 2026-11-17 by downloading the original
ZIP again, checking the recorded GitHub digest and passing the command above
with `--report review.md`. The source JSON remains byte-identical to the
preserved summary. Repeating the command with a different new output filename
produced the same report bytes. A regression test checks the preserved report
against the verified historical JSON and recorded ZIP digest; that test does
not download or revalidate the original ZIP.

The report has a 16 KiB ceiling and contains no local path or generation time.
It cites all four input artifacts, their GitHub ZIP and payload digests, the
candidate/run, reported checks and upstream jobs. Output is created only after
verification and must be a new file; an existing path, including a symlink or
hardlink to the ZIP, is refused. This human-readable view preserves the same
historical-review and release-authorization limitations as the raw summary.
