// Package gitcontracts validates repository-owned Git topology facts.
package gitcontracts

import (
	"errors"
	"fmt"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

// ExactRemoteURL returns the single credential-free URL shared by the fetch
// and push directions of a remote. Lifecycle plans use this stricter contract
// because a rename or transfer must update one unambiguous local locator.
func ExactRemoteURL(topology gitprovider.Topology, remoteName string) (string, error) {
	remote, found := FindRemote(topology, remoteName)
	if !found {
		return "", fmt.Errorf("required Git remote %q is missing", remoteName)
	}
	if len(remote.FetchURLs) != 1 || len(remote.PushURLs) != 1 {
		return "", errors.New("repository lifecycle requires exactly one fetch and one push URL")
	}
	fetch, push := remote.FetchURLs[0], remote.PushURLs[0]
	if fetch.CredentialsRedacted || push.CredentialsRedacted {
		return "", errors.New("repository lifecycle cannot bind a redacted remote URL")
	}
	if fetch.Value == "" || fetch.Value != push.Value {
		return "", errors.New("repository lifecycle requires identical fetch and push URLs")
	}
	return fetch.Value, nil
}

type ExpectedRepository struct {
	Owner string
	Name  string
}

func FindRemote(topology gitprovider.Topology, name string) (gitprovider.Remote, bool) {
	for _, remote := range topology.Remotes {
		if remote.Name == name {
			return remote, true
		}
	}
	return gitprovider.Remote{}, false
}

func ValidateRemote(
	topology gitprovider.Topology,
	remoteName string,
	expected ExpectedRepository,
) []domain.Finding {
	remote, found := FindRemote(topology, remoteName)
	if !found {
		return []domain.Finding{{
			Code: "GDS_REMOTE_MISSING", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Required Git remote %q is missing.", remoteName),
			Evidence: map[string]any{"remote": remoteName, "owner": expected.Owner, "name": expected.Name},
		}}
	}
	if len(remote.FetchURLs) == 0 {
		return []domain.Finding{{
			Code: "GDS_REMOTE_URL_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Git remote %q has no inspectable fetch URL.", remoteName),
			Evidence: map[string]any{"remote": remoteName},
		}}
	}
	findings := validateRemoteURLs(remoteName, "fetch", remote.FetchURLs, expected)
	findings = append(
		findings,
		validateRemoteURLs(remoteName, "push", remote.PushURLs, expected)...,
	)
	return findings
}

func validateRemoteURLs(
	remoteName string,
	direction string,
	remoteURLs []gitprovider.RemoteURL,
	expected ExpectedRepository,
) []domain.Finding {
	findings := []domain.Finding{}
	for index, remoteURL := range remoteURLs {
		if remoteURL.CredentialsRedacted {
			findings = append(findings, domain.Finding{
				Code: "GDS_REMOTE_CREDENTIALS_PRESENT", Severity: domain.SeverityCritical,
				Message: fmt.Sprintf(
					"Git remote %q stores credentials or query material in its %s URL.",
					remoteName, direction,
				),
				Evidence: map[string]any{
					"remote": remoteName, "direction": direction, "url_index": index,
				},
			})
		}
		observed, err := gitprovider.ParseGitHubRepository(remoteURL.Value)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code: "GDS_REMOTE_URL_INVALID", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Git remote %q %s URL cannot be resolved to a GitHub repository.",
					remoteName, direction,
				),
				Evidence: map[string]any{
					"remote": remoteName, "direction": direction,
					"url_index": index, "error": err.Error(),
				},
			})
			continue
		}
		if !strings.EqualFold(observed.Owner, expected.Owner) ||
			!strings.EqualFold(observed.Name, expected.Name) {
			findings = append(findings, domain.Finding{
				Code: "GDS_REMOTE_IDENTITY_MISMATCH", Severity: domain.SeverityHigh,
				Message: fmt.Sprintf(
					"Git remote %q %s URL does not match the repository manifest identity.",
					remoteName, direction,
				),
				Evidence: map[string]any{
					"remote": remoteName, "direction": direction, "url_index": index,
					"expected_owner": expected.Owner, "expected_name": expected.Name,
					"observed_owner": observed.Owner, "observed_name": observed.Name,
				},
			})
		}
	}
	return findings
}
