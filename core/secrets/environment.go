package secrets

import (
	"context"
	"os"
)

type EnvironmentStore struct {
	References map[string]string
	LookupEnv  func(string) (string, bool)
}

func (store EnvironmentStore) Get(_ context.Context, reference string) ([]byte, error) {
	if err := validateReference(reference); err != nil {
		return nil, err
	}
	variable, found := store.References[reference]
	if !found || variable == "" {
		return nil, ErrNotFound
	}
	lookup := store.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, found := lookup(variable)
	if !found {
		return nil, ErrNotFound
	}
	return normalize([]byte(value))
}
