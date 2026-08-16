# GDS source register

This directory records volatile external facts that affect GDS implementation
or compatibility. Official documentation, source repositories, release pages,
and local runtime evidence are evidence; they do not authorize mutations.

`sources.yaml` is the current bootstrap register. A dedicated source-register
schema, freshness command, content-change detector, and release gate belong to
the source-maintenance phase. Until then, missing content digests are
`NOT_PROVEN`, not implied verification.

The currently installed `go1.26.4` toolchain is explicitly development-only.
The register pins `go1.26.5` as the initial release builder because official
Go advisories identify security fixes in that release. The full Go validation
gate fails closed until the exact registered builder is available.

Phase 05 adds current official Codex instruction, skill, plugin, and hook pages
plus the Agent Skills specification and quality guidance. Those sources prove
documented contracts only. The Codex profile remains provisional until an exact
runtime version passes isolated discovery, invocation, hook, and visibility
tests.

Phase 10 adds official capability sources for the exact owner-selected harness
set. `antigravity-cli` is the single Google CLI identity. Its workspace-native
instruction and skill surfaces are `AGENTS.md` and `.agents/skills`; the vendor
global configuration directory is only a product locator. Current Cursor docs
and changelog cover CLI Agent Skills, while the installed MiMo Code runtime
provides bounded skill-discovery inspection. No profile becomes `supported`
from documentation or binary presence alone.
