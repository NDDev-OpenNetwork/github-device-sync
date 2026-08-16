# GDS policy source

Files below this directory are canonical reusable policy sources. Repository
anchors select policy IDs through `policy.profiles`; the compiler resolves IDs,
validates each source, applies the fixed tier order, and emits leaf provenance.

Directory names are organizational only. The `policy.tier`, `priority`, and
`id` fields are authoritative. Duplicate IDs, missing profiles, equal-priority
same-tier leaf conflicts, selector mismatches, and monotonic weakening are
compiler errors.

Every source declares `policy.distribution`. A public repository may consume
only public policy sources; an internal repository may consume public or
internal sources; a private repository may consume all three. The compiler and
projection generator both enforce this boundary.

Generated effective policies are not edited here or in managed repositories.
Change the canonical source, compile, review the semantic delta, and later roll
out an immutable bundle through the approved workflow.
