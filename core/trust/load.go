package trust

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// LoadPolicy reads a bounded, regular, non-symlink offline public trust policy.
func LoadPolicy(path string) (Policy, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 1<<20 {
		return Policy{}, errors.New("trust policy path is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Policy{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Policy{}, errors.New("trust policy contains trailing JSON")
	}
	if policy.SchemaVersion != 1 || policy.PolicyID == "" || len(policy.Identities) == 0 {
		return Policy{}, errors.New("trust policy identity is invalid")
	}
	return policy, nil
}
