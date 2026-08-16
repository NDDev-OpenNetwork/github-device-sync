package domain

import "strings"

// ClassifyFindings reduces a finding set to the strongest applicable exit
// class. The precedence is driven by StrongestClass; the empty finding set
// resolves to ExitSuccess.
func ClassifyFindings(findings []Finding) ExitClass {
	if len(findings) == 0 {
		return ExitSuccess
	}
	class := ExitNotProven
	for _, finding := range findings {
		if strings.Contains(finding.Code, "_PROVIDER_REQUIRED") ||
			strings.Contains(finding.Code, "_LIVE_PROVIDER_DISABLED") {
			class = StrongestClass(class, ExitUnsupported)
		}
		if strings.Contains(finding.Code, "NOT_PROVEN") {
			continue
		}
		if finding.Code == "GDS_CONTEXT_IDENTITY_CONFLICT" {
			class = StrongestClass(class, ExitConflict)
		}
		if finding.Code == "GDS_INSTANCE_INVALID" ||
			strings.Contains(finding.Code, "_INVALID") ||
			strings.HasPrefix(finding.Code, "GDS_YAML_") ||
			strings.HasPrefix(finding.Code, "GDS_JSON_") {
			class = StrongestClass(class, ExitValidation)
		}
		if strings.HasPrefix(finding.Code, "GDS_INPUT_") ||
			strings.HasPrefix(finding.Code, "GDS_SCHEMA_") ||
			strings.HasPrefix(finding.Code, "GDS_FIXTURE_") {
			class = StrongestClass(class, ExitInput)
		}
		if strings.HasPrefix(finding.Code, "GDS_POLICY_") ||
			strings.HasPrefix(finding.Code, "GDS_PROJECTION_") ||
			strings.HasPrefix(finding.Code, "GDS_COMPILED_POLICY_") ||
			strings.HasPrefix(finding.Code, "GDS_BUNDLE_LOCK_") ||
			strings.HasPrefix(finding.Code, "GDS_SKILL_") ||
			strings.HasPrefix(finding.Code, "GDS_PLUGIN_") ||
			strings.HasPrefix(finding.Code, "GDS_HOOK_") ||
			strings.HasPrefix(finding.Code, "GDS_HARNESS_") {
			class = StrongestClass(class, ExitValidation)
		}
		if strings.HasPrefix(finding.Code, "GDS_GITLINK_") ||
			strings.HasPrefix(finding.Code, "GDS_MODULE_") ||
			strings.HasPrefix(finding.Code, "GDS_FORK_") ||
			strings.HasPrefix(finding.Code, "GDS_REMOTE_") {
			class = StrongestClass(class, ExitValidation)
		}
		if strings.HasPrefix(finding.Code, "GDS_GITHUB_") {
			class = StrongestClass(class, ExitValidation)
		}
		if strings.HasPrefix(finding.Code, "GDS_MEMORY_") {
			class = StrongestClass(class, ExitValidation)
		}
		if strings.Contains(finding.Code, "IDENTITY_MISMATCH") {
			class = StrongestClass(class, ExitConflict)
		}
		if strings.Contains(finding.Code, "CREDENTIALS_PRESENT") ||
			strings.Contains(finding.Code, "_UNSAFE") ||
			strings.HasPrefix(finding.Code, "GDS_SECURITY_") ||
			strings.HasPrefix(finding.Code, "GDS_SECRET_") ||
			finding.Code == "GDS_PORTABLE_ABSOLUTE_PATH" {
			class = StrongestClass(class, ExitSecurity)
		}
	}
	return class
}

// StrongestClass returns the highest-priority exit class from the candidates.
// Higher priority means a more severe outcome that dominates softer classes.
func StrongestClass(classes ...ExitClass) ExitClass {
	priority := map[ExitClass]int{
		ExitSuccess: 0, ExitNotProven: 1, ExitValidation: 2,
		ExitInput: 3, ExitStale: 4, ExitApproval: 4,
		ExitAuthorization: 5, ExitConflict: 5, ExitPolicy: 5,
		ExitPartial: 5, ExitProviderTransient: 5,
		ExitUnsupported: 5, ExitSecurity: 6, ExitInternal: 7,
	}
	strongest := ExitSuccess
	for _, class := range classes {
		if priority[class] > priority[strongest] {
			strongest = class
		}
	}
	return strongest
}
