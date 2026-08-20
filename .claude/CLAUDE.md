<!--
GENERATED FILE - DO NOT EDIT DIRECTLY
generator: gds
bundle: 0.4.0-dev
source-tree-digest: sha256:0b8086d56389359ffc8543df968597763e863ea8052848d291244930b25a9d05
input-digest: sha256:980b92014c0a7482cc4e1fac95c8fec0669d05ce38a7ca20682c30b32bf42e9d
output-digest: sha256:88cb57297d8d713287872a8afaca8d42f7146ecf7a091e4996e65eee8f962665
edit-source:
  - .gds/repository.yaml
  - policies/base/repository-default.yaml
  - policies/repositories/github-device-sync.yaml
  - policies/roles/control-plane.yaml
  - templates/agents/repository.md.tmpl
  - templates/github-actions/go.yml.tmpl
  - templates/harnesses/claude.md.tmpl
-->
@AGENTS.md

# Claude Code delta

`AGENTS.md` above is the repository brief and is imported, not restated. This
file carries only what differs for Claude Code.

- Skills load on task match, not on arrival. A task contained in this repository
  goes straight to the relevant source and its verification command; `gds-orient`
  is for estate, device, topology and cross-repository scope.
- Serena memories under `.serena/memories/` are derived evidence. Their status
  records that sources were current and the body is the reviewed one; it does not
  certify the prose is correct.
- Prefer the repository's own commands over ad-hoc equivalents, so a failure is
  reproducible from the brief alone.
