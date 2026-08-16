package github

import (
	"fmt"
	"sort"
	"strings"
)

const maxInstallationPermissions = 64

const (
	// PermissionModeExact requires the token's effective permission map and
	// repository selection to match the declared contract exactly. This is the
	// fail-closed GitHub App installation-token contract (ADR 0021): an
	// over-privileged App cannot silently operate.
	PermissionModeExact = "exact"
	// PermissionModeSuperset requires the token to grant at least the declared
	// permission levels (read <= write) over the declared selection. This is the
	// gh CLI personal-access-token contract: the PAT's scopes are a coarse
	// superset of the fine-grained installation permission map, and the exact
	// effective scopes are inspected live from the X-OAuth-Scopes response
	// header (ADR 0034). Missing declared permissions still fail closed.
	PermissionModeSuperset = "superset"
)

func NewPermissionContract(
	repository map[string]string,
	organization map[string]string,
	repositorySelection string,
) (PermissionContract, error) {
	permissions := make(map[string]string, len(repository)+len(organization))
	for _, source := range []map[string]string{repository, organization} {
		for name, level := range source {
			if _, duplicate := permissions[name]; duplicate {
				return PermissionContract{}, fmt.Errorf(
					"GitHub permission %q is declared in more than one scope", name,
				)
			}
			permissions[name] = level
		}
	}
	contract := PermissionContract{
		Permissions: permissions, RepositorySelection: repositorySelection,
		Mode: PermissionModeExact,
	}
	if err := validatePermissionContract(contract); err != nil {
		return PermissionContract{}, err
	}
	return contract, nil
}

func (contract PermissionContract) Validate(token InstallationToken) error {
	if err := validatePermissionContract(contract); err != nil {
		return err
	}
	if contract.Mode == PermissionModeSuperset {
		return contract.validateSuperset(token)
	}
	return contract.validateExact(token)
}

func (contract PermissionContract) validateExact(token InstallationToken) error {
	if token.RepositorySelection != contract.RepositorySelection {
		return &APIError{Kind: ErrorPermissionContract}
	}
	if len(token.Permissions) != len(contract.Permissions) {
		return &APIError{Kind: ErrorPermissionContract}
	}
	for name, expected := range contract.Permissions {
		if token.Permissions[name] != expected {
			return &APIError{Kind: ErrorPermissionContract}
		}
	}
	return nil
}

// validateSuperset requires every declared permission to be satisfied by an
// effective permission of equal or stronger level (read <= write). Unlike the
// exact contract, additional effective permissions are tolerated: a PAT's OAuth
// scopes cannot be narrowed to one fine-grained installation map.
func (contract PermissionContract) validateSuperset(token InstallationToken) error {
	if token.RepositorySelection != "all" && token.RepositorySelection != "selected" {
		return &APIError{Kind: ErrorPermissionContract}
	}
	if err := validateEffectivePermissions(token.Permissions, token.RepositorySelection); err != nil {
		return &APIError{Kind: ErrorPermissionContract}
	}
	for name, expected := range contract.Permissions {
		effective, granted := token.Permissions[name]
		if !granted || !permissionSatisfies(expected, effective) {
			return &APIError{Kind: ErrorPermissionContract}
		}
	}
	return nil
}

// permissionSatisfies reports whether an effective level is at least as strong
// as the required level. read is satisfied by read or write; write requires
// write.
func permissionSatisfies(required, effective string) bool {
	if required != "read" && required != "write" {
		return false
	}
	if effective != "read" && effective != "write" {
		return false
	}
	if required == "read" {
		return effective == "read" || effective == "write"
	}
	return effective == "write"
}

func (contract PermissionContract) Evidence(token InstallationToken) PermissionEvidence {
	return PermissionEvidence{
		Expected:            clonePermissions(contract.Permissions),
		Effective:           clonePermissions(token.Permissions),
		RepositorySelection: token.RepositorySelection,
		Status:              permissionEvidenceStatus(contract),
	}
}

func permissionEvidenceStatus(contract PermissionContract) string {
	if contract.Mode == PermissionModeSuperset {
		return "verified-superset"
	}
	return "verified-exact"
}

func validatePermissionContract(contract PermissionContract) error {
	if contract.RepositorySelection != "all" && contract.RepositorySelection != "selected" {
		return fmt.Errorf("GitHub repository selection must be all or selected")
	}
	mode := contract.Mode
	if mode == "" {
		mode = PermissionModeExact
	}
	if mode != PermissionModeExact && mode != PermissionModeSuperset {
		return fmt.Errorf("GitHub permission contract mode must be exact or superset")
	}
	if len(contract.Permissions) == 0 || len(contract.Permissions) > maxInstallationPermissions {
		return fmt.Errorf("GitHub permission contract must contain between 1 and %d entries", maxInstallationPermissions)
	}
	for name, level := range contract.Permissions {
		if !validPermissionName(name) || !validPermissionLevel(level) {
			return fmt.Errorf("GitHub permission contract contains an invalid entry")
		}
	}
	return nil
}

func validateEffectivePermissions(permissions map[string]string, selection string) error {
	if selection != "all" && selection != "selected" {
		return fmt.Errorf("GitHub token repository selection is invalid")
	}
	if len(permissions) == 0 || len(permissions) > maxInstallationPermissions {
		return fmt.Errorf("GitHub token permission set is outside the safe bound")
	}
	for name, level := range permissions {
		if !validPermissionName(name) || !validPermissionLevel(level) {
			return fmt.Errorf("GitHub token permission set contains an invalid entry")
		}
	}
	return nil
}

func validPermissionName(value string) bool {
	if len(value) < 2 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return !strings.ContainsAny(value, "\x00\r\n")
}

func validPermissionLevel(value string) bool { return value == "read" || value == "write" }

func clonePermissions(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = source[key]
	}
	return result
}

func cloneToken(token InstallationToken) InstallationToken {
	token.Permissions = clonePermissions(token.Permissions)
	return token
}
