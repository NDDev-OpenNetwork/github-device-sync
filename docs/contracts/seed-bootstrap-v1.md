# GDS clean-device seed bootstrap v1 contract

Status: seed contract defined; the trusted external-release acquisition and the
Ubuntu consumer leg remain `NOT_PROVEN` (completion plan residual #8, stage
`C9`). The interim owner-operated reproducible-source seed is locally
rehearsable on macOS `arm64`.

This contract types the zero-to-one handoff described operationally in
`docs/runbooks/seed-clean-device.md`. It defines the boundary between a bare
supported device and the point where `skills/canonical/gds-bootstrap-device`
assumes authority. It creates no new release identity and grants no external
write authority.

## Boundaries and non-goals

The seed spans exactly two adjacent mutation boundaries and stops at a third:

1. **OS bootstrap boundary** — `modules/macos-ubuntu-bootstrap`. Installs dev
   tools and the CloakBrowser service; installs no `gds`.
2. **Seed boundary (this contract)** — acquire, verify, and trust the first
   `gds` artifact; initialize local state; pass authority on.
3. **Control-plane boundary** — `gds-bootstrap-device` skill and the release,
   estate, and harness command families. Out of scope here. The higher-level
   phased sequencing of this seed seam (OS bootstrap, seed install, then harness
   sync) is `scripts/bootstrap-device.sh`, documented in
   `docs/runbooks/bootstrap-device.md`.

The seed never collects, prints, stores, or uploads credentials. GitHub
authentication stays an explicit owner handoff.

## Typed inputs

| Input | Source | Constraint |
|---|---|---|
| device identity/profile | owner | canonical device ID, OS/arch, selected harnesses |
| OS bootstrap receipt | OS bootstrap boundary | dev-tool + CloakBrowser provisioning complete |
| bootstrap implementation | pinned `macos-ubuntu-bootstrap` release/commit | selected commit and `VERSION` verified before any apply; absolute `BOOTSTRAP_ROOT` |
| seed verifier | owner-operated reproducible build, or previously trusted transfer | independently authenticated digest; compatibility floor checked; never taken from the release it verifies |
| GDS artifact | trusted external release, or owner-operated reproducible source build | byte-identical reproducible build; six-file release dir when hosted |
| offline evidence dir | attached to the same release; split out of a staging download | exactly `provenance.sigstore.json`, `sbom.sigstore.json`, `trusted-root.jsonl`; auxiliary result JSON belongs to neither consumer input |
| trusted-root digest | approved out-of-band channel | never from the release location itself |
| local trust policy | independent consumer policy | pins the expected trusted root |
| install root + state path | owner | canonical, stable per device |

## Trust chain

0. The bootstrap implementation is acquired at its selected immutable commit and
   its identity is verified before it is executed. No repository-relative path
   is used before an absolute root is established.
1. OS bootstrap receipt proves the base runtime and browser service exist. The
   bootstrap boundary never installs `gds` or the seed verifier.
1a. The seed verifier is acquired under a trust mechanism independent of the
   target release, authenticated against an out-of-band digest, and retired
   after the installed release binary's identity is proven. Self-verification,
   mutable `latest`, and remote stream-to-shell are prohibited.
2. The GDS artifact is either a hosted immutable release (verified against its
   evidence directory and the out-of-band trusted-root digest) or an
   owner-operated reproducible build performed on a trusted machine with the
   pinned toolchain.
3. `gds release verify` must return `success` before any install. `NOT_PROVEN`
   is a hard stop.
4. Only after a successful verify does authority pass to the control-plane
   boundary. No step trusts an artifact whose trust policy was served from the
   same untrusted location as the artifact.

## Handoff boundary

The seed is complete when: the artifact is verified, local GDS state is
initialized, and the device identity/profile is available to the
`gds-bootstrap-device` skill. The skill then performs the plan-first install
with approval at each apply. The seed asserts no result the skill is responsible
for.

## Acceptance obligations

Before a seed target may be declared accepted (not merely rehearsed):

- disposable VM of the exact OS/arch, no preinstalled `gds`;
- interrupted-download, bad-digest, and unavailable-source negative paths;
- first install, repeated install idempotency, reboot/login continuity;
- explicit credential handoff with no silent token collection;
- durable install/upgrade/rollback receipts.

Ubuntu `24.04`/`26.04` acceptance remains `NOT_PROVEN` until produced on a real
VM. macOS lifecycle rehearsal is recorded; the external Linux rehearsal is the
open item (stage `C9`).
