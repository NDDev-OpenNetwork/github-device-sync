# GDS performance evidence

Performance modes are explicit and never inferred from `CI=true`.

- `deterministic-required` blocks on stable resource and operation budgets.
- `relative-required` additionally requires an immutable report, baseline
  binding, exact runner digest, and declared maximum regression.
- `absolute-calibrated` additionally requires a policy derived from at least ten
  clean comparable reports and bound to variance evidence.
- `informational` reports every result without becoming a release gate.

Create a relative baseline from one clean deterministic-pass report:

```text
gds-performance-evidence baseline --report report.json \
  --runner-digest sha256:<runner-contract> --id linux-amd64-v1 \
  --output baseline.json
```

Create an absolute policy only after at least ten runs on the same immutable
runner image and scenario:

```text
gds-performance-evidence calibrate \
  --report run-01.json ... --report run-10.json \
  --runner-digest sha256:<runner-contract> --id linux-amd64-v1 \
  --output calibrated-policy.json
```

The generated threshold is an engineering gate, not an owner latency SLO. For
`at-most` metrics it is the observed maximum plus the larger of ten percent or
three coefficients of variation; `at-least` metrics use the symmetric observed
minimum bound. Candidate evaluation rejects a changed runner, environment,
scenario, report digest, policy digest, insufficient sample count, or metric
shape before comparing values.
