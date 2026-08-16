# C0 baseline integrity evidence

Status: local implementation complete; external gates pending

Date: 2026-07-11

## Completed

1. Captured a new immutable C0-start checkpoint without rewriting historical
   Phase 0 evidence:
   `artifacts/inventory/checkpoints/2026-07-11-c0-start/`.
2. Classified all 382 expanded root status paths into six non-overlapping
   ownership classes; unclassified paths: zero.
3. Verified root and all three dirty direct metadata repositories are on
   `main`, have no auxiliary worktrees or conflicts, and match their remote
   `origin/main` OIDs.
4. Added `pytest.ini` so root Python discovery is limited to `tests/` and
   cannot collect tests from independent metadata or project repositories.
5. Added a separate pinned test dependency set in `requirements/test.txt`.
6. Updated `core/README.md` from the executable CLI and package boundaries.
7. Extended `scripts/validate_go_core.sh` with successful static GDS contract
   validation and deterministic repository candidate generation.
8. Added SHA-pinned, least-privilege reusable Go and Python CI callers. Existing
   legacy smoke, actionlint, zizmor, and gitleaks lanes remain independent.
9. Reverified pytest `9.1.1` through the official PyPI JSON API and Go `1.26.5`
   through the official Go release feed; recorded pytest in the source register.

## Evidence

Commands completed successfully:

```text
python3 -m pytest --collect-only -q        24 root-owned tests
python3 -m pytest -q                       24 passed
scripts/validate_go_core.sh --quick        PASS; release remains NOT_PROVEN
bash tools/test-sync.sh                    64 checks, 0 failed
bash -n scripts/validate_go_core.sh
shellcheck scripts/validate_go_core.sh
actionlint                                 no findings
uvx --from zizmor==1.26.1 zizmor ...       no findings
gitleaks dir <each GDS-owned path>          no findings
gds validate memories                      7 valid, generated-unverified
git diff --check                           PASS
GOTOOLCHAIN=go1.26.5 \
GDS_RELEASE_GO_VERSION=go1.26.5 \
scripts/validate_go_core.sh                PASS (full)
```

The Markdown whitespace gate permits exactly two trailing spaces because the
normative design uses CommonMark hard line breaks. One trailing space, three or
more trailing spaces, trailing tabs, and any non-Markdown trailing whitespace
remain failures.

## Pending gates

- Preserve the direct metadata repository work on remote archive branches,
  perform the ADR 0018 device-workspace cutover, and retire the root gitlinks.
- Create dependency-safe atomic commits, publish the root feature branch, and
  obtain hosted CI evidence on its exact OID.
- Restart/reindex Serena after committed configuration is available. The active
  process still reports Bash-only semantics even though tracked project input
  declares Go, Python, and Bash.
- Resolve the pre-C0 direct metadata repository context experiments through the
  later projection migration. They are preserved and not silently accepted as
  target GDS projections.

Exact effects and exclusions for A1/A2 are in
`docs/migration/c0-approval-plan.md`.

## External mutation boundary

No commit, push, pull request, merge, release, provider setting, harness
installation, or system toolchain change was performed by C0 local
implementation.
