# ADR 0040: Tenancy is the provider account

Status: Accepted

Date: 2026-10-03

## Context

The estate taxonomy grew two independent axes: *who owns the repository*
(`owner:` records bound to installations and provider logins) and *what kind
of work it is* (purpose portfolios such as `servers`, `forks`,
`*-projects`). The second axis predates trait-based placement and survived as
inertia: ADR 0026 collapsed per-owner server portfolios into flat
`portfolio:servers`, and ADR 0032 did the same for `portfolio:forks`, purely
because one workspace root may not serve two portfolio selectors.

That coupling has real costs:

- Classification pretended to answer a placement question. `servers` and
  `forks` are not kinds of repository; they are directories that happened to
  want one. A fork of `example-user` and a fork of `example-org` have nothing
  in common except a filesystem preference.
- `fork_portfolio` and `match.fork` decayed into dead configuration that
  still had to be maintained: parsed, validated, and never read by the
  compiler (`core/estate/compiler.go`).
- Selector assignment was forced to emit a portfolio (`minItems: 1`), so
  every estate paid for the abstraction whether it used it or not.
- `GDS_DEVICE_WORKSPACE_ROOT_REUSED` existed only to keep placement
  injective when placement keys were labels. Trait matches do not need it:
  two rules can name the same root and stay disjoint by construction.

## Decision

**A tenant is a provider account — nothing more, nothing else.** One
personal user account, and otherwise organizations by their exact provider
login. The model keeps `portfolio:` as the reference namespace but its values
are tenant names (`portfolio:nddev-it-com`, `portfolio:example-user`), never
purpose names.

1. **`materialization.include[].match` places repositories by facts, not
   labels.** `match.owner_login`, `match.names`, `match.name_prefixes` and
   `match.visibility` are evaluated against the repository's provider
   identity and visibility contract. Includes evaluate in declaration order;
   the first match wins. `selector` membership placement stays valid for
   compatibility but is no longer the reference mechanism.
2. **`policy.match.name_prefixes` lets cross-owner policies exist without a
   fake grouping.** A portfolio-tier policy that governed "everything named
   `server-*`" now says exactly that. The `portfolio` policy tier stays for
   policies that genuinely name a tenant.
3. **Dead fork plumbing stays readable, not normative.** `match.fork` and
   `classification.fork_portfolio` remain in the schema for compatibility
   with historical documents; the compiler already ignored them and now says
   so in the contract.
4. **`GDS_WORKSPACE_PLACEMENT_AMBIGUOUS` is retired.** Ambiguity was a
   symptom of unordered label membership; ordered first-match-wins removes
   it. A misconfigured include list now has one deterministic reading, and
   `GDS_DEVICE_SELECTOR_DUPLICATE` still rejects literally repeated rules —
   keyed on canonical match content for trait entries.

## Consequences

- Estates that want the old purpose-portfolio grouping lose nothing
  semantically: a portfolio is still a legal assignment output. The
  difference is that nothing *requires* one beyond a tenant name, and
  placement no longer reads anchors' `classification.portfolios` when a
  `match` is present.
- An estate that previously used `portfolio:servers` for both policy and
  placement splits the two concerns: placement moves to
  `include[].match.name_prefixes`, policy moves to
  `match.name_prefixes`. The anchor keeps only its tenant.
- `gds portfolio plan` keeps its `portfolio:`/`owner:` target grammar;
  tenant portfolios remain valid plan scopes.
- `schema_version: 1` is preserved: every change is additive or a
  relaxation, so existing v1 documents stay valid.
