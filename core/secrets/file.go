package secrets

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type FileStore struct {
	References map[string]string
}

func (store FileStore) Get(_ context.Context, reference string) ([]byte, error) {
	if err := validateReference(reference); err != nil {
		return nil, err
	}
	configured, found := store.References[reference]
	if !found || !filepath.IsAbs(configured) {
		return nil, ErrNotFound
	}
	path := filepath.Clean(configured)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > MaxSecretBytes {
		return nil, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) ||
		opened.Mode().Perm()&0o077 != 0 || opened.Size() < 1 || opened.Size() > MaxSecretBytes {
		return nil, ErrInvalid
	}
	value, err := io.ReadAll(io.LimitReader(file, MaxSecretBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read secret", ErrUnavailable)
	}
	return normalize(value)
}
