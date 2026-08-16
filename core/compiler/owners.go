package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

// OwnerIdentities maps a lowercased provider login to the owner id the estate
// declares for it.
//
// It exists because a repository anchor carries a GitHub login and a policy
// matches an estate owner id, and those are different vocabularies. The compiler
// used to bridge them by lowercasing the login and hoping the result was the id.
// It usually was not: of five declared owners only `example-user` has an id equal
// to its lowercased login, so `owner:nddev`, `owner:guild`, `owner:example-media`
// and `owner:opennetwork` could never be named by a policy. The one owner policy
// in the estate compiled only because its author had written the login form.
//
// The mapping lives where the estate already declares both halves, so this reads
// a register rather than inventing a second one.
type OwnerIdentities map[string]string

// LoadOwners reads the estate's owner register.
//
// Two logins colliding is rejected rather than resolved. GitHub logins are
// unique, so a collision means the estate declares one account twice under
// different ids, and picking either would decide silently which policies apply.
func (loader *Loader) LoadOwners(root string) (OwnerIdentities, []domain.Finding) {
	directory := filepath.Join(root, "estate", "owners")
	identities := OwnerIdentities{}
	findings := []domain.Finding{}
	declaredBy := map[string]string{}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return identities, append(findings, domain.Finding{
			Code: "GDS_POLICY_OWNER_REGISTER_UNAVAILABLE", Severity: domain.SeverityHigh,
			Message:  "The estate owner register could not be read, so owner-matched policies cannot be resolved.",
			Evidence: map[string]any{"path": directory},
		})
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(directory, name)
		value, decodeErr := serialization.DecodeFile(path)
		if decodeErr != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_OWNER_REGISTER_UNAVAILABLE", Severity: domain.SeverityHigh,
				Message:  "An estate owner document could not be decoded.",
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		document, ok := value.(map[string]any)
		if !ok {
			continue
		}
		owner, ok := document["owner"].(map[string]any)
		if !ok {
			continue
		}
		id, _ := owner["id"].(string)
		login, _ := owner["provider_login"].(string)
		if id == "" || login == "" {
			continue
		}
		key := strings.ToLower(login)
		if previous, duplicate := declaredBy[key]; duplicate && previous != id {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_OWNER_REGISTER_AMBIGUOUS", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Provider login %q is declared by more than one owner id.", login,
				),
				Evidence: map[string]any{"provider_login": login, "owners": []string{previous, id}},
			})
			continue
		}
		declaredBy[key] = id
		identities[key] = id
	}
	return identities, findings
}

// WithOwners returns a compiler that resolves owner matches through the given
// register.
//
// A copy rather than a mutation: one Compiler is shared across callers and, in
// the assurance scenario, across workers. Storing the register in place would
// make which policies apply depend on who compiled last.
func (compiler *Compiler) WithOwners(owners OwnerIdentities) *Compiler {
	if compiler == nil {
		return nil
	}
	copied := *compiler
	copied.owners = owners
	return &copied
}
