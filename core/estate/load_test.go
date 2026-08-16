package estate

import "testing"

func TestValidateInstallationPermissionsRejectsDuplicateAndEmptyContracts(t *testing.T) {
	findings := validateInstallationPermissions([]Installation{
		{
			Installation: InstallationIdentity{ID: "installation:duplicate"},
			Permissions: InstallationPermissions{
				RepositorySelection: "all",
				Repository:          map[string]string{"metadata": "read"},
				Organization:        map[string]string{"metadata": "read"},
			},
		},
		{
			Installation: InstallationIdentity{ID: "installation:empty"},
			Permissions: InstallationPermissions{
				RepositorySelection: "selected",
				Repository:          map[string]string{}, Organization: map[string]string{},
			},
		},
	})
	for _, code := range []string{
		"GDS_ESTATE_PERMISSION_SCOPE_CONFLICT", "GDS_ESTATE_PERMISSION_SET_EMPTY",
	} {
		if !estateHasFinding(findings, code) {
			t.Fatalf("missing %s in %+v", code, findings)
		}
	}
}
