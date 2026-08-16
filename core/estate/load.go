package estate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func Load(root string, schemas *validation.Set) (Config, []domain.Finding) {
	_, findings := schemas.ValidateEstateTree(root)
	if len(findings) != 0 {
		return Config{}, findings
	}
	config := Config{}
	if err := decodeFile(filepath.Join(root, "estate", "estate.yaml"), &config.Root); err != nil {
		return Config{}, []domain.Finding{decodeFinding(err)}
	}
	if err := decodeDirectory(
		filepath.Join(root, "estate", "installations"), &config.Installations,
	); err != nil {
		return Config{}, []domain.Finding{decodeFinding(err)}
	}
	if err := decodeDirectory(
		filepath.Join(root, "estate", "mutations"), &config.Mutations,
	); err != nil {
		return Config{}, []domain.Finding{decodeFinding(err)}
	}
	if findings := validateInstallationPermissions(config.Installations); len(findings) != 0 {
		return Config{}, findings
	}
	if err := decodeDirectory(filepath.Join(root, "estate", "owners"), &config.Owners); err != nil {
		return Config{}, []domain.Finding{decodeFinding(err)}
	}
	if err := decodeDirectory(filepath.Join(root, "estate", "selectors"), &config.Selectors); err != nil {
		return Config{}, []domain.Finding{decodeFinding(err)}
	}
	sort.Slice(config.Installations, func(left, right int) bool {
		return config.Installations[left].Installation.ID < config.Installations[right].Installation.ID
	})
	sort.Slice(config.Mutations, func(left, right int) bool {
		return config.Mutations[left].Mutation.ID < config.Mutations[right].Mutation.ID
	})
	sort.Slice(config.Owners, func(left, right int) bool {
		return config.Owners[left].Owner.ID < config.Owners[right].Owner.ID
	})
	sort.Slice(config.Selectors, func(left, right int) bool {
		if config.Selectors[left].Selector.Priority != config.Selectors[right].Selector.Priority {
			return config.Selectors[left].Selector.Priority < config.Selectors[right].Selector.Priority
		}
		return config.Selectors[left].Selector.ID < config.Selectors[right].Selector.ID
	})
	return config, nil
}

func validateInstallationPermissions(installations []Installation) []domain.Finding {
	findings := []domain.Finding{}
	for _, installation := range installations {
		seen := make(map[string]string, len(installation.Permissions.Repository)+
			len(installation.Permissions.Organization))
		scopes := []struct {
			name        string
			permissions map[string]string
		}{
			{name: "repository", permissions: installation.Permissions.Repository},
			{name: "organization", permissions: installation.Permissions.Organization},
		}
		for _, scope := range scopes {
			for name := range scope.permissions {
				if prior, duplicate := seen[name]; duplicate {
					findings = append(findings, domain.Finding{
						Code: "GDS_ESTATE_PERMISSION_SCOPE_CONFLICT", Severity: domain.SeverityHigh,
						Message: "One GitHub App permission is declared in multiple scopes.",
						Evidence: map[string]any{
							"installation": installation.Installation.ID,
							"permission":   name, "first_scope": prior, "second_scope": scope.name,
						},
					})
					continue
				}
				seen[name] = scope.name
			}
		}
		if len(seen) == 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_ESTATE_PERMISSION_SET_EMPTY", Severity: domain.SeverityHigh,
				Message:  "A GitHub Inventory App installation requires at least one read permission.",
				Evidence: map[string]any{"installation": installation.Installation.ID},
			})
		}
	}
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].Code != findings[right].Code {
			return findings[left].Code < findings[right].Code
		}
		leftKey := fmt.Sprintf(
			"%v/%v", findings[left].Evidence["installation"], findings[left].Evidence["permission"],
		)
		rightKey := fmt.Sprintf(
			"%v/%v", findings[right].Evidence["installation"], findings[right].Evidence["permission"],
		)
		return leftKey < rightKey
	})
	return findings
}

func decodeDirectory[T any](directory string, target *[]T) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	paths := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml")) {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		var value T
		if err := decodeFile(path, &value); err != nil {
			return err
		}
		*target = append(*target, value)
	}
	return nil
}

func decodeFile(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := serialization.DecodeInto(path, raw, target); err != nil {
		return err
	}
	return nil
}

func decodeFinding(err error) domain.Finding {
	return domain.Finding{
		Code: "GDS_ESTATE_TYPED_DECODE_FAILED", Severity: domain.SeverityHigh,
		Message: err.Error(),
	}
}
