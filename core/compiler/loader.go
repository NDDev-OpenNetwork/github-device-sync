package compiler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxPolicyFiles = 10_000

type Loader struct {
	schemas *validation.Set
}

func NewLoader(schemas *validation.Set) *Loader {
	return &Loader{schemas: schemas}
}

func (loader *Loader) Load(root string) (map[string]PolicySource, []domain.Finding) {
	policyRoot := filepath.Join(root, "policies")
	paths := []string{}
	findings := []domain.Finding{}
	err := filepath.WalkDir(policyRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_SYMLINK_FORBIDDEN",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Policy source %s is a symlink.", path),
				Evidence: map[string]any{"path": path},
			})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".yaml" && extension != ".yml" {
			return nil
		}
		paths = append(paths, path)
		if len(paths) > maxPolicyFiles {
			return fmt.Errorf("policy source count exceeds %d", maxPolicyFiles)
		}
		return nil
	})
	if err != nil {
		return nil, append(findings, domain.Finding{
			Code:     "GDS_POLICY_SOURCE_READ_FAILED",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot enumerate policy sources: %v", err),
			Evidence: map[string]any{"root": policyRoot},
		})
	}
	sort.Strings(paths)

	sources := map[string]PolicySource{}
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_PATH_OUTSIDE_ROOT",
				Severity: domain.SeverityCritical,
				Message:  fmt.Sprintf("Policy source %s is outside the repository root.", path),
				Evidence: map[string]any{"path": path, "root": root},
			})
			continue
		}
		value, decodeErr := serialization.DecodeFile(path)
		if decodeErr != nil {
			findings = append(findings, policyInputFinding(path, decodeErr))
			continue
		}
		if schemaFindings := loader.schemas.Validate("policy", value, path); len(schemaFindings) != 0 {
			findings = append(findings, schemaFindings...)
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_DECODE_FAILED",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Cannot normalize policy source %s: %v", path, err),
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		var source PolicySource
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&source); err != nil {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_DECODE_FAILED",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Cannot decode policy source %s: %v", path, err),
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_SOURCE_READ_FAILED",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Cannot hash policy source %s: %v", path, err),
				Evidence: map[string]any{"path": path},
			})
			continue
		}
		source.Path = filepath.ToSlash(relative)
		source.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if previous, duplicate := sources[source.Policy.ID]; duplicate {
			findings = append(findings, domain.Finding{
				Code:     "GDS_POLICY_DUPLICATE_ID",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Policy id %q is declared more than once.", source.Policy.ID),
				Evidence: map[string]any{
					"id": source.Policy.ID, "first": previous.Path, "second": source.Path,
				},
			})
			continue
		}
		sources[source.Policy.ID] = source
	}
	sortFindings(findings)
	return sources, findings
}

func policyInputFinding(path string, err error) domain.Finding {
	code := "GDS_POLICY_INPUT_INVALID"
	var contract *serialization.ContractError
	if errors.As(err, &contract) {
		code = contract.Code
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(),
		Evidence: map[string]any{"path": path},
	}
}

func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code != findings[right].Code {
			return findings[left].Code < findings[right].Code
		}
		return findings[left].Message < findings[right].Message
	})
}
