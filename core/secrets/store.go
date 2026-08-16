// Package secrets provides explicit, read-only secret-manager adapters.
package secrets

import (
	"context"
	"errors"
)

const MaxSecretBytes = 1 << 20

var (
	ErrNotFound    = errors.New("secret reference was not found")
	ErrUnavailable = errors.New("secret provider is unavailable")
	ErrInvalid     = errors.New("secret material is invalid")
)

type Store interface {
	Get(context.Context, string) ([]byte, error)
}

func clone(value []byte) []byte { return append([]byte(nil), value...) }

func validateReference(reference string) error {
	if reference == "" || len(reference) > 256 {
		return ErrInvalid
	}
	for _, character := range reference {
		if character < 0x21 || character > 0x7e {
			return ErrInvalid
		}
	}
	return nil
}

func normalize(value []byte) ([]byte, error) {
	if len(value) == 0 || len(value) > MaxSecretBytes {
		return nil, ErrInvalid
	}
	result := clone(value)
	for len(result) > 0 && (result[len(result)-1] == '\n' || result[len(result)-1] == '\r') {
		result = result[:len(result)-1]
	}
	if len(result) == 0 {
		clear(result)
		return nil, ErrInvalid
	}
	return result, nil
}
