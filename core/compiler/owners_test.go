package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

// The compiler used to synthesise an owner id by lowercasing the repository's
// GitHub login. Of five declared owners only `example-user` has an id equal to its
// lowercased login, so the other four could never be named by a policy -- and
// the failure was silent, because a profile whose match cannot be satisfied is
// skipped rather than rejected.
//
// This derives its expectations from `estate/owners/`, so onboarding an owner
// does not edit the test, and a regression cannot be hidden by a fixture that
// happens to agree with the bug.

func estateOwners(t *testing.T) map[string]string {
	t.Helper()
	directory := filepath.Join(testRepositoryRoot(t), "estate", "owners")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		value, err := serialization.DecodeFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		document, _ := value.(map[string]any)
		owner, _ := document["owner"].(map[string]any)
		id, _ := owner["id"].(string)
		login, _ := owner["provider_login"].(string)
		if id != "" && login != "" {
			declared[login] = id
		}
	}
	if len(declared) == 0 {
		t.Fatal("the estate declares no owners")
	}
	return declared
}

func TestEveryDeclaredOwnerIsMatchableByItsDeclaredID(t *testing.T) {
	t.Parallel()
	root := testRepositoryRoot(t)
	owners, findings := NewLoader(testSchemas(t)).LoadOwners(root)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	declared := estateOwners(t)
	if len(owners) != len(declared) {
		t.Fatalf("register has %d owners, the estate declares %d", len(owners), len(declared))
	}
	for login, id := range declared {
		anchor := domain.RepositoryAnchor{Provider: domain.GitHubLocator{Owner: login}}
		if reason := matchFailure(PolicyMatch{Owner: id}, anchor, owners); reason != "" {
			t.Fatalf("owner %q (login %q) is not matchable by its declared id: %s", id, login, reason)
		}
	}
}

// The login form is what the one owner policy in the estate had to be written
// with while the bug stood. It must stop working, or the correction would be
// cosmetic.
func TestTheLoginFormNoLongerMatches(t *testing.T) {
	t.Parallel()
	owners, findings := NewLoader(testSchemas(t)).LoadOwners(testRepositoryRoot(t))
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	checked := 0
	for login, id := range estateOwners(t) {
		derived := "owner:" + strings.ToLower(login)
		if derived == id {
			continue // example-user: the one owner the bug happened to get right
		}
		checked++
		anchor := domain.RepositoryAnchor{Provider: domain.GitHubLocator{Owner: login}}
		if reason := matchFailure(PolicyMatch{Owner: derived}, anchor, owners); reason == "" {
			t.Fatalf("the synthesised id %q still matches owner %q", derived, id)
		}
	}
	if checked == 0 {
		t.Fatal("no owner distinguishes the declared id from the synthesised one")
	}
}

// Without a register an owner match is unresolvable, and saying so is the point:
// the previous behaviour was to invent an answer and skip the profile quietly.
func TestAnUnresolvableOwnerIsReportedRatherThanDerived(t *testing.T) {
	t.Parallel()
	anchor := domain.RepositoryAnchor{Provider: domain.GitHubLocator{Owner: "NDDev-OpenNetwork"}}
	reason := matchFailure(PolicyMatch{Owner: "owner:opennetwork"}, anchor, nil)
	if !strings.Contains(reason, "cannot be resolved") {
		t.Fatalf("reason = %q", reason)
	}
}

// Two owner ids claiming one GitHub login means the estate declares an account
// twice, and picking either would decide silently which policies apply.
func TestACollidingProviderLoginIsRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "estate", "owners")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"owner:first", "owner:second"} {
		document := "schema_version: 1\nowner:\n  id: \"" + id +
			"\"\n  installation: \"installation:example\"\n  provider_login: \"Example-Org\"\n"
		name := filepath.Join(directory, string(rune('a'+index))+".yaml")
		if err := os.WriteFile(name, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, findings := NewLoader(testSchemas(t)).LoadOwners(root)
	if len(findings) != 1 || findings[0].Code != "GDS_POLICY_OWNER_REGISTER_AMBIGUOUS" {
		t.Fatalf("findings = %#v", findings)
	}
}
