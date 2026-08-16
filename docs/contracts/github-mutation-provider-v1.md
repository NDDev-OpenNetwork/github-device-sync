# GDS GitHub mutation provider v1 contract

Status: local fixture contract; live App installation and writes are
`NOT_PROVEN`.

## Capability separation

The read controller loads only `github-runtime`. A mutating command must load a
second private `github-mutation-runtime`, compare it with canonical
`estate/mutations/*.yaml`, and prove that App IDs, provider installation IDs,
and secret locators are distinct from the Inventory App.

The Mutation App contract is exact:

```text
repository selection: selected
administration: write
contents: write
custom_properties: write
metadata: read
pull_requests: write
workflows: write
organization permissions: none
```

Missing, extra, stronger, differently scoped, or `all`-repository tokens are
rejected before a provider write.

## Repository binding

A mutation factory must be bound to one immutable GitHub repository ID and one
verified owner/name locator before it exposes any write method. The bound
operation subset must be contained by the canonical capability. Methods do not
accept an arbitrary target repository.

The read App proves owner/name-to-ID identity and expected old state. Mutation
methods issue exactly one request and never retry. A typed handler verifies the
result again through the read App before its operation step succeeds.

## Governance transaction contract

`gds github governance --plan` compiles the exact local repository policy,
compares one current read-App snapshot, and stores only drift for managed
fields. Merge settings, Actions policy, selected Actions, workflow-token
permissions, and immutable-release enablement are independent operation steps.
Each step contains:

- immutable GDS and provider repository identities;
- exact read installation and mutation capability identities;
- expected and desired typed field values;
- a stable full-snapshot digest before the write;
- an exact expected full-snapshot digest after the write when observable.

The handler performs a fresh full read immediately before its one write. Any
unrelated drift blocks the step. After the write, it reads through the
Inventory App again and journals typed redacted before/after evidence. A later
`--verify` validates the recorded evidence digest and current desired field
without requiring an earlier step's whole snapshot to equal the final state of
subsequent steps.

The selected-actions endpoint is valid only while `allowed_actions=selected`.
A transition to that mode ends at a discovery barrier and sets
`requires_replan: true`; selected-actions are changed only by the next plan,
after GitHub has made their current value observable.

## Generated projection change-set contract

`gds github projection-pr --plan` renders the current candidate only from
committed canonical inputs, proves the local anchor against one current
provider repository, and constructs a branch/content/draft-PR saga. The plan
contains:

- the exact provider repository ID, read installation, mutation capability,
  owner/name, default branch, and base OID;
- one deterministic GDS branch derived from projection output and base OID;
- every base-relative changed generated path, its `added` or `modified`
  status, desired SHA-256 digest, and current expected blob state;
- one stable draft-PR title/body and the complete final file contract;
- local manifest, policy, candidate worktree, and remote-state digests.

Files already equal on base are excluded. Desired files already present on an
existing GDS branch remain in the plan as idempotent content steps and preserve
their status relative to base. Before any write, an existing branch must be
strictly ahead, not behind, have the same merge base, and contain only a subset
of the final allowed paths with matching statuses. A different open PR or an
unexpected path blocks the operation before mutation.

Apply creates or asserts the branch, performs one expected-state content write
per file, then proves the final branch comparison has exactly the allowed
paths and content digests before creating or accepting one exact draft PR.
Verify re-reads branch, file, comparison, and PR evidence through the read App.
The ordinary provider exposes no branch-delete cleanup because GitHub lacks an
expected-old-OID delete primitive.

## Repository transfer boundary

Repository transfer is not an installation-token mutation. GitHub documents
only a GitHub App user access token for this endpoint. The endpoint returns
`202 Accepted`, reports the original owner in its response, and completes
asynchronously; a personal transfer can also require acceptance by the target
owner.

GDS therefore keeps transfer identity and candidate validation in the
lifecycle model, but every transfer plan is `ready_for_apply: false`. The
installation-token provider exposes no transfer method, and the generic
lifecycle handler rejects transfer before a write. Enabling it requires a
separate user-token credential contract, explicit actor authorization,
acceptance/polling states, timeout and recovery behavior, and runtime fixtures.

## Supported local provider primitives

- non-force branch create and fast-forward update;
- bounded file create/update with required old blob SHA for replacements;
- draft pull-request creation; ordinary mutation does not merge or auto-merge;
- repository rename, archive, and merge settings;
- Actions, selected-actions, and workflow-token settings;
- repository-level immutable-release enable/disable;
- repository custom-property values;
- default-branch rulesets with no bypass actors and a closed rule set;
- explicit repository deletion under its separate approval class.

Custom-property names follow GitHub's documented 1–75 byte
`A-Z a-z 0-9 _ - $ #` grammar. Text and multi-select items are at most 75
bytes and contain printable ASCII except double quotes; multi-select values are
bounded to 200 unique non-empty items. `null` remains the explicit unset value.
Provider and reconciliation paths share the same validator.

Visibility, permission changes, force updates, auto-merge, and ruleset bypass
are absent from the ordinary provider API. Delete and visibility remain
separate-approval gates even though their underlying GitHub permission is
Administration(write). Repository transfer is also absent because its token
and asynchronous completion contracts differ from installation mutations.
Branch deletion is not exposed: GitHub's ref-delete endpoint has no expected
old OID parameter, so a pre-read cannot provide compare-and-swap safety across
the request race window.

Immutable releases use the dedicated repository endpoints rather than the
generic repository PATCH: `PUT` enables, `DELETE` disables, and both must
return `204 No Content`. The provider never attempts to alter organization
enforcement. A policy that requests disable while `enforced_by_owner=true`
fails during remediation construction before any mutation runtime is loaded.

Ruleset observation has an additional privilege boundary. GitHub omits
`bypass_actors` unless the caller has write access to the ruleset. A normal
Inventory App observation can bind visible conditions and rules, but apply and
verify require a repository-bound privileged read through the Mutation App.
Hidden or non-empty bypass actors block the ordinary handler, and every
accepted create/update payload contains an explicit empty `bypass_actors`
array. Removing an intentional bypass requires a separate escalation path.

## Transport and evidence

- API version and media type match the read provider contract.
- Redirects are forbidden; target URLs remain on the configured API origin.
- Mutation requests are serialized per capability with at least one second
  between writes.
- Response bodies are bounded and never copied into errors.
- Evidence contains repository ID, HTTP status, request ID, and rate metadata;
  it never contains tokens, private keys, request authorization, or raw
  provider error bodies.
- `5xx`, conflicts, validation errors, and rate limits return stable classes;
  callers may re-plan but the provider never retries blindly.

## Runtime status

Local contract tests use loopback fixtures and distinct generated App keys.
They prove exact permission rejection, scope binding, mutation spacing,
force-disabled ref updates, no blind retry, bounded typed responses, separate
read/write identities, multi-file change-set verification, unexpected-path
rejection, custom-property reconciliation, and hidden ruleset-bypass
rejection. No live credential or GitHub write has been used.

The canonical estate remains fail-closed by default. Its rollout mode is
`pull-request`, but only managed NDDev source assignments are eligible. Every
apply still requires a matching mutation capability, exact signed approval,
one-shot enablement, fresh full-state evidence, a separate private mutation
runtime, and the device mutation kill switch. Observe-only assignments block
before mutation credentials are loaded.
