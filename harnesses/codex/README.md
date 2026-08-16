# Codex capability profile

This profile records only behavior verified from current official documentation.
It remains `provisional` until a clean runtime fixture proves instruction-chain,
skill discovery, explicit-only invocation, plugin hooks, and public/private
context behavior for an exact Codex version.

The profile is desired compatibility evidence. It does not install plugins,
trust hooks, or modify `$CODEX_HOME`.

`gds-codex-runtime-driver` is the released native evaluator. It creates
isolated Codex homes and Git fixtures, binds every probe to the requested model,
checkpoints exact corpus attempts, compares no-skill and with-skill outputs
through a separate schema-bound judge run, tests a nested public/private Git
boundary, and installs `gds-core` from the exact local marketplace for hook
evidence. Hook execution uses Codex's documented automation-only trust bypass
only after the driver verifies the installed hook files against canonical
digests; it never changes the owner's persistent Codex trust store.
