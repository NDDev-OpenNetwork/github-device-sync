// Package manifest loads repository-owned GDS facts after schema validation.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const RepositoryAnchorPath = ".gds/repository.yaml"

type Loader struct {
	schemas *validation.Set
}

func NewLoader(schemas *validation.Set) *Loader {
	return &Loader{schemas: schemas}
}

func (loader *Loader) LoadRepository(root string) (domain.RepositoryAnchor, []domain.Finding) {
	path := filepath.Join(root, filepath.FromSlash(RepositoryAnchorPath))
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return domain.RepositoryAnchor{}, []domain.Finding{findingForSerialization(path, err)}
	}
	if findings := loader.schemas.Validate("repository", value, path); len(findings) != 0 {
		return domain.RepositoryAnchor{}, findings
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return domain.RepositoryAnchor{}, []domain.Finding{{
			Code:     "GDS_REPOSITORY_ANCHOR_DECODE_FAILED",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot normalize repository anchor %s: %v", path, err),
			Evidence: map[string]any{"path": path},
		}}
	}
	var anchor domain.RepositoryAnchor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&anchor); err != nil {
		return domain.RepositoryAnchor{}, []domain.Finding{{
			Code:     "GDS_REPOSITORY_ANCHOR_DECODE_FAILED",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot decode repository anchor %s: %v", path, err),
			Evidence: map[string]any{"path": path},
		}}
	}
	return anchor, nil
}

func Exists(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(RepositoryAnchorPath)))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func findingForSerialization(path string, err error) domain.Finding {
	code := "GDS_INPUT_PARSE_FAILED"
	var contractError *serialization.ContractError
	if errors.As(err, &contractError) {
		code = contractError.Code
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(),
		Evidence: map[string]any{"path": path},
	}
}
