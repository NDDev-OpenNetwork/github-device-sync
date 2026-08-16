// Package gitref defines the bounded Git reference grammar shared by providers
// and operation contracts.
package gitref

import (
	"errors"
	"strings"
)

const localBranchPrefix = "refs/heads/"

// ValidBranchName reports whether value belongs to GDS's ASCII-safe subset of
// `git check-ref-format --branch`. The subset is intentionally bounded to 255
// bytes because branch names cross JSON, GitHub, and local Git boundaries.
func ValidBranchName(value string) bool {
	if value == "" || value == "HEAD" || len(value) > 255 || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, ".") || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.Contains(value, "..") || strings.Contains(value, "//") ||
		strings.Contains(value, "@{") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '/' || character == '-' {
			continue
		}
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// ValidateLocalBranchRef validates one fully qualified local branch ref.
func ValidateLocalBranchRef(reference string) error {
	if !strings.HasPrefix(reference, localBranchPrefix) {
		return errors.New("branch ref is outside refs/heads")
	}
	if !ValidBranchName(strings.TrimPrefix(reference, localBranchPrefix)) {
		return errors.New("branch ref is unsafe")
	}
	return nil
}
