package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

const (
	runtimeDependenciesRelPath = "estate/runtime-dependencies.yaml"
	bootstrapContractRelPath   = "modules/macos-ubuntu-bootstrap/config/rldyour-contract.json"
	bootstrapConsumerID        = "macos-ubuntu-bootstrap"
)

type runtimeDependencies struct {
	SchemaVersion int                 `json:"schema_version"`
	Dependencies  []runtimeDependency `json:"dependencies"`
}

type runtimeDependency struct {
	ID             string `json:"id"`
	RepositorySlug string `json:"repository_slug"`
	URL            string `json:"url"`
	Consumption    string `json:"consumption"`
	Consumer       string `json:"consumer"`
	Harness        string `json:"harness,omitempty"`
	PinnedSHA      string `json:"pinned_sha"`
	AvailableHead  string `json:"available_head"`
	Version        string `json:"version,omitempty"`
	Backend        string `json:"backend,omitempty"`
	Reconciliation string `json:"reconciliation"`
}

// validateRuntimeDependencies validates the estate runtime-dependency registry
// after the schema gate and cross-checks each bootstrap-consumed runtime-clone
// pin against the in-tree bootstrap contract. When the bootstrap submodule
// worktree is not initialized (common on CI checkouts), the cross-check is
// skipped without a finding, mirroring the gitlink-tolerance idiom in
// core/providers/git. It returns the declared dependency count and any findings.
func (set *Set) validateRuntimeDependencies(worktreeRoot string) (int, []domain.Finding) {
	path := filepath.Join(worktreeRoot, filepath.FromSlash(runtimeDependenciesRelPath))
	findings := set.ValidateFile("runtime-dependencies", path)
	if len(findings) != 0 {
		return 0, findings
	}
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return 0, []domain.Finding{runtimeDependencyDecodeFinding(path, err)}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, []domain.Finding{runtimeDependencyDecodeFinding(path, err)}
	}
	var registry runtimeDependencies
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return 0, []domain.Finding{runtimeDependencyDecodeFinding(path, err)}
	}

	consumedCommits := loadBootstrapHarnessCommits(worktreeRoot)
	seen := map[string]struct{}{}
	for _, dependency := range registry.Dependencies {
		if _, duplicate := seen[dependency.ID]; duplicate {
			findings = append(findings, domain.Finding{
				Code: "GDS_RUNTIME_DEPENDENCY_DUPLICATE", Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Duplicate runtime dependency identity %q.", dependency.ID),
				Evidence: map[string]any{"path": path, "id": dependency.ID},
			})
		}
		seen[dependency.ID] = struct{}{}

		if dependency.Reconciliation == "current" && dependency.PinnedSHA != dependency.AvailableHead {
			findings = append(findings, domain.Finding{
				Code: "GDS_RUNTIME_DEPENDENCY_RECONCILIATION_INCONSISTENT", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Runtime dependency %q is marked current but pinned_sha differs from available_head.",
					dependency.ID,
				),
				Evidence: map[string]any{"path": path, "id": dependency.ID},
			})
		}

		if dependency.Consumption == "runtime-clone" && dependency.Harness != "" &&
			dependency.Consumer == bootstrapConsumerID && consumedCommits != nil {
			if consumed, present := consumedCommits[dependency.Harness]; present &&
				consumed != dependency.PinnedSHA {
				findings = append(findings, domain.Finding{
					Code: "GDS_RUNTIME_DEPENDENCY_PIN_DRIFT", Severity: domain.SeverityHigh,
					Message: fmt.Sprintf(
						"Runtime dependency %q pinned_sha does not match the %q commit consumed by %s.",
						dependency.ID, dependency.Harness, bootstrapConsumerID,
					),
					Evidence: map[string]any{
						"path": path, "id": dependency.ID,
						"registry_pinned_sha":    dependency.PinnedSHA,
						"consumed_module_commit": consumed,
					},
				})
			}
		}
	}
	return len(registry.Dependencies), findings
}

func runtimeDependencyDecodeFinding(path string, err error) domain.Finding {
	return domain.Finding{
		Code:     "GDS_RUNTIME_DEPENDENCIES_TYPED_DECODE_FAILED",
		Severity: domain.SeverityHigh,
		Message:  fmt.Sprintf("Cannot decode runtime dependencies %s: %v", path, err),
		Evidence: map[string]any{"path": path},
	}
}

// loadBootstrapHarnessCommits reads the in-tree bootstrap contract and returns a
// map of harness name to consumed module commit. It returns nil (skip the
// cross-check) when the submodule worktree is absent or the contract cannot be
// parsed. Non-object harness entries such as "active"/"policy" are ignored.
func loadBootstrapHarnessCommits(worktreeRoot string) map[string]string {
	path := filepath.Join(worktreeRoot, filepath.FromSlash(bootstrapContractRelPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var contract struct {
		Harnesses map[string]json.RawMessage `json:"harnesses"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		return nil
	}
	commits := map[string]string{}
	for name, rawEntry := range contract.Harnesses {
		var entry struct {
			ModuleCommit string `json:"module_commit"`
		}
		if json.Unmarshal(rawEntry, &entry) == nil && entry.ModuleCommit != "" {
			commits[name] = entry.ModuleCommit
		}
	}
	return commits
}
