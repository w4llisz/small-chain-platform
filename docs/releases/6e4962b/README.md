# Post-upgrade candidate closure audit: 6e4962b

Reviewed on 2026-11-21 after the pgx v5.13.0 merge. At inspection, remote
`main` was `6e4962b7229253f2545a40beb88019c3bfc01506`, no PR was open, and
[CI #77, attempt 1](https://github.com/w4llisz/small-chain-platform/actions/runs/44718814246)
had completed all 11 jobs successfully. This closes the integration question
left by the dependency PR, whose main-only packaging jobs were skipped.

## Preserved evidence and provenance

| File | Origin and review |
| --- | --- |
| [summary.json](summary.json) | Exact extracted bytes from summary artifact `14621481562`; strict offline verification passed |
| [attestation.json](attestation.json) | Exact extracted bytes from candidate artifact `14621421358`; independent attestation verification passed |
| [review.md](review.md) | Generated from the verified summary; a second render was byte-identical |
| [github-metadata.json](github-metadata.json) | Selected fields transcribed from GitHub's run, jobs and artifacts APIs; a review snapshot, not a signed API response |

Both original ZIPs were downloaded through the repository account and hashed
before reading their entries. Expected digests came from GitHub metadata:

- Summary ZIP: `sha256:d100fb49cc9cf99af47945e001dac507b32bfd9b7b67d7ca9c0748eb11a6704b`
- Summary JSON: `8bb810bb2b0540bf531c5dad461b50ad5410ebea06e6e206b69b81abebf3e5d4`
- Attestation ZIP: `sha256:832f8862ed51ce9307a321a2f4710e97b12b9d7be9098dd45ef517ab3043f027`
- Attestation JSON: `8e3e99030c62197e24914c8284b111869e806fc0aac127cd797819dd73098771`

The attestation JSON hash also matched the summary's indexed payload hash.
All four indexed artifact IDs, names, sizes and ZIP digests matched the live
GitHub metadata. The summary ZIP expires on 2026-12-20 at 10:29:24 UTC;
the preserved JSON remains reviewable afterward, but cannot reproduce the
original ZIP digest without that ZIP. The repository does not retain binaries.

## Gate disposition for this commit

The rows map to the nine [immutable candidate gates](../../release-checklist.md#immutable-candidate-gates).
They describe this historical commit, not whichever commit is now `main`.

| Gate | Observed evidence | Remaining boundary |
| --- | --- | --- |
| Main identity, PRs and clean source | Exact main SHA and empty open-PR list; verified CI attestation reports a clean checkout | This review commit advances main; recheck identity before release |
| Fresh-clone checks | Go 1.27.3 `make verify`, `go mod verify`, integration-tag compilation and vet passed | Compilation ran no database tests; only the new review folder was untracked during source checks |
| Six source/integration jobs and attestation | `verify`, `postgres`, `web`, `e2e`, `measurement`, `incident`, then `candidate-attestation` all successful | These are GitHub results for this SHA, not local PostgreSQL/browser runs |
| Tracked-path hygiene | Attestation reports 168 tracked files and no forbidden paths; local deny-rule check also returned none | Filename rules are not a content-level secret scan |
| Release archive | `release-bundle` successful; summary binds archive SHA-256 `68b2d7db6415d53cc9328e8e3efa2780bc03e8465122349a397f873fe19ca2f8` | Archive binaries were not independently downloaded or executed in this audit |
| Packaged memory runtime | `packaged-smoke` successful for the same archive digest | CI synthetic checksum, retry and shutdown scope |
| Packaged PostgreSQL runtime | `packaged-postgres-smoke` successful for the same archive digest | Ephemeral database, migration/restart scope; no production failover claim |
| Aggregate binding | All ten upstream jobs plus the summary job succeeded; four artifact records agree with GitHub | Index integrity is distinct from independent re-execution |
| Independent summary review | Original ZIP digest, archive shape, canonical schema, repository/SHA/run/version binding and report reproducibility passed | Does not authorize publication or apply to a later SHA |

No failing implementation or CI gate was found for this revision. Publication
is still pending: no final release commit has been selected and no tag or
GitHub Release was created. The preparation checklist remains three checked
items and nine unchecked candidate gates because its tracked template must
not relabel a historical result as current-head success.

For a final release review, keep the completed gate record outside the candidate
tree, bound to the selected SHA and run. Recheck remote main immediately before
the separately authorized tag action. Committing another historical audit is
useful only when it records a substantive change or finding; it is not required
for every green CI run. See the [final review procedure](../../release-checklist.md#final-review-record).

## Reproduce while artifacts are retained

Download the two original, unextracted ZIPs from CI #77 and use new output paths:

```sh
python3 scripts/verify_candidate_evidence_summary.py \
  --archive candidate-evidence-summary-6e4962b.zip \
  --digest sha256:d100fb49cc9cf99af47945e001dac507b32bfd9b7b67d7ca9c0748eb11a6704b \
  --repository w4llisz/small-chain-platform \
  --sha 6e4962b7229253f2545a40beb88019c3bfc01506 \
  --run-id 44718814246 --version v0.1.0 --report review-new.md
python3 scripts/verify_release_attestation.py \
  --archive candidate-6e4962b.zip \
  --digest sha256:832f8862ed51ce9307a321a2f4710e97b12b9d7be9098dd45ef517ab3043f027 \
  --repository w4llisz/small-chain-platform \
  --sha 6e4962b7229253f2545a40beb88019c3bfc01506 \
  --run-id 44718814246
```

Obtain expected identity and digest values independently from GitHub when
reviewing a new candidate. Reusing these values intentionally accepts only
this historical revision.
