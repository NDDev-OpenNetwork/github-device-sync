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
)

const maxPolicyExceptions = 10_000

func (loader *Loader) LoadExceptions(root string) ([]PolicyException, []domain.Finding) {
	exceptionRoot := filepath.Join(root, "estate", "exceptions")
	paths := []string{}
	findings := []domain.Finding{}
	err := filepath.WalkDir(exceptionRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return filepath.SkipDir
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, domain.Finding{
				Code: "GDS_POLICY_EXCEPTION_SYMLINK_FORBIDDEN", Severity: domain.SeverityHigh,
				Message:  "Policy exception sources must not be symlinks.",
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
		if len(paths) > maxPolicyExceptions {
			return fmt.Errorf("policy exception count exceeds %d", maxPolicyExceptions)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, append(findings, domain.Finding{
			Code: "GDS_POLICY_EXCEPTION_READ_FAILED", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot enumerate policy exceptions: %v", err),
			Evidence: map[string]any{"root": exceptionRoot},
		})
	}
	sort.Strings(paths)
	exceptions := make([]PolicyException, 0, len(paths))
	identities := map[string]string{}
	scopes := map[string]string{}
	for _, path := range paths {
		value, decodeErr := serialization.DecodeFile(path)
		if decodeErr != nil {
			findings = append(findings, policyInputFinding(path, decodeErr))
			continue
		}
		if schemaFindings := loader.schemas.Validate(
			"policy-exception", value, path,
		); len(schemaFindings) != 0 {
			findings = append(findings, schemaFindings...)
			continue
		}
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			findings = append(findings, exceptionFinding(
				"GDS_POLICY_EXCEPTION_DECODE_FAILED", path, marshalErr,
			))
			continue
		}
		var exception PolicyException
		if decodeErr := json.NewDecoder(bytes.NewReader(raw)).Decode(&exception); decodeErr != nil {
			findings = append(findings, exceptionFinding(
				"GDS_POLICY_EXCEPTION_DECODE_FAILED", path, decodeErr,
			))
			continue
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			findings = append(findings, exceptionFinding(
				"GDS_POLICY_EXCEPTION_PATH_INVALID", path, relativeErr,
			))
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			findings = append(findings, exceptionFinding(
				"GDS_POLICY_EXCEPTION_READ_FAILED", path, readErr,
			))
			continue
		}
		exception.Path = filepath.ToSlash(relative)
		exception.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if previous, exists := identities[exception.Exception.ID]; exists {
			findings = append(findings, duplicateExceptionFinding(
				"GDS_POLICY_EXCEPTION_DUPLICATE_ID", exception, previous,
			))
			continue
		}
		identities[exception.Exception.ID] = exception.Path
		scope := exception.Exception.RepositoryID + "\x00" + exception.Exception.PolicyPath
		if previous, exists := scopes[scope]; exists {
			findings = append(findings, duplicateExceptionFinding(
				"GDS_POLICY_EXCEPTION_DUPLICATE_SCOPE", exception, previous,
			))
			continue
		}
		scopes[scope] = exception.Path
		exceptions = append(exceptions, exception)
	}
	sortFindings(findings)
	return exceptions, findings
}

func exceptionFinding(code, path string, err error) domain.Finding {
	message := "Policy exception is invalid."
	if err != nil {
		message = fmt.Sprintf("Policy exception is invalid: %v.", err)
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"path": path},
	}
}

func duplicateExceptionFinding(
	code string,
	exception PolicyException,
	previous string,
) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh,
		Message: "Policy exception identity or scope must be unique.",
		Evidence: map[string]any{
			"id": exception.Exception.ID, "first": previous, "second": exception.Path,
			"repository_id": exception.Exception.RepositoryID,
			"policy_path":   exception.Exception.PolicyPath,
		},
	}
}
