package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

var RuntimeCaseIDs = []string{
	"binary-version-detection",
	"clean-install",
	"destructive-implicit-negative",
	"exact-skill-discovery",
	"generated-projection-drift",
	"hook-lifecycle",
	"nested-instruction-discovery",
	"public-private-context-firewall",
	"read-only-explicit-invocation",
	"remove",
	"root-instruction-discovery",
	"update-and-rollback",
}

type RuntimeContractDocument struct {
	SchemaVersion   int                   `json:"schema_version"`
	ContractVersion string                `json:"contract_version"`
	Harnesses       []string              `json:"harnesses"`
	Cases           []RuntimeContractCase `json:"cases"`
}

type RuntimeContractCase struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Required bool   `json:"required"`
	Evidence string `json:"evidence"`
}

type RuntimeContractReport struct {
	Path            string   `json:"path"`
	ContractVersion string   `json:"contract_version"`
	Harnesses       int      `json:"harnesses"`
	Cases           int      `json:"cases"`
	RequiredCaseIDs []string `json:"required_case_ids"`
}

func ValidateRuntimeContract(
	root string,
	schemas *validation.Set,
) (RuntimeContractReport, []domain.Finding) {
	relativePath := filepath.ToSlash(filepath.Join("tests", "harness", "runtime-contract.yaml"))
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	report := RuntimeContractReport{Path: relativePath, RequiredCaseIDs: []string{}}
	findings := schemas.ValidateFile("harness-runtime-contract", path)
	if len(findings) != 0 {
		return report, findings
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return report, []domain.Finding{harnessFinding(
			"GDS_HARNESS_RUNTIME_CONTRACT_READ_FAILED",
			"Cannot read the harness runtime contract plan.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	var document RuntimeContractDocument
	if err := serialization.DecodeInto(path, raw, &document); err != nil {
		return report, []domain.Finding{harnessFinding(
			"GDS_HARNESS_RUNTIME_CONTRACT_DECODE_FAILED",
			"Cannot decode the harness runtime contract plan.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	report.ContractVersion = document.ContractVersion
	report.Harnesses = len(document.Harnesses)
	report.Cases = len(document.Cases)
	actualHarnesses := append([]string(nil), document.Harnesses...)
	sort.Strings(actualHarnesses)
	if fmt.Sprint(actualHarnesses) != fmt.Sprint(CanonicalIDs) {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_RUNTIME_CONTRACT_SET_INVALID",
			"Runtime contract does not cover the exact canonical harness set.",
			map[string]any{"expected": CanonicalIDs, "observed": actualHarnesses},
		))
	}
	seen := map[string]struct{}{}
	for _, runtimeCase := range document.Cases {
		if _, duplicate := seen[runtimeCase.ID]; duplicate {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_RUNTIME_CONTRACT_CASE_DUPLICATE",
				"Runtime contract case occurs more than once.",
				map[string]any{"case": runtimeCase.ID},
			))
		}
		seen[runtimeCase.ID] = struct{}{}
		report.RequiredCaseIDs = append(report.RequiredCaseIDs, runtimeCase.ID)
	}
	sort.Strings(report.RequiredCaseIDs)
	if fmt.Sprint(report.RequiredCaseIDs) != fmt.Sprint(RuntimeCaseIDs) {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_RUNTIME_CONTRACT_CASE_SET_INVALID",
			"Runtime contract does not contain the complete required case set.",
			map[string]any{"expected": RuntimeCaseIDs, "observed": report.RequiredCaseIDs},
		))
	}
	sortFindings(findings)
	return report, findings
}
