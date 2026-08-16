package github

import (
	"testing"
	"time"
)

func farFuture() time.Time {
	return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
}

func TestPermissionContractExactRejectsExtraEffectivePermission(t *testing.T) {
	contract, err := NewPermissionContract(
		map[string]string{"metadata": "read"}, map[string]string{}, "all",
	)
	if err != nil {
		t.Fatal(err)
	}
	token := InstallationToken{
		Value: "ghs", ExpiresAt: farFuture(),
		Permissions:         map[string]string{"metadata": "read", "contents": "read"},
		RepositorySelection: "all",
	}
	if err := contract.Validate(token); err == nil {
		t.Fatal("exact contract accepted a stronger token")
	}
}

func TestPermissionContractSupersetAcceptsStrongerEffectivePermission(t *testing.T) {
	contract, err := NewPermissionContract(
		map[string]string{"metadata": "read", "contents": "read"}, map[string]string{}, "all",
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.Mode = PermissionModeSuperset
	token := InstallationToken{
		Value: "gho", ExpiresAt: farFuture(),
		// The PAT grants contents:write and the extra administration scope; both
		// are tolerated under the superset contract.
		Permissions: map[string]string{
			"metadata": "read", "contents": "write", "administration": "write",
		},
		RepositorySelection: "all",
	}
	if err := contract.Validate(token); err != nil {
		t.Fatalf("superset contract rejected a stronger token: %v", err)
	}
	if evidence := contract.Evidence(token); evidence.Status != "verified-superset" {
		t.Fatalf("evidence status=%q", evidence.Status)
	}
}

func TestPermissionContractSupersetRejectsMissingDeclaredPermission(t *testing.T) {
	contract, err := NewPermissionContract(
		map[string]string{"metadata": "read", "administration": "read"},
		map[string]string{}, "all",
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.Mode = PermissionModeSuperset
	token := InstallationToken{
		Value: "gho", ExpiresAt: farFuture(),
		Permissions:         map[string]string{"metadata": "read"},
		RepositorySelection: "all",
	}
	if err := contract.Validate(token); err == nil {
		t.Fatal("superset contract accepted a token missing a declared permission")
	}
}

func TestPermissionContractSupersetRejectsWeakerEffectivePermission(t *testing.T) {
	contract, err := NewPermissionContract(
		map[string]string{"contents": "write"}, map[string]string{}, "all",
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.Mode = PermissionModeSuperset
	token := InstallationToken{
		Value: "gho", ExpiresAt: farFuture(),
		Permissions:         map[string]string{"contents": "read"},
		RepositorySelection: "all",
	}
	if err := contract.Validate(token); err == nil {
		t.Fatal("superset contract accepted read where write was required")
	}
}

func TestPermissionContractSupersetAcceptsEitherRepositorySelection(t *testing.T) {
	contract, err := NewPermissionContract(
		map[string]string{"metadata": "read"}, map[string]string{}, "selected",
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.Mode = PermissionModeSuperset
	// A PAT enumerates all accessible repositories, so its selection is "all";
	// the superset contract must tolerate this against a "selected" declaration.
	token := InstallationToken{
		Value: "gho", ExpiresAt: farFuture(),
		Permissions:         map[string]string{"metadata": "read"},
		RepositorySelection: "all",
	}
	if err := contract.Validate(token); err != nil {
		t.Fatalf("superset contract rejected all against selected: %v", err)
	}
}

func TestPermissionContractRejectsUnknownMode(t *testing.T) {
	contract := PermissionContract{
		Permissions:         map[string]string{"metadata": "read"},
		RepositorySelection: "all", Mode: "lax",
	}
	if err := validatePermissionContract(contract); err == nil {
		t.Fatal("unknown permission mode was accepted")
	}
}

func TestPermissionSatisfiesRanking(t *testing.T) {
	if !permissionSatisfies("read", "read") || !permissionSatisfies("read", "write") {
		t.Fatal("read must be satisfied by read or write")
	}
	if permissionSatisfies("write", "read") {
		t.Fatal("write must not be satisfied by read")
	}
	if permissionSatisfies("read", "admin") || permissionSatisfies("admin", "read") {
		t.Fatal("invalid levels must not satisfy")
	}
}
